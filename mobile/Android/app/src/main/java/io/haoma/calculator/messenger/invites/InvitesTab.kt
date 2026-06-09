package io.haoma.calculator.messenger.invites

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Close
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.RadioButton
import androidx.compose.material3.RadioButtonDefaults
import androidx.compose.material3.SwipeToDismissBox
import androidx.compose.material3.SwipeToDismissBoxValue
import androidx.compose.material3.Tab
import androidx.compose.material3.TabRow
import androidx.compose.material3.Text
import androidx.compose.material3.rememberSwipeToDismissBoxState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import io.haoma.calculator.messenger.*
import io.haoma.calculator.messenger.AcceptResult
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.PairType
import io.haoma.calculator.messenger.PendingInvite
import io.haoma.calculator.messenger.RecentInvite
import io.haoma.calculator.messenger.RecentOutcome
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.launch


@Composable
fun InvitesTab(store: MessengerStore) {
    val health by store.health.collectAsStateWithLifecycle()
    
    
    var selectedSubtab by rememberSaveable { mutableStateOf(Subtab.Invite) }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .background(BG_BASE),
    ) {
        InvitesHeader(store = store)
        if (health.selfNickIsDefault) {
            DefaultNickBanner(currentNick = health.selfNick.ifEmpty { "(unset)" })
        }
        SubtabRow(selected = selectedSubtab, onSelect = { selectedSubtab = it })

        Column(
            modifier = Modifier
                .fillMaxSize()
                .verticalScroll(rememberScrollState()),
        ) {
            when (selectedSubtab) {
                Subtab.Invite -> InviteSubtab(store)
                Subtab.Accept -> AcceptSubtab(store)
            }
            Spacer(modifier = Modifier.height(24.dp))
        }
    }
}

private enum class Subtab(val label: String) {
    Invite("Invite a friend"),
    Accept("Accept an invite"),
}

@Composable
private fun SubtabRow(selected: Subtab, onSelect: (Subtab) -> Unit) {
    TabRow(
        selectedTabIndex = selected.ordinal,
        containerColor = BG_CARD,
        contentColor = FG_LINK,
        divider = { HorizontalDivider(color = DIVIDER, thickness = 0.5.dp) },
    ) {
        Subtab.entries.forEach { tab ->
            Tab(
                selected = selected == tab,
                onClick = { onSelect(tab) },
                selectedContentColor = FG_PRIMARY,
                unselectedContentColor = FG_DIM,
            ) {
                Text(
                    text = tab.label,
                    fontSize = 13.sp,
                    fontWeight = FontWeight.SemiBold,
                    modifier = Modifier.padding(vertical = 14.dp),
                )
            }
        }
    }
}

@Composable
private fun InviteSubtab(store: MessengerStore) {
    val pending by store.pendingInvites.collectAsStateWithLifecycle()
    val recent by store.recentInvites.collectAsStateWithLifecycle()
    val nowSeconds by tickingNowSeconds()
    var inviteRail by remember { mutableStateOf(PairType.Tor) }

    Section(
        label = "Invite",
        description = "Generate a 7-word invite, share it with the person you want to get connected — they paste it into the Accept section of their app to pair with you.",
    ) {
        ProtocolPicker(selected = inviteRail, onSelect = { inviteRail = it })
        Spacer(modifier = Modifier.height(12.dp))
        CtaButton(label = "Invite", accent = BTN_INVITE, enabled = inviteRail == PairType.Tor) {
            store.inviteOnion("")
        }
    }

    if (pending.isNotEmpty()) {
        Section(label = "Active invites (${pending.size})") {
            pending.forEach { invite ->
                PendingInviteCard(
                    invite = invite,
                    nowSeconds = nowSeconds,
                    onRename = { store.renamePendingInvite(invite.handleId, it) },
                    onCancel = { store.cancelInvite(invite.handleId) },
                )
                Spacer(modifier = Modifier.height(10.dp))
            }
        }
    }

    if (recent.isNotEmpty()) {
        Section(label = "Recent") {
            recent.forEach { entry ->
                SwipeableRecentRow(
                    entry = entry,
                    nowSeconds = nowSeconds,
                    onDismiss = { store.removeRecentInvite(entry.handleId) },
                )
            }
        }
    }
}

@Composable
private fun AcceptSubtab(store: MessengerStore) {
    var acceptRail by remember { mutableStateOf(PairType.Tor) }
    
    
    var wordsInput by rememberSaveable { mutableStateOf("") }
    var acceptError by remember { mutableStateOf<String?>(null) }
    var submitting by remember { mutableStateOf(false) }
    val coroutineScope = rememberCoroutineScope()
    val context = LocalContext.current

    val tokens = remember(wordsInput) {
        wordsInput.trim().split(Regex("\\s+")).filter { it.isNotEmpty() }
    }
    val looksValid = remember(tokens) { EffShort.looksValid(context, tokens) }

    val launchScanner = rememberQrScannerLauncher { decoded ->
        wordsInput = decoded.trim()
        acceptError = null
    }

    Section(
        label = "Accept",
        description = "Received an invitation from your new contact? Enter the 7 words here (or scan their QR) and tap Accept.",
    ) {
        OutlinedTextField(
            value = wordsInput,
            onValueChange = {
                wordsInput = it
                if (acceptError != null) acceptError = null
            },
            placeholder = {
                Text(
                    text = "Enter 7 words here",
                    color = FG_DIM,
                    fontSize = 13.sp,
                )
            },
            leadingIcon = {
                IconButton(onClick = launchScanner) {
                    Icon(
                        imageVector = QrScannerVector,
                        contentDescription = "Scan QR",
                        tint = FG_LINK,
                    )
                }
            },
            textStyle = TextStyle(
                color = FG_PRIMARY,
                fontSize = 14.sp,
                fontFamily = FontFamily.Monospace,
            ),
            colors = textFieldColors(),
            shape = RoundedCornerShape(10.dp),
            minLines = 1,
            maxLines = 3,
            modifier = Modifier
                .fillMaxWidth()
                .heightIn(min = 44.dp),
        )
        Spacer(modifier = Modifier.height(12.dp))
        ProtocolPicker(selected = acceptRail, onSelect = { acceptRail = it })
        Spacer(modifier = Modifier.height(12.dp))
        Row(
            horizontalArrangement = Arrangement.spacedBy(10.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            CtaButton(
                label = if (submitting) "Accepting…" else "Accept",
                accent = if (looksValid) BTN_ACCEPT else BTN_ACCEPT_DIM,
                enabled = !submitting && acceptRail == PairType.Tor,
            ) {
                if (tokens.size < 3) {
                    acceptError = "Need the 7 words from the invite."
                    return@CtaButton
                }
                submitting = true
                acceptError = null
                coroutineScope.launch {
                    when (val r = store.acceptOnion(tokens, "")) {
                        is AcceptResult.Ok -> {
                            wordsInput = ""
                        }
                        is AcceptResult.Error -> {
                            acceptError = r.message
                        }
                    }
                    submitting = false
                }
            }
            CtaButton(
                label = "Scan QR",
                accent = BTN_ACCEPT_DIM,
                enabled = !submitting,
                onClick = launchScanner,
            )
        }
        if (acceptError != null) {
            Spacer(modifier = Modifier.height(8.dp))
            Text(
                text = acceptError!!,
                color = C_DANGER,
                fontSize = 12.sp,
            )
        }
    }
}


@Composable
private fun InvitesHeader(store: MessengerStore) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .background(BG_CARD)
            .padding(horizontal = 16.dp, vertical = 10.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(
            text = "Add contact",
            color = FG_PRIMARY,
            fontWeight = FontWeight.SemiBold,
            fontSize = 17.sp,
            modifier = Modifier.weight(1f),
        )
        io.haoma.calculator.messenger.calls.CallChip(store = store)
    }
}

@Composable
private fun Section(
    label: String,
    description: String? = null,
    content: @Composable () -> Unit,
) {
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = 16.dp, vertical = 16.dp),
    ) {
        Text(
            text = label,
            color = FG_PRIMARY,
            fontSize = 14.sp,
            fontWeight = FontWeight.SemiBold,
        )
        Spacer(modifier = Modifier.height(10.dp))
        content()
        if (description != null) {
            Spacer(modifier = Modifier.height(12.dp))
            Text(
                text = description,
                color = FG_DIM,
                fontSize = 12.sp,
                lineHeight = 16.sp,
            )
        }
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
private fun ProtocolPicker(selected: PairType, onSelect: (PairType) -> Unit) {
    Row(
        horizontalArrangement = Arrangement.spacedBy(18.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        PairType.entries.forEach { rail ->
            val enabled = rail == PairType.Tor
            val isSelected = selected == rail && enabled
            Row(
                modifier = Modifier
                    .clickable(enabled = enabled) { onSelect(rail) }
                    .padding(vertical = 2.dp),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(4.dp),
            ) {
                RadioButton(
                    selected = isSelected,
                    onClick = if (enabled) {
                        { onSelect(rail) }
                    } else null,
                    enabled = enabled,
                    modifier = Modifier.size(18.dp),
                    colors = RadioButtonDefaults.colors(
                        selectedColor = FG_LINK,
                        unselectedColor = FG_DIM,
                        disabledSelectedColor = BTN_DIM,
                        disabledUnselectedColor = BTN_DIM,
                    ),
                )
                Text(
                    text = rail.label,
                    color = if (enabled) FG_PRIMARY else FG_DIM,
                    fontSize = 12.sp,
                )
            }
        }
    }
}


@Composable
private fun CtaButton(
    label: String,
    accent: Color,
    enabled: Boolean = true,
    onClick: () -> Unit,
) {
    Button(
        enabled = enabled,
        onClick = onClick,
        shape = RoundedCornerShape(8.dp),
        contentPadding = PaddingValues(horizontal = 24.dp, vertical = 10.dp),
        colors = ButtonDefaults.buttonColors(
            containerColor = accent,
            contentColor = BG_BASE,
            disabledContainerColor = BTN_DIM,
            disabledContentColor = FG_DIM,
        ),
    ) {
        Text(
            text = label,
            fontSize = 13.sp,
            fontWeight = FontWeight.SemiBold,
        )
    }
}


@Composable
private fun PendingInviteCard(
    invite: PendingInvite,
    nowSeconds: Long,
    onRename: (String) -> Unit,
    onCancel: () -> Unit,
) {
    val clipboard = LocalClipboardManager.current
    val expiresIn = remainingFor(nowSeconds, invite.expiresAt)
    var showQr by remember { mutableStateOf(false) }

    Column(
        modifier = Modifier
            .fillMaxWidth()
            .clip(RoundedCornerShape(8.dp))
            .border(width = 0.5.dp, color = DIVIDER, shape = RoundedCornerShape(8.dp))
            .background(BG_CARD)
            .padding(start = 12.dp, top = 8.dp, end = 4.dp, bottom = 12.dp),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            EditableTitle(
                alias = invite.alias,
                onCommit = onRename,
                modifier = Modifier.weight(1f),
            )
            StatusPill(ready = invite.ready, fallback = invite.probeNote.isNotEmpty())
            Spacer(modifier = Modifier.width(4.dp))
            IconButton(
                onClick = onCancel,
                modifier = Modifier.size(32.dp),
            ) {
                Icon(
                    imageVector = Icons.Filled.Close,
                    contentDescription = "Cancel invite",
                    tint = FG_DIM,
                    modifier = Modifier.size(18.dp),
                )
            }
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
            Row(
                horizontalArrangement = Arrangement.spacedBy(20.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Text(
                    text = "Copy words",
                    color = BTN_ACCEPT,
                    fontSize = 13.sp,
                    fontWeight = FontWeight.SemiBold,
                    modifier = Modifier
                        .clickable { clipboard.setText(AnnotatedString(wordsLine)) }
                        .padding(vertical = 4.dp),
                )
                Text(
                    text = "Show QR",
                    color = BTN_ACCEPT,
                    fontSize = 13.sp,
                    fontWeight = FontWeight.SemiBold,
                    modifier = Modifier
                        .clickable { showQr = true }
                        .padding(vertical = 4.dp),
                )
            }
        }

        Spacer(modifier = Modifier.height(10.dp))

        Text(
            text = "Handle ${invite.handleId.take(8)} · expires $expiresIn",
            color = FG_DIM,
            fontSize = 12.sp,
            fontFamily = FontFamily.Monospace,
        )
    }

    if (showQr && invite.ready) {
        QrOverlay(words = invite.words.joinToString(" "), onDismiss = { showQr = false })
    }
}


@Composable
private fun EditableTitle(
    alias: String,
    onCommit: (String) -> Unit,
    modifier: Modifier = Modifier,
) {
    var editing by remember { mutableStateOf(false) }
    var buffer by remember(alias) { mutableStateOf(alias) }
    
    
    var hadFocus by remember(editing) { mutableStateOf(false) }
    val focusRequester = remember { FocusRequester() }

    LaunchedEffect(editing) {
        if (editing) focusRequester.requestFocus()
    }

    if (editing) {
        BasicTextField(
            value = buffer,
            onValueChange = { buffer = it },
            singleLine = true,
            textStyle = TextStyle(
                color = FG_PRIMARY,
                fontSize = 15.sp,
                fontWeight = FontWeight.SemiBold,
            ),
            cursorBrush = SolidColor(FG_LINK),
            keyboardOptions = KeyboardOptions(imeAction = ImeAction.Done),
            keyboardActions = KeyboardActions(onDone = {
                onCommit(buffer)
                editing = false
            }),
            modifier = modifier
                .focusRequester(focusRequester)
                .onFocusChanged { state ->
                    if (state.isFocused) {
                        hadFocus = true
                    } else if (hadFocus) {
                        onCommit(buffer)
                        editing = false
                    }
                },
        )
    } else {
        Text(
            text = alias.ifEmpty { "✎ Name this invite" },
            color = if (alias.isEmpty()) BTN_INVITE else FG_PRIMARY,
            fontSize = 15.sp,
            fontWeight = FontWeight.SemiBold,
            modifier = modifier.clickable { editing = true },
        )
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
private fun SwipeableRecentRow(
    entry: RecentInvite,
    nowSeconds: Long,
    onDismiss: () -> Unit,
) {
    val dismissState = rememberSwipeToDismissBoxState(
        confirmValueChange = { value ->
            
            
            if (value == SwipeToDismissBoxValue.EndToStart) {
                onDismiss()
                true
            } else false
        },
    )
    SwipeToDismissBox(
        state = dismissState,
        enableDismissFromStartToEnd = false,
        enableDismissFromEndToStart = true,
        backgroundContent = {
            Box(
                modifier = Modifier
                    .fillMaxWidth()
                    .background(C_DANGER.copy(alpha = 0.18f))
                    .padding(horizontal = 16.dp),
                contentAlignment = Alignment.CenterEnd,
            ) {
                Text(
                    text = "Remove",
                    color = C_DANGER,
                    fontSize = 12.sp,
                    fontWeight = FontWeight.SemiBold,
                )
            }
        },
    ) {
        
        
        Box(modifier = Modifier.background(BG_BASE)) {
            RecentRow(entry = entry, nowSeconds = nowSeconds)
        }
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
    focusedContainerColor = BG_FIELD,
    unfocusedContainerColor = BG_FIELD,
    disabledContainerColor = BG_FIELD,
    cursorColor = FG_LINK,
    focusedBorderColor = FG_LINK,
    unfocusedBorderColor = Color.Transparent,
    disabledBorderColor = Color.Transparent,
)


private val BG_BASE = Color(0xFF1D2021)
private val BG_CARD = Color(0xFF282828)
private val BG_BANNER = Color(0xFF32302F)
private val BG_WORDS = Color(0xFF1D2021)
private val BG_FIELD = Color(0xFF3C3836) 
private val DIVIDER = Color(0xFF3C3836)
private val FG_PRIMARY = Color(0xFFEBDBB2)
private val FG_DIM = Color(0xFF7C6F64)
private val FG_LINK = Color(0xFF83A598)
private val BTN_INVITE = Color(0xFFD79921) 
private val BTN_ACCEPT = Color(0xFF5FCC1A) 
private val BTN_ACCEPT_DIM = Color(0xFF3F5A1F) 
private val BTN_DIM = Color(0xFF504945)
private val C_OK = Color(0xFF5FCC1A)
private val C_WARN = Color(0xFFFABD2F)
private val C_DANGER = Color(0xFFCC241D)
private val C_WORDS = Color(0xFFFABD2F)
