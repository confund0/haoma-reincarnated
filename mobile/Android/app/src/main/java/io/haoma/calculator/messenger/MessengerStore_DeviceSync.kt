package io.haoma.calculator.messenger

import io.haoma.calculator.core.ipc.Frame
import io.haoma.calculator.core.ipc.FrameType
import io.haoma.calculator.log.Logger
import kotlin.time.Duration.Companion.seconds
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch


internal val DEVICE_SYNC_TIMEOUT = 30.seconds


data class DeviceSyncWait(val full: Boolean, val sentAt: Long)


internal data class DeviceSyncOutcome(val toast: String, val started: Boolean)


fun MessengerStore.syncMissed() = syncLinkedDevices(full = false)


fun MessengerStore.syncEverything() = syncLinkedDevices(full = true)

internal fun MessengerStore.toast(text: String) {
    if (!_toasts.tryEmit(text)) Logger.d("gapfill", "toast dropped: the buffer is full")
}


private fun MessengerStore.syncLinkedDevices(full: Boolean) {
    if (!_connection.value) {
        toast(SYNC_NO_DAEMON)
        return
    }
    var start = false
    _inheritedPairing.update { st ->
        start = !st.syncing
        if (start) st.copy(syncing = true) else st
    }
    if (!start) {
        Logger.d("gapfill", "sync pressed while a sync request is out; nothing sent")
        return
    }
    scope.launch { runDeviceSync(full) }
}


private suspend fun MessengerStore.runDeviceSync(full: Boolean) {
    var text = SYNC_ENDED
    try {
        val reply = askUnlessDropped(FrameType.DeviceSync, DeviceSyncRequest(full).toJson(), DEVICE_SYNC_TIMEOUT)
        val outcome = deviceSyncOutcome(reply, full)
        if (outcome.started) {
            deviceSyncWait.set(DeviceSyncWait(full, inheritedPairingTimings.nowSeconds()))
        }
        text = outcome.toast
        Logger.d("gapfill", "sync request answered: type=${reply.type} full=$full started=${outcome.started}")
    } finally {
        _inheritedPairing.update { it.copy(syncing = false) }
        toast(text)
    }
}


internal fun MessengerStore.onDeviceSyncFinished(p: DeviceSyncFinishedPush) {
    val wait = deviceSyncWait.getAndSet(null)
    if (wait == null) {
        Logger.d("gapfill", "sync finished push for a sync this phone did not start; no toast")
        return
    }
    val age = inheritedPairingTimings.nowSeconds() - wait.sentAt
    if (age > inheritedPairingTimings.syncFinishWait.inWholeSeconds) {
        Logger.d("gapfill", "sync finished push came after ${age}s, past the wait; no toast")
        return
    }
    toast(deviceSyncFinishedToast(p))
}


internal fun deviceSyncOutcome(reply: Frame, full: Boolean): DeviceSyncOutcome {
    if (reply.type == FrameType.DeviceSyncSent) {
        val r = reply.payload?.let { DeviceSyncSentResponse.fromJson(it) }
        val siblings = r?.siblings
        
        
        return when {
            siblings == null -> DeviceSyncOutcome(SYNC_SENT_UNREADABLE, started = true)
            siblings == 0 -> DeviceSyncOutcome(SYNC_NO_SIBLINGS, started = false)
            r?.alreadyRunning == true -> DeviceSyncOutcome(SYNC_ALREADY_RUNNING, started = false)
            else -> DeviceSyncOutcome(if (full) SYNC_REQUESTED_FULL else SYNC_REQUESTED, started = true)
        }
    }
    return DeviceSyncOutcome(deviceSyncRefusalToast(reasonOf(reply)), started = false)
}


internal fun deviceSyncRefusalToast(reason: String): String {
    val r = explainDeviceSyncRefusal(reason)
    if (r.worded) return r.headline
    val norm = normaliseReason(reason)
    return if (norm.isEmpty()) "${r.headline} The daemon gave no reason." else "Sync refused: $norm"
}


internal fun MessengerStore.dropDeviceSyncWait(reason: String) {
    if (deviceSyncWait.getAndSet(null) != null) {
        Logger.d("gapfill", "sync wait dropped: $reason")
    }
}


internal fun deviceSyncFinishedToast(p: DeviceSyncFinishedPush): String {
    val parts = mutableListOf<String>()
    when (p.changes) {
        0 -> {}
        1 -> parts += "1 change"
        else -> parts += "${p.changes} changes"
    }
    if (p.held > 0) parts += "${p.held} not placed yet"
    val brought = parts.joinToString(", ")

    
    if (p.unreachable > 0 && p.answered == 0 && !p.timedOut) {
        return if (p.unreachable == 1) "Sync failed: could not reach the linked device"
        else "Sync failed: could not reach the linked devices"
    }
    if (p.timedOut || p.unreachable > 0) {
        val heard = if (p.answered == 0 && p.timedOut && p.unreachable == 0) {
            if (p.siblings == 1) "no answer from the linked device" else "no answer from the linked devices"
        } else {
            "${p.answered} of ${p.siblings} linked devices answered"
        }
        val head = if (p.timedOut) "Sync stopped waiting" else "Sync finished"
        return "$head: $heard" + (if (brought.isEmpty()) "" else ", $brought")
    }
    val name = if (p.full) "Full sync finished" else "Sync finished"
    return "$name: " + brought.ifEmpty { "nothing new" }
}


internal fun explainDeviceSyncRefusal(reason: String): Refusal {
    val norm = normaliseReason(reason)
    return when {
        
        
        norm.contains(REASON_NO_ANSWER) -> Refusal(
            "The sync request had no answer.",
            listOf(
                "No answer came back from the daemon, so this device cannot say whether it asked your linked devices.",
                "Asking again is safe.",
            ),
        )

        
        norm.contains("encode_frame") -> Refusal(
            "The daemon's answer could not be read.",
            listOf("If this device has a linked device, it was asked. Anything missing will appear in the chats as it arrives."),
        )

        norm.contains("bad_payload") -> Refusal(
            "The daemon could not read the sync request.",
            listOf("Nothing was asked. This build and the daemon disagree about the request."),
        )

        else -> Refusal("The sync request was refused.", listOf(unworded(norm)), worded = false)
    }
}


internal const val SYNC_ACTION = "sync"
internal const val SYNC_EVERYTHING_LABEL = "sync everything"
internal const val SYNC_REQUESTED = "Requested sync"
internal const val SYNC_REQUESTED_FULL = "Requested FULL sync"
internal const val SYNC_ALREADY_RUNNING = "A sync is already running"
internal const val SYNC_NO_SIBLINGS = "No linked device to sync with"
internal const val SYNC_NO_DAEMON = "Not connected to the daemon. Sync again when it is back."
internal const val SYNC_SENT_UNREADABLE = "Sync requested, but the answer could not be read"
internal const val SYNC_ENDED = "The sync request ended before an answer came back."
