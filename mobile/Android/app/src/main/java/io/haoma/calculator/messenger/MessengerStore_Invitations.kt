package io.haoma.calculator.messenger

import io.haoma.calculator.core.ipc.Frame
import io.haoma.calculator.core.ipc.FrameType
import io.haoma.calculator.log.Logger
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import org.json.JSONObject


data class InvitationsView(
    val entries: List<InvitationEntry> = emptyList(),
    val listFailure: List<String> = emptyList(),
    val acceptingMsgId: String = "",
    val acceptingName: String = "",
    val outcome: AcceptOutcome? = null,
    val note: String = "",
) {
    

    fun acceptable(nowSeconds: Long): List<InvitationEntry> =
        entries.filter { it.direction == InvitationDirection.IN && it.standing(nowSeconds) }
}


data class AcceptOutcome(val ok: Boolean, val lines: List<String>, val torSlow: Boolean = false)


internal suspend fun MessengerStore.refreshInvitations() {
    if (!_connection.value) return
    val generation = invitationsListSeq.incrementAndGet()
    val reply = askUnlessDropped(FrameType.InvitationsList, JSONObject(), inheritedPairingTimings.listTimeout)
    if (invitationsListSeq.get() != generation) return
    val payload = reply.payload
    if (reply.type == FrameType.InvitationsListed && payload != null) {
        applyInvitationsList(InvitationsListedResponse.fromJson(payload).invitations, generation)
        return
    }
    val reason = reasonOf(reply)
    Logger.w("pairing", "the invitations could not be listed: ${normaliseReason(reason)}")
    _invitations.update { it.copy(listFailure = explainInvitationsListRefusal(reason).lines) }
}


fun MessengerStore.requestInvitations() {
    if (!_connection.value) {
        _invitations.update { it.copy(note = READ_AGAIN_NO_DAEMON) }
        return
    }
    _invitations.update { it.copy(note = "") }
    scope.launch { refreshInvitations() }
}


internal fun MessengerStore.applyInvitationsList(entries: List<InvitationEntry>, generation: Long) {
    val now = inheritedPairingTimings.nowSeconds()
    val standing = HashMap<String, Long>()
    for (e in entries) {
        if (e.direction != InvitationDirection.OUT || e.fingerprint.isEmpty()) continue
        if (e.state != InvitationState.LIVE || e.expiresAt <= 0L) continue
        standing[e.fingerprint] = maxOf(standing[e.fingerprint] ?: 0L, e.expiresAt)
    }
    _invitations.update { it.copy(entries = entries, listFailure = emptyList()) }
    _inheritedPairing.update { st ->
        val applied = maxOf(st.appliedListSeq, generation)
        
        
        val lost = st.lostOffer?.takeUnless {
            generation > it.sentListSeq && (standing[it.fingerprint] ?: 0L) > now
        }
        st.copy(standingOut = standing, appliedListSeq = applied, lostOffer = lost)
    }
    Logger.d("pairing", "invitations: ${entries.size} row(s), ${standing.size} from this device")
}


internal fun MessengerStore.onPairingCompleted() {
    scope.launch {
        refreshInheritedContacts()
        refreshInvitations()
    }
}


internal fun MessengerStore.onInvitationMarker() {
    scope.launch { refreshInvitations() }
}


fun MessengerStore.acceptInvitation(msgId: String) {
    val entry = _invitations.value.entries.firstOrNull { it.msgId == msgId } ?: return
    val name = entry.label.ifEmpty { "that contact" }
    val now = inheritedPairingTimings.nowSeconds()
    if (entry.direction != InvitationDirection.IN) {
        _invitations.update { it.copy(outcome = AcceptOutcome(false, listOf(ACCEPT_OWN_INVITATION))) }
        return
    }
    if (!entry.standing(now)) {
        _invitations.update {
            it.copy(outcome = AcceptOutcome(false, listOf(ACCEPT_NOT_STANDING, askForAnother(name))))
        }
        return
    }
    if (!_connection.value) {
        _invitations.update { it.copy(outcome = AcceptOutcome(false, listOf(ACCEPT_NOT_SENT))) }
        return
    }
    var claimed = false
    _invitations.update { v ->
        claimed = v.acceptingMsgId.isEmpty()
        if (claimed) v.copy(acceptingMsgId = msgId, acceptingName = name, outcome = null) else v
    }
    if (!claimed) return
    Logger.d("pairing", "accepting an invitation")
    scope.launch {
        val reply = askUnlessDropped(
            FrameType.InvitationAccept,
            InvitationAcceptRequest(msgId).toJson(),
            inheritedPairingTimings.acceptTimeout,
        )
        val outcome = acceptOutcome(reply, name)
        _invitations.update { it.copy(acceptingMsgId = "", acceptingName = "", outcome = outcome) }
        Logger.d("pairing", "an accept ended: ok=${outcome.ok}")
        
        refreshInvitations()
    }
}


fun MessengerStore.dismissAcceptOutcome() {
    _invitations.update { it.copy(outcome = null) }
}


internal fun acceptOutcome(reply: Frame, name: String): AcceptOutcome {
    if (reply.type == FrameType.InvitationAccepted) {
        return AcceptOutcome(
            true,
            listOf(
                "invitation accepted — you can reach $name from this device now",
                "it joined the conversation you already had with them; there is no second one",
            ),
        )
    }
    val reason = reasonOf(reply)
    return AcceptOutcome(false, explainAcceptRefusal(reason).lines, torSlow = acceptDialFailed(reason))
}


internal fun acceptDialFailed(reason: String): Boolean =
    normaliseReason(reason).contains("rendezvous_unreachable")


internal fun explainAcceptRefusal(reason: String): Refusal {
    val norm = normaliseReason(reason)
    return when {
        norm.isEmpty() -> Refusal("The invitation was not accepted.", listOf(unworded(norm)), worded = false)

        
        norm.contains(REASON_NO_ANSWER) -> Refusal(
            "The accept had no answer.",
            listOf(
                "No answer came back from the daemon. The invitation may already have been answered.",
                "If the pairing landed, the contact shows in this tab.",
            ),
        )

        norm.contains("unknown_invitation") -> Refusal(
            "This device holds no invitation under that row.",
            listOf("The list here was out of date. It has been read again."),
        )

        norm.contains("invitation_not_live") -> Refusal(
            "That invitation is no longer the one to answer.",
            listOf(
                "A newer invitation from the same device retired it, it has already been answered, " +
                    "or an accept of it is still running.",
            ),
        )

        norm.contains("invitation_lapsed") -> Refusal(
            "That invitation ran out before it was accepted.",
            listOf("Ask them to offer another from the device that cannot reach you."),
        )

        norm.contains("fingerprint_mismatch") -> Refusal(
            "Whoever answered is not the person that invitation was addressed to.",
            listOf(
                "Nothing was paired and nothing was saved. This is what the check exists for.",
                "Tell them their invitation was answered by somebody else, and have them offer a fresh one.",
            ),
        )

        norm.contains("rendezvous_refused") -> Refusal(
            "The far side refused this device.",
            listOf("That invitation is finished. Ask them to offer another."),
        )

        
        acceptDialFailed(norm) -> Refusal(
            TOR_SLOW,
            listOf("The invitation stands until its deadline; accept it again here."),
        )

        norm.contains("import_failed") -> Refusal(
            "The pairing material would not install on this device.",
            listOf("That invitation is spent. Ask them to offer another."),
        )

        norm.contains("store_read_failed") -> Refusal(
            "This device could not read the invitation.",
            listOf("Nothing was dialled. Try again; if it keeps failing, this device's storage is the thing to look at."),
        )

        norm.contains("bad_payload") -> Refusal(
            "The rendezvous answered with something this device cannot use.",
            listOf(
                "That invitation is spent. A rendezvous hands its material over once, whatever came of it.",
                "Ask them to offer another from the device that cannot reach you.",
            ),
        )

        norm.contains("backend_unavailable") || norm.contains("not_ready") || norm.contains("build_failed") -> Refusal(
            "The daemon is not ready to answer an invitation.",
            listOf("Wait until the Status tab shows the backend up, then try again."),
        )

        norm.contains("bad_request") -> Refusal(
            "This device would not dial that invitation.",
            listOf("It is finished here. Ask them to offer another."),
        )

        
        norm.contains("encode_frame") -> Refusal(
            "The pairing completed, but the daemon's answer could not be read.",
            listOf("The contact shows in this tab."),
        )

        else -> Refusal("The invitation was not accepted.", listOf(unworded(norm)), worded = false)
    }
}

internal fun explainInvitationsListRefusal(reason: String): Refusal {
    val norm = normaliseReason(reason)
    return when {
        norm.contains(REASON_NO_ANSWER) -> Refusal(
            "The invitations could not be read.",
            listOf("No answer came back from the daemon. This is not an empty list."),
        )

        norm.contains("store_read_failed") -> Refusal(
            "The invitations could not be read.",
            listOf(
                "This is not an empty list. It is a list that failed, and there may be an invitation standing behind it.",
                "If it keeps failing, this device's storage is the thing to look at.",
            ),
        )

        norm.contains("backend_unavailable") || norm.contains("not_ready") -> Refusal(
            "The daemon is not ready to list invitations.",
            listOf("Wait until the Status tab shows the backend up, then read the list again."),
        )

        norm.contains("encode_frame") -> Refusal(
            "The daemon could not put the list into a frame.",
            listOf("Nothing is wrong with the invitations themselves. A build that keeps doing this is the thing to look at."),
        )

        else -> Refusal("The invitations could not be read.", listOf(unworded(norm)), worded = false)
    }
}


internal const val INVITATIONS_SECTION = "invitations"
internal const val ACCEPT_ACTION = "Accept"
internal const val ACCEPT_OWN_INVITATION =
    "that invitation is one you offered — only the person it was offered to can answer it"
internal const val ACCEPT_NOT_STANDING = "that invitation cannot be accepted — it no longer stands"
internal const val ACCEPT_NOT_SENT =
    "The daemon is not reachable, so nothing was sent. Try again when it is back."

internal fun askForAnother(name: String) = "ask $name to offer another from the device that cannot reach you"
internal fun acceptingLine(name: String) =
    "accepting $name's invitation — dialling over Tor, this can take a while…"
