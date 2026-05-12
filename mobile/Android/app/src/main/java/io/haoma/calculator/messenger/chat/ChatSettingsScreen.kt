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
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Checkbox
import androidx.compose.material3.CheckboxDefaults
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
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
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import io.haoma.calculator.messenger.*
import io.haoma.calculator.messenger.ChatEntry
import io.haoma.calculator.messenger.ChatKind
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.contacts.shortPeerId


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
    var risksAcked by remember { mutableStateOf(false) }
    var confirm by remember { mutableStateOf<ChatConfirm?>(null) }

    val initialIndex = retentionOptionIndex(chat.retentionTtl)
    val initialSendReceipts = !chat.disableReadReceipts
    val initialMuted = chat.notificationsMuted
    val dirty by remember(chat, retentionIndex, sendReceipts, muted) {
        derivedStateOf {
            retentionIndex != initialIndex ||
                sendReceipts != initialSendReceipts ||
                muted != initialMuted
        }
    }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .background(BG_BASE)
            .verticalScroll(rememberScrollState()),
    ) {
        Header(title = chatTitle(chat), store = store, onBack = onBack)

        RetentionSection(
            currentIndex = retentionIndex,
            onPick = { retentionIndex = it },
        )

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
                )
                onBack()
            },
            onCancel = onBack,
        )

        DangerSection(
            risksAcked = risksAcked,
            onRiskCheck = { risksAcked = it },
            onClear = { confirm = ChatConfirm.Clear },
            onDelete = { confirm = ChatConfirm.Delete },
        )

        Spacer(modifier = Modifier.height(24.dp))
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
            .background(BG_BAR)
            .padding(horizontal = 12.dp, vertical = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(
            text = "‹",
            color = FG_LINK,
            fontSize = 22.sp,
            fontWeight = FontWeight.Bold,
            modifier = Modifier
                .clickable(onClick = onBack)
                .padding(horizontal = 8.dp, vertical = 4.dp),
        )
        Spacer(modifier = Modifier.width(20.dp))
        Text(
            text = title,
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
    labelColor: Color = FG_DIM,
    content: @Composable () -> Unit,
) {
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = 16.dp, vertical = 14.dp),
    ) {
        Text(
            text = label.uppercase(),
            color = labelColor,
            fontSize = 11.sp,
            fontWeight = FontWeight.SemiBold,
        )
        Spacer(modifier = Modifier.height(8.dp))
        content()
    }
    HorizontalDivider(color = DIVIDER, thickness = 0.5.dp)
}

@Composable
private fun RetentionSection(currentIndex: Int, onPick: (Int) -> Unit) {
    var expanded by remember { mutableStateOf(false) }
    Section(label = "Disappearing messages") {
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
                    color = FG_PRIMARY,
                    fontFamily = FontFamily.Monospace,
                    fontSize = 16.sp,
                    fontWeight = FontWeight.SemiBold,
                    modifier = Modifier.weight(1f),
                )
                Text(
                    text = "▾",
                    color = FG_LINK,
                    fontSize = 14.sp,
                )
            }
            
            
            MaterialTheme(
                colorScheme = darkColorScheme(
                    surface = BG_BAR,
                    onSurface = FG_PRIMARY,
                ),
            ) {
                DropdownMenu(
                    expanded = expanded,
                    onDismissRequest = { expanded = false },
                ) {
                    retentionLevels.forEachIndexed { idx, lvl ->
                        DropdownMenuItem(
                            text = {
                                Text(
                                    text = lvl.label,
                                    color = if (idx == currentIndex) FG_LINK else FG_PRIMARY,
                                    fontFamily = FontFamily.Monospace,
                                )
                            },
                            onClick = {
                                onPick(idx)
                                expanded = false
                            },
                        )
                    }
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
                    checkedColor = FG_LINK,
                    uncheckedColor = FG_DIM,
                    checkmarkColor = BG_BASE,
                ),
            )
            Spacer(modifier = Modifier.width(4.dp))
            Text(
                text = label,
                color = FG_PRIMARY,
                fontSize = 15.sp,
                fontWeight = FontWeight.Medium,
            )
        }
        Spacer(modifier = Modifier.height(2.dp))
        Text(
            text = description,
            color = FG_DIM,
            fontSize = 12.sp,
            modifier = Modifier.padding(start = 48.dp),
        )
    }
    HorizontalDivider(color = DIVIDER, thickness = 0.5.dp)
}

@Composable
private fun SaveRow(dirty: Boolean, onSave: () -> Unit, onCancel: () -> Unit) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = 16.dp, vertical = 14.dp),
        horizontalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        Button(
            enabled = dirty,
            onClick = onSave,
            colors = ButtonDefaults.buttonColors(
                containerColor = BTN_PRIMARY,
                contentColor = BG_BASE,
                disabledContainerColor = BTN_DIM,
                disabledContentColor = FG_DIM,
            ),
        ) { Text("Save") }
        TextButton(onClick = onCancel) {
            Text("Cancel", color = FG_LINK)
        }
    }
    HorizontalDivider(color = DIVIDER, thickness = 0.5.dp)
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
            .padding(horizontal = 16.dp, vertical = 14.dp),
    ) {
        Text(
            text = "DANGER",
            color = C_DANGER,
            fontSize = 11.sp,
            fontWeight = FontWeight.SemiBold,
        )
        Spacer(modifier = Modifier.height(8.dp))
        Row(verticalAlignment = Alignment.CenterVertically) {
            Checkbox(
                checked = risksAcked,
                onCheckedChange = onRiskCheck,
                colors = CheckboxDefaults.colors(
                    checkedColor = C_DANGER,
                    uncheckedColor = FG_DIM,
                    checkmarkColor = BG_BASE,
                ),
            )
            Spacer(modifier = Modifier.width(4.dp))
            Text(
                text = "I understand risks",
                color = FG_PRIMARY,
                fontSize = 14.sp,
            )
        }
        Spacer(modifier = Modifier.height(12.dp))
        DangerButton(
            label = "Clear chat",
            enabled = risksAcked,
            onClick = onClear,
        )
        Spacer(modifier = Modifier.height(8.dp))
        DangerButton(
            label = "Delete chat",
            enabled = risksAcked,
            onClick = onDelete,
        )
    }
}

@Composable
private fun DangerButton(label: String, enabled: Boolean, onClick: () -> Unit) {
    Button(
        enabled = enabled,
        onClick = onClick,
        colors = ButtonDefaults.buttonColors(
            containerColor = C_DANGER,
            contentColor = BG_BASE,
            disabledContainerColor = BTN_DIM,
            disabledContentColor = FG_DIM,
        ),
        modifier = Modifier.fillMaxWidth(),
    ) {
        Text(label)
    }
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
            surface = BG_BAR,
            onSurface = FG_PRIMARY,
            background = BG_BAR,
            onBackground = FG_PRIMARY,
        ),
    ) {
        AlertDialog(
            onDismissRequest = onDismiss,
            title = { Text(title, color = FG_PRIMARY) },
            text = { Text(body, color = FG_DIM, fontSize = 14.sp) },
            confirmButton = {
                TextButton(onClick = onConfirm) {
                    Text(confirmLabel, color = C_DANGER, fontWeight = FontWeight.SemiBold)
                }
            },
            dismissButton = {
                TextButton(onClick = onDismiss) {
                    Text("Cancel", color = FG_LINK)
                }
            },
            containerColor = BG_BAR,
        )
    }
}

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


private val BG_BASE = Color(0xFF1D2021)
private val BG_BAR = Color(0xFF282828)
private val DIVIDER = Color(0xFF3C3836)
private val FG_PRIMARY = Color(0xFFEBDBB2)
private val FG_DIM = Color(0xFF7C6F64)
private val FG_LINK = Color(0xFF83A598)
private val BTN_PRIMARY = Color(0xFF5FCC1A)
private val BTN_DIM = Color(0xFF504945)
private val C_DANGER = Color(0xFFCC241D) 
