package io.haoma.calculator.messenger.contacts

import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.defaultMinSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.haoma.calculator.messenger.ACCEPT_ACTION
import io.haoma.calculator.messenger.HaomaPalette
import io.haoma.calculator.messenger.INVITATIONS_SECTION
import io.haoma.calculator.messenger.InheritedContactEntry
import io.haoma.calculator.messenger.InheritedPairing
import io.haoma.calculator.messenger.InvitationEntry
import io.haoma.calculator.messenger.InvitationsView
import io.haoma.calculator.messenger.LINKED_DEVICE_SECTION
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.NOBODY_TO_PAIR
import io.haoma.calculator.messenger.PAIR_ACTION
import io.haoma.calculator.messenger.PAIR_ALL_ACTION
import io.haoma.calculator.messenger.PAIR_ALL_TITLE
import io.haoma.calculator.messenger.PAIR_ALL_WHAT_HAPPENS
import io.haoma.calculator.messenger.PAIR_ANYWAY_ACTION
import io.haoma.calculator.messenger.PairState
import io.haoma.calculator.messenger.PresenceArrows
import io.haoma.calculator.messenger.PresenceView
import io.haoma.calculator.messenger.READ_AGAIN_ACTION
import io.haoma.calculator.messenger.RETIRE_CONFIRM_ACTION
import io.haoma.calculator.messenger.RETIRE_CONFIRM_TITLE
import io.haoma.calculator.messenger.retireConfirmFor
import io.haoma.calculator.messenger.STOP_ACTION
import io.haoma.calculator.messenger.SYNCHRONIZED
import io.haoma.calculator.messenger.SYNC_ACTION
import io.haoma.calculator.messenger.SYNC_EVERYTHING_LABEL
import io.haoma.calculator.messenger.TOR_SLOW
import io.haoma.calculator.messenger.acceptInvitation
import io.haoma.calculator.messenger.acceptingLine
import io.haoma.calculator.messenger.allAlreadyInRun
import io.haoma.calculator.messenger.dismissAcceptOutcome
import io.haoma.calculator.messenger.inheritedContactName
import io.haoma.calculator.messenger.inheritedRowInfo
import io.haoma.calculator.messenger.leavingAlone
import io.haoma.calculator.messenger.pairAllInherited
import io.haoma.calculator.messenger.pairInherited
import io.haoma.calculator.messenger.requestInheritedContacts
import io.haoma.calculator.messenger.requestInvitations
import io.haoma.calculator.messenger.stopInheritedPairing
import io.haoma.calculator.messenger.syncEverything
import io.haoma.calculator.messenger.syncMissed
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale


internal val INHERITED_NAME_COLOUR = HaomaPalette.BTN_GIVE
internal val PAIR_ACTION_COLOUR = HaomaPalette.FG_SECONDARY


internal val SECTION_BREAK_GAP = 12.dp
internal val SECTION_BREAK_THICKNESS = 4.dp

@Composable
internal fun SectionBreak() {
    Spacer(modifier = Modifier.height(SECTION_BREAK_GAP))
    HorizontalDivider(color = HaomaPalette.DIVIDER, thickness = SECTION_BREAK_THICKNESS)
}

@Composable
internal fun LinkedDeviceSection(
    store: MessengerStore,
    pairing: InheritedPairing,
    siblingPresence: PresenceView?,
) {
    var confirmPairAll by remember { mutableStateOf(false) }
    var confirmRetire by remember { mutableStateOf<Triple<String, String, Boolean>?>(null) }
    var opened by remember { mutableStateOf<String?>(null) }
    val now = System.currentTimeMillis() / 1000L

    Column(modifier = Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 12.dp)) {
        Row(modifier = Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
            
            
            PresenceArrows(view = siblingPresence ?: PresenceView())
            Spacer(modifier = Modifier.width(8.dp))
            Text(
                text = LINKED_DEVICE_SECTION.uppercase(),
                color = HaomaPalette.FG_PRIMARY,
                fontSize = 13.sp,
                fontWeight = FontWeight.SemiBold,
            )
            Spacer(modifier = Modifier.weight(1f))
            
            
            ActionText(
                label = SYNC_ACTION.uppercase(),
                enabled = !pairing.syncing,
                onClick = { store.syncMissed() },
                onLongClick = { store.syncEverything() },
                onLongClickLabel = SYNC_EVERYTHING_LABEL,
            )
        }
        Spacer(modifier = Modifier.height(8.dp))

        if (pairing.listFailure.isNotEmpty()) {
            FailureLines(pairing.listFailure) { store.requestInheritedContacts() }
            Spacer(modifier = Modifier.height(8.dp))
        }

        if (pairing.contacts.isEmpty()) {
            if (pairing.listFailure.isEmpty()) {
                Text(text = SYNCHRONIZED, color = HaomaPalette.FG_DIM, fontSize = 14.sp)
            }
        } else {
            for (c in pairing.contacts) {
                val state = pairing.stateOf(c.fingerprint, now)
                InheritedRow(
                    contact = c,
                    state = state,
                    details = pairing.detailsOf(c.fingerprint, now),
                    open = opened == c.fingerprint,
                    onToggle = { opened = if (opened == c.fingerprint) null else c.fingerprint },
                    onPair = {
                        
                        
                        val question = retireConfirmFor(state)
                        if (question == null) {
                            store.pairInherited(c.fingerprint, anyway = c.waiting)
                        } else {
                            confirmRetire = Triple(c.fingerprint, question, c.waiting)
                        }
                    },
                )
                HorizontalDivider(color = HaomaPalette.DIVIDER, thickness = 0.5.dp)
            }
            Spacer(modifier = Modifier.height(10.dp))
            Row(verticalAlignment = Alignment.CenterVertically) {
                ActionText(
                    label = PAIR_ALL_ACTION,
                    colour = PAIR_ACTION_COLOUR,
                    onClick = { confirmPairAll = true },
                )
                if (pairing.queue.isNotEmpty()) {
                    Spacer(modifier = Modifier.width(8.dp))
                    ActionText(label = STOP_ACTION, onClick = { store.stopInheritedPairing() })
                }
            }
        }

        if (pairing.note.isNotEmpty()) {
            Spacer(modifier = Modifier.height(8.dp))
            Text(text = pairing.note, color = HaomaPalette.FG_SECONDARY, fontSize = 13.sp)
        }
    }
    HorizontalDivider(color = HaomaPalette.DIVIDER, thickness = 0.5.dp)

    if (confirmPairAll) {
        PairAllDialog(
            candidates = pairing.pairAllCandidates(now).size,
            leftAlone = pairing.leftAloneByPairAll(now),
            onConfirm = {
                confirmPairAll = false
                store.pairAllInherited()
            },
            onDismiss = { confirmPairAll = false },
        )
    }

    confirmRetire?.let { (fingerprint, question, anyway) ->
        ConfirmDialog(
            title = RETIRE_CONFIRM_TITLE,
            body = question,
            confirmLabel = RETIRE_CONFIRM_ACTION,
            onConfirm = {
                confirmRetire = null
                store.pairInherited(fingerprint, anyway = anyway)
            },
            onDismiss = { confirmRetire = null },
        )
    }
}


@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun ActionText(
    label: String,
    enabled: Boolean = true,
    colour: Color = HaomaPalette.FG_LINK,
    onClick: () -> Unit,
    onLongClick: (() -> Unit)? = null,
    onLongClickLabel: String? = null,
) {
    val press = if (onLongClick == null) {
        Modifier.clickable(enabled = enabled, onClick = onClick)
    } else {
        Modifier.combinedClickable(
            enabled = enabled,
            onLongClickLabel = onLongClickLabel,
            onLongClick = onLongClick,
            onClick = onClick,
        )
    }
    Box(
        modifier = Modifier
            .defaultMinSize(minWidth = 48.dp, minHeight = 48.dp)
            .then(press)
            .padding(horizontal = 8.dp),
        contentAlignment = Alignment.Center,
    ) {
        Text(
            text = label,
            color = if (enabled) colour else HaomaPalette.FG_DIM,
            fontSize = 14.sp,
            fontWeight = FontWeight.Medium,
        )
    }
}

@Composable
private fun ConfirmDialog(
    title: String,
    body: String,
    confirmLabel: String,
    onConfirm: () -> Unit,
    onDismiss: () -> Unit,
) {
    MaterialTheme(
        colorScheme = darkColorScheme(
            surface = HaomaPalette.BG_BAR,
            onSurface = HaomaPalette.FG_PRIMARY,
            background = HaomaPalette.BG_BAR,
            onBackground = HaomaPalette.FG_PRIMARY,
        ),
    ) {
        AlertDialog(
            onDismissRequest = onDismiss,
            title = { Text(title, color = HaomaPalette.FG_PRIMARY) },
            text = { Text(body, color = HaomaPalette.FG_SECONDARY, fontSize = 14.sp) },
            confirmButton = {
                TextButton(onClick = onConfirm) {
                    Text(confirmLabel, color = HaomaPalette.BTN_GIVE, fontWeight = FontWeight.SemiBold)
                }
            },
            dismissButton = {
                TextButton(onClick = onDismiss) {
                    Text("Cancel", color = HaomaPalette.FG_LINK)
                }
            },
            containerColor = HaomaPalette.BG_BAR,
        )
    }
}


@Composable
private fun InheritedRow(
    contact: InheritedContactEntry,
    state: String,
    details: List<String>,
    open: Boolean,
    onToggle: () -> Unit,
    onPair: () -> Unit,
) {
    val inRun = state == PairState.WAITING || state == PairState.OFFERING
    val info = if (state.isEmpty()) inheritedRowInfo(contact) else ""
    Column(modifier = Modifier.fillMaxWidth().padding(vertical = 8.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Column(modifier = Modifier.weight(1f).clickable(onClick = onToggle)) {
                Text(
                    text = inheritedContactName(contact),
                    color = INHERITED_NAME_COLOUR,
                    fontWeight = FontWeight.SemiBold,
                    fontSize = 15.sp,
                )
                if (state.isNotEmpty()) {
                    Text(text = state, color = stateColour(state), fontSize = 13.sp)
                } else if (info.isNotEmpty()) {
                    Text(text = info, color = HaomaPalette.FG_DIM, fontSize = 13.sp)
                }
            }
            if (!contact.invited) {
                Spacer(modifier = Modifier.width(4.dp))
                ActionText(
                    label = if (contact.waiting) PAIR_ANYWAY_ACTION else PAIR_ACTION,
                    enabled = !inRun,
                    colour = PAIR_ACTION_COLOUR,
                    onClick = onPair,
                )
            }
        }
        for (line in details) {
            Text(text = line, color = detailColour(line), fontSize = 12.sp, lineHeight = 16.sp)
        }
        if (open) {
            Spacer(modifier = Modifier.height(4.dp))
            DetailLine("Real nick", contact.nick)
            DetailLine("Local alias", contact.alias)
            DetailLine("Fingerprint", formatFingerprint(contact.fingerprint))
        }
    }
}

@Composable
private fun DetailLine(label: String, value: String) {
    Row {
        Text(
            text = "$label: ",
            color = HaomaPalette.FG_DIM,
            fontSize = 12.sp,
        )
        Text(
            text = value.ifEmpty { "—" },
            color = HaomaPalette.FG_SECONDARY,
            fontSize = 12.sp,
            fontFamily = FontFamily.Monospace,
        )
    }
}


@Composable
private fun PairAllDialog(
    candidates: Int,
    leftAlone: Int,
    onConfirm: () -> Unit,
    onDismiss: () -> Unit,
) {
    val body = buildList {
        if (candidates == 0) {
            add(if (leftAlone > 0) allAlreadyInRun(leftAlone) else NOBODY_TO_PAIR)
        } else {
            add("Contacts: $candidates")
            add(PAIR_ALL_WHAT_HAPPENS)
            if (leftAlone > 0) add(leavingAlone(leftAlone))
        }
    }.joinToString("\n\n")
    MaterialTheme(
        colorScheme = darkColorScheme(
            surface = HaomaPalette.BG_BAR,
            onSurface = HaomaPalette.FG_PRIMARY,
            background = HaomaPalette.BG_BAR,
            onBackground = HaomaPalette.FG_PRIMARY,
        ),
    ) {
        AlertDialog(
            onDismissRequest = onDismiss,
            title = { Text(PAIR_ALL_TITLE, color = HaomaPalette.FG_PRIMARY) },
            text = { Text(body, color = HaomaPalette.FG_SECONDARY, fontSize = 14.sp) },
            confirmButton = {
                if (candidates > 0) {
                    TextButton(onClick = onConfirm) {
                        Text(PAIR_ALL_ACTION, color = HaomaPalette.BTN_GIVE, fontWeight = FontWeight.SemiBold)
                    }
                }
            },
            dismissButton = {
                TextButton(onClick = onDismiss) {
                    Text("Cancel", color = HaomaPalette.FG_LINK)
                }
            },
            containerColor = HaomaPalette.BG_BAR,
        )
    }
}

@Composable
internal fun InvitationsSection(
    store: MessengerStore,
    view: InvitationsView,
    acceptable: List<InvitationEntry>,
    connected: Boolean,
    endLine: Boolean,
) {
    Column(modifier = Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 12.dp)) {
        Text(
            text = INVITATIONS_SECTION.uppercase(),
            color = HaomaPalette.FG_PRIMARY,
            fontSize = 13.sp,
            fontWeight = FontWeight.SemiBold,
        )
        Spacer(modifier = Modifier.height(8.dp))

        if (view.listFailure.isNotEmpty()) {
            FailureLines(view.listFailure) { store.requestInvitations() }
            if (view.note.isNotEmpty()) {
                Text(text = view.note, color = HaomaPalette.FG_SECONDARY, fontSize = 13.sp)
            }
            Spacer(modifier = Modifier.height(8.dp))
        }

        if (view.acceptingMsgId.isNotEmpty()) {
            Text(text = acceptingLine(view.acceptingName), color = HaomaPalette.C_WARN, fontSize = 13.sp)
            Spacer(modifier = Modifier.height(8.dp))
        }

        view.outcome?.let { outcome ->
            Column(modifier = Modifier.fillMaxWidth().clickable { store.dismissAcceptOutcome() }) {
                outcome.lines.forEachIndexed { i, line ->
                    Text(
                        text = line,
                        color = when {
                            i > 0 -> HaomaPalette.FG_DIM
                            outcome.ok -> HaomaPalette.C_OK
                            outcome.torSlow -> HaomaPalette.C_ATTENTION
                            else -> HaomaPalette.C_DANGER
                        },
                        fontSize = if (i == 0) 14.sp else 12.sp,
                        lineHeight = 16.sp,
                    )
                }
            }
            Spacer(modifier = Modifier.height(8.dp))
        }

        for (e in acceptable) {
            Row(
                modifier = Modifier.fillMaxWidth().padding(vertical = 8.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Column(modifier = Modifier.weight(1f)) {
                    Text(
                        text = e.label.ifEmpty { "(unnamed contact)" },
                        color = HaomaPalette.FG_PRIMARY,
                        fontWeight = FontWeight.SemiBold,
                        fontSize = 15.sp,
                    )
                    Text(
                        text = "they offered · lapses ${deadline(e.expiresAt)}",
                        color = HaomaPalette.FG_DIM,
                        fontSize = 12.sp,
                    )
                }
                val canAccept = connected && view.acceptingMsgId.isEmpty()
                ActionText(label = ACCEPT_ACTION, enabled = canAccept, onClick = { store.acceptInvitation(e.msgId) })
            }
            HorizontalDivider(color = HaomaPalette.DIVIDER, thickness = 0.5.dp)
        }
    }
    
    if (endLine) {
        HorizontalDivider(color = HaomaPalette.DIVIDER, thickness = 0.5.dp)
    }
}


@Composable
private fun FailureLines(lines: List<String>, onReadAgain: () -> Unit) {
    lines.forEachIndexed { i, line ->
        Text(
            text = line,
            color = if (i == 0) HaomaPalette.C_DANGER else HaomaPalette.FG_DIM,
            fontSize = if (i == 0) 14.sp else 12.sp,
            lineHeight = 16.sp,
        )
    }
    ActionText(label = READ_AGAIN_ACTION, onClick = onReadAgain)
}


private fun deadline(expiresAt: Long): String =
    if (expiresAt <= 0L) "not recorded"
    else SimpleDateFormat("yyyy-MM-dd HH:mm z", Locale.US).format(Date(expiresAt * 1000L))


internal fun detailColour(line: String): Color =
    if (line == TOR_SLOW) HaomaPalette.C_ATTENTION else HaomaPalette.FG_DIM


private fun stateColour(state: String): Color = when (state) {
    PairState.WAITING -> HaomaPalette.FG_DIM
    PairState.OFFERING -> HaomaPalette.C_WARN
    PairState.OFFERED -> HaomaPalette.C_OK
    PairState.FAILED -> HaomaPalette.C_DANGER
    PairState.UNKNOWN -> HaomaPalette.C_WARN
    else -> HaomaPalette.FG_DIM
}
