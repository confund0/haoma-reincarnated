package io.haoma.calculator.messenger.settings

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
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Checkbox
import androidx.compose.material3.CheckboxDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.haoma.calculator.messenger.*
import io.haoma.calculator.messenger.ChatDefaultsSettings
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.chat.retentionLevels
import io.haoma.calculator.messenger.chat.retentionOptionIndex
import kotlinx.coroutines.launch


@Composable
internal fun ChatDefaultsSection(store: MessengerStore, onBack: () -> Unit) {
    val initial = remember { store.loadChatDefaults() }
    val coroutineScope = rememberCoroutineScope()

    Column(
        modifier = Modifier
            .fillMaxSize()
            .background(BG_BASE)
            .verticalScroll(rememberScrollState()),
    ) {
        SectionHeader(title = "Chat defaults", store = store, onBack = onBack)

        if (initial == null) {
            VaultUnavailableBanner()
            return@Column
        }

        val initialRetentionIndex = retentionOptionIndex(initial.retentionSeconds)
        var retentionIndex by remember { mutableIntStateOf(initialRetentionIndex) }
        var sendReceipts by remember { mutableStateOf(initial.sendReceipts) }
        var saving by remember { mutableStateOf(false) }
        var error by remember { mutableStateOf<String?>(null) }

        val current by remember(retentionIndex, sendReceipts) {
            derivedStateOf {
                ChatDefaultsSettings(
                    retentionSeconds = retentionLevels[retentionIndex].seconds.toLong(),
                    sendReceipts = sendReceipts,
                )
            }
        }
        val dirty by remember(current, initial) { derivedStateOf { current != initial } }

        Section(label = "Disappearing messages") {
            RetentionDropdown(
                currentIndex = retentionIndex,
                onPick = { retentionIndex = it; if (error != null) error = null },
            )
        }

        Section(label = "Read receipts") {
            ToggleRow(
                label = "Send read receipts by default",
                hint = "Tells the other side when you read their messages.",
                checked = sendReceipts,
                onCheckedChange = { sendReceipts = it; if (error != null) error = null },
            )
        }

        Section(label = "Apply scope") {
            Text(
                text = "Applies to chats created after saving. Existing chats keep their " +
                    "per-chat retention + receipts settings.",
                color = FG_DIM,
                fontSize = 12.sp,
            )
        }

        Spacer(modifier = Modifier.height(8.dp))

        Row(
            modifier = Modifier
                .fillMaxWidth()
                .padding(horizontal = 16.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Button(
                enabled = dirty && !saving,
                onClick = {
                    val snapshot = current
                    saving = true
                    error = null
                    coroutineScope.launch {
                        val result = store.saveChatDefaults(snapshot)
                        saving = false
                        result.onSuccess { onBack() }
                        result.onFailure { t -> error = t.message ?: "save failed" }
                    }
                },
                colors = ButtonDefaults.buttonColors(
                    containerColor = BTN_PRIMARY,
                    contentColor = BG_BASE,
                    disabledContainerColor = BTN_DIM,
                    disabledContentColor = FG_DIM,
                ),
            ) {
                Text(if (saving) "Saving…" else "Save")
            }
            Spacer(modifier = Modifier.width(12.dp))
            TextButton(
                enabled = dirty && !saving,
                onClick = {
                    retentionIndex = initialRetentionIndex
                    sendReceipts = initial.sendReceipts
                    error = null
                },
            ) {
                Text("Reset", color = if (dirty && !saving) FG_LINK else FG_DIM)
            }
            if (saving) {
                Spacer(modifier = Modifier.width(12.dp))
                CircularProgressIndicator(
                    color = BTN_PRIMARY,
                    strokeWidth = 2.dp,
                    modifier = Modifier
                        .height(20.dp)
                        .width(20.dp),
                )
                Spacer(modifier = Modifier.width(8.dp))
                Text(
                    text = "re-sealing vault (1–3s)…",
                    color = FG_DIM,
                    fontSize = 12.sp,
                )
            }
        }

        error?.let { message ->
            Spacer(modifier = Modifier.height(10.dp))
            Text(
                text = message,
                color = C_DANGER,
                fontSize = 13.sp,
                modifier = Modifier.padding(horizontal = 16.dp),
            )
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
        verticalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        Text(
            text = label.uppercase(),
            color = FG_DIM,
            fontSize = 11.sp,
            fontWeight = FontWeight.SemiBold,
        )
        content()
    }
    HorizontalDivider(color = DIVIDER, thickness = 0.5.dp)
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
                color = FG_PRIMARY,
                fontFamily = FontFamily.Monospace,
                fontSize = 14.sp,
                fontWeight = FontWeight.SemiBold,
                modifier = Modifier.weight(1f),
            )
            Text(text = "▾", color = FG_LINK, fontSize = 14.sp)
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

@Composable
private fun ToggleRow(
    label: String,
    hint: String,
    checked: Boolean,
    onCheckedChange: (Boolean) -> Unit,
) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .clickable { onCheckedChange(!checked) }
            .padding(vertical = 6.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Checkbox(
            checked = checked,
            onCheckedChange = onCheckedChange,
            colors = CheckboxDefaults.colors(
                checkedColor = BTN_PRIMARY,
                uncheckedColor = FG_DIM,
                checkmarkColor = BG_BASE,
            ),
        )
        Spacer(modifier = Modifier.width(8.dp))
        Column(modifier = Modifier.weight(1f)) {
            Text(text = label, color = FG_PRIMARY, fontSize = 14.sp)
            if (hint.isNotEmpty()) {
                Text(text = hint, color = FG_DIM, fontSize = 12.sp)
            }
        }
    }
}

@Composable
private fun VaultUnavailableBanner() {
    Box(
        modifier = Modifier
            .fillMaxSize()
            .padding(24.dp),
        contentAlignment = Alignment.Center,
    ) {
        Text(
            text = "Vault session unavailable — re-unlock the app to edit chat defaults.",
            color = FG_DIM,
            fontSize = 13.sp,
        )
    }
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
