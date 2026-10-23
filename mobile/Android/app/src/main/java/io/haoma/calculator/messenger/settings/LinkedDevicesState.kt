package io.haoma.calculator.messenger.settings

import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.withStyle
import io.haoma.calculator.messenger.DestroyPinListedResponse
import io.haoma.calculator.messenger.DestroyWaiting
import io.haoma.calculator.messenger.DeviceLinkEntry
import io.haoma.calculator.messenger.DeviceLinkLogLine
import io.haoma.calculator.messenger.DeviceLinkMode
import io.haoma.calculator.messenger.DeviceLinkOutcome
import io.haoma.calculator.messenger.DeviceLinkRun
import io.haoma.calculator.messenger.WIPE_BLOCK_PATH


enum class LinkedDevicesView {
    
    Form,

    
    Run,

    
    Linked,

    
    Waiting,
}


fun linkedDevicesView(
    run: DeviceLinkRun?,
    devices: List<DeviceLinkEntry>,
    waiting: DestroyWaiting? = null,
): LinkedDevicesView = when {
    
    
    waiting != null -> LinkedDevicesView.Waiting
    run != null -> LinkedDevicesView.Run
    devices.isNotEmpty() -> LinkedDevicesView.Linked
    else -> LinkedDevicesView.Form
}


fun destroyWaitRemaining(deadline: Long, nowSeconds: Long): String {
    val left = deadline - nowSeconds
    if (left <= 0L) return "00:00"
    val minutes = (left + 59L) / 60L
    return "%02d:%02d".format(minutes / 60L, minutes % 60L)
}


fun joinTabIsOffered(contactCount: Int, chatCount: Int): Boolean =
    contactCount == 0 && chatCount == 0


data class LinkedDeviceCard(
    val idShort: String,
    val linkedAtSeconds: Long,
    val guardHeld: Boolean,
    val saltHeld: Boolean,
    val canCheckOtherPin: Boolean,
)


fun linkedDeviceCard(device: DeviceLinkEntry, guard: DestroyPinListedResponse?): LinkedDeviceCard {
    val saltHeld = guard?.siblings?.any { it.peerId == device.peerId && it.saltHeld } == true
    return LinkedDeviceCard(
        idShort = shortPeerId(device.peerId),
        linkedAtSeconds = device.linkedAt,
        guardHeld = guard?.set == true,
        saltHeld = saltHeld,
        
        
        canCheckOtherPin = saltHeld,
    )
}


fun shortPeerId(peerId: String): String = peerId.take(8)


data class DeviceLinkLogRow(
    val text: String,
    val marker: String,
    val aside: Boolean,
    val spinner: Boolean,
    
    val torSlow: Boolean = false,
)


const val LOG_MARKER_NONE = ""


const val LOG_MARKER_DONE = "✓"


const val LOG_MARKER_FAILED = "✗"


const val LOG_MARKER_WARNING = "!"


fun deviceLinkLogRows(run: DeviceLinkRun): List<DeviceLinkLogRow> {
    val spinAt = if (run.busy) run.log.indexOfLast { !it.aside && it.outcome == DeviceLinkOutcome.None } else -1
    return run.log.mapIndexed { i, line ->
        DeviceLinkLogRow(
            text = line.text,
            marker = markerFor(line),
            aside = line.aside,
            spinner = i == spinAt,
            torSlow = line.torSlow,
        )
    }
}

private fun markerFor(line: DeviceLinkLogLine): String = when {
    line.aside -> LOG_MARKER_NONE
    line.outcome == DeviceLinkOutcome.Done -> LOG_MARKER_DONE
    line.outcome == DeviceLinkOutcome.Warning -> LOG_MARKER_WARNING
    line.outcome == DeviceLinkOutcome.Failed -> LOG_MARKER_FAILED
    else -> LOG_MARKER_NONE
}


const val LINKED_DEVICES_LABEL = "Linked devices"

const val TAB_INVITE = "Invite a device"
const val TAB_JOIN = "Join a device"

const val JOIN_NOT_OFFERED =
    "This device already has contacts or chats, so it cannot join another device. " +
        "A joining device takes on the other device's identity whole, which only " +
        "works from a device you have not set up yet. Invite from here instead.\n\n" +
        "To make this device empty, erase it: $WIPE_BLOCK_PATH."


fun boldedPath(text: String, bold: String = WIPE_BLOCK_PATH): AnnotatedString = buildAnnotatedString {
    var from = 0
    while (true) {
        val at = text.indexOf(bold, from)
        if (at < 0) {
            append(text.substring(from))
            return@buildAnnotatedString
        }
        append(text.substring(from, at))
        withStyle(SpanStyle(fontWeight = FontWeight.Bold)) { append(bold) }
        from = at + bold.length
    }
}

const val FIELD_MY_PIN = "This device's destroy PIN"
const val FIELD_OTHER_PIN = "Your other device's destroy PIN"
const val FIELD_WORDS = "The seven words from your other device"

const val ACTION_INVITE = "Invite"
const val ACTION_JOIN = "Join"
const val ACTION_CLOSE = "Close"
const val ACTION_CANCEL = "Cancel"
const val ACTION_SET_DESTROY_PIN = "Set destroy PIN"
const val ACTION_VERIFY_DESTROY_PIN = "Verify destroy PIN"
const val ACTION_UNLINK = "Unlink"
const val ACTION_STOP_WAITING = "Stop waiting"

const val WAITING_TITLE = "Holding the address for that device"


fun destroyWaitText(remaining: String): String =
    "Holding this device's address another $remaining so that device can be " +
        "told it was removed.\n\n" +
        "It is already off this identity. If it was switched off when you " +
        "destroyed it, this is how it finds out and erases itself — the first " +
        "time it reaches this device.\n\n" +
        "Until the window ends you cannot link another device, because you can " +
        "only have one.\n\n" +
        "Stopping now lets you link a new device. It does not undo the destroy, " +
        "and that device will never be told, so whether it erased itself stays " +
        "unknown."

const val WAITING_ACK = "I know what I am doing"
const val ACTION_DESTROY = "Destroy"

const val PIN_HELP =
    "Both PINs are typed now, while you are holding both devices. They are " +
        "written after the link comes up. The two must differ."


const val SET_DESTROY_PIN_HELP =
    "The two devices are linked, but this one never got its destroy PIN. " +
        "Linking did not finish. You can finish it from here — the devices " +
        "stay linked and nothing is set up again."


enum class WordsSlot {
    
    Readout,

    
    Field,

    
    None,
}


fun wordsSlot(mode: DeviceLinkMode): WordsSlot = when (mode) {
    DeviceLinkMode.Inviting -> WordsSlot.Readout
    DeviceLinkMode.Joining -> WordsSlot.Field
    DeviceLinkMode.GuardOnly -> WordsSlot.None
}


fun runTitle(mode: DeviceLinkMode): String = when (mode) {
    DeviceLinkMode.Inviting -> TAB_INVITE
    DeviceLinkMode.Joining -> TAB_JOIN
    DeviceLinkMode.GuardOnly -> ACTION_SET_DESTROY_PIN
}


fun runAction(mode: DeviceLinkMode): String = when (mode) {
    DeviceLinkMode.Inviting -> ACTION_INVITE
    DeviceLinkMode.Joining -> ACTION_JOIN
    DeviceLinkMode.GuardOnly -> ACTION_SET_DESTROY_PIN
}

const val GUARD_HELD = "This device holds a destroy PIN."
const val GUARD_NOT_HELD = "This device holds no destroy PIN."
const val SALT_HELD = "This device holds your other device's details, so its PIN can be checked."
const val SALT_NOT_HELD =
    "This device does not hold your other device's details yet, so its PIN cannot be checked."


fun linkedActionBusyLine(label: String): String = "$label: waiting for the answer."


fun unlinkConfirmText(peerId: String): String =
    "Unlink ${shortPeerId(peerId)}?\n\n" +
        "This device stops treating it as one of yours. That device keeps " +
        "everything it already holds.\n\n" +
        "The link cannot be undone. Linking again sets a new destroy PIN."


fun destroyConfirmText(peerId: String): String =
    "Destroy ${shortPeerId(peerId)}?\n\n" +
        "That device is told to erase everything and return to first-run. It " +
        "loses its reach and its data. This device unlinks it either way.\n\n" +
        "It cannot be undone. A device that is switched off cannot be erased, " +
        "and a wrong PIN changes nothing."
