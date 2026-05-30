package io.haoma.calculator.messenger.calls.video

import android.Manifest
import android.annotation.SuppressLint
import android.content.Context
import android.content.pm.PackageManager
import android.graphics.ImageFormat
import android.hardware.camera2.CameraCaptureSession
import android.hardware.camera2.CameraCharacteristics
import android.hardware.camera2.CameraDevice
import android.hardware.camera2.CameraManager
import android.hardware.camera2.CaptureRequest
import android.hardware.camera2.params.StreamConfigurationMap
import android.media.Image
import android.media.ImageReader
import android.util.Range
import android.net.LocalSocket
import android.net.LocalSocketAddress
import android.os.Build
import android.os.Handler
import android.os.HandlerThread
import android.os.SystemClock
import androidx.core.content.ContextCompat
import io.haoma.calculator.log.Logger
import io.haoma.calculator.messenger.shortCallId
import kotlinx.coroutines.CoroutineScope
import java.io.IOException
import java.io.OutputStream
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicLong


class CameraSource(
    private val context: Context,
    val callId: String,
    @Suppress("unused") private val parentScope: CoroutineScope,
    private val unixName: String,
    private val mutedProvider: () -> Boolean = { false },
    val width: Int = DEFAULT_WIDTH,
    val height: Int = DEFAULT_HEIGHT,
) {
    private val started = AtomicBoolean(false)
    private val stopped = AtomicBoolean(false)

    private var handlerThread: HandlerThread? = null
    private var handler: Handler? = null
    private var imageReader: ImageReader? = null
    private var cameraDevice: CameraDevice? = null
    private var captureSession: CameraCaptureSession? = null

    @Volatile private var socket: LocalSocket? = null
    @Volatile private var socketOut: OutputStream? = null

    private val frameBytes = width * height * 3 / 2
    
    
    private val outBuf = ByteArray(8 + frameBytes)

    private val startedAtElapsedMs = SystemClock.elapsedRealtime()
    private val firstFrameLogged = AtomicBoolean(false)
    private val frameCount = AtomicLong(0L)
    private var lastHeartbeatElapsedMs = 0L

    
    @SuppressLint("MissingPermission")
    fun start() {
        if (!started.compareAndSet(false, true)) return
        if (stopped.get()) return

        if (ContextCompat.checkSelfPermission(context, Manifest.permission.CAMERA)
            != PackageManager.PERMISSION_GRANTED
        ) {
            Logger.d("call", "cam: CAMERA permission denied; aborting source call=${shortCallId(callId)}")
            stopped.set(true)
            return
        }

        
        val s = LocalSocket()
        try {
            s.connect(LocalSocketAddress(unixName, LocalSocketAddress.Namespace.ABSTRACT))
        } catch (t: Throwable) {
            Logger.d(
                "call",
                "cam: socket connect failed call=${shortCallId(callId)} unix=$unixName err=${t.message}",
            )
            try { s.close() } catch (_: Throwable) {}
            stopped.set(true)
            return
        }
        socket = s
        socketOut = s.outputStream
        Logger.d("call", "cam: socket connected call=${shortCallId(callId)} unix=$unixName")

        val ht = HandlerThread("haoma-camera-source").apply { start() }
        handlerThread = ht
        handler = Handler(ht.looper)

        val reader = ImageReader.newInstance(width, height, ImageFormat.YUV_420_888, 4)
        reader.setOnImageAvailableListener({ r -> onImageAvailable(r) }, handler)
        imageReader = reader

        val mgr = context.getSystemService(Context.CAMERA_SERVICE) as CameraManager
        val cameraId = pickFrontCamera(mgr) ?: run {
            Logger.d("call", "cam: no camera available; aborting source call=${shortCallId(callId)}")
            stop()
            return
        }
        logDeviceAndCameraCaps(mgr, cameraId)

        try {
            mgr.openCamera(cameraId, object : CameraDevice.StateCallback() {
                override fun onOpened(device: CameraDevice) {
                    cameraDevice = device
                    startSession(device, reader)
                }

                override fun onDisconnected(device: CameraDevice) {
                    Logger.d("call", "cam: device disconnected call=${shortCallId(callId)}")
                    device.close()
                }

                override fun onError(device: CameraDevice, error: Int) {
                    Logger.d("call", "cam: device error=$error call=${shortCallId(callId)}")
                    device.close()
                    stop()
                }
            }, handler)
        } catch (e: SecurityException) {
            Logger.d("call", "cam: openCamera SecurityException: ${e.message}")
            stop()
            return
        } catch (e: Exception) {
            Logger.d("call", "cam: openCamera failed: ${e.message}")
            stop()
            return
        }
    }

    private fun startSession(device: CameraDevice, reader: ImageReader) {
        val surfaces = listOf(reader.surface)
        try {
            @Suppress("DEPRECATION")
            device.createCaptureSession(surfaces, object : CameraCaptureSession.StateCallback() {
                override fun onConfigured(session: CameraCaptureSession) {
                    captureSession = session
                    try {
                        val req = device.createCaptureRequest(CameraDevice.TEMPLATE_PREVIEW)
                        req.addTarget(reader.surface)
                        applyTargetFpsRange(device.id, req)
                        session.setRepeatingRequest(req.build(), null, handler)
                        Logger.d("call", "cam: session active ${width}x${height} call=${shortCallId(callId)}")
                    } catch (e: Exception) {
                        Logger.d("call", "cam: setRepeatingRequest failed: ${e.message}")
                        stop()
                    }
                }

                override fun onConfigureFailed(session: CameraCaptureSession) {
                    Logger.d("call", "cam: createCaptureSession onConfigureFailed call=${shortCallId(callId)}")
                    stop()
                }
            }, handler)
        } catch (e: Exception) {
            Logger.d("call", "cam: createCaptureSession threw: ${e.message}")
            stop()
        }
    }

    private fun onImageAvailable(reader: ImageReader) {
        val img = try {
            reader.acquireLatestImage()
        } catch (e: Exception) {
            return
        } ?: return
        try {
            val n = frameCount.incrementAndGet()
            if (firstFrameLogged.compareAndSet(false, true)) {
                val elapsed = SystemClock.elapsedRealtime() - startedAtElapsedMs
                Logger.d(
                    "call",
                    "cam: first frame ${img.width}x${img.height} after ${elapsed}ms call=${shortCallId(callId)}",
                )
                logFirstFrameDiagnostic(img)
            }
            val now = SystemClock.elapsedRealtime()
            if (now - lastHeartbeatElapsedMs > HEARTBEAT_MS) {
                lastHeartbeatElapsedMs = now
                Logger.d("call", "cam: heartbeat call=${shortCallId(callId)} frames=$n")
            }

            if (stopped.get()) return
            val out = socketOut ?: return
            if (mutedProvider()) return

            packI420(img, outBuf, 8)
            val pts = System.nanoTime()
            for (i in 0..7) outBuf[7 - i] = ((pts ushr (i * 8)) and 0xff).toByte()

            try {
                out.write(outBuf, 0, outBuf.size)
            } catch (e: IOException) {
                Logger.i(
                    "call",
                    "cam: socket write failed call=${shortCallId(callId)} err=${e.message}; stopping",
                )
                stop()
            }
        } finally {
            img.close()
        }
    }

    
    private fun packI420(img: Image, dst: ByteArray, dstOffset: Int) {
        val sensorW = width    
        val sensorH = height   

        var dstIdx = dstOffset
        for (planeIdx in 0..2) {
            val plane = img.planes[planeIdx]
            val src = plane.buffer
            val rowStride = plane.rowStride
            val pixelStride = plane.pixelStride
            val srcPlaneW = if (planeIdx == 0) sensorW else sensorW / 2
            val srcPlaneH = if (planeIdx == 0) sensorH else sensorH / 2
            
            val dstPlaneW = srcPlaneH
            val dstPlaneH = srcPlaneW

            for (yDst in 0 until dstPlaneH) {
                val srcCol = srcPlaneW - 1 - yDst
                val srcColByteOff = srcCol * pixelStride
                val dstRowBase = dstIdx + yDst * dstPlaneW
                for (xDst in 0 until dstPlaneW) {
                    dst[dstRowBase + xDst] = src.get(xDst * rowStride + srcColByteOff)
                }
            }
            dstIdx += dstPlaneW * dstPlaneH
        }
    }

    
    private fun applyTargetFpsRange(cameraId: String, req: CaptureRequest.Builder) {
        val mgr = context.getSystemService(Context.CAMERA_SERVICE) as CameraManager
        val ranges = try {
            mgr.getCameraCharacteristics(cameraId)
                .get(CameraCharacteristics.CONTROL_AE_AVAILABLE_TARGET_FPS_RANGES)
        } catch (e: Exception) {
            Logger.d("call", "cam: fps range — getCharacteristics failed: ${e.message}")
            return
        }
        if (ranges == null || ranges.isEmpty()) {
            Logger.d("call", "cam: fps range — no AE_AVAILABLE_TARGET_FPS_RANGES")
            return
        }
        val pick = ranges
            .filter { TARGET_FPS in it.lower..it.upper }
            .minWithOrNull(compareBy({ it.upper - it.lower }, { it.upper }))
            ?: ranges.minByOrNull { kotlin.math.abs(it.upper - TARGET_FPS) }
            ?: return
        req.set(CaptureRequest.CONTROL_AE_TARGET_FPS_RANGE, pick)
        Logger.d(
            "call",
            "cam: fps range pick=[${pick.lower},${pick.upper}] target=$TARGET_FPS " +
                "available=${ranges.joinToString(",") { "[${it.lower},${it.upper}]" }}",
        )
    }

    
    private fun logDeviceAndCameraCaps(mgr: CameraManager, cameraId: String) {
        val chars = try {
            mgr.getCameraCharacteristics(cameraId)
        } catch (e: Exception) {
            Logger.d("call", "camdiag: getCameraCharacteristics failed: ${e.message}")
            return
        }
        Logger.d(
            "call",
            "camdiag build manufacturer=${Build.MANUFACTURER} model=${Build.MODEL} " +
                "device=${Build.DEVICE} hardware=${Build.HARDWARE} " +
                "sdk=${Build.VERSION.SDK_INT} release=${Build.VERSION.RELEASE} buildId=${Build.ID}",
        )

        val hwLevel = chars.get(CameraCharacteristics.INFO_SUPPORTED_HARDWARE_LEVEL)
        val sensorOrientation = chars.get(CameraCharacteristics.SENSOR_ORIENTATION)
        val lensFacing = chars.get(CameraCharacteristics.LENS_FACING)
        val pixelArray = chars.get(CameraCharacteristics.SENSOR_INFO_PIXEL_ARRAY_SIZE)
        Logger.d(
            "call",
            "camdiag camera id=$cameraId hwLevel=$hwLevel sensorOrientation=$sensorOrientation " +
                "lensFacing=$lensFacing pixelArray=$pixelArray requested=${width}x${height} " +
                "maxImages=4",
        )

        val cfgMap = chars.get(CameraCharacteristics.SCALER_STREAM_CONFIGURATION_MAP)
            as? StreamConfigurationMap
        if (cfgMap != null) {
            val sizes = cfgMap.getOutputSizes(ImageFormat.YUV_420_888)?.toList().orEmpty()
            val containsRequested = sizes.any { it.width == width && it.height == height }
            Logger.d(
                "call",
                "camdiag yuv420 supports=$containsRequested count=${sizes.size} " +
                    "sizes=${sizes.joinToString(",") { "${it.width}x${it.height}" }}",
            )
        } else {
            Logger.d("call", "camdiag yuv420 SCALER_STREAM_CONFIGURATION_MAP null")
        }
    }

    
    private fun logFirstFrameDiagnostic(img: Image) {
        Logger.d(
            "call",
            "camdiag img width=${img.width} height=${img.height} format=${img.format} " +
                "timestamp=${img.timestamp} planes=${img.planes.size}",
        )
        for (i in img.planes.indices) {
            val p = img.planes[i]
            val b = p.buffer
            val expectedPlaneH = if (i == 0) img.height else img.height / 2
            val expectedTight = p.rowStride * expectedPlaneH
            Logger.d(
                "call",
                "camdiag plane[$i] rowStride=${p.rowStride} pixelStride=${p.pixelStride} " +
                    "capacity=${b.capacity()} remaining=${b.remaining()} " +
                    "expectedTight=$expectedTight direct=${b.isDirect}",
            )
        }
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
            val hb = try { img.hardwareBuffer } catch (_: Throwable) { null }
            if (hb != null) {
                Logger.d(
                    "call",
                    "camdiag hwbuf width=${hb.width} height=${hb.height} layers=${hb.layers} " +
                        "format=${hb.format} usage=0x${hb.usage.toString(16)}",
                )
                try { hb.close() } catch (_: Throwable) {}
            } else {
                Logger.d("call", "camdiag hwbuf null (API ${Build.VERSION.SDK_INT})")
            }
        }
    }

    private fun pickFrontCamera(mgr: CameraManager): String? {
        val ids = try {
            mgr.cameraIdList
        } catch (e: Exception) {
            Logger.d("call", "cam: cameraIdList failed: ${e.message}")
            return null
        }
        for (id in ids) {
            val chars = try {
                mgr.getCameraCharacteristics(id)
            } catch (e: Exception) {
                continue
            }
            val facing = chars.get(CameraCharacteristics.LENS_FACING) ?: continue
            if (facing == CameraCharacteristics.LENS_FACING_FRONT) return id
        }
        return ids.firstOrNull()
    }

    
    fun stop() {
        if (!stopped.compareAndSet(false, true)) return
        try { captureSession?.close() } catch (_: Exception) {}
        captureSession = null
        try { cameraDevice?.close() } catch (_: Exception) {}
        cameraDevice = null
        try { imageReader?.close() } catch (_: Exception) {}
        imageReader = null
        handlerThread?.quitSafely()
        handlerThread = null
        handler = null
        
        
        socketOut = null
        try { socket?.close() } catch (_: Exception) {}
        socket = null
        Logger.d("call", "cam: stopped call=${shortCallId(callId)} totalFrames=${frameCount.get()}")
    }

    companion object {
        const val DEFAULT_WIDTH = 640
        const val DEFAULT_HEIGHT = 480
        
        
        private const val TARGET_FPS = 15
        private const val HEARTBEAT_MS = 5_000L
    }
}
