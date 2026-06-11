package io.haoma.calculator.messenger.settings

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.haoma.calculator.messenger.CtaButton
import io.haoma.calculator.messenger.HaomaPalette
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.Section
import io.haoma.calculator.messenger.loadTorSettings
import io.haoma.calculator.messenger.saveTorPassword
import kotlinx.coroutines.launch


@Composable
internal fun TorSection(store: MessengerStore, onBack: () -> Unit) {
    var initial by remember { mutableStateOf(store.loadTorSettings()) }
    val coroutineScope = rememberCoroutineScope()
    var dialogOpen by remember { mutableStateOf(false) }

    val snapshot = initial
    if (snapshot == null) {
        Column(modifier = Modifier.fillMaxSize().background(HaomaPalette.BG_BASE)) {
            SectionHeader(title = "Tor", store = store, onBack = onBack)
            VaultUnavailableBanner(message = "Vault session unavailable — re-unlock the app to edit Tor settings.")
        }
        return
    }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .background(HaomaPalette.BG_BASE)
            .verticalScroll(rememberScrollState()),
    ) {
        SectionHeader(title = "Tor", store = store, onBack = onBack)

        Section(label = "Tor authentication") {
            EmbeddedStatusRow(hasPassword = snapshot.hasPassword)
            Spacer(modifier = Modifier.height(12.dp))
            CtaButton(
                label = "Change password override…",
                accent = HaomaPalette.FG_LINK,
            ) {
                dialogOpen = true
            }
        }

        Section(
            label = "Privacy posture",
            description = "On Android, haomad spawns its own tor child with cookie auth — the password override is only used if you point haomad at an external tor (rare). Stored in the vault, re-sealed on save; live haomad picks changes up immediately, no restart.",
        ) {}

        Spacer(modifier = Modifier.height(24.dp))
    }

    if (dialogOpen) {
        TorPasswordDialog(
            onDismiss = { dialogOpen = false },
            onSave = { pwd, onResult ->
                coroutineScope.launch {
                    val result = store.saveTorPassword(pwd)
                    onResult(result)
                    if (result.isSuccess) {
                        dialogOpen = false
                        initial = store.loadTorSettings()
                    }
                }
            },
        )
    }
}

@Composable
private fun EmbeddedStatusRow(hasPassword: Boolean) {
    val overrideLabel = if (hasPassword) "configured" else "not set"
    Column(verticalArrangement = Arrangement.spacedBy(2.dp)) {
        Text(
            text = "Embedded tor — cookie auth",
            color = HaomaPalette.C_OK,
            fontSize = 14.sp,
            fontWeight = FontWeight.SemiBold,
        )
        Text(
            text = "Password override: $overrideLabel.",
            color = HaomaPalette.FG_SECONDARY,
            fontSize = 13.sp,
        )
    }
}

@Composable
private fun TorPasswordDialog(
    onDismiss: () -> Unit,
    onSave: (String, (Result<Unit>) -> Unit) -> Unit,
) {
    var draft by remember { mutableStateOf("") }
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
            title = { Text("Change Tor password", color = HaomaPalette.FG_PRIMARY) },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    Text(
                        text = "New control-port password (leave blank to clear).",
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
                            value = draft,
                            onValueChange = { draft = it; if (error != null) error = null },
                            singleLine = true,
                            visualTransformation = PasswordVisualTransformation(),
                            keyboardOptions = KeyboardOptions(
                                keyboardType = KeyboardType.Password,
                                imeAction = ImeAction.Done,
                            ),
                            placeholder = {
                                Text(
                                    text = "(blank to clear)",
                                    color = HaomaPalette.FG_DIM,
                                    fontSize = 13.sp,
                                )
                            },
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
                        val pwd = draft
                        saving = true
                        error = null
                        onSave(pwd) { result ->
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
internal fun dialogFieldColors() = OutlinedTextFieldDefaults.colors(
    focusedTextColor = HaomaPalette.FG_PRIMARY,
    unfocusedTextColor = HaomaPalette.FG_PRIMARY,
    cursorColor = HaomaPalette.FG_LINK,
    focusedBorderColor = HaomaPalette.FG_LINK,
    unfocusedBorderColor = HaomaPalette.DIVIDER,
)
