package io.haoma.calculator.messenger.calls.video

import android.net.LocalSocket
import android.net.LocalSocketAddress
import android.os.SystemClock
import io.haoma.calculator.log.Logger
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.flow.asSharedFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import java.io.BufferedInputStream
import java.io.DataInputStream
import java.io.EOFException
import java.io.IOException
import java.nio.ByteBuffer
import java.nio.ByteOrder
import java.util.concurrent.atomic.AtomicLong


class VideoFrameStream(
    val callId: String,
    val side: String,
    val unixName: String,
    private val parentScope: CoroutineScope,
    val width: Int = DEFAULT_WIDTH,
    val height: Int = DEFAULT_HEIGHT,
) {

    sealed interface StreamState {
        data object Connecting : StreamState
        data object Connected : StreamState
        data class Closed(val reason: String) : StreamState
    }

    class FrameSlot(width: Int, height: Int) {
        val buffer: ByteBuffer =
            ByteBuffer.allocateDirect(width * height * 3 / 2).order(ByteOrder.nativeOrder())
        @Volatile var ptsNs: Long = -1L
        @Volatile var painted: Boolean = true   

        fun reset() {
            buffer.clear()
            ptsNs = -1L
            painted = true
        }
    }

    private val frameBytes = width * height * 3 / 2

    private val ring: Array<FrameSlot> = Array(RING_SIZE) { FrameSlot(width, height) }
    private val ringLock = Any()

    private val _state = MutableStateFlow<StreamState>(StreamState.Connecting)
    val state: kotlinx.coroutines.flow.StateFlow<StreamState> = _state

    
    private val _frameTicks = MutableSharedFlow<Long>(extraBufferCapacity = 16)
    val frameTicks: SharedFlow<Long> = _frameTicks.asSharedFlow()

    private val lastFrameAt = AtomicLong(0L)
    val lastFrameAtElapsedNs: Long get() = lastFrameAt.get()

    private val frameCount = AtomicLong(0L)

    @Volatile private var socket: LocalSocket? = null
    @Volatile private var job: Job? = null
    @Volatile private var closed: Boolean = false

    fun start() {
        if (job != null) return
        Logger.d("call", "video_stream connect call=$callId side=$side unix=$unixName")
        job = parentScope.launch(Dispatchers.IO) { runReader() }
    }

    fun close() {
        if (closed) return
        closed = true
        val reason = when (val s = _state.value) {
            is StreamState.Closed -> s.reason
            else -> "explicit_close"
        }
        Logger.i("call", "video_stream closed call=$callId side=$side reason=$reason")
        try { socket?.close() } catch (_: Throwable) {  }
        job?.cancel()
        synchronized(ringLock) { ring.forEach { it.reset() } }
        _state.update { StreamState.Closed(reason) }
    }

    
    fun latestFrame(targetPtsNs: Long): FrameSlot? {
        synchronized(ringLock) {
            var best: FrameSlot? = null
            for (slot in ring) {
                if (slot.ptsNs < 0L) continue
                if (slot.ptsNs > targetPtsNs) continue
                if (best == null || slot.ptsNs > best.ptsNs) best = slot
            }
            best?.painted = true
            return best
        }
    }

    private suspend fun runReader() {
        val s = LocalSocket()
        socket = s
        try {
            s.connect(LocalSocketAddress(unixName, LocalSocketAddress.Namespace.ABSTRACT))
        } catch (t: Throwable) {
            Logger.e("call", "video_stream error call=$callId side=$side connect", t)
            _state.value = StreamState.Closed("connect_failed: ${t.message ?: t::class.simpleName}")
            return
        }
        Logger.d("call", "video_stream connected call=$callId side=$side")
        _state.value = StreamState.Connected

        val din = DataInputStream(BufferedInputStream(s.inputStream, frameBytes + 8))
        val scratch = ByteArray(frameBytes)
        var heartbeatDueAt = SystemClock.elapsedRealtime() + HEARTBEAT_MS

        try {
            while (kotlinx.coroutines.currentCoroutineContext().isActive && !closed) {
                val ptsNs: Long = try {
                    din.readLong()
                } catch (_: EOFException) {
                    _state.value = StreamState.Closed("eof")
                    Logger.i("call", "video_stream eof call=$callId side=$side")
                    return
                }
                din.readFully(scratch)

                val slot = pickFreeSlot()
                slot.buffer.clear()
                slot.buffer.put(scratch)
                slot.buffer.flip()
                slot.ptsNs = ptsNs
                slot.painted = false

                val count = frameCount.incrementAndGet()
                lastFrameAt.set(SystemClock.elapsedRealtimeNanos())
                _frameTicks.tryEmit(ptsNs)

                val now = SystemClock.elapsedRealtime()
                if (now >= heartbeatDueAt) {
                    heartbeatDueAt = now + HEARTBEAT_MS
                    Logger.d(
                        "call",
                        "video_stream call=$callId side=$side frames=$count lastPts=$ptsNs",
                    )
                }
            }
        } catch (e: IOException) {
            if (!closed) {
                Logger.e("call", "video_stream error call=$callId side=$side io", e)
                _state.value = StreamState.Closed("io: ${e.message ?: e::class.simpleName}")
            }
        } catch (t: Throwable) {
            if (!closed) {
                Logger.e("call", "video_stream error call=$callId side=$side", t)
                _state.value = StreamState.Closed("err: ${t.message ?: t::class.simpleName}")
            }
        } finally {
            try { s.close() } catch (_: Throwable) {  }
        }
    }

    private fun pickFreeSlot(): FrameSlot {
        synchronized(ringLock) {
            
            var victim: FrameSlot? = null
            for (slot in ring) {
                if (slot.painted) {
                    victim = slot
                    break
                }
            }
            if (victim == null) {
                victim = ring.minByOrNull { if (it.ptsNs < 0L) Long.MAX_VALUE else it.ptsNs } ?: ring[0]
            }
            return victim
        }
    }

    companion object {
        const val DEFAULT_WIDTH = 640
        const val DEFAULT_HEIGHT = 480
        private const val RING_SIZE = 4
        private const val HEARTBEAT_MS = 1_000L
    }
}
