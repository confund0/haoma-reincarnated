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
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Checkbox
import androidx.compose.material3.CheckboxDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
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
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.window.PopupProperties
import io.haoma.calculator.messenger.ChatDefaultsSettings
import io.haoma.calculator.messenger.CtaButton
import io.haoma.calculator.messenger.DirtyBar
import io.haoma.calculator.messenger.HaomaDropdownItem
import io.haoma.calculator.messenger.HaomaPalette
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.Section
import io.haoma.calculator.messenger.chat.retentionLevels
import io.haoma.calculator.messenger.chat.retentionOptionIndex
import io.haoma.calculator.messenger.saveChatDefaults
import io.haoma.calculator.messenger.loadChatDefaults
import kotlinx.coroutines.launch


@Composable
internal fun ChatDefaultsSection(store: MessengerStore, onBack: () -> Unit) {
    var initial by remember { mutableStateOf(store.loadChatDefaults()) }
    val coroutineScope = rememberCoroutineScope()
    val scrollState = rememberScrollState()

    val snapshot = initial
    if (snapshot == null) {
        Column(modifier = Modifier.fillMaxSize().background(HaomaPalette.BG_BASE)) {
            SectionHeader(title = "Chat defaults", store = store, onBack = onBack)
            VaultUnavailableBanner(message = "Vault session unavailable — re-unlock the app to edit chat defaults.")
        }
        return
    }

    val initialRetentionIndex = retentionOptionIndex(snapshot.retentionSeconds)
    var retentionIndex by remember(snapshot) { mutableIntStateOf(initialRetentionIndex) }
    var sendReceipts by remember(snapshot) { mutableStateOf(snapshot.sendReceipts) }
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
    val dirty by remember(current, snapshot) { derivedStateOf { current != snapshot } }

    Column(modifier = Modifier.fillMaxSize().background(HaomaPalette.BG_BASE)) {
        SectionHeader(title = "Chat defaults", store = store, onBack = onBack)
        DirtyBar(
            visible = dirty && !saving,
            onTap = { coroutineScope.launch { scrollState.animateScrollTo(scrollState.maxValue) } },
        )

        Column(modifier = Modifier.weight(1f).verticalScroll(scrollState)) {
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

            Section(
                label = "Apply scope",
                description = "Applies to chats created after saving. Existing chats keep their per-chat retention + receipts settings.",
            ) {}

            Spacer(modifier = Modifier.height(8.dp))

            SaveResetRow(
                dirty = dirty,
                saving = saving,
                onSave = {
                    val saveSnapshot = current
                    saving = true
                    error = null
                    coroutineScope.launch {
                        val result = store.saveChatDefaults(saveSnapshot)
                        saving = false
                        
                        
                        result.onSuccess { initial = store.loadChatDefaults() }
                        result.onFailure { t -> error = t.message ?: "save failed" }
                    }
                },
                onReset = {
                    retentionIndex = initialRetentionIndex
                    sendReceipts = snapshot.sendReceipts
                    error = null
                },
            )

            error?.let { message ->
                Spacer(modifier = Modifier.height(10.dp))
                Text(
                    text = message,
                    color = HaomaPalette.C_DANGER,
                    fontSize = 13.sp,
                    modifier = Modifier.padding(horizontal = 16.dp),
                )
            }

            Spacer(modifier = Modifier.height(24.dp))
        }
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
            Text(text = "▾", color = HaomaPalette.FG_LINK, fontSize = 14.sp)
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
internal fun ToggleRow(
    label: String,
    hint: String,
    checked: Boolean,
    onCheckedChange: (Boolean) -> Unit,
    enabled: Boolean = true,
) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .clickable(enabled = enabled) { onCheckedChange(!checked) }
            .padding(vertical = 6.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        if (enabled) {
            Checkbox(
                checked = checked,
                onCheckedChange = onCheckedChange,
                colors = CheckboxDefaults.colors(
                    checkedColor = HaomaPalette.BTN_GIVE,
                    uncheckedColor = HaomaPalette.FG_DIM,
                    checkmarkColor = HaomaPalette.BG_BASE,
                ),
            )
        } else {
            
            
            Box(
                modifier = Modifier.size(48.dp),
                contentAlignment = Alignment.Center,
            ) {
                Text(
                    text = "×",
                    color = HaomaPalette.C_DANGER,
                    fontSize = 22.sp,
                    fontWeight = FontWeight.Bold,
                )
            }
        }
        Spacer(modifier = Modifier.width(8.dp))
        Column(modifier = Modifier.weight(1f)) {
            Text(
                text = label,
                color = if (enabled) HaomaPalette.FG_LINK else HaomaPalette.FG_DIM,
                fontSize = 14.sp,
                fontWeight = FontWeight.SemiBold,
            )
            if (hint.isNotEmpty()) {
                Text(
                    text = hint,
                    color = HaomaPalette.FG_SECONDARY,
                    fontSize = 12.sp,
                )
            }
        }
    }
}


@Composable
internal fun SaveResetRow(
    dirty: Boolean,
    saving: Boolean,
    onSave: () -> Unit,
    onReset: () -> Unit,
) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = 16.dp),
        horizontalArrangement = Arrangement.spacedBy(12.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        CtaButton(
            label = if (saving) "Saving…" else "Save",
            accent = HaomaPalette.BTN_PRIMARY,
            enabled = dirty && !saving,
            onClick = onSave,
        )
        Text(
            text = "Reset",
            color = if (dirty && !saving) HaomaPalette.FG_LINK else HaomaPalette.FG_DIM,
            fontSize = 13.sp,
            fontWeight = FontWeight.SemiBold,
            modifier = Modifier
                .clickable(enabled = dirty && !saving, onClick = onReset)
                .padding(horizontal = 8.dp, vertical = 10.dp),
        )
        if (saving) {
            CircularProgressIndicator(
                color = HaomaPalette.BTN_PRIMARY,
                strokeWidth = 2.dp,
                modifier = Modifier.height(20.dp).width(20.dp),
            )
            Text(
                text = "re-sealing vault (1–3s)…",
                color = HaomaPalette.FG_SECONDARY,
                fontSize = 12.sp,
            )
        }
    }
}

@Composable
internal fun VaultUnavailableBanner(message: String) {
    Box(
        modifier = Modifier
            .fillMaxSize()
            .padding(24.dp),
        contentAlignment = Alignment.Center,
    ) {
        Text(
            text = message,
            color = HaomaPalette.FG_SECONDARY,
            fontSize = 13.sp,
        )
    }
}
