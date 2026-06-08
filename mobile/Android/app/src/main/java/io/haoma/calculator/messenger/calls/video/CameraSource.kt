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
import android.view.Surface
import androidx.core.content.ContextCompat
import io.haoma.calculator.log.Logger
import io.haoma.calculator.messenger.shortCallId
import kotlinx.coroutines.CoroutineScope
import java.io.IOException
import java.io.OutputStream
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicLong
import java.util.concurrent.atomic.AtomicReference


enum class CameraFacing { Front, Back }


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

    
    private var captureHandlerThread: HandlerThread? = null
    private var captureHandler: Handler? = null
    private var controlHandlerThread: HandlerThread? = null
    private var controlHandler: Handler? = null
    private var imageReader: ImageReader? = null
    private var cameraDevice: CameraDevice? = null
    private var captureSession: CameraCaptureSession? = null

    
    private val currentFacing = AtomicReference(CameraFacing.Front)
    
    
    private val switchInFlight = AtomicBoolean(false)
    
    
    @Volatile private var sensorOrientation: Int = 270

    @Volatile private var socket: LocalSocket? = null
    @Volatile private var socketOut: OutputStream? = null

    
    private var previewSurface: Surface? = null

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

        val captureHT = HandlerThread("haoma-camera-capture").apply { start() }
        captureHandlerThread = captureHT
        captureHandler = Handler(captureHT.looper)
        val controlHT = HandlerThread("haoma-camera-control").apply { start() }
        controlHandlerThread = controlHT
        controlHandler = Handler(controlHT.looper)

        val reader = ImageReader.newInstance(width, height, ImageFormat.YUV_420_888, 4)
        reader.setOnImageAvailableListener({ r -> onImageAvailable(r) }, captureHandler)
        imageReader = reader

        val mgr = context.getSystemService(Context.CAMERA_SERVICE) as CameraManager
        val cameraId = pickCamera(mgr, currentFacing.get()) ?: run {
            Logger.d("call", "cam: no camera available; aborting source call=${shortCallId(callId)}")
            stop()
            return
        }
        logDeviceAndCameraCaps(mgr, cameraId)
        sensorOrientation = readSensorOrientation(mgr, cameraId)

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
            }, controlHandler)
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
        configureSession(device, reader, previewSurface)
    }

    
    private fun configureSession(
        device: CameraDevice,
        reader: ImageReader,
        preview: Surface?,
    ) {
        val surfaces =
            if (preview != null) listOf(reader.surface, preview) else listOf(reader.surface)
        try {
            @Suppress("DEPRECATION")
            device.createCaptureSession(surfaces, object : CameraCaptureSession.StateCallback() {
                override fun onConfigured(session: CameraCaptureSession) {
                    captureSession = session
                    submitRepeatingRequest(device, session, reader, preview)
                }

                override fun onConfigureFailed(session: CameraCaptureSession) {
                    Logger.d(
                        "call",
                        "cam: createCaptureSession onConfigureFailed preview=${preview != null} " +
                            "call=${shortCallId(callId)}",
                    )
                    if (preview != null && !stopped.get()) {
                        Logger.d("call", "cam: retrying session without preview call=${shortCallId(callId)}")
                        previewSurface = null
                        configureSession(device, reader, null)
                    } else {
                        stop()
                    }
                }
            }, controlHandler)
        } catch (e: Exception) {
            Logger.d("call", "cam: createCaptureSession threw: ${e.message}")
            if (preview != null && !stopped.get()) {
                previewSurface = null
                configureSession(device, reader, null)
            } else {
                stop()
            }
        }
    }

    private fun submitRepeatingRequest(
        device: CameraDevice,
        session: CameraCaptureSession,
        reader: ImageReader,
        preview: Surface?,
    ) {
        try {
            val req = device.createCaptureRequest(CameraDevice.TEMPLATE_PREVIEW)
            req.addTarget(reader.surface)
            if (preview != null) req.addTarget(preview)
            applyTargetFpsRange(device.id, req)
            session.setRepeatingRequest(req.build(), null, controlHandler)
            Logger.d(
                "call",
                "cam: session active ${width}x${height} preview=${preview != null} " +
                    "call=${shortCallId(callId)}",
            )
        } catch (e: Exception) {
            Logger.d("call", "cam: setRepeatingRequest failed: ${e.message}")
            stop()
        }
    }

    
    fun attachPreviewSurface(surface: Surface) {
        val h = controlHandler ?: run {
            
            previewSurface = surface
            return
        }
        h.post {
            if (stopped.get()) return@post
            if (previewSurface === surface) return@post
            previewSurface = surface
            val device = cameraDevice
            val reader = imageReader
            if (device != null && reader != null) {
                try { captureSession?.close() } catch (_: Exception) {}
                captureSession = null
                Logger.d("call", "cam: attach preview reconfiguring call=${shortCallId(callId)}")
                configureSession(device, reader, surface)
            } else {
                Logger.d("call", "cam: attach preview deferred (no device/reader yet) call=${shortCallId(callId)}")
            }
        }
    }

    
    fun detachPreviewSurface() {
        val h = controlHandler ?: run {
            previewSurface = null
            return
        }
        h.post {
            if (stopped.get()) return@post
            if (previewSurface == null) return@post
            previewSurface = null
            val device = cameraDevice
            val reader = imageReader
            if (device != null && reader != null) {
                try { captureSession?.close() } catch (_: Exception) {}
                captureSession = null
                Logger.d("call", "cam: detach preview reconfiguring call=${shortCallId(callId)}")
                configureSession(device, reader, null)
            }
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
        } catch (e: IllegalStateException) {
            
            
            if (stopped.get()) {
                Logger.d("call", "cam: dropped frame on teardown call=${shortCallId(callId)}")
            } else {
                Logger.w(
                    "call",
                    "cam: buffer inaccessible while running call=${shortCallId(callId)} err=${e.message}",
                )
            }
        } finally {
            try {
                img.close()
            } catch (_: Exception) {
                
                
            }
        }
    }

    
    private fun packI420(img: Image, dst: ByteArray, dstOffset: Int) {
        val sensorW = width    
        val sensorH = height   
        val ccw = sensorOrientation != 90

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

            if (ccw) {
                for (yDst in 0 until dstPlaneH) {
                    val srcCol = srcPlaneW - 1 - yDst
                    val srcColByteOff = srcCol * pixelStride
                    val dstRowBase = dstIdx + yDst * dstPlaneW
                    for (xDst in 0 until dstPlaneW) {
                        dst[dstRowBase + xDst] = src.get(xDst * rowStride + srcColByteOff)
                    }
                }
            } else {
                for (yDst in 0 until dstPlaneH) {
                    val dstRowBase = dstIdx + yDst * dstPlaneW
                    val srcColByteOff = yDst * pixelStride
                    for (xDst in 0 until dstPlaneW) {
                        val srcRow = srcPlaneH - 1 - xDst
                        dst[dstRowBase + xDst] = src.get(srcRow * rowStride + srcColByteOff)
                    }
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

    
    private fun pickCamera(mgr: CameraManager, want: CameraFacing): String? {
        val ids = try {
            mgr.cameraIdList
        } catch (e: Exception) {
            Logger.d("call", "cam: cameraIdList failed: ${e.message}")
            return null
        }
        val target = when (want) {
            CameraFacing.Front -> CameraCharacteristics.LENS_FACING_FRONT
            CameraFacing.Back -> CameraCharacteristics.LENS_FACING_BACK
        }
        for (id in ids) {
            val chars = try {
                mgr.getCameraCharacteristics(id)
            } catch (e: Exception) {
                continue
            }
            val facing = chars.get(CameraCharacteristics.LENS_FACING) ?: continue
            if (facing == target) return id
        }
        return ids.firstOrNull()
    }

    private fun readSensorOrientation(mgr: CameraManager, cameraId: String): Int {
        val degrees = try {
            mgr.getCameraCharacteristics(cameraId)
                .get(CameraCharacteristics.SENSOR_ORIENTATION) ?: 270
        } catch (e: Exception) {
            Logger.d("call", "cam: sensorOrientation read failed: ${e.message}; defaulting 270")
            270
        }
        if (degrees != 90 && degrees != 270) {
            Logger.w(
                "call",
                "cam: unusual sensorOrientation=$degrees camera=$cameraId — falling back to CCW matrix",
            )
        }
        return degrees
    }

    
    fun facing(): CameraFacing = currentFacing.get()

    
    @SuppressLint("MissingPermission")
    fun requestFacing(facing: CameraFacing) {
        val h = controlHandler ?: run {
            
            currentFacing.set(facing)
            Logger.d("call", "cam: requestFacing pre-start stash=$facing call=${shortCallId(callId)}")
            return
        }
        if (!switchInFlight.compareAndSet(false, true)) {
            Logger.d(
                "call",
                "cam: requestFacing dropped target=$facing switch_in_flight call=${shortCallId(callId)}",
            )
            return
        }
        h.post {
            var openIssued = false
            try {
                if (stopped.get()) return@post
                if (currentFacing.get() == facing) return@post
                if (ContextCompat.checkSelfPermission(context, Manifest.permission.CAMERA)
                    != PackageManager.PERMISSION_GRANTED
                ) {
                    Logger.w("call", "cam: requestFacing denied CAMERA call=${shortCallId(callId)}")
                    return@post
                }
                val mgr = context.getSystemService(Context.CAMERA_SERVICE) as CameraManager
                val newId = pickCamera(mgr, facing) ?: run {
                    Logger.w("call", "cam: requestFacing no lens for $facing call=${shortCallId(callId)}")
                    return@post
                }
                currentFacing.set(facing)
                Logger.i(
                    "call",
                    "cam: requestFacing → $facing id=$newId call=${shortCallId(callId)}",
                )
                try { captureSession?.close() } catch (_: Exception) {}
                captureSession = null
                try { cameraDevice?.close() } catch (_: Exception) {}
                cameraDevice = null
                sensorOrientation = readSensorOrientation(mgr, newId)
                logDeviceAndCameraCaps(mgr, newId)
                val reader = imageReader ?: run {
                    Logger.w("call", "cam: requestFacing no reader call=${shortCallId(callId)}")
                    return@post
                }
                try {
                    mgr.openCamera(newId, object : CameraDevice.StateCallback() {
                        override fun onOpened(device: CameraDevice) {
                            cameraDevice = device
                            startSession(device, reader)
                            
                            
                            switchInFlight.set(false)
                        }

                        override fun onDisconnected(device: CameraDevice) {
                            Logger.d("call", "cam: device disconnected (post-switch) call=${shortCallId(callId)}")
                            device.close()
                            switchInFlight.set(false)
                        }

                        override fun onError(device: CameraDevice, error: Int) {
                            Logger.d("call", "cam: device error=$error (post-switch) call=${shortCallId(callId)}")
                            device.close()
                            switchInFlight.set(false)
                            stop()
                        }
                    }, controlHandler)
                    openIssued = true
                } catch (e: SecurityException) {
                    Logger.d("call", "cam: requestFacing openCamera SecurityException: ${e.message}")
                    stop()
                } catch (e: Exception) {
                    Logger.d("call", "cam: requestFacing openCamera failed: ${e.message}")
                    stop()
                }
            } finally {
                
                
                if (!openIssued) switchInFlight.set(false)
            }
        }
    }

    
    fun stop() {
        if (!stopped.compareAndSet(false, true)) return
        try { captureSession?.close() } catch (_: Exception) {}
        captureSession = null
        try { cameraDevice?.close() } catch (_: Exception) {}
        cameraDevice = null
        try { imageReader?.close() } catch (_: Exception) {}
        imageReader = null
        previewSurface = null
        
        
        switchInFlight.set(false)
        captureHandlerThread?.quitSafely()
        captureHandlerThread = null
        captureHandler = null
        controlHandlerThread?.quitSafely()
        controlHandlerThread = null
        controlHandler = null
        
        
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
