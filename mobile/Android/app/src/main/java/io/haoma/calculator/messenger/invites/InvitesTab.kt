package io.haoma.calculator.messenger.invites

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import io.haoma.calculator.messenger.*
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.PairType
import io.haoma.calculator.messenger.PendingInvite
import io.haoma.calculator.messenger.RecentInvite
import io.haoma.calculator.messenger.RecentOutcome
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow


@Composable
fun InvitesTab(store: MessengerStore) {
    val pending by store.pendingInvites.collectAsStateWithLifecycle()
    val recent by store.recentInvites.collectAsStateWithLifecycle()
    val health by store.health.collectAsStateWithLifecycle()
    val nowSeconds by tickingNowSeconds()

    var aliasInput by remember { mutableStateOf("") }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .background(BG_BASE)
            .verticalScroll(rememberScrollState()),
    ) {
        if (health.selfNickIsDefault) {
            DefaultNickBanner(currentNick = health.selfNick.ifEmpty { "(unset)" })
        }

        Section(label = "Invite") {
            OutlinedTextField(
                value = aliasInput,
                onValueChange = { aliasInput = it },
                singleLine = true,
                placeholder = {
                    Text(
                        text = "Alias (optional — local note for this invite)",
                        color = FG_DIM,
                        fontSize = 13.sp,
                    )
                },
                colors = textFieldColors(),
                modifier = Modifier.fillMaxWidth(),
            )
            Spacer(modifier = Modifier.height(10.dp))
            RailButtonRow { rail ->
                if (rail == PairType.Tor) {
                    store.inviteOnion(aliasInput)
                    aliasInput = ""
                }
            }
        }

        Section(label = "Accept") {
            RailButtonRow { rail ->
                if (rail == PairType.Tor) store.openAccept(PairType.Tor)
            }
        }

        if (pending.isNotEmpty()) {
            Section(label = "Active invites (${pending.size})") {
                pending.forEach { invite ->
                    PendingInviteCard(
                        invite = invite,
                        nowSeconds = nowSeconds,
                        onCancel = { store.cancelInvite(invite.handleId) },
                    )
                    Spacer(modifier = Modifier.height(10.dp))
                }
            }
        }

        if (recent.isNotEmpty()) {
            Section(label = "Recent") {
                recent.forEach { entry ->
                    RecentRow(entry = entry, nowSeconds = nowSeconds)
                }
            }
        }

        Spacer(modifier = Modifier.height(24.dp))
    }
}


@Composable
private fun Section(label: String, content: @Composable () -> Unit) {
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = 16.dp, vertical = 14.dp),
    ) {
        Text(
            text = label.uppercase(),
            color = FG_DIM,
            fontSize = 11.sp,
            fontWeight = FontWeight.SemiBold,
        )
        Spacer(modifier = Modifier.height(8.dp))
        content()
    }
    HorizontalDivider(color = DIVIDER, thickness = 0.5.dp)
}


@Composable
private fun DefaultNickBanner(currentNick: String) {
    Box(
        modifier = Modifier
            .fillMaxWidth()
            .background(BG_BANNER)
            .padding(horizontal = 16.dp, vertical = 10.dp),
    ) {
        Text(
            text = "Heads-up — your nick is still '$currentNick'. Set it via Settings → Profile before pairing for real (the joiner sees it as your label).",
            color = C_WARN,
            fontSize = 13.sp,
        )
    }
}


@Composable
private fun RailButtonRow(onSelect: (PairType) -> Unit) {
    Row(horizontalArrangement = Arrangement.spacedBy(10.dp)) {
        PairType.entries.forEach { rail ->
            val enabled = rail == PairType.Tor
            Button(
                enabled = enabled,
                onClick = { onSelect(rail) },
                colors = ButtonDefaults.buttonColors(
                    containerColor = BTN_PRIMARY,
                    contentColor = BG_BASE,
                    disabledContainerColor = BTN_DIM,
                    disabledContentColor = FG_DIM,
                ),
            ) {
                Text(rail.label)
            }
        }
    }
}


@Composable
private fun PendingInviteCard(
    invite: PendingInvite,
    nowSeconds: Long,
    onCancel: () -> Unit,
) {
    val clipboard = LocalClipboardManager.current
    val title = invite.alias.ifEmpty { "Untitled invite" }
    val expiresIn = remainingFor(nowSeconds, invite.expiresAt)

    Column(
        modifier = Modifier
            .fillMaxWidth()
            .clip(RoundedCornerShape(8.dp))
            .border(width = 0.5.dp, color = DIVIDER, shape = RoundedCornerShape(8.dp))
            .background(BG_CARD)
            .padding(12.dp),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(
                text = title,
                color = FG_PRIMARY,
                fontSize = 15.sp,
                fontWeight = FontWeight.SemiBold,
                modifier = Modifier.weight(1f),
            )
            StatusPill(ready = invite.ready, fallback = invite.probeNote.isNotEmpty())
        }

        Spacer(modifier = Modifier.height(8.dp))

        if (!invite.ready) {
            Text(
                text = "Publishing onion (~30–60s)…",
                color = FG_DIM,
                fontSize = 13.sp,
            )
        } else {
            val wordsLine = invite.words.joinToString(" ")
            Box(
                modifier = Modifier
                    .fillMaxWidth()
                    .background(BG_WORDS)
                    .padding(horizontal = 10.dp, vertical = 8.dp),
            ) {
                Text(
                    text = wordsLine,
                    color = C_WORDS,
                    fontFamily = FontFamily.Monospace,
                    fontSize = 14.sp,
                    fontWeight = FontWeight.SemiBold,
                )
            }
            Spacer(modifier = Modifier.height(6.dp))
            Text(
                text = if (invite.probeNote.isNotEmpty()) {
                    "Descriptor publication slow — share words anyway."
                } else {
                    "Share these 7 words OOB."
                },
                color = if (invite.probeNote.isNotEmpty()) C_WARN else FG_DIM,
                fontSize = 12.sp,
            )
            Spacer(modifier = Modifier.height(8.dp))
            Text(
                text = "Copy words",
                color = FG_LINK,
                fontSize = 13.sp,
                fontWeight = FontWeight.Medium,
                modifier = Modifier
                    .clickable { clipboard.setText(AnnotatedString(wordsLine)) }
                    .padding(vertical = 4.dp),
            )
        }

        Spacer(modifier = Modifier.height(10.dp))

        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(
                text = "Handle ${invite.handleId.take(8)} · expires $expiresIn",
                color = FG_DIM,
                fontSize = 12.sp,
                fontFamily = FontFamily.Monospace,
                modifier = Modifier.weight(1f),
            )
            OutlinedButton(
                onClick = onCancel,
                colors = ButtonDefaults.outlinedButtonColors(contentColor = C_WARN),
            ) {
                Text("Cancel")
            }
        }
    }
}

@Composable
private fun StatusPill(ready: Boolean, fallback: Boolean) {
    val (text, fg) = when {
        !ready -> "publishing" to FG_DIM
        fallback -> "ready (slow)" to C_WARN
        else -> "ready" to C_OK
    }
    Box(
        modifier = Modifier
            .background(fg.copy(alpha = 0.18f))
            .padding(horizontal = 8.dp, vertical = 2.dp),
    ) {
        Text(
            text = text,
            color = fg,
            fontFamily = FontFamily.Monospace,
            fontSize = 11.sp,
            fontWeight = FontWeight.SemiBold,
        )
    }
}


@Composable
private fun RecentRow(entry: RecentInvite, nowSeconds: Long) {
    val (glyph, color) = when (entry.outcome) {
        RecentOutcome.Success -> "✓" to C_OK
        RecentOutcome.Failed -> "✗" to C_DANGER
        RecentOutcome.Cancelled -> "○" to FG_DIM
    }
    val label = entry.alias.ifEmpty { "Untitled" }
    val detail = when (entry.outcome) {
        RecentOutcome.Success -> {
            val nick = entry.nick.ifEmpty { "(no nick)" }
            "paired with $nick · ${entry.peerId.take(8)}"
        }
        RecentOutcome.Failed -> "failed: ${entry.reason.ifEmpty { "unknown" }}"
        RecentOutcome.Cancelled -> "cancelled"
    }
    val ago = ago(nowSeconds, entry.at / 1000L)

    Row(
        modifier = Modifier
            .fillMaxWidth()
            .padding(vertical = 4.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(
            text = glyph,
            color = color,
            fontFamily = FontFamily.Monospace,
            fontSize = 14.sp,
            fontWeight = FontWeight.SemiBold,
            modifier = Modifier.width(20.dp),
        )
        Column(modifier = Modifier.weight(1f)) {
            Text(
                text = label,
                color = FG_PRIMARY,
                fontSize = 14.sp,
                fontWeight = FontWeight.Medium,
            )
            Text(
                text = detail,
                color = FG_DIM,
                fontSize = 12.sp,
            )
        }
        Text(
            text = ago,
            color = FG_DIM,
            fontFamily = FontFamily.Monospace,
            fontSize = 12.sp,
        )
    }
}


@Composable
private fun tickingNowSeconds(): androidx.compose.runtime.State<Long> {
    val state = remember { MutableStateFlow(System.currentTimeMillis() / 1000L) }
    LaunchedEffect(Unit) {
        while (true) {
            state.value = System.currentTimeMillis() / 1000L
            delay(30_000L)
        }
    }
    return state.collectAsStateWithLifecycle()
}

private fun remainingFor(nowSeconds: Long, expiresAtSeconds: Long): String {
    if (expiresAtSeconds <= 0L) return "—"
    val diff = expiresAtSeconds - nowSeconds
    if (diff <= 0L) return "expired"
    val mins = diff / 60L
    val hours = mins / 60L
    return when {
        hours > 0L -> "in ${hours}h${mins % 60}m"
        mins > 0L -> "in ${mins}m"
        else -> "in <1m"
    }
}

private fun ago(nowSeconds: Long, tsSeconds: Long): String {
    if (tsSeconds <= 0L) return "—"
    val diff = nowSeconds - tsSeconds
    if (diff <= 0L) return "now"
    return when {
        diff < 60L -> "now"
        diff < 3600L -> "${diff / 60L}m"
        diff < 86400L -> "${diff / 3600L}h"
        else -> "${diff / 86400L}d"
    }
}


@Composable
private fun textFieldColors() = OutlinedTextFieldDefaults.colors(
    focusedTextColor = FG_PRIMARY,
    unfocusedTextColor = FG_PRIMARY,
    disabledTextColor = FG_DIM,
    cursorColor = FG_LINK,
    focusedBorderColor = FG_LINK,
    unfocusedBorderColor = DIVIDER,
    disabledBorderColor = DIVIDER,
)


private val BG_BASE = Color(0xFF1D2021)
private val BG_CARD = Color(0xFF282828)
private val BG_BANNER = Color(0xFF32302F)
private val BG_WORDS = Color(0xFF1D2021)
private val DIVIDER = Color(0xFF3C3836)
private val FG_PRIMARY = Color(0xFFEBDBB2)
private val FG_DIM = Color(0xFF7C6F64)
private val FG_LINK = Color(0xFF83A598)
private val BTN_PRIMARY = Color(0xFF5FCC1A)
private val BTN_DIM = Color(0xFF504945)
private val C_OK = Color(0xFF5FCC1A)
private val C_WARN = Color(0xFFFABD2F)
private val C_DANGER = Color(0xFFCC241D)
private val C_WORDS = Color(0xFFFABD2F)
