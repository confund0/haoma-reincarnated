package io.haoma.calculator.messenger.calls.video

import android.opengl.EGL14
import android.opengl.EGLConfig
import android.opengl.EGLContext
import android.opengl.EGLDisplay
import android.opengl.EGLSurface
import android.opengl.GLES20
import android.os.Handler
import android.os.HandlerThread
import android.os.SystemClock
import android.view.Choreographer
import android.view.Surface
import io.haoma.calculator.log.Logger
import io.haoma.calculator.messenger.ClockSample
import io.haoma.calculator.messenger.shortCallId
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import java.nio.ByteBuffer
import java.nio.ByteOrder
import java.nio.FloatBuffer


internal class VideoRenderer(
    private val callId: String,
    private val streamProvider: () -> VideoFrameStream?,
    private val clockSampleProvider: () -> ClockSample?,
) {

    private val _firstFrameAt = MutableStateFlow<Long?>(null)
    val firstFrameAt: StateFlow<Long?> = _firstFrameAt.asStateFlow()

    private val _syncing = MutableStateFlow(true)
    val syncing: StateFlow<Boolean> = _syncing.asStateFlow()

    private val renderThread = HandlerThread("video-render-${shortCallId(callId)}")
    private var handler: Handler? = null

    
    private var eglDisplay: EGLDisplay = EGL14.EGL_NO_DISPLAY
    private var eglContext: EGLContext = EGL14.EGL_NO_CONTEXT
    private var eglSurface: EGLSurface = EGL14.EGL_NO_SURFACE
    private var program: Int = 0
    private var texY: Int = 0
    private var texU: Int = 0
    private var texV: Int = 0
    private var aPosLoc: Int = -1
    private var aTexLoc: Int = -1
    private var uYLoc: Int = -1
    private var uULoc: Int = -1
    private var uVLoc: Int = -1
    private var quadBuf: FloatBuffer? = null
    private var surfaceWidth = 0
    private var surfaceHeight = 0

    @Volatile private var running = false
    private var lastPaintedPts: Long = Long.MIN_VALUE
    private var lastSyncDebugAt: Long = 0L
    private var lastPaintDebugAt: Long = 0L
    private var loggedFirstFrame = false

    private val frameCb = object : Choreographer.FrameCallback {
        override fun doFrame(frameTimeNanos: Long) {
            if (!running) return
            try {
                paintTick()
            } catch (t: Throwable) {
                Logger.e("call", "videotile paint err call=${shortCallId(callId)}", t)
            }
            Choreographer.getInstance().postFrameCallback(this)
        }
    }

    
    fun start(surface: Surface, width: Int, height: Int) {
        if (renderThread.isAlive.not()) renderThread.start()
        if (handler == null) handler = Handler(renderThread.looper)
        surfaceWidth = width
        surfaceHeight = height
        Logger.i(
            "call",
            "videotile renderer start call=${shortCallId(callId)} surface=${width}x$height",
        )
        handler?.post {
            initEgl(surface)
            running = true
            Choreographer.getInstance().postFrameCallback(frameCb)
        }
    }

    
    fun resize(width: Int, height: Int) {
        handler?.post {
            surfaceWidth = width
            surfaceHeight = height
        }
    }

    
    fun stop() {
        Logger.i("call", "videotile renderer stop call=${shortCallId(callId)}")
        val h = handler
        if (h == null) {
            renderThread.quitSafely()
            return
        }
        running = false
        h.post {
            Choreographer.getInstance().removeFrameCallback(frameCb)
            tearDownEgl()
        }
        renderThread.quitSafely()
    }

    
    private fun paintTick() {
        val stream = streamProvider() ?: run {
            markSyncing("no_stream")
            return
        }
        val sample = clockSampleProvider()
        val now = SystemClock.elapsedRealtimeNanos()

        val slot: VideoFrameStream.FrameSlot? = if (sample == null ||
            (now - sample.receivedAtElapsedNs) > STALE_SAMPLE_THRESHOLD_NS) {
            markSyncing(if (sample == null) "no_sample" else "stale_sample")
            
            pickPassiveFrame(stream)
        } else {
            var target = sample.senderPtsNs + (now - sample.receivedAtElapsedNs)
            val newest = pickPassiveFrame(stream)
            if (newest != null && newest.ptsNs > target + RECOVERY_SNAP_NS) {
                target = newest.ptsNs
            }
            markLive()
            stream.latestFrame(target)
        }

        if (slot == null || slot.ptsNs == lastPaintedPts) {
            
            
            return
        }

        drawFrame(slot, stream.width, stream.height)
        lastPaintedPts = slot.ptsNs

        if (!loggedFirstFrame) {
            loggedFirstFrame = true
            _firstFrameAt.value = now
            Logger.i(
                "call",
                "videotile first_frame call=${shortCallId(callId)} pts=${slot.ptsNs}",
            )
        }

        
        val elapsedMs = SystemClock.elapsedRealtime()
        if (elapsedMs - lastPaintDebugAt >= 1_000L) {
            lastPaintDebugAt = elapsedMs
            val ageMs = if (sample != null) (now - sample.receivedAtElapsedNs) / 1_000_000L else -1L
            Logger.d(
                "call",
                "videotile paint call=${shortCallId(callId)} picked_pts=${slot.ptsNs} sample_age_ms=$ageMs syncing=${_syncing.value}",
            )
        }
    }

    private fun pickPassiveFrame(stream: VideoFrameStream): VideoFrameStream.FrameSlot? {
        
        
        return stream.latestFrame(Long.MAX_VALUE)
    }

    private fun markSyncing(reason: String) {
        if (!_syncing.value) {
            _syncing.value = true
            Logger.i("call", "videotile syncing call=${shortCallId(callId)} reason=$reason")
            lastSyncDebugAt = SystemClock.elapsedRealtime()
        }
    }

    private fun markLive() {
        if (_syncing.value) {
            _syncing.value = false
            Logger.i("call", "videotile live call=${shortCallId(callId)}")
        }
    }

    private fun drawFrame(slot: VideoFrameStream.FrameSlot, w: Int, h: Int) {
        val buf = slot.buffer
        val ySize = w * h
        val uvSize = w * h / 4
        val uvW = w / 2
        val uvH = h / 2

        
        buf.position(0).limit(ySize)
        GLES20.glActiveTexture(GLES20.GL_TEXTURE0)
        GLES20.glBindTexture(GLES20.GL_TEXTURE_2D, texY)
        GLES20.glTexImage2D(
            GLES20.GL_TEXTURE_2D, 0, GLES20.GL_LUMINANCE,
            w, h, 0, GLES20.GL_LUMINANCE, GLES20.GL_UNSIGNED_BYTE, buf.slice(),
        )

        
        buf.position(ySize).limit(ySize + uvSize)
        GLES20.glActiveTexture(GLES20.GL_TEXTURE1)
        GLES20.glBindTexture(GLES20.GL_TEXTURE_2D, texU)
        GLES20.glTexImage2D(
            GLES20.GL_TEXTURE_2D, 0, GLES20.GL_LUMINANCE,
            uvW, uvH, 0, GLES20.GL_LUMINANCE, GLES20.GL_UNSIGNED_BYTE, buf.slice(),
        )

        
        buf.position(ySize + uvSize).limit(ySize + 2 * uvSize)
        GLES20.glActiveTexture(GLES20.GL_TEXTURE2)
        GLES20.glBindTexture(GLES20.GL_TEXTURE_2D, texV)
        GLES20.glTexImage2D(
            GLES20.GL_TEXTURE_2D, 0, GLES20.GL_LUMINANCE,
            uvW, uvH, 0, GLES20.GL_LUMINANCE, GLES20.GL_UNSIGNED_BYTE, buf.slice(),
        )

        
        buf.position(0).limit(buf.capacity())

        
        val srcAspect = w.toFloat() / h.toFloat()
        val dstAspect = surfaceWidth.toFloat() / surfaceHeight.toFloat()
        val vpW: Int
        val vpH: Int
        val vpX: Int
        val vpY: Int
        if (dstAspect > srcAspect) {
            vpH = surfaceHeight
            vpW = (surfaceHeight * srcAspect).toInt()
            vpX = (surfaceWidth - vpW) / 2
            vpY = 0
        } else {
            vpW = surfaceWidth
            vpH = (surfaceWidth / srcAspect).toInt()
            vpX = 0
            vpY = (surfaceHeight - vpH) / 2
        }

        GLES20.glClearColor(0f, 0f, 0f, 1f)
        GLES20.glClear(GLES20.GL_COLOR_BUFFER_BIT)
        GLES20.glViewport(vpX, vpY, vpW, vpH)

        GLES20.glUseProgram(program)
        GLES20.glUniform1i(uYLoc, 0)
        GLES20.glUniform1i(uULoc, 1)
        GLES20.glUniform1i(uVLoc, 2)

        val q = quadBuf!!
        q.position(0)
        GLES20.glEnableVertexAttribArray(aPosLoc)
        GLES20.glVertexAttribPointer(aPosLoc, 2, GLES20.GL_FLOAT, false, 16, q)
        q.position(2)
        GLES20.glEnableVertexAttribArray(aTexLoc)
        GLES20.glVertexAttribPointer(aTexLoc, 2, GLES20.GL_FLOAT, false, 16, q)
        GLES20.glDrawArrays(GLES20.GL_TRIANGLE_STRIP, 0, 4)

        EGL14.eglSwapBuffers(eglDisplay, eglSurface)
    }

    private fun initEgl(surface: Surface) {
        eglDisplay = EGL14.eglGetDisplay(EGL14.EGL_DEFAULT_DISPLAY)
        check(eglDisplay != EGL14.EGL_NO_DISPLAY) { "eglGetDisplay failed" }
        val versions = IntArray(2)
        check(EGL14.eglInitialize(eglDisplay, versions, 0, versions, 1)) {
            "eglInitialize failed"
        }

        val cfgAttribs = intArrayOf(
            EGL14.EGL_RED_SIZE, 8,
            EGL14.EGL_GREEN_SIZE, 8,
            EGL14.EGL_BLUE_SIZE, 8,
            EGL14.EGL_ALPHA_SIZE, 8,
            EGL14.EGL_RENDERABLE_TYPE, EGL14.EGL_OPENGL_ES2_BIT,
            EGL14.EGL_SURFACE_TYPE, EGL14.EGL_WINDOW_BIT,
            EGL14.EGL_NONE,
        )
        val cfgs = arrayOfNulls<EGLConfig>(1)
        val numCfgs = IntArray(1)
        check(EGL14.eglChooseConfig(eglDisplay, cfgAttribs, 0, cfgs, 0, 1, numCfgs, 0)
            && numCfgs[0] > 0) { "eglChooseConfig failed" }
        val cfg = cfgs[0]!!

        val ctxAttribs = intArrayOf(EGL14.EGL_CONTEXT_CLIENT_VERSION, 2, EGL14.EGL_NONE)
        eglContext = EGL14.eglCreateContext(eglDisplay, cfg, EGL14.EGL_NO_CONTEXT, ctxAttribs, 0)
        check(eglContext != EGL14.EGL_NO_CONTEXT) { "eglCreateContext failed" }

        val surfAttribs = intArrayOf(EGL14.EGL_NONE)
        eglSurface = EGL14.eglCreateWindowSurface(eglDisplay, cfg, surface, surfAttribs, 0)
        check(eglSurface != EGL14.EGL_NO_SURFACE) { "eglCreateWindowSurface failed" }

        check(EGL14.eglMakeCurrent(eglDisplay, eglSurface, eglSurface, eglContext)) {
            "eglMakeCurrent failed"
        }

        program = buildProgram()
        aPosLoc = GLES20.glGetAttribLocation(program, "aPos")
        aTexLoc = GLES20.glGetAttribLocation(program, "aTex")
        uYLoc = GLES20.glGetUniformLocation(program, "uY")
        uULoc = GLES20.glGetUniformLocation(program, "uU")
        uVLoc = GLES20.glGetUniformLocation(program, "uV")

        
        val quad = floatArrayOf(
            
            -1f, -1f, 0f, 1f,
            1f, -1f, 1f, 1f,
            -1f, 1f, 0f, 0f,
            1f, 1f, 1f, 0f,
        )
        quadBuf = ByteBuffer.allocateDirect(quad.size * 4)
            .order(ByteOrder.nativeOrder()).asFloatBuffer().apply { put(quad); position(0) }

        val texes = IntArray(3)
        GLES20.glGenTextures(3, texes, 0)
        texY = texes[0]; texU = texes[1]; texV = texes[2]
        for (t in texes) {
            GLES20.glBindTexture(GLES20.GL_TEXTURE_2D, t)
            GLES20.glTexParameteri(GLES20.GL_TEXTURE_2D, GLES20.GL_TEXTURE_MIN_FILTER, GLES20.GL_LINEAR)
            GLES20.glTexParameteri(GLES20.GL_TEXTURE_2D, GLES20.GL_TEXTURE_MAG_FILTER, GLES20.GL_LINEAR)
            GLES20.glTexParameteri(GLES20.GL_TEXTURE_2D, GLES20.GL_TEXTURE_WRAP_S, GLES20.GL_CLAMP_TO_EDGE)
            GLES20.glTexParameteri(GLES20.GL_TEXTURE_2D, GLES20.GL_TEXTURE_WRAP_T, GLES20.GL_CLAMP_TO_EDGE)
        }
        GLES20.glPixelStorei(GLES20.GL_UNPACK_ALIGNMENT, 1)

        GLES20.glClearColor(0f, 0f, 0f, 1f)
        GLES20.glClear(GLES20.GL_COLOR_BUFFER_BIT)
        EGL14.eglSwapBuffers(eglDisplay, eglSurface)
    }

    private fun tearDownEgl() {
        try {
            if (eglDisplay != EGL14.EGL_NO_DISPLAY) {
                EGL14.eglMakeCurrent(
                    eglDisplay,
                    EGL14.EGL_NO_SURFACE, EGL14.EGL_NO_SURFACE, EGL14.EGL_NO_CONTEXT,
                )
                if (program != 0) GLES20.glDeleteProgram(program)
                if (texY != 0) GLES20.glDeleteTextures(3, intArrayOf(texY, texU, texV), 0)
                if (eglSurface != EGL14.EGL_NO_SURFACE) EGL14.eglDestroySurface(eglDisplay, eglSurface)
                if (eglContext != EGL14.EGL_NO_CONTEXT) EGL14.eglDestroyContext(eglDisplay, eglContext)
                EGL14.eglReleaseThread()
                EGL14.eglTerminate(eglDisplay)
            }
        } catch (t: Throwable) {
            Logger.w("call", "videotile egl teardown err call=${shortCallId(callId)} ${t.message ?: "?"}")
        } finally {
            eglDisplay = EGL14.EGL_NO_DISPLAY
            eglContext = EGL14.EGL_NO_CONTEXT
            eglSurface = EGL14.EGL_NO_SURFACE
            program = 0
            texY = 0; texU = 0; texV = 0
        }
    }

    private fun buildProgram(): Int {
        val vs = compileShader(GLES20.GL_VERTEX_SHADER, VERTEX_SRC)
        val fs = compileShader(GLES20.GL_FRAGMENT_SHADER, FRAGMENT_SRC)
        val p = GLES20.glCreateProgram()
        GLES20.glAttachShader(p, vs)
        GLES20.glAttachShader(p, fs)
        GLES20.glLinkProgram(p)
        val status = IntArray(1)
        GLES20.glGetProgramiv(p, GLES20.GL_LINK_STATUS, status, 0)
        check(status[0] == GLES20.GL_TRUE) {
            "shader link failed: ${GLES20.glGetProgramInfoLog(p)}"
        }
        GLES20.glDeleteShader(vs)
        GLES20.glDeleteShader(fs)
        return p
    }

    private fun compileShader(type: Int, src: String): Int {
        val s = GLES20.glCreateShader(type)
        GLES20.glShaderSource(s, src)
        GLES20.glCompileShader(s)
        val status = IntArray(1)
        GLES20.glGetShaderiv(s, GLES20.GL_COMPILE_STATUS, status, 0)
        check(status[0] == GLES20.GL_TRUE) {
            "shader compile failed: ${GLES20.glGetShaderInfoLog(s)}"
        }
        return s
    }

    companion object {
        private const val STALE_SAMPLE_THRESHOLD_NS = 5_000_000_000L
        private const val RECOVERY_SNAP_NS = 500_000_000L

        private const val VERTEX_SRC = """
            attribute vec2 aPos;
            attribute vec2 aTex;
            varying vec2 vTex;
            void main() {
                vTex = aTex;
                gl_Position = vec4(aPos, 0.0, 1.0);
            }
        """

        private const val FRAGMENT_SRC = """
            precision mediump float;
            varying vec2 vTex;
            uniform sampler2D uY;
            uniform sampler2D uU;
            uniform sampler2D uV;
            void main() {
                float y = texture2D(uY, vTex).r;
                float u = texture2D(uU, vTex).r - 0.5;
                float v = texture2D(uV, vTex).r - 0.5;
                y = 1.1643 * (y - 0.0625);
                float r = y + 1.5958 * v;
                float g = y - 0.39173 * u - 0.81290 * v;
                float b = y + 2.017 * u;
                gl_FragColor = vec4(r, g, b, 1.0);
            }
        """
    }
}
