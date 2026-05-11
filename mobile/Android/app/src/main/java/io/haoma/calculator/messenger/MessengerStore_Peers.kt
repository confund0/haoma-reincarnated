package io.haoma.calculator.messenger

import io.haoma.calculator.core.ipc.FrameType
import kotlinx.coroutines.launch


fun MessengerStore.setAlias(peerId: String, alias: String) {
    scope.launch {
        val c = ipc ?: run {
            appendStatus("alias edit: ipc not connected", level = StatusLevel.WARN)
            return@launch
        }
        try {
            val reply = c.request(
                type = FrameType.SetAlias,
                payload = SetAliasRequest(peerId, alias).toJson(),
            )
            if (reply.type == FrameType.Error) {
                val err = reply.payload?.let(ErrorPayload::fromJson)
                appendStatus("alias edit error: ${err?.message ?: "?"}", level = StatusLevel.WARN)
                return@launch
            }
            if (reply.type != FrameType.AliasUpdated) {
                appendStatus(
                    "alias edit unexpected response: ${reply.type}",
                    level = StatusLevel.WARN,
                )
                return@launch
            }
            val resp = reply.payload?.let(AliasUpdatedResponse::fromJson) ?: return@launch
            appendStatus("renamed ${shortChat(resp.peer.id)} → ${resp.peer.label.ifEmpty { "(unnamed)" }}")
        } catch (t: Throwable) {
            appendStatus("alias edit failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
        }
    }
}


fun MessengerStore.peerAction(peerId: String, action: PeerAction) {
    scope.launch {
        val c = ipc ?: run {
            appendStatus("peer action: ipc not connected", level = StatusLevel.WARN)
            return@launch
        }
        try {
            val reply = c.request(
                type = FrameType.PeerAction,
                payload = PeerActionRequest(peerId, action).toJson(),
            )
            if (reply.type == FrameType.Error) {
                val err = reply.payload?.let(ErrorPayload::fromJson)
                appendStatus("peer action error: ${err?.message ?: "?"}", level = StatusLevel.WARN)
                return@launch
            }
            if (reply.type != FrameType.PeerActionApplied) {
                appendStatus(
                    "peer action unexpected response: ${reply.type}",
                    level = StatusLevel.WARN,
                )
                return@launch
            }
            val resp = reply.payload?.let(PeerActionAppliedResponse::fromJson) ?: return@launch
            val verb = when (action) {
                PeerAction.Retire -> "unpaired"
                PeerAction.Delete -> "deleted"
            }
            val displayed = resp.peer.label.ifEmpty { "(unnamed)" }
            if (resp.deletedCount > 0) {
                appendStatus("$verb $displayed (${resp.deletedCount} events purged)")
            } else {
                appendStatus("$verb $displayed")
            }
        } catch (t: Throwable) {
            appendStatus("peer action failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
        }
    }
}


suspend fun MessengerStore.getPeerFingerprint(peerId: String): String? {
    val c = ipc ?: return null
    return try {
        val reply = c.request(
            type = FrameType.GetPeerFingerprint,
            payload = GetPeerFingerprintRequest(peerId).toJson(),
        )
        if (reply.type != FrameType.PeerFingerprint) return null
        reply.payload?.let(PeerFingerprintPayload::fromJson)?.fingerprint
    } catch (t: Throwable) {
        appendStatus("fingerprint fetch failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
        null
    }
}
