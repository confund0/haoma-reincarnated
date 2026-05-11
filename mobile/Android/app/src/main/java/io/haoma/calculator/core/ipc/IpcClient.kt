package io.haoma.calculator.core.ipc

import io.haoma.calculator.log.Logger
import java.io.File
import java.security.KeyStore
import java.security.cert.CertificateFactory
import java.security.cert.X509Certificate
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicLong
import javax.net.ssl.SSLContext
import javax.net.ssl.TrustManagerFactory
import javax.net.ssl.X509TrustManager
import kotlin.time.Duration
import kotlin.time.Duration.Companion.milliseconds
import kotlin.time.Duration.Companion.seconds
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.channels.BufferOverflow
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.delay
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asSharedFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withTimeout
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.ByteString
import org.json.JSONObject


class IpcClient(
    private val frontendDir: File,
    private val addr: String,
    private val clientName: String,
    private val clientVersion: String,
    private val scope: CoroutineScope,
    private val handshakeTimeout: Duration = HANDSHAKE_TIMEOUT,
    private val reconnectInitial: Duration = RECONNECT_INITIAL,
    private val reconnectMax: Duration = RECONNECT_MAX,
) {
    private val incomingFlow = MutableSharedFlow<Frame>(
        replay = 0,
        extraBufferCapacity = INCOMING_BUFFER,
        onBufferOverflow = BufferOverflow.DROP_OLDEST,
    )
    val incoming: SharedFlow<Frame> = incomingFlow.asSharedFlow()

    private val connectionFlow = MutableStateFlow(false)
    val connection: StateFlow<Boolean> = connectionFlow.asStateFlow()

    private val httpClient: OkHttpClient
    private val bearer: String

    @Volatile private var session: Session? = null
    private var loopJob: Job? = null

    private val pendingMu = Mutex()
    private val pending = mutableMapOf<String, CompletableDeferred<Frame>>()
    private val corrSeq = AtomicLong(0)

    init {
        val cert = readPinnedCert(File(frontendDir, "cert.pem"))
        val tm = singleCertTrustManager(cert)
        val sslCtx = SSLContext.getInstance("TLSv1.3").apply {
            init(null, arrayOf(tm), null)
        }
        httpClient = OkHttpClient.Builder()
            .sslSocketFactory(sslCtx.socketFactory, tm)
            .pingInterval(WS_PING_INTERVAL.inWholeMilliseconds, TimeUnit.MILLISECONDS)
            .build()
        bearer = readToken(File(frontendDir, "token"))
    }

    fun connect() {
        if (loopJob != null) return
        loopJob = scope.launch { runLoop() }
    }

    suspend fun close() {
        loopJob?.cancelAndJoin()
        loopJob = null
        session?.ws?.close(WS_CLOSE_NORMAL, null)
        session = null
        connectionFlow.value = false
        
        pendingMu.withLock {
            pending.values.forEach { it.cancel() }
            pending.clear()
        }
    }

    
    fun send(frame: Frame) {
        val s = session ?: return
        s.ws.send(frame.encode())
    }

    
    suspend fun request(
        type: String,
        payload: JSONObject? = null,
        timeout: Duration = REQUEST_TIMEOUT,
    ): Frame {
        val id = nextCorrId()
        val deferred = CompletableDeferred<Frame>()
        pendingMu.withLock { pending[id] = deferred }
        try {
            send(Frame(type = type, id = id, payload = payload))
            return withTimeout(timeout) { deferred.await() }
        } finally {
            pendingMu.withLock { pending.remove(id) }
        }
    }

    private fun nextCorrId(): String = "c-${corrSeq.incrementAndGet().toString(16)}"

    private suspend fun runLoop() {
        var backoff = reconnectInitial
        while (true) {
            scope.coroutineContext.ensureActive()
            val connected = runOnce()
            if (connected) backoff = reconnectInitial
            try {
                delay(backoff)
            } catch (_: Throwable) {
                return
            }
            backoff = (backoff * 2).coerceAtMost(reconnectMax)
        }
    }

    
    private suspend fun runOnce(): Boolean {
        val frames = Channel<RawEvent>(Channel.UNLIMITED)
        val request = Request.Builder()
            .url("wss://$addr/ws")
            .header("Authorization", "Bearer $bearer")
            .build()
        val listener = ChannelListener(frames)
        val ws = httpClient.newWebSocket(request, listener)
        val s = Session(ws, frames)
        session = s

        try {
            
            
            val sentHello = withTimeout(handshakeTimeout) {
                while (true) {
                    when (val ev = frames.receive()) {
                        is RawEvent.Open -> {
                            ws.send(helloFrame().encode())
                            return@withTimeout true
                        }
                        is RawEvent.Failure -> {
                            Logger.w("ipc", "dial failed: ${ev.message}")
                            return@withTimeout false
                        }
                        is RawEvent.Closing, is RawEvent.Closed -> return@withTimeout false
                        else -> {  }
                    }
                }
                @Suppress("UNREACHABLE_CODE") false
            }
            if (!sentHello) return false

            val welcome = withTimeout(handshakeTimeout) {
                awaitNextFrame(frames)
            } ?: return false

            if (welcome.type != FrameType.Welcome) {
                Logger.w("ipc", "expected welcome, got type=${welcome.type}")
                return false
            }
            
            incomingFlow.emit(welcome)
            connectionFlow.value = true
            Logger.i("ipc", "handshake ok at wss://$addr/ws")

            
            while (true) {
                val ev = frames.receive()
                when (ev) {
                    is RawEvent.Frame -> dispatch(ev.frame)
                    is RawEvent.Failure -> {
                        Logger.w("ipc", "ws failure: ${ev.message}")
                        return true
                    }
                    is RawEvent.Closing, is RawEvent.Closed -> {
                        Logger.i("ipc", "ws closed")
                        return true
                    }
                    is RawEvent.Open -> {  }
                }
            }
        } catch (t: Throwable) {
            Logger.w("ipc", "session ended: ${t.message}")
            return connectionFlow.value
        } finally {
            connectionFlow.value = false
            session = null
            try {
                ws.close(WS_CLOSE_NORMAL, null)
            } catch (_: Throwable) {
                
            }
        }
    }

    private suspend fun awaitNextFrame(frames: Channel<RawEvent>): Frame? {
        while (true) {
            when (val ev = frames.receive()) {
                is RawEvent.Frame -> return ev.frame
                is RawEvent.Failure, is RawEvent.Closing, is RawEvent.Closed -> return null
                is RawEvent.Open -> {  }
            }
        }
    }

    private suspend fun dispatch(frame: Frame) {
        
        if (!frame.id.isNullOrEmpty()) {
            val deferred = pendingMu.withLock { pending.remove(frame.id) }
            if (deferred != null) {
                deferred.complete(frame)
                return
            }
        }
        
        if (frame.type == FrameType.Ping) {
            send(Frame(type = FrameType.Pong, id = frame.id))
            return
        }
        incomingFlow.emit(frame)
    }

    private fun helloFrame(): Frame {
        val payload = JSONObject().apply {
            put("client_name", clientName)
            if (clientVersion.isNotEmpty()) put("client_version", clientVersion)
        }
        return Frame(type = FrameType.Hello, id = "handshake", payload = payload)
    }

    private data class Session(val ws: WebSocket, val frames: Channel<RawEvent>)

    private sealed interface RawEvent {
        data object Open : RawEvent
        data class Frame(val frame: io.haoma.calculator.core.ipc.Frame) : RawEvent
        data class Closing(val code: Int, val reason: String) : RawEvent
        data class Closed(val code: Int, val reason: String) : RawEvent
        data class Failure(val message: String) : RawEvent
    }

    private class ChannelListener(private val out: Channel<RawEvent>) : WebSocketListener() {
        override fun onOpen(webSocket: WebSocket, response: Response) {
            out.trySend(RawEvent.Open)
        }
        override fun onMessage(webSocket: WebSocket, text: String) {
            try {
                out.trySend(RawEvent.Frame(Frame.decode(text)))
            } catch (t: Throwable) {
                out.trySend(RawEvent.Failure("decode: ${t.message}"))
            }
        }
        override fun onMessage(webSocket: WebSocket, bytes: ByteString) {
            out.trySend(RawEvent.Failure("unexpected binary frame (${bytes.size} bytes)"))
        }
        override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
            out.trySend(RawEvent.Closing(code, reason))
        }
        override fun onClosed(webSocket: WebSocket, code: Int, reason: String) {
            out.trySend(RawEvent.Closed(code, reason))
            out.close()
        }
        override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
            out.trySend(RawEvent.Failure(t.message ?: t.javaClass.simpleName))
            out.close()
        }
    }

    companion object {
        private val HANDSHAKE_TIMEOUT: Duration = 10.seconds
        private val RECONNECT_INITIAL: Duration = 500.milliseconds
        private val RECONNECT_MAX: Duration = 30.seconds
        private val REQUEST_TIMEOUT: Duration = 10.seconds
        private val WS_PING_INTERVAL: Duration = 30.seconds
        private const val INCOMING_BUFFER = 64
        private const val WS_CLOSE_NORMAL = 1000
    }
}


private fun readPinnedCert(certFile: File): X509Certificate {
    require(certFile.exists()) { "ipc: cert.pem missing at ${certFile.absolutePath}" }
    val factory = CertificateFactory.getInstance("X.509")
    return certFile.inputStream().use { stream ->
        factory.generateCertificate(stream) as? X509Certificate
            ?: error("ipc: cert.pem at ${certFile.absolutePath} is not X.509")
    }
}

private fun singleCertTrustManager(cert: X509Certificate): X509TrustManager {
    val keyStore = KeyStore.getInstance(KeyStore.getDefaultType()).apply {
        load(null, null)
        setCertificateEntry("haoma-frontend", cert)
    }
    val tmf = TrustManagerFactory.getInstance(TrustManagerFactory.getDefaultAlgorithm()).apply {
        init(keyStore)
    }
    val managers = tmf.trustManagers.filterIsInstance<X509TrustManager>()
    require(managers.size == 1) { "ipc: expected exactly one X509TrustManager, got ${managers.size}" }
    return managers.first()
}

private fun readToken(tokenFile: File): String {
    require(tokenFile.exists()) { "ipc: token missing at ${tokenFile.absolutePath}" }
    val raw = tokenFile.readText(Charsets.UTF_8).trim()
    require(raw.isNotEmpty()) { "ipc: token at ${tokenFile.absolutePath} is empty" }
    return raw
}
