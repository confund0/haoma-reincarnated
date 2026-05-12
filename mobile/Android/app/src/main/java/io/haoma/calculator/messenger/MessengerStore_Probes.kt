package io.haoma.calculator.messenger

import io.haoma.calculator.core.ipc.FrameType
import io.haoma.calculator.log.Logger
import kotlinx.coroutines.launch


private const val PROBES_LOG_TAG = "MessengerStore.Probes"


fun MessengerStore.requestExternalProbeBurst() {
    
    
    val cached = health.value.externalReach
    if (cached != null && cached.ok) {
        val ageSecs = System.currentTimeMillis() / 1000L - cached.at
        if (ageSecs < EXTERNAL_FRESH_SECS) {
            Logger.d(PROBES_LOG_TAG, "external burst skipped — cached ok ${ageSecs}s old")
            return
        }
    }
    scope.launch {
        val c = ipc ?: run {
            Logger.d(PROBES_LOG_TAG, "external burst skipped — ipc not connected yet")
            return@launch
        }
        Logger.d(PROBES_LOG_TAG, "external burst kicked")
        try {
            c.request(
                type = FrameType.ExternalProbeBurst,
                payload = ExternalProbeBurstRequest().toJson(),
            )
        } catch (t: Throwable) {
            Logger.d(PROBES_LOG_TAG, "external burst request failed: ${t.message}")
        }
    }
}


private const val EXTERNAL_FRESH_SECS = 30L


fun MessengerStore.requestPeerSelfProbe(peerId: String) {
    if (peerId.isEmpty()) return
    scope.launch {
        val c = ipc ?: return@launch
        try {
            c.request(
                type = FrameType.PeerSelfProbe,
                payload = PeerSelfProbeRequest(peerId).toJson(),
            )
        } catch (t: Throwable) {
            Logger.d(PROBES_LOG_TAG, "self probe request failed: ${t.message}")
        }
    }
}


fun MessengerStore.requestSelfProbeForActiveSurface() {
    val screen = current.value
    val peerId: String = when (screen) {
        is Screen.ChatDetail -> peerIdForChat(screen.chatId)
        is Screen.ChatSettings -> peerIdForChat(screen.chatId)
        is Screen.ContactDetail -> screen.peerId
        else -> randomPairedPeerId()
    } ?: return
    requestPeerSelfProbe(peerId)
}


private fun MessengerStore.randomPairedPeerId(): String? {
    val candidates = _peers.value.filter { it.retiredAt == 0L }
    if (candidates.isEmpty()) return null
    return candidates.random().id
}


private fun MessengerStore.peerIdForChat(chatId: String): String? {
    val chat = _chats.value.firstOrNull { it.chatId == chatId } ?: return null
    return chat.peerId.takeIf { it.isNotEmpty() }
}
