package io.haoma.calculator.messenger

import io.haoma.calculator.core.ipc.Frame
import io.haoma.calculator.core.ipc.FrameType
import io.haoma.calculator.log.Logger
import kotlin.time.Duration
import kotlin.time.Duration.Companion.seconds
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import org.json.JSONObject


enum class DeviceLinkMode {
    
    Inviting,

    
    Joining,

    
    GuardOnly,
}


enum class DeviceLinkPhase {
    
    Collecting,

    
    Working,

    
    Done,
}


enum class DeviceLinkOutcome { None, Done, Warning, Failed }


data class DeviceLinkLogLine(
    val text: String,
    val outcome: DeviceLinkOutcome = DeviceLinkOutcome.None,
    val aside: Boolean = false,
    
    val torSlow: Boolean = false,
)


data class DeviceLinkRun(
    val id: Long,
    val mode: DeviceLinkMode,
    val phase: DeviceLinkPhase = DeviceLinkPhase.Collecting,
    val myPin: String = "",
    val otherPin: String = "",
    val wordsInput: String = "",
    val words: List<String> = emptyList(),
    val wordsReleased: Boolean = false,
    val handleId: String = "",
    val peerId: String = "",
    val adoptedFingerprint: String = "",
    val expiresAt: Long = 0L,
    val log: List<DeviceLinkLogLine> = emptyList(),
    val note: String = "",
    val busy: Boolean = false,
    val failed: Boolean = false,
    val retryable: Boolean = false,
    val guardStepsStarted: Boolean = false,
    val dialStartedAtMs: Long = 0L,
    
    val closing: Boolean = false,
)


data class DeviceLinkTimings(
    
    val saltWaitMs: Long = 90_000L,

    
    val saltPollMs: Long = 2_000L,

    
    val nowSeconds: () -> Long = { System.currentTimeMillis() / 1000L },

    
    val nowMs: () -> Long = { System.currentTimeMillis() },

    
    val closeRetryMs: Long = 1_000L,
)


fun MessengerStore.beginDeviceLinkRun(mode: DeviceLinkMode): Long {
    val id = deviceLinkRunSeq.incrementAndGet()
    _deviceLinkRun.value = DeviceLinkRun(id = id, mode = mode)
    Logger.d("devicelink", "run $id opened mode=$mode")
    return id
}


fun MessengerStore.endDeviceLinkRun() {
    val run = _deviceLinkRun.value ?: return
    if (run.phase != DeviceLinkPhase.Done) {
        _deviceLinkRun.value = null
        Logger.d("devicelink", "run ${run.id} ended")
        return
    }
    var closing = false
    _deviceLinkRun.update { cur ->
        closing = false
        if (cur == null || cur.id != run.id || cur.closing) return@update cur
        closing = true
        cur.copy(myPin = "", otherPin = "", retryable = false, closing = true)
    }
    if (closing) scope.launch { closeEndedRun(run.id) }
}


internal suspend fun MessengerStore.closeEndedRun(runId: Long) {
    var devices = readLinkedDevices()
    if (devices == null) {
        delay(deviceLinkTimings.closeRetryMs)
        devices = readLinkedDevices()
    }
    readDestroyGuard()
    if (devices == null) {
        updateRun(runId) {
            it.copy(
                log = it.log + DeviceLinkLogLine(LINKED_DEVICES_NOT_READ, outcome = DeviceLinkOutcome.Failed),
                closing = false,
            )
        }
        Logger.w("devicelink", "run $runId kept open; the linked devices could not be read")
        return
    }
    _deviceLinkRun.update { cur -> if (cur != null && cur.id == runId) null else cur }
    Logger.d("devicelink", "run $runId ended linked_devices=${devices.size}")
}


fun MessengerStore.cancelDeviceLinkRun() {
    val run = _deviceLinkRun.value
    val handle = run?.handleId.orEmpty()
    endDeviceLinkRun()
    if (handle.isEmpty()) return
    scope.launch {
        try {
            askDaemon(FrameType.DeviceLinkCancel, DeviceLinkCancelRequest(handle).toJson())
            Logger.d("devicelink", "offer $handle withdrawn")
        } catch (t: Throwable) {
            Logger.w("devicelink", "offer $handle withdraw failed: ${t.message ?: "?"}")
        }
    }
}


fun MessengerStore.setDeviceLinkPins(mine: String, other: String) {
    _deviceLinkRun.update { run ->
        if (run == null) null else run.copy(myPin = mine, otherPin = other, note = pinNote(mine, other))
    }
}


data class DeviceLinkDraft(
    val joinTab: Boolean = false,
    val myPin: String = "",
    val otherPin: String = "",
    val words: String = "",
)


fun MessengerStore.updateDeviceLinkDraft(f: (DeviceLinkDraft) -> DeviceLinkDraft) {
    _deviceLinkDraft.update(f)
}


fun MessengerStore.beginDeviceLinkRunFromDraft(mode: DeviceLinkMode): Long? {
    val draft = _deviceLinkDraft.value
    val id = deviceLinkRunSeq.incrementAndGet()
    val run = DeviceLinkRun(
        id = id,
        mode = mode,
        myPin = draft.myPin,
        otherPin = draft.otherPin,
        wordsInput = if (mode == DeviceLinkMode.Joining) draft.words else "",
        note = pinNote(draft.myPin, draft.otherPin),
    )
    
    
    if (!_deviceLinkRun.compareAndSet(null, run)) {
        Logger.d("devicelink", "run from the form ignored; a run already stands")
        return null
    }
    Logger.d("devicelink", "run $id opened mode=$mode")
    _deviceLinkDraft.value = DeviceLinkDraft(joinTab = draft.joinTab)
    return id
}


fun MessengerStore.startDeviceLinkFromDraft(mode: DeviceLinkMode, wordsLookValid: Boolean = true) {
    if (beginDeviceLinkRunFromDraft(mode) == null) return
    startDeviceLink(wordsLookValid)
}


fun MessengerStore.forgetTypedJoinSecrets() {
    _deviceLinkDraft.value = DeviceLinkDraft()
    _deviceLinkRun.update { cur ->
        when {
            cur == null -> null
            cur.phase == DeviceLinkPhase.Collecting -> null
            else -> cur.copy(myPin = "", otherPin = "", wordsInput = "", words = emptyList(), note = "")
        }
    }
    _pairJoin.update { PairJoinState(waitingSinceMs = it.waitingSinceMs) }
    Logger.d("devicelink", "typed PINs and words dropped")
}


fun MessengerStore.setDeviceLinkWords(text: String) {
    _deviceLinkRun.update { run -> run?.copy(wordsInput = text) }
}


internal fun pinNote(mine: String, other: String): String {
    val a = mine.trim()
    val b = other.trim()
    if (!isFourDigits(a) || !isFourDigits(b)) return ""
    return if (a == b) PIN_MUST_DIFFER else ""
}

internal fun isFourDigits(s: String): Boolean =
    s.length == 4 && s.all { it in '0'..'9' }


fun MessengerStore.startDeviceLink(wordsLookValid: Boolean = true) {
    scope.launch { proceedDeviceLink(wordsLookValid) }
}


fun MessengerStore.startDeviceLinkGuardRetry() {
    scope.launch { retryDeviceLinkGuard() }
}


internal suspend fun MessengerStore.proceedDeviceLink(wordsLookValid: Boolean = true) {
    val run = _deviceLinkRun.value ?: return
    if (run.phase != DeviceLinkPhase.Collecting) return
    val id = run.id
    val mine = run.myPin.trim()
    val other = run.otherPin.trim()
    val words = splitWords(run.wordsInput)

    if (!isFourDigits(mine) || !isFourDigits(other)) {
        refuse(id, PIN_FOUR_DIGITS)
        return
    }
    if (mine == other) {
        refuse(id, PIN_MUST_DIFFER)
        return
    }
    if (run.mode == DeviceLinkMode.Joining && words.size < 3) {
        refuse(id, WORDS_NEEDED)
        return
    }
    if (run.mode == DeviceLinkMode.Joining && !wordsLookValid) {
        refuse(id, JOIN_WORDS_WRONG)
        return
    }

    if (!claimDeviceLinkProceed(id, mine, other, words)) {
        Logger.d("devicelink", "run $id proceed ignored; another press already started it")
        return
    }
    when (run.mode) {
        DeviceLinkMode.Inviting -> sendDeviceLinkOffer(id)
        DeviceLinkMode.Joining -> sendDeviceLinkAccept(id, words)
        DeviceLinkMode.GuardOnly -> startGuardOnly(id)
    }
}


internal fun MessengerStore.claimDeviceLinkProceed(id: Long, mine: String, other: String, words: List<String>): Boolean {
    var claimed = false
    _deviceLinkRun.update { cur ->
        claimed = false
        if (cur == null || cur.id != id || cur.phase != DeviceLinkPhase.Collecting) return@update cur
        claimed = true
        cur.copy(
            myPin = mine,
            otherPin = other,
            words = if (cur.mode == DeviceLinkMode.Joining) words else cur.words,
            phase = DeviceLinkPhase.Working,
            note = "",
        )
    }
    return claimed
}


internal fun splitWords(text: String): List<String> =
    text.trim().split(WHITESPACE).filter { it.isNotEmpty() }

private val WHITESPACE = Regex("\\s+")


private suspend fun MessengerStore.sendDeviceLinkOffer(runId: Long) {
    say(runId, OFFER_PUBLISHING)
    val reply = try {
        askDaemon(FrameType.DeviceLinkOffer, DeviceLinkOfferRequest().toJson())
    } catch (t: Throwable) {
        Logger.w("devicelink", "run $runId offer failed: ${t.message ?: "?"}")
        failRun(runId, FAILURE_FALLBACK)
        return
    }
    if (!isCurrent(runId)) return
    if (reply.type == FrameType.DeviceLinkFailed) {
        deviceLinkFailed(reply.payload?.let(DeviceLinkFailedPush::fromJson) ?: DeviceLinkFailedPush("", "", ""))
        return
    }
    if (reply.type != FrameType.DeviceLinkStarted || reply.payload == null) {
        failRun(runId, FAILURE_FALLBACK)
        reply.payload?.let(ErrorPayload::fromJson)?.let { aside(runId, it.message) }
        return
    }
    val started = DeviceLinkStartedResponse.fromJson(reply.payload)
    updateRun(runId) {
        it.copy(handleId = started.handleId, words = started.words, expiresAt = started.expiresAt)
    }
    Logger.d("devicelink", "run $runId offer live handle=${started.handleId} expires=${started.expiresAt}")
}


internal fun MessengerStore.deviceLinkProbe(p: DeviceLinkProbePush) {
    val run = _deviceLinkRun.value ?: return
    if (run.mode != DeviceLinkMode.Inviting || run.phase != DeviceLinkPhase.Working) return
    if (!pushBelongsToRun(run, p.handleId)) return
    if (!p.ready) {
        if (!run.wordsReleased) say(run.id, probeStillWaiting(p.attempt))
        return
    }
    if (run.wordsReleased) return
    updateRun(run.id) { it.copy(wordsReleased = true) }
    stepOk(run.id, OFFER_WAITING_FOR_JOIN)
    aside(run.id, if (p.error.isEmpty()) PROBE_REACHABLE else PROBE_UNCONFIRMED)
}


private suspend fun MessengerStore.sendDeviceLinkAccept(runId: Long, words: List<String>) {
    say(runId, JOIN_CONTACTING)
    updateRun(runId) { it.copy(dialStartedAtMs = deviceLinkTimings.nowMs()) }
    
    
    val reply = try {
        askUnlessDropped(FrameType.DeviceLinkAccept, DeviceLinkAcceptRequest(words).toJson(), DEVICE_LINK_ACCEPT_TIMEOUT)
    } finally {
        updateRun(runId) { it.copy(dialStartedAtMs = 0L) }
    }
    if (!isCurrent(runId)) return
    if (reply.type == FrameType.Error) {
        val code = reasonOf(reply)
        val line = joinFailureLine(code)
        if (line != null) {
            if (retryDeviceLinkJoinLater(runId, line, torSlow = joinDialFailed(code))) {
                Logger.i("devicelink", "run $runId join refused reason=$code; the form is kept")
            }
            return
        }
        if (code == REASON_NO_ANSWER) {
            joinWentUnanswered(runId)
            return
        }
    }
    if (reply.type == FrameType.DeviceLinkFailed) {
        deviceLinkFailed(reply.payload?.let(DeviceLinkFailedPush::fromJson) ?: DeviceLinkFailedPush("", "", ""))
        return
    }
    if (reply.type != FrameType.DeviceLinkAccepted || reply.payload == null) {
        failRun(runId, FAILURE_FALLBACK)
        reply.payload?.let(ErrorPayload::fromJson)?.let { aside(runId, it.message) }
        return
    }
    val accepted = DeviceLinkAcceptedResponse.fromJson(reply.payload)
    updateRun(runId) {
        it.copy(peerId = accepted.peerId, adoptedFingerprint = accepted.adoptedFingerprint)
    }
    
    
    aside(runId, "$JOIN_FINGERPRINT_PREFIX${accepted.adoptedFingerprint}")
    val claimed = claimGuardSteps(runId) ?: return
    runGuardSteps(runId, claimed)
}


internal fun MessengerStore.deviceLinkCompleted(p: DeviceLinkCompletedPush) {
    scope.launch { takeDeviceLinkCompletion(p) }
}


internal suspend fun MessengerStore.takeDeviceLinkCompletion(p: DeviceLinkCompletedPush) {
    val run = _deviceLinkRun.value
    if (run == null) {
        
        
        readLinkedDevices()
        return
    }
    if (!pushBelongsToRun(run, p.handleId)) return
    val runId = run.id
    updateRun(runId) { if (it.peerId.isEmpty()) it.copy(peerId = p.peerId) else it }
    val peerId = claimGuardSteps(runId) ?: return
    runGuardSteps(runId, peerId)
}


internal fun MessengerStore.claimGuardSteps(runId: Long): String? {
    var peerId: String? = null
    _deviceLinkRun.update { cur ->
        peerId = null
        if (cur == null || cur.id != runId) return@update cur
        if (cur.phase != DeviceLinkPhase.Working) {
            Logger.d("devicelink", "run $runId completion ignored; the run is not at the step that takes it")
            return@update cur
        }
        if (cur.guardStepsStarted) {
            Logger.d("devicelink", "run $runId completion ignored; the guard steps already started")
            return@update cur
        }
        if (cur.peerId.isEmpty()) return@update cur
        peerId = cur.peerId
        cur.copy(guardStepsStarted = true)
    }
    return peerId
}


internal fun MessengerStore.deviceLinkFailed(p: DeviceLinkFailedPush) {
    val run = _deviceLinkRun.value ?: return
    if (!pushBelongsToRun(run, p.handleId)) return
    val text = explainDeviceLinkFailure(p.reason, p.detail)
    
    
    failRun(run.id, text.headline)
    text.guidance.forEach { aside(run.id, it) }
    Logger.w("devicelink", "run ${run.id} failed reason=${p.reason}")
}


internal fun pushBelongsToRun(run: DeviceLinkRun, handleId: String): Boolean {
    if (run.handleId.isEmpty()) return true
    return handleId.isEmpty() || handleId == run.handleId
}


internal suspend fun MessengerStore.runGuardSteps(runId: Long, peerId: String) {
    val run = currentRun(runId) ?: return
    if (run.myPin.isEmpty() || run.otherPin.isEmpty()) {
        failRun(runId, PINS_NOT_COLLECTED)
        return
    }
    stepOk(runId, GUARD_SETTING)
    if (!writeOwnGuard(runId, run.myPin)) return
    stepOk(runId, GUARD_WAITING)
    if (!awaitSalt(runId, peerId)) return
    stepOk(runId, GUARD_CHECKING)
    verifyOtherGuard(runId, peerId, run.otherPin)
}


private suspend fun MessengerStore.writeOwnGuard(runId: Long, pin: String): Boolean {
    val reply = try {
        askDaemon(FrameType.SetDestroyPin, SetDestroyPinRequest(pin).toJson())
    } catch (t: Throwable) {
        Logger.w("devicelink", "run $runId set_destroy_pin failed: ${t.message ?: "?"}")
        null
    }
    if (!isCurrent(runId)) return false
    val accepted = reply != null &&
        reply.type == FrameType.DestroyPinSet &&
        reply.payload != null &&
        DestroyPinSetResponse.fromJson(reply.payload).accepted
    if (!accepted) {
        failRetryable(runId, GUARD_NOT_TAKEN)
        return false
    }
    return true
}


private suspend fun MessengerStore.awaitSalt(runId: Long, peerId: String): Boolean {
    val timings = deviceLinkTimings
    var waited = 0L
    while (true) {
        val listed = readDestroyGuard()
        if (!isCurrent(runId)) {
            Logger.d("devicelink", "run $runId salt wait abandoned; the run was closed")
            return false
        }
        if (listed != null && listed.siblings.any { it.peerId == peerId && it.saltHeld }) return true
        if (waited >= timings.saltWaitMs) {
            
            
            askForSaltAgain(runId)
            
            
            finishLinked(runId, GUARD_NO_DETAILS)
            return false
        }
        delay(timings.saltPollMs)
        waited += timings.saltPollMs
        if (!isCurrent(runId)) {
            Logger.d("devicelink", "run $runId salt wait abandoned; the run was closed")
            return false
        }
    }
}


private suspend fun MessengerStore.askForSaltAgain(runId: Long) {
    val reply = try {
        askDaemon(FrameType.DeviceSync, DeviceSyncRequest(false).toJson())
    } catch (t: Throwable) {
        Logger.w("devicelink", "run $runId devicesync after the salt wait failed: ${t.message ?: "?"}")
        return
    }
    Logger.d("devicelink", "run $runId asked for a sync after the salt wait: type=${reply.type}")
}


private suspend fun MessengerStore.verifyOtherGuard(runId: Long, peerId: String, pin: String) {
    
    
    val reply = askUnlessDropped(
        FrameType.VerifyDestroyPin,
        VerifyDestroyPinRequest(peerId, pin).toJson(),
        DEVICE_LINK_VERIFY_TIMEOUT,
    )
    if (!isCurrent(runId)) return
    
    
    if (isNoAnswer(reply)) {
        finishLinkedUnchecked(runId, VERIFY_DAEMON_NO_ANSWER)
        return
    }
    if (reply.type != FrameType.DestroyPinVerified || reply.payload == null) {
        finishLinkedUnchecked(runId, VERIFY_NOT_SENT)
        return
    }
    val out = DestroyPinVerifiedResponse.fromJson(reply.payload)
    when {
        
        
        out.reason.isNotEmpty() -> failRun(runId, verifyOutcome(out).text)
        !out.answered -> finishLinkedUnchecked(runId, VERIFY_NO_ANSWER)
        !out.verified -> failRun(runId, VERIFY_REFUSED)
        else -> finishRun(runId, CEREMONY_DONE)
    }
}


internal suspend fun MessengerStore.retryDeviceLinkGuard() {
    val run = _deviceLinkRun.value ?: return
    if (!run.retryable) return
    val peerId = run.peerId.ifEmpty { _linkedDevices.value.firstOrNull()?.peerId.orEmpty() }
    if (peerId.isEmpty()) {
        failRun(run.id, NOT_LINKED)
        return
    }
    updateRun(run.id) {
        it.copy(phase = DeviceLinkPhase.Working, failed = false, retryable = false, busy = true, peerId = peerId)
    }
    say(run.id, GUARD_SETTING)
    if (!writeOwnGuard(run.id, run.myPin)) return
    stepOk(run.id, GUARD_WAITING)
    if (!awaitSalt(run.id, peerId)) return
    stepOk(run.id, GUARD_CHECKING)
    verifyOtherGuard(run.id, peerId, run.otherPin)
}


private suspend fun MessengerStore.startGuardOnly(runId: Long) {
    val peerId = _linkedDevices.value.firstOrNull()?.peerId.orEmpty()
    if (peerId.isEmpty()) {
        failRun(runId, NOT_LINKED)
        return
    }
    updateRun(runId) { it.copy(peerId = peerId) }
    val claimed = claimGuardSteps(runId) ?: return
    runGuardSteps(runId, claimed)
}


internal suspend fun MessengerStore.reconcileDeviceLinkAfterConnect() {
    val devices = readLinkedDevices()
    val guard = readDestroyGuard()
    val run = _deviceLinkRun.value ?: return
    if (run.phase != DeviceLinkPhase.Working) return
    val runId = run.id
    val sibling = devices?.firstOrNull()?.peerId.orEmpty()

    if (sibling.isNotEmpty()) {
        if (guard?.set == true) {
            
            
            if (run.otherPin.isEmpty()) {
                finishRun(runId, CEREMONY_LINKED_UNCHECKED)
                return
            }
            updateRun(runId) { if (it.peerId.isEmpty()) it.copy(peerId = sibling) else it }
            aside(runId, LINKED_WHILE_AWAY)
            stepOk(runId, GUARD_CHECKING)
            verifyOtherGuard(runId, sibling, run.otherPin)
            return
        }
        
        
        updateRun(runId) { if (it.peerId.isEmpty()) it.copy(peerId = sibling) else it }
        val peerId = claimGuardSteps(runId)
        if (peerId == null) {
            aside(runId, RECONNECTED)
            return
        }
        aside(runId, LINKED_WHILE_AWAY)
        runGuardSteps(runId, peerId)
        return
    }

    val deadline = run.expiresAt
    if (deadline > 0L && deviceLinkTimings.nowSeconds() > deadline) {
        failRun(runId, OFFER_RAN_OUT)
        aside(runId, NOTHING_LINKED)
        return
    }
    
    
    aside(runId, RECONNECTED)
}


internal suspend fun MessengerStore.readLinkedDevices(): List<DeviceLinkEntry>? {
    return try {
        val reply = askDaemon(FrameType.DeviceLinksList, DeviceLinksListRequest().toJson())
        if (reply.type != FrameType.DeviceLinksListed || reply.payload == null) return null
        val devices = DeviceLinksListedResponse.fromJson(reply.payload).devices
        _linkedDevices.value = devices
        devices
    } catch (t: Throwable) {
        Logger.w("devicelink", "devicelinks_list failed: ${t.message ?: "?"}")
        null
    }
}


internal suspend fun MessengerStore.readDestroyGuard(): DestroyPinListedResponse? {
    return try {
        val reply = askDaemon(FrameType.GetDestroyPin, GetDestroyPinRequest().toJson())
        if (reply.type != FrameType.DestroyPinListed || reply.payload == null) return null
        val listed = DestroyPinListedResponse.fromJson(reply.payload)
        _destroyGuard.value = listed
        listed
    } catch (t: Throwable) {
        Logger.w("devicelink", "get_destroy_pin failed: ${t.message ?: "?"}")
        null
    }
}


suspend fun MessengerStore.stopDestroyWaiting(): DeviceLinkActionResult {
    val reply = try {
        askDaemon(FrameType.StopDestroyWaiting)
    } catch (t: Throwable) {
        Logger.w("devicelink", "stop_destroy_waiting failed: ${t.message ?: "?"}")
        null
    }
    if (reply == null || reply.type != FrameType.DestroyWaitingStopped) {
        return DeviceLinkActionResult(false, STOP_WAITING_FAILED)
    }
    readLinkedDevices()
    readDestroyGuard()
    Logger.i("devicelink", "stopped waiting for a destroy receipt")
    return DeviceLinkActionResult(true, STOP_WAITING_DONE)
}

fun MessengerStore.guardOnlyIsOffered(): Boolean =
    _linkedDevices.value.isNotEmpty() && _destroyGuard.value?.set == false


data class DeviceLinkActionResult(
    val ok: Boolean,
    val text: String,
)


suspend fun MessengerStore.verifyOtherDestroyPin(peerId: String, pin: String): DeviceLinkActionResult =
    runLinkedAction(peerId, ACTION_LABEL_VERIFY) { checkOtherDestroyPin(peerId, pin) }

private suspend fun MessengerStore.checkOtherDestroyPin(peerId: String, pin: String): DeviceLinkActionResult {
    val trimmed = pin.trim()
    if (!isFourDigits(trimmed)) return DeviceLinkActionResult(false, PIN_FOUR_DIGITS)
    val reply = askUnlessDropped(
        FrameType.VerifyDestroyPin,
        VerifyDestroyPinRequest(peerId, trimmed).toJson(),
        DEVICE_LINK_VERIFY_TIMEOUT,
    )
    if (isNoAnswer(reply)) {
        Logger.w("devicelink", "verify_destroy_pin had no answer from the daemon")
        return DeviceLinkActionResult(false, VERIFY_DAEMON_NO_ANSWER)
    }
    val out = if (reply.type == FrameType.DestroyPinVerified && reply.payload != null) {
        DestroyPinVerifiedResponse.fromJson(reply.payload)
    } else {
        null
    }
    val result = verifyOutcome(out)
    Logger.d("devicelink", "verify_destroy_pin answered ok=${result.ok}")
    return result
}


suspend fun MessengerStore.unlinkLinkedDevice(peerId: String): DeviceLinkActionResult =
    runLinkedAction(peerId, ACTION_LABEL_UNLINK) { sendUnlink(peerId, "", wantedDestroy = false) }


suspend fun MessengerStore.destroyLinkedDevice(peerId: String, pin: String): DeviceLinkActionResult =
    runLinkedAction(peerId, ACTION_LABEL_DESTROY) {
        val trimmed = pin.trim()
        if (!isFourDigits(trimmed)) {
            DeviceLinkActionResult(false, PIN_FOUR_DIGITS)
        } else {
            sendUnlink(peerId, trimmed, wantedDestroy = true)
        }
    }


internal suspend fun MessengerStore.runLinkedAction(
    peerId: String,
    label: String,
    action: suspend () -> DeviceLinkActionResult,
): DeviceLinkActionResult {
    val cur = _linkedAction.value
    if (cur.busyLabel.isNotEmpty() || !_linkedAction.compareAndSet(cur, LinkedActionState(peerId = peerId, busyLabel = label))) {
        Logger.d("devicelink", "linked action refused; another is running")
        return DeviceLinkActionResult(false, LINKED_ACTION_RUNNING)
    }
    var result: DeviceLinkActionResult? = null
    try {
        val r = action()
        result = r
        return r
    } finally {
        _linkedAction.value = LinkedActionState(peerId = peerId, note = result?.text.orEmpty())
    }
}


private fun isNoAnswer(reply: Frame): Boolean =
    reply.type == FrameType.Error && reasonOf(reply) == REASON_NO_ANSWER


private suspend fun MessengerStore.sendUnlink(
    peerId: String,
    pin: String,
    wantedDestroy: Boolean,
): DeviceLinkActionResult {
    val reply = askUnlessDropped(
        FrameType.UnlinkDevice,
        UnlinkDeviceRequest(peerId, pin).toJson(),
        DEVICE_LINK_DESTROY_TIMEOUT,
    )
    if (isNoAnswer(reply)) {
        
        
        Logger.w("devicelink", "unlink_device had no answer from the daemon destroy=$wantedDestroy")
        return DeviceLinkActionResult(false, if (wantedDestroy) DESTROY_DAEMON_NO_ANSWER else UNLINK_DAEMON_NO_ANSWER)
    }
    val out = if (reply.type == FrameType.DeviceUnlinked && reply.payload != null) {
        DeviceUnlinkedResponse.fromJson(reply.payload)
    } else {
        null
    }
    if (out?.unlinked == true) {
        readLinkedDevices()
        readDestroyGuard()
    }
    val result = unlinkOutcome(out, wantedDestroy)
    Logger.d(
        "devicelink",
        "unlink_device destroy=$wantedDestroy unlinked=${out?.unlinked ?: false} " +
            "sent=${out?.sent ?: false} receipted=${out?.receipted ?: false}",
    )
    return result
}


internal fun verifyOutcome(out: DestroyPinVerifiedResponse?): DeviceLinkActionResult = when {
    out == null -> DeviceLinkActionResult(false, VERIFY_NOT_SENT)
    out.reason == DestroyPinVerifiedReason.ANSWER_DID_NOT_CHECK_OUT ->
        DeviceLinkActionResult(false, VERIFY_ANSWER_BAD)
    out.reason == DestroyPinVerifiedReason.NO_SALT_FOR_SIBLING ->
        DeviceLinkActionResult(false, VERIFY_NO_DETAILS)
    out.reason == DestroyPinVerifiedReason.NOT_MY_DEVICE ->
        DeviceLinkActionResult(false, VERIFY_NOT_MY_DEVICE)
    out.reason == DestroyPinSetReason.BAD_PIN_SHAPE ->
        DeviceLinkActionResult(false, PIN_FOUR_DIGITS)
    
    
    out.reason.isNotEmpty() -> DeviceLinkActionResult(false, VERIFY_NOT_SENT)
    !out.answered -> DeviceLinkActionResult(false, VERIFY_NO_ANSWER)
    !out.verified -> DeviceLinkActionResult(false, VERIFY_REFUSED)
    else -> DeviceLinkActionResult(true, VERIFY_ACCEPTED)
}


internal fun unlinkOutcome(out: DeviceUnlinkedResponse?, wantedDestroy: Boolean): DeviceLinkActionResult = when {
    out == null -> DeviceLinkActionResult(false, UNLINK_NOT_DONE)
    out.reason == DestroyPinSetReason.WRONG_DESTROY_PIN ->
        DeviceLinkActionResult(false, DESTROY_WRONG_PIN)
    out.reason == DestroyPinVerifiedReason.NO_SALT_FOR_SIBLING ->
        DeviceLinkActionResult(false, DESTROY_NO_DETAILS)
    out.reason == DestroyPinVerifiedReason.NOT_MY_DEVICE ->
        DeviceLinkActionResult(false, UNLINK_NOT_MY_DEVICE)
    out.reason == DestroyPinSetReason.BAD_PIN_SHAPE ->
        DeviceLinkActionResult(false, PIN_FOUR_DIGITS)
    !out.unlinked -> DeviceLinkActionResult(false, UNLINK_NOT_DONE)
    !wantedDestroy -> DeviceLinkActionResult(true, UNLINK_DONE)
    out.receipted -> DeviceLinkActionResult(true, DESTROY_ANSWERED)
    out.sent -> DeviceLinkActionResult(true, DESTROY_SENT_NO_ANSWER)
    else -> DeviceLinkActionResult(true, DESTROY_NOT_REACHED)
}


data class DeviceLinkFailureText(
    val headline: String,
    val guidance: List<String> = emptyList(),
)


internal fun explainDeviceLinkFailure(reason: String, detail: String): DeviceLinkFailureText {
    val norm = reason.trim().lowercase().replace('-', '_')
    val extra = detail.trim()
    return when {
        norm.isEmpty() ->
            DeviceLinkFailureText(FAILURE_FALLBACK, if (extra.isEmpty()) emptyList() else listOf(extra))

        
        norm.contains("no_answer") -> DeviceLinkFailureText(
            "The link may have gone through.",
            listOf(
                "Restart the app, then read the linked-device list on this device. " +
                    "If it lists the other device, this one catches up on its own. " +
                    "If it does not, this device kept part of a join that never finished: " +
                    "erase it under $WIPE_BLOCK_PATH, then join again.",
            ),
        )

        norm.contains("not_empty") || norm.contains("has_contact") ||
            norm.contains("has_chat") || norm.contains("has_peer") ||
            norm.contains("nonempty") -> DeviceLinkFailureText(
            "This device already has contacts or chats, so it cannot join another device.",
            listOf(
                "A joining device takes on the other device's identity whole, " +
                    "which only works from a clean slate.",
                "Use a device you have not set up yet, or erase this one under " +
                    "$WIPE_BLOCK_PATH, then join again.",
            ),
        )

        norm.contains("peer_check") || norm.contains("registry") -> DeviceLinkFailureText(
            "This device could not confirm it is a clean slate, so it did not join.",
            listOf(
                "Joining is only safe from a device with no contacts and no chats, " +
                    "and that could not be checked just now.",
                "Check that this device's daemon is up, then join again.",
            ),
        )

        norm.contains("bundle") -> DeviceLinkFailureText(
            "The other device never sent its identity, so this device stayed unchanged.",
            listOf(
                "The handshake got as far as connecting, then stopped. " +
                    "Nothing was adopted and nothing was linked.",
                "Offer the link again on the other device and enter the fresh words here.",
            ),
        )

        norm.contains("expire") || norm.contains("timeout") || norm.contains("timed_out") ->
            DeviceLinkFailureText(
                "The offer ran out before the other device joined.",
                listOf("Offer the link again and enter the fresh words on the other device."),
            )

        norm.contains("cancel") || norm.contains("withdraw") ->
            DeviceLinkFailureText("The offer was withdrawn before the other device joined.")

        
        norm.contains("already_linked") || norm.contains("already_a_device") ->
            DeviceLinkFailureText(
                "This device already has a linked device, and you can only have one.",
                listOf("Unlink it from the linked-device list, then link the new device."),
            )

        norm.contains("word") -> DeviceLinkFailureText(
            "Those words did not open a link offer.",
            listOf("Re-read them from the offering device, all of them, in order, then join again."),
        )

        norm.contains("unreachable") || norm.contains("dial") || norm.contains("connect") ->
            DeviceLinkFailureText(
                "This device could not reach the one that made the offer.",
                listOf("Both devices need a working Tor connection. Check the status on each, then try again."),
            )

        else -> DeviceLinkFailureText(
            FAILURE_FALLBACK,
            listOf(if (extra.isEmpty()) norm else "$norm: $extra"),
        )
    }
}


internal fun MessengerStore.isCurrent(runId: Long): Boolean =
    _deviceLinkRun.value?.id == runId

private fun MessengerStore.currentRun(runId: Long): DeviceLinkRun? =
    _deviceLinkRun.value?.takeIf { it.id == runId }


private fun MessengerStore.updateRun(runId: Long, f: (DeviceLinkRun) -> DeviceLinkRun) {
    _deviceLinkRun.update { cur -> if (cur == null || cur.id != runId) cur else f(cur) }
}


private fun MessengerStore.say(runId: Long, text: String) {
    updateRun(runId) { it.copy(log = it.log + DeviceLinkLogLine(text), busy = true) }
}


private fun MessengerStore.aside(runId: Long, text: String) {
    updateRun(runId) { it.copy(log = it.log + DeviceLinkLogLine(text, aside = true)) }
}


private fun MessengerStore.stepOk(runId: Long, next: String) {
    updateRun(runId) {
        it.copy(
            log = it.log.markLast(DeviceLinkOutcome.Done) + DeviceLinkLogLine(next),
            busy = true,
        )
    }
}


private fun MessengerStore.refuse(runId: Long, text: String) {
    updateRun(runId) {
        it.copy(log = it.log + DeviceLinkLogLine(text, outcome = DeviceLinkOutcome.Failed), busy = false)
    }
    Logger.d("devicelink", "run $runId refused: $text")
}


private fun MessengerStore.failRun(runId: Long, text: String) {
    updateRun(runId) {
        it.copy(
            log = it.log.markLast(DeviceLinkOutcome.Failed) + DeviceLinkLogLine(text, aside = true),
            phase = DeviceLinkPhase.Done,
            busy = false,
            failed = true,
        )
    }
    Logger.w("devicelink", "run $runId ended badly: $text")
}


private fun MessengerStore.failRetryable(runId: Long, text: String) {
    failRun(runId, text)
    updateRun(runId) { it.copy(retryable = true) }
}


private fun MessengerStore.finishRun(runId: Long, text: String) {
    updateRun(runId) {
        it.copy(
            log = it.log.markLast(DeviceLinkOutcome.Done) +
                DeviceLinkLogLine(text, outcome = DeviceLinkOutcome.Done),
            phase = DeviceLinkPhase.Done,
            busy = false,
            failed = false,
            retryable = false,
        )
    }
    Logger.d("devicelink", "run $runId finished")
}


private fun MessengerStore.finishLinked(runId: Long, text: String) {
    updateRun(runId) {
        it.copy(
            log = it.log + DeviceLinkLogLine(text, outcome = DeviceLinkOutcome.Done),
            phase = DeviceLinkPhase.Done,
            busy = false,
            failed = false,
            retryable = false,
        )
    }
    Logger.d("devicelink", "run $runId finished linked; the other device's details have not arrived")
}


private fun MessengerStore.finishLinkedUnchecked(runId: Long, reason: String) {
    updateRun(runId) {
        it.copy(
            log = it.log.markLast(DeviceLinkOutcome.Warning) +
                DeviceLinkLogLine(reason, aside = true) +
                DeviceLinkLogLine(LINKED_VERIFY_LATER),
            phase = DeviceLinkPhase.Done,
            busy = false,
            failed = false,
            retryable = false,
        )
    }
    Logger.w("devicelink", "run $runId finished linked; the PIN check did not finish: $reason")
}


internal fun MessengerStore.retryDeviceLinkJoinLater(
    runId: Long,
    line: String = JOIN_TOR_SLOW,
    torSlow: Boolean = line == JOIN_TOR_SLOW,
): Boolean {
    var moved = false
    _deviceLinkRun.update { cur ->
        moved = false
        if (cur == null || cur.id != runId) return@update cur
        if (cur.mode != DeviceLinkMode.Joining || cur.phase != DeviceLinkPhase.Working) return@update cur
        
        
        if (cur.peerId.isNotEmpty() || cur.guardStepsStarted) return@update cur
        moved = true
        cur.copy(
            log = cur.log.markLast(DeviceLinkOutcome.Failed) + DeviceLinkLogLine(line, aside = true, torSlow = torSlow),
            phase = DeviceLinkPhase.Collecting,
            busy = false,
            failed = false,
            retryable = false,
            dialStartedAtMs = 0L,
        )
    }
    return moved
}


private fun MessengerStore.joinWentUnanswered(runId: Long) {
    val run = currentRun(runId) ?: return
    if (run.phase != DeviceLinkPhase.Working || run.guardStepsStarted) {
        Logger.d("devicelink", "run $runId no_answer ignored; the run has moved on")
        return
    }
    val text = explainDeviceLinkFailure(REASON_NO_ANSWER, "")
    failRun(runId, text.headline)
    text.guidance.forEach { aside(runId, it) }
}


private fun List<DeviceLinkLogLine>.markLast(outcome: DeviceLinkOutcome): List<DeviceLinkLogLine> {
    val i = indexOfLast { !it.aside }
    if (i < 0) return this
    return toMutableList().also { it[i] = it[i].copy(outcome = outcome) }
}


internal val DEVICE_LINK_REQUEST_TIMEOUT: Duration = 90.seconds


internal val DEVICE_LINK_VERIFY_TIMEOUT: Duration = 180.seconds


internal val DEVICE_LINK_DESTROY_TIMEOUT: Duration = 240.seconds


internal suspend fun MessengerStore.askDaemon(type: String, payload: JSONObject? = null): Frame {
    requestSender?.let { return it(type, payload) }
    val c = ipc ?: throw IllegalStateException(NO_DAEMON)
    return c.request(type = type, payload = payload, timeout = DEVICE_LINK_REQUEST_TIMEOUT)
}


internal const val PIN_FOUR_DIGITS = "Each PIN is four digits."
internal const val PIN_MUST_DIFFER = "The two PINs must differ."
internal const val WORDS_NEEDED = "The seven words are needed to join."

internal const val OFFER_PUBLISHING = "publishing the invitation"
internal const val OFFER_WAITING_FOR_JOIN = "waiting for your other device to join"
internal const val PROBE_REACHABLE =
    "Reachable. The new device can find this one now. Enter the words there."
internal const val PROBE_UNCONFIRMED =
    "Could not confirm this device is reachable. The words above are still live, " +
        "so enter them on the new device."

internal fun probeStillWaiting(attempt: Int): String =
    "still publishing this device's rendezvous (attempt $attempt). No answer yet."

internal const val JOIN_CONTACTING = "contacting your other device"
internal const val JOIN_FINGERPRINT_PREFIX = "Both devices now share this identity: "

internal const val PINS_NOT_COLLECTED = "The PINs were not collected, so this device has no guard."
internal const val GUARD_SETTING = "setting this device's destroy PIN"
internal const val GUARD_WAITING = "waiting for your other device"
internal const val GUARD_CHECKING = "checking your other device's PIN"
internal const val GUARD_NOT_TAKEN =
    "This device did not take that PIN. The two are linked; set it again from this screen."
internal const val GUARD_NO_DETAILS =
    "linked, and this device's PIN is set. " +
        "Your other device's PIN: details not received yet. Check it later from this screen."
internal const val VERIFY_NOT_SENT = "The check could not be sent."
internal const val VERIFY_NO_ANSWER = "Your other device did not answer."
internal const val VERIFY_REFUSED = "Your other device did not accept that PIN."


internal const val VERIFY_ANSWER_BAD = "Your other device's answer did not check out."


internal const val VERIFY_DAEMON_NO_ANSWER = "No answer came back from the daemon, so the check did not finish."


internal const val LINKED_VERIFY_LATER =
    "Linked. Please press [Verify destroy PIN] later to check your other device's PIN."
internal const val CEREMONY_DONE = "linked, and both PINs check out"


internal const val CEREMONY_LINKED_UNCHECKED =
    "linked, and this device's PIN is set. Your other device's PIN was not " +
        "checked. Check it from this screen."
internal const val NOT_LINKED = "This device is not linked, so there is nothing to guard."


internal const val LINKED_DEVICES_NOT_READ =
    "The linked devices could not be read. Press Close to try again."


internal const val VERIFY_ACCEPTED = "Your other device accepted that PIN."
internal const val VERIFY_NO_DETAILS =
    "Your other device has not sent its details yet, so its PIN cannot be checked."
internal const val VERIFY_NOT_MY_DEVICE = "That is not one of your devices."

internal const val UNLINK_DONE = "Unlinked. This device has forgotten your other device."
internal const val UNLINK_NOT_DONE = "The unlink could not be done."
internal const val UNLINK_NOT_MY_DEVICE = "That is not one of your devices."


internal const val UNLINK_DAEMON_NO_ANSWER =
    "No answer came back from the daemon. The unlink may have been done; check the state again."
internal const val DESTROY_DAEMON_NO_ANSWER =
    "No answer came back from the daemon. The destroy may have been done; check the state again."


internal const val LINKED_ACTION_RUNNING = "Another action is still running. Wait for its answer."


internal const val ACTION_LABEL_VERIFY = "Verify destroy PIN"
internal const val ACTION_LABEL_UNLINK = "Unlink"
internal const val ACTION_LABEL_DESTROY = "Destroy"


data class LinkedActionState(
    val peerId: String = "",
    val busyLabel: String = "",
    val note: String = "",
)

internal const val DESTROY_WRONG_PIN = "That device did not accept that PIN. Nothing was changed."
internal const val DESTROY_NO_DETAILS = "That device has not sent what is needed yet. Nothing was changed."


internal const val DESTROY_ANSWERED =
    "Unlinked. That device answered that it erased itself."
internal const val DESTROY_SENT_NO_ANSWER =
    "Unlinked. The command went out and no answer came back."
internal const val DESTROY_NOT_REACHED =
    "Unlinked. That device could not be reached, so nothing was commanded."

internal const val FAILURE_FALLBACK = "The device link did not go through."
internal const val OFFER_RAN_OUT = "The offer ran out before the other device joined."
internal const val NOTHING_LINKED = "Nothing was linked."
internal const val STOP_WAITING_DONE =
    "Stopped waiting. That device is off this identity and you can link another one. " +
        "Whether it erased itself is not known."
internal const val STOP_WAITING_FAILED = "The wait could not be stopped."

internal const val RECONNECTED = "The connection to the daemon dropped and came back."
internal const val LINKED_WHILE_AWAY =
    "The link came up while the connection was down. Carrying on with the PINs."
internal const val NO_DAEMON = "There is no daemon to ask."
