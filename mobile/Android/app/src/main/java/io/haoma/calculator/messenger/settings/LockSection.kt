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
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.OutlinedTextFieldDefaults
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
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.haoma.calculator.messenger.*
import io.haoma.calculator.messenger.LockSettings
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.THREAT_PRESET_BUNDLES
import kotlinx.coroutines.launch


@Composable
internal fun LockSection(store: MessengerStore, onBack: () -> Unit) {
    val initial = remember { store.loadLockSettings() }
    val coroutineScope = rememberCoroutineScope()

    if (initial == null) {
        Column(
            modifier = Modifier
                .fillMaxSize()
                .background(BG_BASE),
        ) {
            SectionHeader(title = "Lock", store = store, onBack = onBack)
            VaultUnavailableBanner()
        }
        return
    }

    var presetIndex by remember { mutableIntStateOf(presetIndexOf(initial.threatProfile)) }
    var idleIndex by remember { mutableIntStateOf(idleIndexOf(initial.idleAction)) }
    var panicIndex by remember { mutableIntStateOf(panicIndexOf(initial.panicAction)) }
    var idleTimeoutText by remember {
        mutableStateOf(initial.idleTimeoutSeconds.takeIf { it > 0 }?.toString() ?: "")
    }
    var pinValidityText by remember {
        mutableStateOf(initial.pinValiditySec.toString())
    }

    var saving by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    var confirmPreset by remember { mutableStateOf<String?>(null) }
    var showPatternDialog by remember { mutableStateOf(false) }
    var showPassphraseDialog by remember { mutableStateOf(false) }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .background(BG_BASE)
            .verticalScroll(rememberScrollState()),
    ) {
        SectionHeader(title = "Lock", store = store, onBack = onBack)

        val current by remember(idleIndex, idleTimeoutText, pinValidityText, panicIndex) {
            derivedStateOf {
                LockSettings(
                    threatProfile = initial.threatProfile,
                    idleAction = IDLE_OPTIONS[idleIndex],
                    idleTimeoutSeconds = idleTimeoutText.trim().toIntOrNull() ?: 0,
                    pinValiditySec = pinValidityText.trim().toIntOrNull() ?: 0,
                    panicAction = PANIC_VALUES[panicIndex],
                )
            }
        }
        val clearThreatProfile by remember(presetIndex) {
            derivedStateOf { presetIndex == 0 && initial.threatProfile.isNotEmpty() }
        }
        val dirty by remember(current, presetIndex, initial) {
            derivedStateOf {
                current.idleAction != initial.idleAction ||
                    current.idleTimeoutSeconds != initial.idleTimeoutSeconds ||
                    current.pinValiditySec != initial.pinValiditySec ||
                    current.panicAction != initial.panicAction ||
                    presetIndex != presetIndexOf(initial.threatProfile)
            }
        }

        
        val driftStatus = remember(current, initial.threatProfile) {
            buildDriftStatus(initial.threatProfile, current)
        }

        Section(label = "Threat model") {
            Text(
                text = driftStatus,
                color = FG_DIM,
                fontSize = 12.sp,
                fontFamily = FontFamily.Monospace,
            )
            Spacer(modifier = Modifier.height(6.dp))
            EnumDropdown(
                label = "Preset",
                options = PRESET_LABELS,
                currentIndex = presetIndex,
                onPick = { presetIndex = it; if (error != null) error = null },
            )
            Spacer(modifier = Modifier.height(6.dp))
            Button(
                onClick = {
                    val target = PRESET_VALUES[presetIndex]
                    if (target.isEmpty()) {
                        
                        
                        return@Button
                    }
                    confirmPreset = target
                },
                enabled = PRESET_VALUES[presetIndex].isNotEmpty() && !saving,
                colors = ButtonDefaults.buttonColors(
                    containerColor = BTN_PRIMARY,
                    contentColor = BG_BASE,
                    disabledContainerColor = BTN_DIM,
                    disabledContentColor = FG_DIM,
                ),
            ) {
                Text("Apply preset…")
            }
            Spacer(modifier = Modifier.height(8.dp))
            Text(
                text = "Activist — coming when the data-destruction primitives ship.",
                color = FG_DIM,
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
            Button(
                onClick = { showPatternDialog = true },
                enabled = !saving,
                colors = ButtonDefaults.buttonColors(
                    containerColor = BTN_PRIMARY,
                    contentColor = BG_BASE,
                    disabledContainerColor = BTN_DIM,
                    disabledContentColor = FG_DIM,
                ),
            ) {
                Text("Change unlock pattern…")
            }
            Text(
                text = "The digit secret that reveals the messenger from soft-lock. " +
                    "Two ways to enter it: long-hold [5] then slide through the digits, " +
                    "or long-hold [1] then tap the digits and long-hold [=] to submit. " +
                    "Default `78963` is in effect until you change it here.",
                color = FG_DIM,
                fontSize = 12.sp,
            )
            Spacer(modifier = Modifier.height(8.dp))
            Button(
                onClick = { showPassphraseDialog = true },
                enabled = !saving,
                colors = ButtonDefaults.buttonColors(
                    containerColor = BTN_PRIMARY,
                    contentColor = BG_BASE,
                    disabledContainerColor = BTN_DIM,
                    disabledContentColor = FG_DIM,
                ),
            ) {
                Text("Change passphrase…")
            }
            Text(
                text = "The master key your vault is encrypted with. Required at every " +
                    "cold-boot unseal. No recovery if forgotten — vault contents are " +
                    "permanently lost.",
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
                    val timeout = idleTimeoutText.trim().toIntOrNull()
                    if (timeout == null || timeout <= 0) {
                        error = "Idle timeout must be a positive integer"
                        return@Button
                    }
                    val validity = pinValidityText.trim().toIntOrNull()
                    if (validity == null || validity < 0) {
                        error = "PIN validity must be ≥ 0"
                        return@Button
                    }
                    val snapshot = current.copy(
                        idleTimeoutSeconds = timeout,
                        pinValiditySec = validity,
                    )
                    val clear = clearThreatProfile
                    saving = true
                    error = null
                    coroutineScope.launch {
                        val result = store.saveLock(snapshot, clear)
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
                    presetIndex = presetIndexOf(initial.threatProfile)
                    idleIndex = idleIndexOf(initial.idleAction)
                    panicIndex = panicIndexOf(initial.panicAction)
                    idleTimeoutText =
                        initial.idleTimeoutSeconds.takeIf { it > 0 }?.toString() ?: ""
                    pinValidityText = initial.pinValiditySec.toString()
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

    confirmPreset?.let { presetId ->
        ApplyPresetConfirmDialog(
            presetId = presetId,
            onDismiss = { confirmPreset = null },
            onApply = {
                confirmPreset = null
                saving = true
                error = null
                coroutineScope.launch {
                    val result = store.applyThreatPreset(presetId)
                    saving = false
                    result.onSuccess { onBack() }
                    result.onFailure { t -> error = t.message ?: "apply failed" }
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
            surface = BG_BAR,
            onSurface = FG_PRIMARY,
            background = BG_BAR,
            onBackground = FG_PRIMARY,
        ),
    ) {
        AlertDialog(
            onDismissRequest = { if (!saving) onDismiss() },
            title = { Text("Change unlock pattern", color = FG_PRIMARY) },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    Text(
                        text = "Digits only, length ≥ 4. Default is `78963` until " +
                            "first changed.",
                        color = FG_DIM,
                        fontSize = 13.sp,
                    )
                    if (saving) {
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            CircularProgressIndicator(
                                color = BTN_PRIMARY,
                                strokeWidth = 2.dp,
                                modifier = Modifier.height(20.dp).width(20.dp),
                            )
                            Spacer(modifier = Modifier.width(10.dp))
                            Text(
                                text = "re-sealing vault (1–3s)…",
                                color = FG_DIM,
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
                            label = { Text("Current pattern", color = FG_DIM) },
                            colors = patternFieldColors(),
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
                            label = { Text("New pattern", color = FG_DIM) },
                            colors = patternFieldColors(),
                            modifier = Modifier.fillMaxWidth(),
                        )
                    }
                    error?.let {
                        Text(text = it, color = C_DANGER, fontSize = 13.sp)
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
                        color = if (saving) FG_DIM else FG_LINK,
                        fontWeight = FontWeight.SemiBold,
                    )
                }
            },
            dismissButton = {
                TextButton(
                    enabled = !saving,
                    onClick = onDismiss,
                ) {
                    Text(text = "Cancel", color = if (saving) FG_DIM else FG_LINK)
                }
            },
            containerColor = BG_BAR,
        )
    }
}

@Composable
private fun patternFieldColors() = OutlinedTextFieldDefaults.colors(
    focusedTextColor = FG_PRIMARY,
    unfocusedTextColor = FG_PRIMARY,
    cursorColor = FG_LINK,
    focusedBorderColor = FG_LINK,
    unfocusedBorderColor = DIVIDER,
)

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
            surface = BG_BAR,
            onSurface = FG_PRIMARY,
            background = BG_BAR,
            onBackground = FG_PRIMARY,
        ),
    ) {
        AlertDialog(
            onDismissRequest = { if (!saving) onDismiss() },
            title = { Text("Change passphrase", color = FG_PRIMARY) },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    Text(
                        text = "Master vault key. No recovery if forgotten — vault " +
                            "contents become permanently unreadable.",
                        color = C_DANGER,
                        fontSize = 13.sp,
                    )
                    if (saving) {
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            CircularProgressIndicator(
                                color = BTN_PRIMARY,
                                strokeWidth = 2.dp,
                                modifier = Modifier.height(20.dp).width(20.dp),
                            )
                            Spacer(modifier = Modifier.width(10.dp))
                            Text(
                                text = "re-sealing vault (1–3s)…",
                                color = FG_DIM,
                                fontSize = 12.sp,
                            )
                        }
                    } else {
                        OutlinedTextField(
                            value = oldDraft,
                            onValueChange = {
                                oldDraft = it
                                if (error != null) error = null
                            },
                            singleLine = true,
                            visualTransformation = PasswordVisualTransformation(),
                            keyboardOptions = KeyboardOptions(
                                keyboardType = KeyboardType.Password,
                            ),
                            label = { Text("Current passphrase", color = FG_DIM) },
                            colors = patternFieldColors(),
                            modifier = Modifier.fillMaxWidth(),
                        )
                        OutlinedTextField(
                            value = newDraft,
                            onValueChange = {
                                newDraft = it
                                if (error != null) error = null
                            },
                            singleLine = true,
                            visualTransformation = PasswordVisualTransformation(),
                            keyboardOptions = KeyboardOptions(
                                keyboardType = KeyboardType.Password,
                            ),
                            label = { Text("New passphrase", color = FG_DIM) },
                            colors = patternFieldColors(),
                            modifier = Modifier.fillMaxWidth(),
                        )
                    }
                    error?.let {
                        Text(text = it, color = C_DANGER, fontSize = 13.sp)
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
                        color = if (saving) FG_DIM else FG_LINK,
                        fontWeight = FontWeight.SemiBold,
                    )
                }
            },
            dismissButton = {
                TextButton(
                    enabled = !saving,
                    onClick = onDismiss,
                ) {
                    Text(text = "Cancel", color = if (saving) FG_DIM else FG_LINK)
                }
            },
            containerColor = BG_BAR,
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

@Composable
private fun ApplyPresetConfirmDialog(
    presetId: String,
    onDismiss: () -> Unit,
    onApply: () -> Unit,
) {
    val label = PRESET_LABEL_BY_ID[presetId] ?: presetId
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
            title = { Text("Apply $label preset?", color = FG_PRIMARY) },
            text = {
                Text(
                    text = "Your current Lock + Panic settings will be overwritten with " +
                        "the $label bundle.",
                    color = FG_DIM,
                    fontSize = 13.sp,
                )
            },
            confirmButton = {
                TextButton(onClick = onApply) {
                    Text(text = "Apply", color = FG_LINK, fontWeight = FontWeight.SemiBold)
                }
            },
            dismissButton = {
                TextButton(onClick = onDismiss) {
                    Text(text = "Cancel", color = FG_LINK)
                }
            },
            containerColor = BG_BAR,
        )
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
private fun EnumDropdown(
    label: String,
    options: List<String>,
    currentIndex: Int,
    onPick: (Int) -> Unit,
) {
    var expanded by remember { mutableStateOf(false) }
    Column(verticalArrangement = Arrangement.spacedBy(2.dp)) {
        Text(text = label, color = FG_DIM, fontSize = 12.sp)
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
                    options.forEachIndexed { idx, lbl ->
                        DropdownMenuItem(
                            text = {
                                Text(
                                    text = lbl,
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
private fun NumericField(
    label: String,
    value: String,
    onValueChange: (String) -> Unit,
) {
    Column(verticalArrangement = Arrangement.spacedBy(2.dp)) {
        Text(text = label, color = FG_DIM, fontSize = 12.sp)
        OutlinedTextField(
            value = value,
            onValueChange = { input ->
                
                
                onValueChange(input.filter { it.isDigit() })
            },
            singleLine = true,
            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number),
            colors = OutlinedTextFieldDefaults.colors(
                focusedTextColor = FG_PRIMARY,
                unfocusedTextColor = FG_PRIMARY,
                cursorColor = FG_LINK,
                focusedBorderColor = FG_LINK,
                unfocusedBorderColor = DIVIDER,
            ),
            modifier = Modifier.fillMaxWidth(),
        )
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
            text = "Vault session unavailable — re-unlock the app to edit Lock settings.",
            color = FG_DIM,
            fontSize = 13.sp,
        )
    }
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

private val BG_BASE = Color(0xFF1D2021)
private val BG_BAR = Color(0xFF282828)
private val DIVIDER = Color(0xFF3C3836)
private val FG_PRIMARY = Color(0xFFEBDBB2)
private val FG_DIM = Color(0xFF7C6F64)
private val FG_LINK = Color(0xFF83A598)
private val BTN_PRIMARY = Color(0xFF5FCC1A)
private val BTN_DIM = Color(0xFF504945)
private val C_DANGER = Color(0xFFCC241D)
