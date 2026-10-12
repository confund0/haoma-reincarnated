package io.haoma.calculator.messenger

import io.haoma.calculator.core.ipc.Frame
import io.haoma.calculator.core.ipc.FrameType
import io.haoma.calculator.log.Logger
import io.haoma.calculator.messenger.settings.LINKED_DEVICES_LABEL
import kotlin.time.Duration
import kotlin.time.Duration.Companion.minutes
import kotlin.time.Duration.Companion.seconds
import kotlinx.coroutines.async
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.coroutines.selects.select
import org.json.JSONObject


internal object PairState {
    const val WAITING = "waiting"
    const val OFFERING = "Preparing the invitation. Keep this device on."
    const val OFFERED = "invitation sent"
    const val FAILED = "it did not go out"
    const val UNKNOWN = "no answer came back"
}


internal const val REASON_NO_ANSWER = "no_answer"


data class InheritedPairingTimings(
    

    val offerTimeout: Duration = 330.seconds,

    
    val acceptTimeout: Duration = 330.seconds,

    
    val listTimeout: Duration = 10.seconds,

    
    val lostOfferRecheckMs: Long = 10_000L,

    
    val syncFinishWait: Duration = 11.minutes,

    
    val nowSeconds: () -> Long = { System.currentTimeMillis() / 1000L },
)


data class LostOffer(val fingerprint: String, val sentAt: Long, val sentListSeq: Long)


data class InheritedPairing(
    val contacts: List<InheritedContactEntry> = emptyList(),
    val linked: Boolean = false,
    val listFailure: List<String> = emptyList(),
    val states: Map<String, String> = emptyMap(),
    val details: Map<String, List<String>> = emptyMap(),
    val outcomeListSeq: Map<String, Long> = emptyMap(),
    val offeredUntil: Map<String, Long> = emptyMap(),
    val standingOut: Map<String, Long> = emptyMap(),
    val appliedListSeq: Long = 0L,
    val lostOffer: LostOffer? = null,
    val queue: List<String> = emptyList(),
    val running: Boolean = false,
    val note: String = "",
    
    
    val syncing: Boolean = false,
    val offeredThisSession: Set<String> = emptySet(),
    val pushedDuringOffer: Set<String> = emptySet(),
    val openOnly: Set<String> = emptySet(),
) {
    

    val showSection: Boolean get() = linked || listFailure.isNotEmpty()

    
    fun standing(fingerprint: String, nowSeconds: Long): Boolean =
        (standingOut[fingerprint] ?: 0L) > nowSeconds

    
    private fun listIsNewerThanOutcome(fingerprint: String): Boolean =
        appliedListSeq > (outcomeListSeq[fingerprint] ?: Long.MAX_VALUE)

    
    fun stateOf(fingerprint: String, nowSeconds: Long): String {
        val standing = standing(fingerprint, nowSeconds)
        return when (val own = states[fingerprint]) {
            null -> if (standing) PairState.OFFERED else ""
            PairState.UNKNOWN -> if (standing) PairState.OFFERED else PairState.UNKNOWN
            PairState.OFFERED -> when {
                listIsNewerThanOutcome(fingerprint) -> if (standing) PairState.OFFERED else ""
                (offeredUntil[fingerprint] ?: Long.MAX_VALUE) <= nowSeconds -> ""
                else -> PairState.OFFERED
            }
            PairState.FAILED ->
                if (standing && listIsNewerThanOutcome(fingerprint)) PairState.OFFERED else PairState.FAILED
            else -> own
        }
    }

    
    fun detailsOf(fingerprint: String, nowSeconds: Long): List<String> =
        if (states[fingerprint] != stateOf(fingerprint, nowSeconds)) emptyList() else details[fingerprint].orEmpty()

    
    fun withContacts(contacts: List<InheritedContactEntry>, linked: Boolean): InheritedPairing {
        val held = contacts.mapTo(HashSet()) { it.fingerprint }
        
        
        val invited = contacts.filter { it.invited }.mapTo(HashSet()) { it.fingerprint }
        val keptQueue = if (linked) queue.filter { it in held && it !in invited } else emptyList()
        
        
        val dropped = queue.filterNot { it in keptQueue }.filterTo(HashSet()) { states[it] == PairState.WAITING }
        val keep = { fp: String -> fp in held && fp !in dropped }
        val stoppedByUnlink = !linked && queue.any { it in held }
        
        
        val droppedByUnlink = queue.filter { it in held }.distinct().size
        return copy(
            contacts = contacts,
            linked = linked,
            listFailure = emptyList(),
            states = states.filterKeys(keep),
            details = details.filterKeys(keep),
            outcomeListSeq = outcomeListSeq.filterKeys(keep),
            offeredUntil = offeredUntil.filterKeys(keep),
            
            lostOffer = lostOffer?.takeIf { it.fingerprint in held },
            queue = keptQueue,
            openOnly = openOnly.filterTo(HashSet()) { it in keptQueue },
            note = if (stoppedByUnlink) pairingStopped(droppedByUnlink) else note,
        )
    }

    
    fun enqueue(
        want: List<String>,
        anyway: Boolean = false,
        copiesOf: ((InheritedContactEntry) -> Int)? = null,
        openOnly: Boolean = false,
    ): Pair<InheritedPairing, Int> {
        val byFingerprint = contacts.associateBy { it.fingerprint }
        var nextStates = states
        var nextDetails = details
        var nextQueue = queue
        var nextOpenOnly = this.openOnly
        var joined = 0
        for (fp in want) {
            val entry = byFingerprint[fp] ?: continue
            if (entry.invited) continue
            if (entry.waiting && !anyway) continue
            val state = nextStates[fp]
            when (state) {
                PairState.WAITING, PairState.OFFERING -> continue
            }
            if (fp in nextQueue) continue
            val copies = copiesOf?.invoke(entry)?.coerceAtLeast(1) ?: when (state) {
                PairState.OFFERED, PairState.UNKNOWN -> 1
                else -> entry.offers.coerceAtLeast(1)
            }
            nextStates = nextStates + (fp to PairState.WAITING)
            nextDetails = nextDetails - fp
            nextQueue = nextQueue + List(copies) { fp }
            nextOpenOnly = if (openOnly) nextOpenOnly + fp else nextOpenOnly - fp
            joined++
        }
        return copy(states = nextStates, details = nextDetails, queue = nextQueue, openOnly = nextOpenOnly) to joined
    }

    
    fun requeueOffered(nowSeconds: Long): Pair<InheritedPairing, Int> {
        if (!linked) return this to 0
        val want = contacts.filter {
            it.fingerprint in offeredThisSession && it.open > 0 &&
                stateOf(it.fingerprint, nowSeconds) in REQUEUED_STATES
        }.map { it.fingerprint }
        if (want.isEmpty()) return this to 0
        return enqueue(want, copiesOf = { it.open }, openOnly = true)
    }

    
    fun pairAllCandidates(nowSeconds: Long): List<String> =
        contacts.filterNot(::skippedByPairAll).map { it.fingerprint }
            .filter { stateOf(it, nowSeconds) !in LEFT_ALONE_BY_PAIR_ALL }

    
    fun leftAloneByPairAll(nowSeconds: Long): Int =
        contacts.count { !skippedByPairAll(it) && stateOf(it.fingerprint, nowSeconds) in LEFT_ALONE_BY_PAIR_ALL }

    private fun skippedByPairAll(c: InheritedContactEntry): Boolean = c.waiting || c.invited

    
    fun stopped(): Pair<InheritedPairing, Int> {
        val dropped = queue.toSet()
        val reset = dropped.filterTo(HashSet()) { states[it] == PairState.WAITING }
        return copy(
            queue = emptyList(),
            states = states.filterKeys { it !in reset },
            details = details.filterKeys { it !in reset },
            offeredThisSession = offeredThisSession - dropped,
            openOnly = openOnly - dropped,
        ) to dropped.size
    }

    companion object {
        
        private val REQUEUED_STATES = setOf("", PairState.OFFERED)

        private val LEFT_ALONE_BY_PAIR_ALL = setOf(
            PairState.WAITING,
            PairState.OFFERING,
            PairState.OFFERED,
            PairState.UNKNOWN,
        )
    }
}


internal fun retireConfirmFor(state: String): String? = when (state) {
    PairState.OFFERED -> RETIRE_CONFIRM_STANDING
    PairState.UNKNOWN -> RETIRE_CONFIRM_MAY_STAND
    else -> null
}


internal suspend fun MessengerStore.refreshInheritedContacts(requeue: Boolean = false) {
    if (!_connection.value) return
    val generation = inheritedListSeq.incrementAndGet()
    val reply = askUnlessDropped(
        FrameType.ListInheritedContacts,
        JSONObject(),
        inheritedPairingTimings.listTimeout,
    )
    if (inheritedListSeq.get() != generation) {
        Logger.d("pairing", "an inherited contacts answer was overtaken and dropped")
        return
    }
    val payload = reply.payload
    if (reply.type == FrameType.InheritedContactsListed && payload != null) {
        val body = InheritedContactsBody.fromJson(payload)
        if (requeue) applyInheritedContactsAndRequeue(body) else applyInheritedContacts(body)
        return
    }
    val refusal = explainInheritedListRefusal(reasonOf(reply))
    Logger.w("pairing", "the inherited contacts could not be listed: ${normaliseReason(reasonOf(reply))}")
    _inheritedPairing.update { it.copy(listFailure = refusal.lines) }
}


fun MessengerStore.requestInheritedContacts() {
    if (!_connection.value) {
        _inheritedPairing.update { it.copy(note = READ_AGAIN_NO_DAEMON) }
        return
    }
    scope.launch { refreshInheritedContacts() }
}


internal fun MessengerStore.applyInheritedContactsPush(body: InheritedContactsBody) {
    
    inheritedListSeq.incrementAndGet()
    applyInheritedContactsAndRequeue(body, remember = true)
}


private fun MessengerStore.applyInheritedContactsAndRequeue(body: InheritedContactsBody, remember: Boolean = false) {
    val now = inheritedPairingTimings.nowSeconds()
    var joined = 0
    var start = false
    _inheritedPairing.update { st ->
        val listed = st.withContacts(body.contacts, body.linked)
        val noted = if (remember) {
            val inFlight = listed.states.filterValues { it == PairState.OFFERING }.keys
            listed.copy(pushedDuringOffer = listed.pushedDuringOffer + inFlight)
        } else {
            listed
        }
        val (next, n) = noted.requeueOffered(now)
        joined = n
        start = n > 0 && !next.running
        if (n > 0) next.copy(running = next.running || start, note = pairingStarted(n)) else next
    }
    Logger.d("pairing", "inherited contacts: ${body.contacts.size} row(s), linked=${body.linked}")
    if (joined > 0) Logger.d("pairing", "pairing run queued again after a new list: joined=$joined started_here=$start")
    if (start) scope.launch { runInheritedPairing() }
}


internal fun MessengerStore.applyInheritedContacts(body: InheritedContactsBody) {
    _inheritedPairing.update { it.withContacts(body.contacts, body.linked) }
    Logger.d("pairing", "inherited contacts: ${body.contacts.size} row(s), linked=${body.linked}")
}


fun MessengerStore.pairInherited(fingerprint: String, anyway: Boolean = false) {
    queuePairings { it.enqueue(listOf(fingerprint), anyway) }
}


fun MessengerStore.pairAllInherited() {
    val now = inheritedPairingTimings.nowSeconds()
    queuePairings { it.enqueue(it.pairAllCandidates(now)) }
}


private fun MessengerStore.queuePairings(enqueue: (InheritedPairing) -> Pair<InheritedPairing, Int>) {
    var joined = 0
    var start = false
    _inheritedPairing.update { st ->
        val (next, n) = enqueue(st)
        joined = n
        start = n > 0 && !next.running
        val note = if (n > 0) pairingStarted(n) else PAIRING_ALREADY
        next.copy(running = next.running || start, note = note)
    }
    Logger.d("pairing", "pairing run asked to start: joined=$joined started_here=$start")
    if (start) scope.launch { runInheritedPairing() }
}


fun MessengerStore.stopInheritedPairing() {
    var dropped = 0
    _inheritedPairing.update { st ->
        val (next, n) = st.stopped()
        dropped = n
        next.copy(note = if (n > 0) pairingStopped(n) else NOTHING_WAITING)
    }
    Logger.d("pairing", "pairing run stopped: dropped=$dropped")
}


internal fun MessengerStore.resumeInheritedPairing() {
    var start = false
    var left = 0
    _inheritedPairing.update { st ->
        start = !st.running && st.queue.isNotEmpty() && _connection.value
        left = st.queue.distinct().size
        if (start) st.copy(running = true, note = pairingResumed(left)) else st
    }
    if (!start) return
    Logger.i("pairing", "pairing run resumed with the connection: $left left")
    scope.launch { runInheritedPairing() }
}


internal suspend fun MessengerStore.bootstrapPairingSurfaces() {
    refreshInheritedContacts()
    refreshInvitations()
    resumeInheritedPairing()
}


internal fun MessengerStore.dropInheritedQueueOnStop() {
    _inheritedPairing.update { st ->
        val (next, n) = st.stopped()
        if (n > 0) next.copy(note = pairingStopped(n)) else next
    }
}


private enum class RunStep { End, Hold, Wait, Offer }


private suspend fun MessengerStore.runInheritedPairing() {
    var head: String? = null
    var ended = false
    try {
        while (true) {
            var step = RunStep.End
            var next: String? = null
            var nextOpenOnly = false
            var held = 0
            val now = inheritedPairingTimings.nowSeconds()
            val offerSeconds = inheritedPairingTimings.offerTimeout.inWholeSeconds
            _inheritedPairing.update { st ->
                next = null
                held = 0
                val lost = st.lostOffer
                when {
                    st.queue.isEmpty() -> {
                        step = RunStep.End
                        st.copy(running = false, note = "")
                    }
                    
                    
                    !_connection.value -> {
                        step = RunStep.Hold
                        held = st.queue.distinct().size
                        st.copy(running = false, note = pairingHeld(held))
                    }
                    lost != null && now < lost.sentAt + offerSeconds -> {
                        step = RunStep.Wait
                        val name = st.contacts.firstOrNull { it.fingerprint == lost.fingerprint }
                            ?.let(::inheritedContactName) ?: shortFingerprint(lost.fingerprint)
                        st.copy(note = waitingForLostOffer(name))
                    }
                    else -> {
                        step = RunStep.Offer
                        val fp = st.queue.first()
                        next = fp
                        nextOpenOnly = fp in st.openOnly
                        val rest = st.queue.drop(1)
                        st.copy(
                            lostOffer = null,
                            queue = rest,
                            
                            openOnly = if (fp in rest) st.openOnly else st.openOnly - fp,
                            states = st.states + (fp to PairState.OFFERING),
                            details = st.details - fp,
                            offeredThisSession = st.offeredThisSession + fp,
                        )
                    }
                }
            }
            when (step) {
                RunStep.End -> {
                    ended = true
                    Logger.d("pairing", "pairing run finished")
                    return
                }
                RunStep.Hold -> {
                    ended = true
                    Logger.i("pairing", "pairing run held, the daemon is not reachable: $held waiting")
                    
                    
                    if (_connection.value) resumeInheritedPairing()
                    return
                }
                RunStep.Wait -> {
                    
                    
                    refreshInvitations()
                    if (_inheritedPairing.value.lostOffer != null) {
                        kotlinx.coroutines.delay(inheritedPairingTimings.lostOfferRecheckMs)
                    }
                }
                RunStep.Offer -> {
                    val fp = next ?: continue
                    head = fp
                    offerOne(fp, nextOpenOnly)
                    head = null
                }
            }
        }
    } finally {
        if (!ended) {
            val left = head
            _inheritedPairing.update { st ->
                if (left != null && st.states[left] == PairState.OFFERING) {
                    st.copy(
                        running = false,
                        states = st.states + (left to PairState.UNKNOWN),
                        details = st.details + (left to explainOfferRefusal(REASON_NO_ANSWER).lines),
                        pushedDuringOffer = st.pushedDuringOffer - left,
                    )
                } else {
                    st.copy(running = false)
                }
            }
            Logger.w("pairing", "pairing run ended by a throw or a cancellation")
        }
    }
}


private suspend fun MessengerStore.offerOne(fp: String, openOnly: Boolean = false) {
    val sentAt = inheritedPairingTimings.nowSeconds()
    val sentListSeq = invitationsListSeq.get()
    val reply = askUnlessDropped(
        FrameType.InvitationOffer,
        
        
        InvitationOfferRequest(fp, force = true, openOnly = openOnly).toJson(),
        inheritedPairingTimings.offerTimeout,
    )
    val outcome = offerOutcome(reply)
    var pushedDuring = false
    _inheritedPairing.update { st0 ->
        pushedDuring = fp in st0.pushedDuringOffer
        val st = if (pushedDuring) st0.copy(pushedDuringOffer = st0.pushedDuringOffer - fp) else st0
        
        
        if (st.contacts.none { it.fingerprint == fp }) {
            st
        } else {
            st.copy(
                states = st.states + (fp to outcome.state),
                details = st.details + (fp to outcome.lines),
                outcomeListSeq = st.outcomeListSeq + (fp to invitationsListSeq.get()),
                offeredUntil = if (outcome.expiresAt > 0L) {
                    st.offeredUntil + (fp to outcome.expiresAt)
                } else {
                    st.offeredUntil - fp
                },
                lostOffer = if (outcome.state == PairState.UNKNOWN) LostOffer(fp, sentAt, sentListSeq) else null,
            )
        }
    }
    Logger.d("pairing", "one pairing offer ended: state=${outcome.state}")
    
    if (_connection.value) scope.launch { refreshInvitations() }
    
    
    if (pushedDuring && _connection.value) {
        Logger.d("pairing", "a push came during the offer; reading the inherited contacts again")
        refreshInheritedContacts(requeue = true)
    }
}


internal data class OfferOutcome(val state: String, val lines: List<String>, val expiresAt: Long = 0L)


internal fun offerOutcome(reply: Frame): OfferOutcome {
    if (reply.type == FrameType.InvitationOffered) {
        val p = reply.payload ?: return OfferOutcome(PairState.OFFERED, listOf(OFFERED_UNREADABLE))
        val r = InvitationOfferedResponse.fromJson(p)
        return OfferOutcome(PairState.OFFERED, listOf(handedTo(r.siblings)), r.expiresAt)
    }
    val reason = reasonOf(reply)
    val norm = normaliseReason(reason)
    return when {
        norm.contains(REASON_NO_ANSWER) -> OfferOutcome(PairState.UNKNOWN, explainOfferRefusal(reason).lines)
        norm.contains("encode_frame") -> OfferOutcome(PairState.OFFERED, listOf(OFFERED_UNREADABLE))
        
        
        norm.contains("nothing_open") -> OfferOutcome(PairState.OFFERED, emptyList())
        else -> OfferOutcome(PairState.FAILED, explainOfferRefusal(reason).lines)
    }
}


internal suspend fun MessengerStore.askWithin(type: String, payload: JSONObject?, timeout: Duration): Frame {
    timedRequestSender?.let { return it(type, payload, timeout) }
    requestSender?.let { return it(type, payload) }
    val c = ipc ?: throw IllegalStateException(NO_DAEMON)
    return c.request(type = type, payload = payload, timeout = timeout)
}


internal suspend fun MessengerStore.askUnlessDropped(type: String, payload: JSONObject?, timeout: Duration): Frame {
    val answer: Frame? = coroutineScope {
        val dropped = async { _connection.first { !it } }
        val reply = async { runCatching { askWithin(type, payload, timeout) }.getOrNull() }
        try {
            
            
            select<Frame?> {
                reply.onAwait { it }
                dropped.onAwait { null }
            }
        } finally {
            dropped.cancel()
            reply.cancel()
        }
    }
    return answer ?: noAnswerFrame()
}


internal fun noAnswerFrame(): Frame =
    Frame(type = FrameType.Error, payload = JSONObject().put("code", REASON_NO_ANSWER))


internal fun reasonOf(reply: Frame): String =
    reply.payload?.let { ErrorPayload.fromJson(it).code }.orEmpty()


internal data class Refusal(val headline: String, val guidance: List<String>, val worded: Boolean = true) {
    val lines: List<String> get() = listOf(headline) + guidance
}

internal fun normaliseReason(reason: String): String =
    reason.trim().lowercase().replace('-', '_')


internal fun unworded(norm: String): String =
    if (norm.isEmpty()) "The daemon gave no reason." else "The daemon refused with \"$norm\", which this build has no wording for."

internal fun explainOfferRefusal(reason: String): Refusal {
    val norm = normaliseReason(reason)
    return when {
        norm.isEmpty() -> Refusal("The invitation was not offered.", listOf(unworded(norm)), worded = false)

        
        norm.contains(REASON_NO_ANSWER) -> Refusal(
            "The invitation had no answer.",
            listOf(
                "No answer came back from the daemon, so this device cannot say whether the invitation went out.",
                "If it did, this row changes to invitation sent once the daemon is back. " +
                    "Offer again only if it does not, because a second invitation retires the first.",
            ),
        )

        
        norm.contains("self_check_failed") -> Refusal(
            TOR_SLOW,
            listOf("Nothing new went out."),
        )

        
        norm.contains("offer_in_progress") -> Refusal(
            "Another invitation is being offered right now.",
            listOf("Nothing went out for this contact. Pair again once the other offer has finished."),
        )

        norm.contains("already_paired") -> Refusal(
            "This device is already paired with every device of that person it knows of.",
            listOf("An invitation is for a contact this device has no pairing with. Open the conversation and write to them instead."),
        )

        
        norm.contains("contact_offers") -> Refusal(
            "That person's device offers this pairing, not this one.",
            listOf("Nothing went out. Their invitation shows under invitations when it arrives; accept it there."),
        )

        
        norm.contains("nothing_open") -> Refusal(
            "An invitation from this device already stands for each device of that person.",
            listOf("Nothing new went out, and nothing was taken back."),
        )

        
        norm.contains("invitation_from_contact") -> Refusal(
            "That person's device has already sent this device an invitation.",
            listOf("Nothing went out. Accept their invitation under invitations."),
        )

        norm.contains("unknown_contact") -> Refusal(
            "No conversation on this device belongs to that person.",
            listOf("This list may be out of date. Tap Read again."),
        )

        norm.contains("not_inherited") -> Refusal(
            "That conversation did not come from one of your own devices.",
            listOf(
                "An invitation travels through a device of yours that still holds a pairing with that person, " +
                    "and this contact reached you some other way.",
            ),
        )

        norm.contains("no_sibling") -> Refusal(
            "This device has no linked device to carry the invitation.",
            listOf(
                "The invitation reaches that person through a device of yours that is still paired with them.",
                "Link this device to another of yours in Settings, under $LINKED_DEVICES_LABEL.",
            ),
        )

        norm.contains("rail_send_failed") -> Refusal(
            "None of your devices accepted the invitation to pass on.",
            listOf("They may be offline. Check that your linked device is on, then pair again."),
        )

        
        norm.contains("tag_failed") || norm.contains("key_stash_failed") -> Refusal(
            "This device could not record the invitation, so it took it back.",
            listOf(
                "An invitation offered earlier may still stand. If it does, this row changes to invitation sent.",
                "If it keeps failing, this device's storage is the thing to look at.",
            ),
        )

        norm.contains("persist_failed") || norm.contains("store_read_failed") -> Refusal(
            "This device could not record the invitation, so it took it back.",
            listOf("Pair again; if it keeps failing, this device's storage is the thing to look at."),
        )

        norm.contains("invite_failed") || norm.contains("build_failed") -> Refusal(
            "This device could not mint a pairing to offer.",
            listOf(
                "Check that Tor is up in the Status tab, then pair again.",
                "An invitation offered earlier may still stand. If it does, this row changes to invitation sent.",
            ),
        )

        norm.contains("backend_unavailable") || norm.contains("not_ready") -> Refusal(
            "The daemon is not ready to offer an invitation.",
            listOf(
                "Wait until the Status tab shows the backend up, then pair again.",
                "An invitation offered earlier may still stand. If it does, this row changes to invitation sent.",
            ),
        )

        norm.contains("bad_request") -> Refusal(
            "The daemon could not tell which person this was for.",
            listOf("Pair again from this list."),
        )

        
        norm.contains("encode_frame") -> Refusal(
            "The invitation went out, but the daemon's answer could not be read.",
            listOf(OFFERED_UNREADABLE),
        )

        else -> Refusal("The invitation was not offered.", listOf(unworded(norm)), worded = false)
    }
}

internal fun explainInheritedListRefusal(reason: String): Refusal {
    val norm = normaliseReason(reason)
    return when {
        
        norm.contains(REASON_NO_ANSWER) -> Refusal(
            "The contacts with no pairing could not be read.",
            listOf("No answer came back from the daemon. This is not an empty section."),
        )

        norm.contains("store_read_failed") -> Refusal(
            "The contacts with no pairing could not be read.",
            listOf(
                "This is not an empty section. It is a section that failed, and there may be contacts behind it.",
                "If it keeps failing, this device's storage is the thing to look at.",
            ),
        )

        norm.contains("encode_frame") -> Refusal(
            "The daemon could not put the contacts into a frame.",
            listOf("Nothing is wrong with the contacts themselves. A build that keeps doing this is the thing to look at."),
        )

        else -> Refusal("The contacts with no pairing could not be read.", listOf(unworded(norm)), worded = false)
    }
}


internal const val LINKED_DEVICE_SECTION = "linked device"
internal const val SYNCHRONIZED = "synchronized"
internal const val PAIR_ACTION = "Pair"
internal const val PAIR_ANYWAY_ACTION = "Pair anyway"
internal const val PAIR_ALL_ACTION = "Pair all"
internal const val STOP_ACTION = "Stop"
internal const val READ_AGAIN_ACTION = "Read again"
internal const val PAIR_ALL_TITLE = "pair all"
internal const val PAIR_ALL_WHAT_HAPPENS =
    "one invitation for each of their devices this device is missing, one at a time. Each one mints an onion."

internal const val PAIRING_ALREADY = "those contacts are already in the pairing run"
internal const val NOTHING_WAITING = "no contact is waiting to be offered"
internal const val NOBODY_TO_PAIR = "there is nobody here to pair with"
internal const val READ_AGAIN_NO_DAEMON = "The daemon is not reachable. Read again when it is back."
internal const val RETIRE_CONFIRM_TITLE = "pair"
internal const val RETIRE_CONFIRM_STANDING = "This retires the invitation that stands. Offer a new one?"
internal const val RETIRE_CONFIRM_MAY_STAND =
    "An invitation may stand, and a new one retires it. Offer a new one?"
internal const val RETIRE_CONFIRM_ACTION = "Offer a new one"
internal const val OFFERED_UNREADABLE =
    "the daemon's answer could not be read, so this device cannot say how many of your devices took it"

internal fun pairingStarted(n: Int) = "pairing with $n contact(s), one at a time…"
internal fun pairingHeld(n: Int) =
    "pairing held — $n contact(s) still to offer. The run starts again when the daemon is back."
internal fun pairingResumed(n: Int) = "pairing resumed — $n contact(s) left to offer"
internal fun pairingStopped(n: Int) =
    "pairing stopped — $n contact(s) dropped from the run. Any invitation already sent still stands."
internal fun allAlreadyInRun(n: Int) = "every contact here is already in the run — $n of them"
internal fun leavingAlone(n: Int) =
    "leaving $n contact(s) alone — an invitation is already out for them, and a second one would retire it"
internal fun handedTo(n: Int) = "handed to $n of your devices to pass on"
internal fun waitingForLostOffer(name: String) =
    "the next offer waits — no answer came back for $name. It goes when that invitation shows " +
        "here, or when five minutes have passed since it was sent."


internal fun inheritedRowInfo(c: InheritedContactEntry): String = when {
    c.invited -> inheritedInvitedLine(inheritedContactName(c))
    c.waiting -> inheritedWaitingLine(inheritedContactName(c))
    c.paired > 0 && c.devices > 0 -> inheritedDevicesLine(c.paired, c.devices)
    else -> ""
}


internal fun inheritedInvitedLine(name: String) = "accept $name's invitation"


internal fun inheritedWaitingLine(name: String) = "waiting for $name's device to offer"


internal fun inheritedDevicesLine(paired: Int, devices: Int) = "$paired of $devices devices"


internal fun inheritedContactName(c: InheritedContactEntry): String =
    c.label.ifEmpty { shortFingerprint(c.fingerprint) }


internal fun shortFingerprint(fp: String): String =
    if (fp.length <= 10) fp else fp.substring(0, 8) + "…"
