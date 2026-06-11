package io.haoma.calculator.messenger.chat

import androidx.compose.foundation.background
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
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Checkbox
import androidx.compose.material3.CheckboxDefaults
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.window.PopupProperties
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import io.haoma.calculator.messenger.ChatEntry
import io.haoma.calculator.messenger.ChatKind
import io.haoma.calculator.messenger.CtaButton
import io.haoma.calculator.messenger.DangerButton
import io.haoma.calculator.messenger.DirtyBar
import io.haoma.calculator.messenger.HaomaDropdownItem
import io.haoma.calculator.messenger.HaomaPalette
import io.haoma.calculator.messenger.HaomaTextField
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.Section
import io.haoma.calculator.messenger.clearChat
import io.haoma.calculator.messenger.contacts.shortPeerId
import io.haoma.calculator.messenger.deleteChat
import io.haoma.calculator.messenger.setChatSettings
import kotlinx.coroutines.launch


@Composable
fun ChatSettingsScreen(
    store: MessengerStore,
    chatId: String,
    onBack: () -> Unit,
) {
    val chats by store.chats.collectAsStateWithLifecycle()
    val chat = chats.firstOrNull { it.chatId == chatId }

    if (chat == null) {
        LaunchedEffect(Unit) { onBack() }
        return
    }

    var retentionIndex by remember(chat.retentionTtl) {
        mutableStateOf(retentionOptionIndex(chat.retentionTtl))
    }
    var sendReceipts by remember(chat.disableReadReceipts) {
        mutableStateOf(!chat.disableReadReceipts)
    }
    var muted by remember(chat.notificationsMuted) {
        mutableStateOf(chat.notificationsMuted)
    }
    var nickOverride by remember(chat.nickOverride) {
        mutableStateOf(chat.nickOverride)
    }
    var risksAcked by remember { mutableStateOf(false) }
    var confirm by remember { mutableStateOf<ChatConfirm?>(null) }
    val scrollState = rememberScrollState()
    val scope = rememberCoroutineScope()

    val initialIndex = retentionOptionIndex(chat.retentionTtl)
    val initialSendReceipts = !chat.disableReadReceipts
    val initialMuted = chat.notificationsMuted
    val initialNickOverride = chat.nickOverride
    val dirty by remember(chat, retentionIndex, sendReceipts, muted, nickOverride) {
        derivedStateOf {
            retentionIndex != initialIndex ||
                sendReceipts != initialSendReceipts ||
                muted != initialMuted ||
                nickOverride.trim() != initialNickOverride
        }
    }

    Column(modifier = Modifier.fillMaxSize().background(HaomaPalette.BG_BASE)) {
        Header(title = chatTitle(chat), store = store, onBack = onBack)
        DirtyBar(
            visible = dirty,
            onTap = { scope.launch { scrollState.animateScrollTo(scrollState.maxValue) } },
        )

        Column(modifier = Modifier.weight(1f).verticalScroll(scrollState)) {
            Section(
                label = "Your nick in this chat",
                description = "Empty = use your global nick. Set to present a different name to this conversation.",
            ) {
                val placeholder = "Use global nick (${store.health.value.selfNick.ifEmpty { "mynick" }})"
                HaomaTextField(
                    value = nickOverride,
                    onValueChange = { incoming ->
                        
                        
                        val cleaned = incoming.filter { it.code >= 0x20 && it.code != 0x7f }
                        if (cleaned.length <= NICK_OVERRIDE_MAX_LEN) {
                            nickOverride = cleaned
                        }
                    },
                    placeholder = placeholder,
                    modifier = Modifier.fillMaxWidth(),
                )
            }

            Section(label = "Disappearing messages") {
                RetentionDropdown(
                    currentIndex = retentionIndex,
                    onPick = { retentionIndex = it },
                )
            }

            ToggleSection(
                label = "Send read receipts",
                checked = sendReceipts,
                onCheckedChange = { sendReceipts = it },
                description = "Tells the other side when you read their messages.",
            )

            ToggleSection(
                label = "Mute notifications",
                checked = muted,
                onCheckedChange = { muted = it },
                description = "This chat won't pop a notification while muted.",
            )

            SaveRow(
                dirty = dirty,
                onSave = {
                    val ttl = retentionLevels[retentionIndex].seconds
                    store.setChatSettings(
                        chatId = chatId,
                        retentionTtl = ttl,
                        disableReadReceipts = !sendReceipts,
                        notificationsMuted = muted,
                        nickOverride = nickOverride.trim(),
                    )
                    
                    
                },
                onReset = {
                    retentionIndex = initialIndex
                    sendReceipts = initialSendReceipts
                    muted = initialMuted
                    nickOverride = initialNickOverride
                },
            )

            DangerSection(
                risksAcked = risksAcked,
                onRiskCheck = { risksAcked = it },
                onClear = { confirm = ChatConfirm.Clear },
                onDelete = { confirm = ChatConfirm.Delete },
            )

            Spacer(modifier = Modifier.height(24.dp))
        }
    }

    confirm?.let { which ->
        ConfirmDialog(
            which = which,
            onDismiss = { confirm = null },
            onConfirm = {
                confirm = null
                when (which) {
                    ChatConfirm.Clear -> store.clearChat(chatId)
                    ChatConfirm.Delete -> store.deleteChat(chatId)
                }
                onBack()
            },
        )
    }
}

private enum class ChatConfirm { Clear, Delete }

@Composable
private fun Header(title: String, store: MessengerStore, onBack: () -> Unit) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .background(HaomaPalette.BG_BAR)
            .padding(horizontal = 12.dp, vertical = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(
            text = "‹",
            color = HaomaPalette.FG_LINK,
            fontSize = 22.sp,
            fontWeight = FontWeight.Bold,
            modifier = Modifier
                .clickable(onClick = onBack)
                .padding(horizontal = 8.dp, vertical = 4.dp),
        )
        Spacer(modifier = Modifier.width(20.dp))
        Text(
            text = title,
            color = HaomaPalette.FG_PRIMARY,
            fontWeight = FontWeight.SemiBold,
            fontSize = 17.sp,
            modifier = Modifier.weight(1f),
        )
        io.haoma.calculator.messenger.calls.CallChip(store = store)
    }
}

@Composable
private fun RetentionDropdown(currentIndex: Int, onPick: (Int) -> Unit) {
    var expanded by remember { mutableStateOf(false) }
    Box {
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .clickable { expanded = true }
                .padding(vertical = 4.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Text(
                text = retentionLevels[currentIndex].label,
                color = HaomaPalette.BTN_GIVE,
                fontFamily = FontFamily.Monospace,
                fontSize = 14.sp,
                fontWeight = FontWeight.SemiBold,
                modifier = Modifier.weight(1f),
            )
            Text(
                text = "▾",
                color = HaomaPalette.FG_LINK,
                fontSize = 14.sp,
            )
        }
        MaterialTheme(
            colorScheme = darkColorScheme(
                surface = HaomaPalette.BG_BAR,
                onSurface = HaomaPalette.FG_PRIMARY,
            ),
        ) {
            DropdownMenu(
                expanded = expanded,
                onDismissRequest = { expanded = false },
                properties = PopupProperties(focusable = false),
            ) {
                retentionLevels.forEachIndexed { idx, lvl ->
                    HaomaDropdownItem(
                        label = lvl.label,
                        selected = idx == currentIndex,
                        onClick = { onPick(idx); expanded = false },
                    )
                }
            }
        }
    }
}

@Composable
private fun ToggleSection(
    label: String,
    checked: Boolean,
    onCheckedChange: (Boolean) -> Unit,
    description: String,
) {
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = 16.dp, vertical = 14.dp),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Checkbox(
                checked = checked,
                onCheckedChange = onCheckedChange,
                colors = CheckboxDefaults.colors(
                    checkedColor = HaomaPalette.BTN_GIVE,
                    uncheckedColor = HaomaPalette.FG_DIM,
                    checkmarkColor = HaomaPalette.BG_BASE,
                ),
            )
            Spacer(modifier = Modifier.width(4.dp))
            Text(
                text = label,
                color = HaomaPalette.FG_LINK,
                fontSize = 14.sp,
                fontWeight = FontWeight.SemiBold,
            )
        }
        Spacer(modifier = Modifier.height(2.dp))
        Text(
            text = description,
            color = HaomaPalette.FG_SECONDARY,
            fontSize = 12.sp,
            modifier = Modifier.padding(start = 48.dp),
        )
    }
    HorizontalDivider(color = HaomaPalette.DIVIDER, thickness = 0.5.dp)
}

@Composable
private fun SaveRow(dirty: Boolean, onSave: () -> Unit, onReset: () -> Unit) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = 16.dp, vertical = 14.dp),
        horizontalArrangement = Arrangement.spacedBy(12.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        CtaButton(
            label = "Save",
            accent = HaomaPalette.BTN_PRIMARY,
            enabled = dirty,
            onClick = onSave,
        )
        Text(
            text = "Reset",
            color = if (dirty) HaomaPalette.FG_LINK else HaomaPalette.FG_DIM,
            fontSize = 13.sp,
            fontWeight = FontWeight.SemiBold,
            modifier = Modifier
                .clickable(enabled = dirty, onClick = onReset)
                .padding(horizontal = 8.dp, vertical = 10.dp),
        )
    }
    HorizontalDivider(color = HaomaPalette.DIVIDER, thickness = 0.5.dp)
}

@Composable
private fun DangerSection(
    risksAcked: Boolean,
    onRiskCheck: (Boolean) -> Unit,
    onClear: () -> Unit,
    onDelete: () -> Unit,
) {
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = 16.dp, vertical = 16.dp),
    ) {
        Text(
            text = "DANGER",
            color = HaomaPalette.C_DANGER,
            fontSize = 13.sp,
            fontWeight = FontWeight.SemiBold,
        )
        Spacer(modifier = Modifier.height(10.dp))
        Row(verticalAlignment = Alignment.CenterVertically) {
            Checkbox(
                checked = risksAcked,
                onCheckedChange = onRiskCheck,
                colors = CheckboxDefaults.colors(
                    checkedColor = HaomaPalette.C_DANGER,
                    uncheckedColor = HaomaPalette.FG_DIM,
                    checkmarkColor = HaomaPalette.BG_BASE,
                ),
            )
            Spacer(modifier = Modifier.width(8.dp))
            Text(
                text = "I understand risks",
                color = HaomaPalette.FG_PRIMARY,
                fontSize = 14.sp,
            )
        }
        Spacer(modifier = Modifier.height(12.dp))
        DangerButton(
            label = "Clear chat",
            enabled = risksAcked,
            modifier = Modifier.fillMaxWidth(),
            onClick = onClear,
        )
        Spacer(modifier = Modifier.height(8.dp))
        DangerButton(
            label = "Delete chat",
            enabled = risksAcked,
            modifier = Modifier.fillMaxWidth(),
            onClick = onDelete,
        )
    }
    HorizontalDivider(color = HaomaPalette.DIVIDER, thickness = 0.5.dp)
}

@Composable
private fun ConfirmDialog(
    which: ChatConfirm,
    onDismiss: () -> Unit,
    onConfirm: () -> Unit,
) {
    val (title, body, confirmLabel) = when (which) {
        ChatConfirm.Clear -> Triple(
            "Clear chat?",
            "Purges every message in this conversation. The chat row stays but the timeline empties on both your devices.",
            "Clear",
        )
        ChatConfirm.Delete -> Triple(
            "Delete chat?",
            "Purges messages and drops the chat row entirely. The peer stays paired — you can start a new chat from Contacts.",
            "Delete",
        )
    }
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
                    Text(
                        text = confirmLabel,
                        color = HaomaPalette.C_DANGER,
                        fontWeight = FontWeight.SemiBold,
                    )
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

private const val NICK_OVERRIDE_MAX_LEN = 32

private fun chatTitle(chat: ChatEntry): String = when {
    chat.label.isNotEmpty() -> chat.label
    chat.kind == ChatKind.Group && chat.groupAlias.isNotEmpty() -> chat.groupAlias
    chat.kind == ChatKind.Group && chat.groupName.isNotEmpty() -> chat.groupName
    chat.peerId.isNotEmpty() -> shortPeerId(chat.peerId)
    else -> shortPeerId(chat.chatId)
}


internal data class RetentionLevel(val label: String, val seconds: Int)

internal val retentionLevels: List<RetentionLevel> = listOf(
    RetentionLevel("Off", 0),
    RetentionLevel("1m", 60),
    RetentionLevel("10m", 600),
    RetentionLevel("1h", 3600),
    RetentionLevel("6h", 6 * 3600),
    RetentionLevel("1d", 24 * 3600),
    RetentionLevel("3d", 3 * 24 * 3600),
    RetentionLevel("1w", 7 * 24 * 3600),
    RetentionLevel("2w", 14 * 24 * 3600),
    RetentionLevel("4w", 28 * 24 * 3600),
)

internal fun retentionOptionIndex(seconds: Long): Int {
    val target = seconds.toInt()
    val idx = retentionLevels.indexOfFirst { it.seconds == target }
    return if (idx >= 0) idx else 0
}
