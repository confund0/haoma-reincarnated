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
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
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
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.window.PopupProperties
import io.haoma.calculator.messenger.CtaButton
import io.haoma.calculator.messenger.DirtyBar
import io.haoma.calculator.messenger.HaomaDropdownItem
import io.haoma.calculator.messenger.HaomaPalette
import io.haoma.calculator.messenger.HaomaTextField
import io.haoma.calculator.messenger.LockSettings
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.Section
import io.haoma.calculator.messenger.THREAT_PRESET_BUNDLES
import io.haoma.calculator.messenger.applyThreatPreset
import io.haoma.calculator.messenger.changePassphrase
import io.haoma.calculator.messenger.changeUnlockPattern
import io.haoma.calculator.messenger.loadLockSettings
import io.haoma.calculator.messenger.saveLock
import kotlinx.coroutines.launch


@Composable
internal fun LockSection(store: MessengerStore, onBack: () -> Unit) {
    var initial by remember { mutableStateOf(store.loadLockSettings()) }
    val coroutineScope = rememberCoroutineScope()
    val scrollState = rememberScrollState()

    val snapshot = initial
    if (snapshot == null) {
        Column(modifier = Modifier.fillMaxSize().background(HaomaPalette.BG_BASE)) {
            SectionHeader(title = "Security", store = store, onBack = onBack)
            VaultUnavailableBanner(message = "Vault session unavailable — re-unlock the app to edit Security settings.")
        }
        return
    }

    
    var presetIndex by remember(snapshot) { mutableIntStateOf(presetIndexOf(snapshot.threatProfile)) }
    var idleIndex by remember(snapshot) { mutableIntStateOf(idleIndexOf(snapshot.idleAction)) }
    var panicIndex by remember(snapshot) { mutableIntStateOf(panicIndexOf(snapshot.panicAction)) }
    var idleTimeoutText by remember(snapshot) {
        mutableStateOf(snapshot.idleTimeoutSeconds.takeIf { it > 0 }?.toString() ?: "")
    }
    var pinValidityText by remember(snapshot) {
        mutableStateOf(snapshot.pinValiditySec.toString())
    }

    var saving by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    var confirmPreset by remember { mutableStateOf<String?>(null) }
    var showPatternDialog by remember { mutableStateOf(false) }
    var showPassphraseDialog by remember { mutableStateOf(false) }

    val current by remember(idleIndex, idleTimeoutText, pinValidityText, panicIndex) {
        derivedStateOf {
            LockSettings(
                threatProfile = snapshot.threatProfile,
                idleAction = IDLE_OPTIONS[idleIndex],
                idleTimeoutSeconds = idleTimeoutText.trim().toIntOrNull() ?: 0,
                pinValiditySec = pinValidityText.trim().toIntOrNull() ?: 0,
                panicAction = PANIC_VALUES[panicIndex],
            )
        }
    }
    val dirty by remember(current, snapshot) {
        derivedStateOf {
            current.idleAction != snapshot.idleAction ||
                current.idleTimeoutSeconds != snapshot.idleTimeoutSeconds ||
                current.pinValiditySec != snapshot.pinValiditySec ||
                current.panicAction != snapshot.panicAction
        }
    }

    
    val driftStatus = remember(current, snapshot.threatProfile) {
        buildDriftStatus(snapshot.threatProfile, current)
    }

    Column(modifier = Modifier.fillMaxSize().background(HaomaPalette.BG_BASE)) {
        SectionHeader(title = "Security", store = store, onBack = onBack)
        DirtyBar(
            visible = dirty && !saving,
            onTap = { coroutineScope.launch { scrollState.animateScrollTo(scrollState.maxValue) } },
        )

        Column(modifier = Modifier.weight(1f).verticalScroll(scrollState)) {
            Section(label = "Threat model") {
                Text(
                    text = driftStatus,
                    color = HaomaPalette.FG_SECONDARY,
                    fontSize = 12.sp,
                    fontFamily = FontFamily.Monospace,
                )
                Spacer(modifier = Modifier.height(6.dp))
                EnumDropdown(
                    label = "Preset",
                    options = PRESET_LABELS,
                    currentIndex = presetIndex,
                    onPick = { newIdx ->
                        val savedIdx = presetIndexOf(snapshot.threatProfile)
                        if (newIdx == savedIdx || saving) return@EnumDropdown
                        
                        
                        presetIndex = newIdx
                        confirmPreset = PRESET_VALUES[newIdx]
                        if (error != null) error = null
                    },
                )
                Spacer(modifier = Modifier.height(8.dp))
                Text(
                    text = "Activist — coming when the data-destruction primitives ship.",
                    color = HaomaPalette.FG_SECONDARY,
                    fontSize = 12.sp,
                )
            }

            Section(label = "Idle") {
                EnumDropdown(
                    label = "Idle action",
                    options = IDLE_LABELS,
                    currentIndex = idleIndex,
                    onPick = { idleIndex = it; if (error != null) error = null },
                )
                Spacer(modifier = Modifier.height(6.dp))
                NumericField(
                    label = "Idle timeout (seconds)",
                    value = idleTimeoutText,
                    onValueChange = { idleTimeoutText = it; if (error != null) error = null },
                )
            }

            Section(label = "PIN") {
                NumericField(
                    label = "PIN validity (seconds, 0 = no escalation)",
                    value = pinValidityText,
                    onValueChange = { pinValidityText = it; if (error != null) error = null },
                )
            }

            Section(label = "Panic") {
                EnumDropdown(
                    label = "Panic action",
                    options = PANIC_LABELS,
                    currentIndex = panicIndex,
                    onPick = { panicIndex = it; if (error != null) error = null },
                )
            }

            Section(label = "Credentials") {
                CtaButton(
                    label = "Change unlock pattern…",
                    accent = HaomaPalette.FG_LINK,
                    enabled = !saving,
                ) { showPatternDialog = true }
                Spacer(modifier = Modifier.height(6.dp))
                Text(
                    text = "The digit secret that reveals the messenger from soft-lock. Two ways to enter it: long-hold [5] then slide through the digits, or long-hold [1] then tap the digits and long-hold [=] to submit. Default `78963` is in effect until you change it here.",
                    color = HaomaPalette.FG_SECONDARY,
                    fontSize = 13.sp,
                    lineHeight = 18.sp,
                )
                Spacer(modifier = Modifier.height(12.dp))
                CtaButton(
                    label = "Change passphrase…",
                    accent = HaomaPalette.FG_LINK,
                    enabled = !saving,
                ) { showPassphraseDialog = true }
                Spacer(modifier = Modifier.height(6.dp))
                Text(
                    text = "The master key your vault is encrypted with. Required at every cold-boot unseal. No recovery if forgotten — vault contents are permanently lost.",
                    color = HaomaPalette.FG_SECONDARY,
                    fontSize = 13.sp,
                    lineHeight = 18.sp,
                )
            }

            Spacer(modifier = Modifier.height(8.dp))

            SaveResetRow(
                dirty = dirty,
                saving = saving,
                onSave = {
                    val timeout = idleTimeoutText.trim().toIntOrNull()
                    if (timeout == null || timeout <= 0) {
                        error = "Idle timeout must be a positive integer"
                        return@SaveResetRow
                    }
                    val validity = pinValidityText.trim().toIntOrNull()
                    if (validity == null || validity < 0) {
                        error = "PIN validity must be ≥ 0"
                        return@SaveResetRow
                    }
                    val saveSnapshot = current.copy(
                        idleTimeoutSeconds = timeout,
                        pinValiditySec = validity,
                    )
                    saving = true
                    error = null
                    coroutineScope.launch {
                        
                        
                        val result = store.saveLock(saveSnapshot, clearThreatProfile = false)
                        saving = false
                        result.onSuccess { initial = store.loadLockSettings() }
                        result.onFailure { t -> error = t.message ?: "save failed" }
                    }
                },
                onReset = {
                    idleIndex = idleIndexOf(snapshot.idleAction)
                    panicIndex = panicIndexOf(snapshot.panicAction)
                    idleTimeoutText =
                        snapshot.idleTimeoutSeconds.takeIf { it > 0 }?.toString() ?: ""
                    pinValidityText = snapshot.pinValiditySec.toString()
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

    confirmPreset?.let { presetId ->
        ApplyPresetConfirmDialog(
            presetId = presetId,
            onDismiss = {
                
                
                confirmPreset = null
                presetIndex = presetIndexOf(snapshot.threatProfile)
            },
            onApply = {
                confirmPreset = null
                saving = true
                error = null
                coroutineScope.launch {
                    val result = if (presetId.isEmpty()) {
                        
                        
                        store.saveLock(snapshot.copy(threatProfile = ""), clearThreatProfile = true)
                    } else {
                        store.applyThreatPreset(presetId)
                    }
                    saving = false
                    result.onSuccess {
                        
                        
                        initial = store.loadLockSettings()
                    }
                    result.onFailure { t ->
                        error = t.message ?: "apply failed"
                        
                        presetIndex = presetIndexOf(snapshot.threatProfile)
                    }
                }
            },
        )
    }

    if (showPatternDialog) {
        ChangePatternDialog(
            onDismiss = { showPatternDialog = false },
            onSave = { old, new, onResult ->
                coroutineScope.launch {
                    val result = store.changeUnlockPattern(old, new)
                    onResult(result)
                    if (result.isSuccess) showPatternDialog = false
                }
            },
        )
    }

    if (showPassphraseDialog) {
        ChangePassphraseDialog(
            onDismiss = { showPassphraseDialog = false },
            onSave = { old, new, onResult ->
                coroutineScope.launch {
                    val result = store.changePassphrase(old, new)
                    onResult(result)
                    if (result.isSuccess) showPassphraseDialog = false
                }
            },
        )
    }
}

@Composable
private fun EnumDropdown(
    label: String,
    options: List<String>,
    currentIndex: Int,
    onPick: (Int) -> Unit,
) {
    var expanded by remember { mutableStateOf(false) }
    Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
        Text(
            text = label,
            color = HaomaPalette.FG_LINK,
            fontSize = 14.sp,
            fontWeight = FontWeight.SemiBold,
        )
        Box {
            Row(
                modifier = Modifier
                    .fillMaxWidth()
                    .clickable { expanded = true }
                    .padding(vertical = 4.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Text(
                    text = options[currentIndex],
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
                    options.forEachIndexed { idx, lbl ->
                        HaomaDropdownItem(
                            label = lbl,
                            selected = idx == currentIndex,
                            onClick = { onPick(idx); expanded = false },
                        )
                    }
                }
            }
        }
    }
}

@Composable
private fun NumericField(
    label: String,
    value: String,
    onValueChange: (String) -> Unit,
) {
    Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
        Text(
            text = label,
            color = HaomaPalette.FG_LINK,
            fontSize = 14.sp,
            fontWeight = FontWeight.SemiBold,
        )
        HaomaTextField(
            value = value,
            onValueChange = { input -> onValueChange(input.filter { it.isDigit() }) },
            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number),
            modifier = Modifier.fillMaxWidth(),
        )
    }
}

@Composable
private fun ChangePatternDialog(
    onDismiss: () -> Unit,
    onSave: (String, String, (Result<Unit>) -> Unit) -> Unit,
) {
    var oldDraft by remember { mutableStateOf("") }
    var newDraft by remember { mutableStateOf("") }
    var saving by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }

    MaterialTheme(
        colorScheme = darkColorScheme(
            surface = HaomaPalette.BG_BAR,
            onSurface = HaomaPalette.FG_PRIMARY,
            background = HaomaPalette.BG_BAR,
            onBackground = HaomaPalette.FG_PRIMARY,
        ),
    ) {
        AlertDialog(
            onDismissRequest = { if (!saving) onDismiss() },
            title = { Text("Change unlock pattern", color = HaomaPalette.FG_PRIMARY) },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    Text(
                        text = "Digits only, length ≥ 4. Default is `78963` until first changed.",
                        color = HaomaPalette.FG_SECONDARY,
                        fontSize = 13.sp,
                    )
                    if (saving) {
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            CircularProgressIndicator(
                                color = HaomaPalette.BTN_PRIMARY,
                                strokeWidth = 2.dp,
                                modifier = Modifier.height(20.dp).width(20.dp),
                            )
                            Spacer(modifier = Modifier.width(10.dp))
                            Text(
                                text = "re-sealing vault (1–3s)…",
                                color = HaomaPalette.FG_SECONDARY,
                                fontSize = 12.sp,
                            )
                        }
                    } else {
                        OutlinedTextField(
                            value = oldDraft,
                            onValueChange = {
                                oldDraft = it.filter { c -> c.isDigit() }
                                if (error != null) error = null
                            },
                            singleLine = true,
                            visualTransformation = PasswordVisualTransformation(),
                            keyboardOptions = KeyboardOptions(
                                keyboardType = KeyboardType.NumberPassword,
                            ),
                            label = { Text("Current pattern", color = HaomaPalette.FG_DIM) },
                            colors = dialogFieldColors(),
                            modifier = Modifier.fillMaxWidth(),
                        )
                        OutlinedTextField(
                            value = newDraft,
                            onValueChange = {
                                newDraft = it.filter { c -> c.isDigit() }
                                if (error != null) error = null
                            },
                            singleLine = true,
                            visualTransformation = PasswordVisualTransformation(),
                            keyboardOptions = KeyboardOptions(
                                keyboardType = KeyboardType.NumberPassword,
                            ),
                            label = { Text("New pattern", color = HaomaPalette.FG_DIM) },
                            colors = dialogFieldColors(),
                            modifier = Modifier.fillMaxWidth(),
                        )
                    }
                    error?.let {
                        Text(text = it, color = HaomaPalette.C_DANGER, fontSize = 13.sp)
                    }
                }
            },
            confirmButton = {
                TextButton(
                    enabled = !saving,
                    onClick = {
                        saving = true
                        error = null
                        onSave(oldDraft, newDraft) { result ->
                            saving = false
                            result.onFailure { t -> error = t.message ?: "save failed" }
                        }
                    },
                ) {
                    Text(
                        text = "Save",
                        color = if (saving) HaomaPalette.FG_DIM else HaomaPalette.FG_LINK,
                        fontWeight = FontWeight.SemiBold,
                    )
                }
            },
            dismissButton = {
                TextButton(
                    enabled = !saving,
                    onClick = onDismiss,
                ) {
                    Text(
                        text = "Cancel",
                        color = if (saving) HaomaPalette.FG_DIM else HaomaPalette.FG_LINK,
                    )
                }
            },
            containerColor = HaomaPalette.BG_BAR,
        )
    }
}

@Composable
private fun ChangePassphraseDialog(
    onDismiss: () -> Unit,
    onSave: (String, String, (Result<Unit>) -> Unit) -> Unit,
) {
    var oldDraft by remember { mutableStateOf("") }
    var newDraft by remember { mutableStateOf("") }
    var saving by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }

    MaterialTheme(
        colorScheme = darkColorScheme(
            surface = HaomaPalette.BG_BAR,
            onSurface = HaomaPalette.FG_PRIMARY,
            background = HaomaPalette.BG_BAR,
            onBackground = HaomaPalette.FG_PRIMARY,
        ),
    ) {
        AlertDialog(
            onDismissRequest = { if (!saving) onDismiss() },
            title = { Text("Change passphrase", color = HaomaPalette.FG_PRIMARY) },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    Text(
                        text = "Master vault key. No recovery if forgotten — vault contents become permanently unreadable.",
                        color = HaomaPalette.C_DANGER,
                        fontSize = 13.sp,
                    )
                    if (saving) {
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            CircularProgressIndicator(
                                color = HaomaPalette.BTN_PRIMARY,
                                strokeWidth = 2.dp,
                                modifier = Modifier.height(20.dp).width(20.dp),
                            )
                            Spacer(modifier = Modifier.width(10.dp))
                            Text(
                                text = "re-sealing vault (1–3s)…",
                                color = HaomaPalette.FG_SECONDARY,
                                fontSize = 12.sp,
                            )
                        }
                    } else {
                        OutlinedTextField(
                            value = oldDraft,
                            onValueChange = { oldDraft = it; if (error != null) error = null },
                            singleLine = true,
                            visualTransformation = PasswordVisualTransformation(),
                            keyboardOptions = KeyboardOptions(
                                keyboardType = KeyboardType.Password,
                            ),
                            label = { Text("Current passphrase", color = HaomaPalette.FG_DIM) },
                            colors = dialogFieldColors(),
                            modifier = Modifier.fillMaxWidth(),
                        )
                        OutlinedTextField(
                            value = newDraft,
                            onValueChange = { newDraft = it; if (error != null) error = null },
                            singleLine = true,
                            visualTransformation = PasswordVisualTransformation(),
                            keyboardOptions = KeyboardOptions(
                                keyboardType = KeyboardType.Password,
                            ),
                            label = { Text("New passphrase", color = HaomaPalette.FG_DIM) },
                            colors = dialogFieldColors(),
                            modifier = Modifier.fillMaxWidth(),
                        )
                    }
                    error?.let {
                        Text(text = it, color = HaomaPalette.C_DANGER, fontSize = 13.sp)
                    }
                }
            },
            confirmButton = {
                TextButton(
                    enabled = !saving,
                    onClick = {
                        if (newDraft.isEmpty()) {
                            error = "new passphrase must not be empty"
                            return@TextButton
                        }
                        saving = true
                        error = null
                        onSave(oldDraft, newDraft) { result ->
                            saving = false
                            result.onFailure { t -> error = t.message ?: "save failed" }
                        }
                    },
                ) {
                    Text(
                        text = "Save",
                        color = if (saving) HaomaPalette.FG_DIM else HaomaPalette.FG_LINK,
                        fontWeight = FontWeight.SemiBold,
                    )
                }
            },
            dismissButton = {
                TextButton(
                    enabled = !saving,
                    onClick = onDismiss,
                ) {
                    Text(
                        text = "Cancel",
                        color = if (saving) HaomaPalette.FG_DIM else HaomaPalette.FG_LINK,
                    )
                }
            },
            containerColor = HaomaPalette.BG_BAR,
        )
    }
}

@Composable
private fun ApplyPresetConfirmDialog(
    presetId: String,
    onDismiss: () -> Unit,
    onApply: () -> Unit,
) {
    val clearing = presetId.isEmpty()
    val label = if (clearing) "" else PRESET_LABEL_BY_ID[presetId] ?: presetId
    val title = if (clearing) "Clear preset label?" else "Apply $label preset?"
    val body = if (clearing) {
        "Removes the preset tag. Your idle / PIN / panic values stay as they are — only the bundle label is cleared."
    } else {
        "Your current Lock + Panic settings will be overwritten with the $label bundle."
    }
    val confirmLabel = if (clearing) "Clear" else "Apply"
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
            text = {
                Text(
                    text = body,
                    color = HaomaPalette.FG_SECONDARY,
                    fontSize = 13.sp,
                )
            },
            confirmButton = {
                TextButton(onClick = onApply) {
                    Text(
                        text = confirmLabel,
                        color = HaomaPalette.FG_LINK,
                        fontWeight = FontWeight.SemiBold,
                    )
                }
            },
            dismissButton = {
                TextButton(onClick = onDismiss) {
                    Text(text = "Cancel", color = HaomaPalette.FG_LINK)
                }
            },
            containerColor = HaomaPalette.BG_BAR,
        )
    }
}


private fun buildDriftStatus(activeProfile: String, current: LockSettings): String {
    if (activeProfile.isEmpty()) return "Custom (no preset selected)"
    val label = PRESET_LABEL_BY_ID[activeProfile] ?: activeProfile
    val bundle = THREAT_PRESET_BUNDLES[activeProfile] ?: return label
    val matches = bundle.idleAction == current.idleAction &&
        bundle.idleTimeoutSeconds == current.idleTimeoutSeconds &&
        bundle.pinValiditySec == current.pinValiditySec &&
        bundle.panicAction == current.panicAction
    return if (matches) label else "$label-modified"
}

private fun presetIndexOf(profile: String): Int = when (profile) {
    "domestic" -> 1
    "privacy" -> 2
    else -> 0
}

private fun idleIndexOf(action: String): Int {
    val idx = IDLE_OPTIONS.indexOf(action)
    return if (idx >= 0) idx else 0
}

private fun panicIndexOf(action: String): Int {
    val idx = PANIC_VALUES.indexOf(action)
    return if (idx >= 0) idx else 0
}

private val PRESET_LABELS: List<String> = listOf("(unset)", "Domestic", "Privacy")
private val PRESET_VALUES: List<String> = listOf("", "domestic", "privacy")
private val PRESET_LABEL_BY_ID: Map<String, String> = mapOf(
    "domestic" to "Domestic",
    "privacy" to "Privacy",
    "activist" to "Activist",
)


private val IDLE_LABELS: List<String> = listOf("safe-lock", "soft-lock", "hard-lock")
private val IDLE_OPTIONS: List<String> = listOf("safe-lock", "soft-lock", "hard-lock")


private val PANIC_LABELS: List<String> = listOf(
    "(disabled — /panic = /quit)",
    "safe-lock",
    "hard-lock",
    "self-destruct",
)
private val PANIC_VALUES: List<String> = listOf("", "safe-lock", "hard-lock", "self-destruct")
