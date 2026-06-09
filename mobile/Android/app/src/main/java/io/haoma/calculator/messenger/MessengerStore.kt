package io.haoma.calculator.messenger

import android.content.Context
import io.haoma.calculator.core.BinaryFingerprints
import io.haoma.calculator.core.DisguiseStore
import io.haoma.calculator.core.VaultSession
import io.haoma.calculator.core.computeFingerprints
import io.haoma.calculator.core.ipc.IpcClient
import io.haoma.calculator.log.Logger
import io.haoma.calculator.messenger.calls.video.CameraSource
import io.haoma.calculator.messenger.calls.video.VideoFrameStream
import java.io.File
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import java.util.concurrent.atomic.AtomicLong
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.drop
import kotlinx.coroutines.flow.launchIn
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.onEach
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.flow.stateIn
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking


class MessengerStore(
    private val clientName: String = "haoma-android",
    private val clientVersion: String = "",
    internal val vaultSessionProvider: () -> VaultSession? = { null },
    internal val disguise: DisguiseStore? = null,
    internal val notificationPoster: io.haoma.calculator.notifications.NotificationPoster? = null,
    internal val appContext: Context? = null,
) {
    internal val scope = CoroutineScope(
        SupervisorJob() + Dispatchers.Default + Logger.coroutineExceptionHandler,
    )

    
    internal val _peers = MutableStateFlow<List<PeerEntry>>(emptyList())
    val peers: StateFlow<List<PeerEntry>> = _peers.asStateFlow()

    internal val _chats = MutableStateFlow<List<ChatEntry>>(emptyList())
    val chats: StateFlow<List<ChatEntry>> = _chats.asStateFlow()

    internal val _presence = MutableStateFlow<Map<String, String>>(emptyMap())
    val presence: StateFlow<Map<String, String>> = _presence.asStateFlow()

    internal val _health = MutableStateFlow(SystemHealth.INITIAL)
    val health: StateFlow<SystemHealth> = _health.asStateFlow()

    internal val _statusLog = MutableStateFlow<List<StatusLine>>(emptyList())
    val statusLog: StateFlow<List<StatusLine>> = _statusLog.asStateFlow()

    internal val _torInfoSnapshot = MutableStateFlow<TorInfoResponse?>(null)
    val torInfoSnapshot: StateFlow<TorInfoResponse?> = _torInfoSnapshot.asStateFlow()

    
    internal val _fingerprints = MutableStateFlow<BinaryFingerprints?>(null)
    val fingerprints: StateFlow<BinaryFingerprints?> = _fingerprints.asStateFlow()

    
    internal val _systemInfo = MutableStateFlow<SystemInfoResponse?>(null)
    val systemInfo: StateFlow<SystemInfoResponse?> = _systemInfo.asStateFlow()

    internal val _backStack = MutableStateFlow<List<Screen>>(listOf(Screen.Tabbed(Tab.Chats)))
    val backStack: StateFlow<List<Screen>> = _backStack.asStateFlow()

    
    internal val _noticePassphraseIsDefault = MutableStateFlow(false)
    internal val _noticeSnooze = MutableStateFlow<Map<String, NoticeSnoozeEntry>>(emptyMap())

    
    val notices: StateFlow<List<Notice>> = combine(
        _noticePassphraseIsDefault,
        _noticeSnooze,
    ) { passphraseDefault, snooze ->
        produceNotices(passphraseDefault, snooze)
    }.stateIn(scope, SharingStarted.Eagerly, emptyList())

    
    val unsnoozedNotices: StateFlow<List<Notice>> = combine(
        notices,
        kotlinx.coroutines.flow.flow {
            while (true) {
                emit(System.currentTimeMillis())
                kotlinx.coroutines.delay(60_000L)
            }
        },
    ) { all, now ->
        all.filter { it.snoozeUntil <= now }
    }.stateIn(scope, SharingStarted.Eagerly, emptyList())

    internal val _connection = MutableStateFlow(false)
    val connection: StateFlow<Boolean> = _connection.asStateFlow()

    internal val _pendingInvites = MutableStateFlow<List<PendingInvite>>(emptyList())
    val pendingInvites: StateFlow<List<PendingInvite>> = _pendingInvites.asStateFlow()

    internal val _recentInvites = MutableStateFlow<List<RecentInvite>>(emptyList())
    val recentInvites: StateFlow<List<RecentInvite>> = _recentInvites.asStateFlow()

    
    internal val _freshPeers = MutableStateFlow<Set<String>>(emptySet())
    val freshPeers: StateFlow<Set<String>> = _freshPeers.asStateFlow()

    
    fun markFreshPeer(peerId: String) {
        _freshPeers.update { it + peerId }
    }

    
    fun clearFreshPeer(peerId: String) {
        _freshPeers.update { it - peerId }
    }

    
    fun clearAllFreshPeers() {
        _freshPeers.value = emptySet()
    }

    
    internal val _activeCalls = MutableStateFlow<Map<String, CallEntry>>(emptyMap())
    val activeCalls: StateFlow<Map<String, CallEntry>> = _activeCalls.asStateFlow()

    
    internal val _mutedCalls = MutableStateFlow<Map<String, Boolean>>(emptyMap())
    val mutedCalls: StateFlow<Map<String, Boolean>> = _mutedCalls.asStateFlow()

    
    internal val _callStreamState = MutableStateFlow<Map<String, CallStreamState>>(emptyMap())
    val callStreamState: StateFlow<Map<String, CallStreamState>> = _callStreamState.asStateFlow()

    
    internal val _recordAudioGranted = MutableStateFlow(false)
    val recordAudioGranted: StateFlow<Boolean> = _recordAudioGranted.asStateFlow()

    
    internal val _bluetoothConnectGranted = MutableStateFlow(false)
    val bluetoothConnectGranted: StateFlow<Boolean> = _bluetoothConnectGranted.asStateFlow()

    
    internal val _cameraGranted = MutableStateFlow(false)
    val cameraGranted: StateFlow<Boolean> = _cameraGranted.asStateFlow()

    
    internal val _videoRawUnixNames = MutableStateFlow<Map<String, Map<String, String>>>(emptyMap())
    val videoRawUnixNames: StateFlow<Map<String, Map<String, String>>> = _videoRawUnixNames.asStateFlow()

    
    internal val _videoStreams = MutableStateFlow<Map<String, Map<String, VideoFrameStream>>>(emptyMap())

    
    internal val _cameraSources = MutableStateFlow<Map<String, CameraSource>>(emptyMap())

    
    internal val _callClockSamples = MutableStateFlow<Map<String, ClockSample>>(emptyMap())
    val callClockSamples: StateFlow<Map<String, ClockSample>> = _callClockSamples.asStateFlow()

    
    internal val _videoMutedCalls = MutableStateFlow<Map<String, Boolean>>(emptyMap())
    val videoMutedCalls: StateFlow<Map<String, Boolean>> = _videoMutedCalls.asStateFlow()

    
    internal val _peerVideoMutedCalls = MutableStateFlow<Map<String, Boolean>>(emptyMap())
    val peerVideoMutedCalls: StateFlow<Map<String, Boolean>> = _peerVideoMutedCalls.asStateFlow()

    
    internal val _videoFacing =
        MutableStateFlow<Map<String, io.haoma.calculator.messenger.calls.video.CameraFacing>>(emptyMap())
    val videoFacing: StateFlow<Map<String, io.haoma.calculator.messenger.calls.video.CameraFacing>> =
        _videoFacing.asStateFlow()

    
    val videoCallActive: StateFlow<Boolean> = _activeCalls
        .map { calls -> calls.values.any { !it.isTerminal && CallModality.Video in it.modalities } }
        .distinctUntilChanged()
        .stateIn(scope, SharingStarted.Eagerly, false)

    
    internal val _callWindowOpen = MutableStateFlow(false)
    val callWindowOpen: StateFlow<Boolean> = _callWindowOpen.asStateFlow()

    
    @Volatile
    var audioRouter: io.haoma.calculator.messenger.calls.AudioRouter? = null
        internal set

    
    internal val _imageBytesByMsgId = MutableStateFlow<Map<String, ByteArray>>(emptyMap())
    val imageBytesByMsgId: StateFlow<Map<String, ByteArray>> = _imageBytesByMsgId.asStateFlow()

    
    internal val _openTransientMsgIds = MutableStateFlow<Set<String>>(emptySet())

    
    internal val _viewerTarget = MutableStateFlow<ViewerTarget?>(null)
    val viewerTarget: StateFlow<ViewerTarget?> = _viewerTarget.asStateFlow()

    
    internal val _imageDimsByMsgId = MutableStateFlow<Map<String, Pair<Int, Int>>>(emptyMap())
    val imageDimsByMsgId: StateFlow<Map<String, Pair<Int, Int>>> = _imageDimsByMsgId.asStateFlow()

    
    internal val _videoTransientByMsgId = MutableStateFlow<Map<String, String>>(emptyMap())
    val videoTransientByMsgId: StateFlow<Map<String, String>> = _videoTransientByMsgId.asStateFlow()
    internal val _videoThumbsByMsgId = MutableStateFlow<Map<String, android.graphics.Bitmap?>>(emptyMap())
    val videoThumbsByMsgId: StateFlow<Map<String, android.graphics.Bitmap?>> = _videoThumbsByMsgId.asStateFlow()

    
    internal val _videoViewerTarget = MutableStateFlow<VideoViewerTarget?>(null)
    val videoViewerTarget: StateFlow<VideoViewerTarget?> = _videoViewerTarget.asStateFlow()

    
    internal val _timelines = MutableStateFlow<Map<String, TimelineCache>>(emptyMap())

    
    internal val _drafts = MutableStateFlow<Map<String, String>>(emptyMap())
    val drafts: StateFlow<Map<String, String>> = _drafts.asStateFlow()

    
    internal val _replyTargets = MutableStateFlow<Map<String, TimelineEvent>>(emptyMap())
    val replyTargets: StateFlow<Map<String, TimelineEvent>> = _replyTargets.asStateFlow()

    
    internal val _pendingAnchors = MutableStateFlow<Map<String, AnchorState>>(emptyMap())
    val pendingAnchors: StateFlow<Map<String, AnchorState>> = _pendingAnchors.asStateFlow()

    
    internal val anchorSeq = AtomicLong(0L)

    
    internal val _chatSearch = MutableStateFlow<ChatSearchState?>(null)
    val chatSearch: StateFlow<ChatSearchState?> = _chatSearch.asStateFlow()

    
    private val envelopeIndex = HashMap<String, String>()

    
    val current: StateFlow<Screen> = _backStack
        .map { it.lastOrNull() ?: Screen.Tabbed(Tab.Chats) }
        .stateIn(scope, SharingStarted.Eagerly, _backStack.value.last())

    
    @Volatile internal var ipc: IpcClient? = null

    
    @Volatile private var ipcIncomingJob: Job? = null
    @Volatile private var ipcConnectionJob: Job? = null

    init {
        
        
        val ctx = appContext
        if (ctx != null) {
            scope.launch(Dispatchers.IO) {
                try {
                    _fingerprints.value = computeFingerprints(ctx)
                } catch (t: Throwable) {
                    Logger.w("messenger", "fingerprints compute failed: ${t.message}")
                }
            }
            
            
            videoCallActive
                .drop(1)
                .distinctUntilChanged()
                .onEach { active ->
                    Logger.i("call", "videoCallActive=$active — refreshing FGS type")
                    io.haoma.calculator.core.HaomaCoreService.refreshType(ctx)
                }
                .launchIn(scope)

            
            scope.launch {
                val staleSince = mutableMapOf<String, Long>()
                while (true) {
                    delay(1_000L)
                    val now = System.currentTimeMillis()
                    val calls = _activeCalls.value
                    val states = _callStreamState.value
                    val it = staleSince.entries.iterator()
                    while (it.hasNext()) {
                        if (it.next().key !in calls) it.remove()
                    }
                    for ((callId, call) in calls) {
                        if (call.status != CallStatus.Accepted) continue
                        val s = states[callId] ?: continue
                        val mic = s.mic ?: continue
                        val spk = s.spk ?: continue
                        val sampleStale = (now - mic.lastSampleAtMs) > 5_000L &&
                            (now - spk.lastSampleAtMs) > 5_000L
                        val peerSilent = spk.prevFramesIn != 0L &&
                            spk.framesIn == spk.prevFramesIn
                        val dead = sampleStale || peerSilent
                        if (dead) {
                            val since = staleSince[callId]
                            if (since == null) {
                                staleSince[callId] = now
                                val reason = if (peerSilent) "peer-silent" else "bilateral-stale"
                                Logger.w("call", "dead-call signal entered call=${shortCallId(callId)} reason=$reason")
                            } else if (now - since > 10_000L) {
                                val reason = if (peerSilent) "peer-silent" else "bilateral-stale"
                                Logger.w(
                                    "call",
                                    "dead-call >10s call=${shortCallId(callId)} reason=$reason — auto-hangup",
                                )
                                staleSince.remove(callId)
                                respondCall(callId, CallAction.End, reason = CallEndReasonAutoHangup)
                            }
                        } else if (staleSince.remove(callId) != null) {
                            Logger.i("call", "dead-call signal cleared call=${shortCallId(callId)}")
                        }
                    }
                }
            }
        }
    }

    
    @Volatile internal var currentFocusChatId: String = ""

    @Volatile
    internal var lastSoftLocked: Boolean = false

    
    fun onDaemonsReady(haomaAddr: String, frontendDir: File) {
        if (ipc != null) {
            Logger.w("messenger", "onDaemonsReady called while ipc already attached; ignoring")
            return
        }
        appendStatus("daemons up; dialing IPC at wss://$haomaAddr/ws")
        val client = IpcClient(
            frontendDir = frontendDir,
            addr = haomaAddr,
            clientName = clientName,
            clientVersion = clientVersion,
            scope = scope,
        )
        ipc = client

        ipcIncomingJob = client.incoming
            .onEach { dispatch(it) }
            .launchIn(scope)

        ipcConnectionJob = client.connection
            .drop(1) 
            .distinctUntilChanged()
            .onEach { up ->
                _connection.value = up
                if (up) {
                    appendStatus("ipc connected — subscribing")
                    bootstrapAfterConnect()
                } else {
                    appendStatus("ipc disconnected; reconnecting")
                }
            }
            .launchIn(scope)

        client.connect()
    }

    fun onDaemonsStopped() {
        val client = ipc ?: return
        ipc = null
        appendStatus("daemons stopping; closing IPC")
        
        
        try {
            runBlocking { client.close() }
        } catch (t: Throwable) {
            Logger.e("messenger", "ipc close on teardown", t)
        }
        
        
        ipcIncomingJob?.cancel()
        ipcIncomingJob = null
        ipcConnectionJob?.cancel()
        ipcConnectionJob = null
        _connection.value = false
        
        
        notificationPoster?.cancelAll()
        
        
        lastSoftLocked = false
        
        
        closeAllVideoStreams()
        _activeCalls.value = emptyMap()
        _mutedCalls.value = emptyMap()
        _callStreamState.value = emptyMap()
        _callClockSamples.value = emptyMap()
        _videoMutedCalls.value = emptyMap()
        _peerVideoMutedCalls.value = emptyMap()
        _videoFacing.value = emptyMap()
        _callWindowOpen.value = false
        
        
        _drafts.value = emptyMap()
        _replyTargets.value = emptyMap()
        _pendingAnchors.value = emptyMap()
        _chatSearch.value = null
    }

    
    private fun bootstrapAfterConnect() {
        scope.launch {
            val c = ipc ?: return@launch
            try {
                val sub = c.request(
                    type = io.haoma.calculator.core.ipc.FrameType.Subscribe,
                    payload = SubscribeRequest().toJson(),
                )
                val topics = sub.payload?.let { SubscribedResponse.fromJson(it).topics } ?: emptyList()
                appendStatus(
                    "subscribed — topics=" + if (topics.isEmpty()) "(all)" else topics.joinToString(","),
                )
                
                
                emitLockState(lastSoftLocked)
                
                
                pushSettingsSync()
                fetchPeers(c)
                fetchChats(c)
                fetchTorInfo(c)
                fetchSystemInfo(c)
                
                
                for (chatId in _timelines.value.keys) {
                    loadTimeline(chatId, head = true)
                }
                
                
                requestExternalProbeBurst()
                requestSelfProbeForActiveSurface()
            } catch (t: Throwable) {
                Logger.w("messenger", "bootstrap failed: ${t.message}")
                appendStatus("bootstrap failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
            }
        }
    }

    private suspend fun fetchPeers(c: IpcClient) {
        try {
            val reply = c.request(type = io.haoma.calculator.core.ipc.FrameType.ListPeers)
            val peers = reply.payload?.peerArray("peers").orEmpty()
            _peers.value = peers
            appendStatus("peers snapshot: ${peers.size} row(s)")
        } catch (t: Throwable) {
            appendStatus("list_peers failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
        }
    }

    private suspend fun fetchChats(c: IpcClient) {
        try {
            val reply = c.request(type = io.haoma.calculator.core.ipc.FrameType.ListChats)
            val chats = reply.payload?.chatArray("chats").orEmpty()
            _chats.value = chats
            appendStatus("chats snapshot: ${chats.size} row(s)")
        } catch (t: Throwable) {
            appendStatus("list_chats failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
        }
    }

    private suspend fun fetchSystemInfo(c: IpcClient) {
        try {
            val reply = c.request(type = io.haoma.calculator.core.ipc.FrameType.SystemInfo)
            val info = reply.payload?.let(SystemInfoResponse::fromJson) ?: return
            _systemInfo.value = info
        } catch (t: Throwable) {
            appendStatus("system_info failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
        }
    }

    private suspend fun fetchTorInfo(c: IpcClient) {
        try {
            val reply = c.request(type = io.haoma.calculator.core.ipc.FrameType.TorInfo)
            val info = reply.payload?.let(TorInfoResponse::fromJson) ?: return
            _torInfoSnapshot.value = info
            _health.update {
                it.copy(
                    tor = info.health,
                    onionCount = info.slots.size,
                )
            }
        } catch (t: Throwable) {
            appendStatus("tor_info failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
        }
    }

    
    fun refreshTorInfo() {
        scope.launch {
            val c = ipc
            if (c == null) {
                appendStatus("tor_info refresh: ipc not connected", level = StatusLevel.WARN)
                return@launch
            }
            fetchTorInfo(c)
        }
    }

    
    internal fun upsertPeer(p: PeerEntry) {
        _peers.update { list ->
            val idx = list.indexOfFirst { it.id == p.id }
            if (idx < 0) list + p else list.toMutableList().apply { this[idx] = p }
        }
        if (p.effective.isNotEmpty()) {
            _presence.update { it + (p.id to p.effective) }
        }
    }

    internal fun upsertChat(c: ChatEntry) {
        _chats.update { list ->
            val idx = list.indexOfFirst { it.chatId == c.chatId }
            if (idx < 0) list + c else list.toMutableList().apply { this[idx] = c }
        }
    }

    internal fun upsertTimeline(chatId: String, transform: (TimelineCache) -> TimelineCache) {
        _timelines.update { map ->
            val current = map[chatId] ?: TimelineCache(chatId = chatId)
            val next = transform(current)
            if (next === current) map else map + (chatId to next)
        }
    }

    internal fun setDraft(chatId: String, text: String) {
        if (chatId.isEmpty()) return
        _drafts.update { map ->
            if (text.isEmpty()) {
                if (chatId in map) map - chatId else map
            } else {
                if (map[chatId] == text) map else map + (chatId to text)
            }
        }
    }

    internal fun clearDraft(chatId: String) {
        if (chatId.isEmpty()) return
        _drafts.update { if (chatId in it) it - chatId else it }
    }

    internal fun setReplyTarget(chatId: String, target: TimelineEvent) {
        if (chatId.isEmpty()) return
        _replyTargets.update { map ->
            if (map[chatId]?.msgId == target.msgId) map else map + (chatId to target)
        }
    }

    internal fun clearReplyTarget(chatId: String) {
        if (chatId.isEmpty()) return
        _replyTargets.update { if (chatId in it) it - chatId else it }
    }

    internal fun mapPeer(peerId: String, transform: (PeerEntry) -> PeerEntry) {
        _peers.update { list -> list.map { if (it.id == peerId) transform(it) else it } }
    }

    internal fun mapChat(chatId: String, transform: (ChatEntry) -> ChatEntry) {
        _chats.update { list -> list.map { if (it.chatId == chatId) transform(it) else it } }
    }

    
    internal fun peerIdOrWarn(chatId: String, verb: String): String? {
        val pid = _chats.value.firstOrNull { it.chatId == chatId }?.peerId.orEmpty()
        if (pid.isEmpty()) {
            appendStatus("$verb: chat not found ($chatId)", level = StatusLevel.WARN)
            return null
        }
        return pid
    }

    internal fun rememberEnvelope(envelopeId: String, chatId: String) {
        synchronized(envelopeIndex) { envelopeIndex[envelopeId] = chatId }
    }

    internal fun lookupEnvelope(envelopeId: String): String? =
        synchronized(envelopeIndex) { envelopeIndex[envelopeId] }

    internal fun forgetEnvelopesFor(chatId: String) {
        synchronized(envelopeIndex) {
            val drop = envelopeIndex.entries.filter { it.value == chatId }.map { it.key }
            for (k in drop) envelopeIndex.remove(k)
        }
    }

    internal fun indexEnvelopes(chatId: String, events: List<TimelineEvent>) {
        synchronized(envelopeIndex) {
            for (ev in events) {
                if (ev.isOutbound && ev.envelopeId.isNotEmpty()) {
                    envelopeIndex[ev.envelopeId] = chatId
                }
            }
        }
    }

    
    internal fun movePendingToRecent(
        handleId: String,
        outcome: RecentOutcome,
        peerId: String = "",
        nick: String = "",
        reason: String = "",
    ) {
        val pending = _pendingInvites.value.firstOrNull { it.handleId == handleId } ?: return
        _pendingInvites.update { list -> list.filterNot { it.handleId == handleId } }
        val entry = RecentInvite(
            handleId = handleId,
            alias = pending.alias,
            outcome = outcome,
            peerId = peerId,
            nick = nick,
            reason = reason,
            at = System.currentTimeMillis(),
        )
        _recentInvites.update { list ->
            val next = listOf(entry) + list
            if (next.size > RECENT_INVITES_CAP) next.take(RECENT_INVITES_CAP) else next
        }
    }

    
    internal fun appendStatus(
        text: String,
        level: StatusLevel = StatusLevel.INFO,
        source: StatusSource = StatusSource.System,
    ) {
        val line = StatusLine(at = System.currentTimeMillis(), text = text, level = level, source = source)
        _statusLog.update { list ->
            val next = list + line
            
            
            if (next.size > STATUS_LOG_CAP) next.takeLast(STATUS_LOG_CAP) else next
        }
        
        
        if (source == StatusSource.System) {
            when (level) {
                StatusLevel.WARN -> Logger.w("status", text)
                else -> Logger.i("status", text)
            }
        }
    }

    internal fun shortChat(id: String): String =
        if (id.length > 8) id.substring(0, 8) else id

    companion object {
        private const val STATUS_LOG_CAP = 500
        private const val RECENT_INVITES_CAP = 5
    }
}


data class SystemHealth(
    val backendReachable: Boolean,
    val tor: TorHealth,
    val onionCount: Int,
    val selfNick: String,
    val selfNickIsDefault: Boolean,
    val daemonVersion: String,
    val protocolVersion: Int,
    
    val externalReach: ExternalReach? = null,
    
    val selfReach: Map<String, SelfReach> = emptyMap(),
    

    val backendStatusAt: Long = 0L,
) {
    companion object {
        val INITIAL = SystemHealth(
            backendReachable = false,
            tor = TorHealth.ZERO,
            onionCount = 0,
            selfNick = "",
            selfNickIsDefault = true,
            daemonVersion = "",
            protocolVersion = 0,
        )
    }
}


data class ExternalReach(val ok: Boolean, val lastTarget: String, val at: Long)


data class SelfReach(val onion: String, val ok: Boolean, val at: Long)


data class ViewerTarget(
    val chatId: String,
    val msgId: String,
    val displayName: String,
)


data class VideoViewerTarget(
    val chatId: String,
    val msgId: String,
    val displayName: String,
    val path: String,
    val mime: String,
)

enum class StatusLevel { INFO, WARN }


enum class StatusSource { System, Cli }

data class StatusLine(
    val at: Long,
    val text: String,
    val level: StatusLevel,
    val source: StatusSource = StatusSource.System,
) {
    fun stamp(): String = TS_FMT.format(Date(at))

    companion object {
        private val TS_FMT = SimpleDateFormat("HH:mm:ss", Locale.US)
    }
}
