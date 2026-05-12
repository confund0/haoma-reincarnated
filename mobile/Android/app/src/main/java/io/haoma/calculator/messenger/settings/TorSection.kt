package io.haoma.calculator.messenger.settings

import androidx.compose.foundation.background
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
import androidx.compose.material3.HorizontalDivider
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
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.haoma.calculator.messenger.*
import io.haoma.calculator.messenger.MessengerStore
import kotlinx.coroutines.launch


@Composable
internal fun TorSection(store: MessengerStore, onBack: () -> Unit) {
    val initial = remember { store.loadTorSettings() }
    val coroutineScope = rememberCoroutineScope()

    var dialogOpen by remember { mutableStateOf(false) }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .background(BG_BASE)
            .verticalScroll(rememberScrollState()),
    ) {
        SectionHeader(title = "Tor", store = store, onBack = onBack)

        if (initial == null) {
            VaultUnavailableBanner()
            return@Column
        }

        Section(label = "Tor authentication") {
            EmbeddedStatusRow(hasPassword = initial.hasPassword)
            Spacer(modifier = Modifier.height(8.dp))
            Button(
                onClick = { dialogOpen = true },
                colors = ButtonDefaults.buttonColors(
                    containerColor = BTN_PRIMARY,
                    contentColor = BG_BASE,
                ),
            ) {
                Text("Change password override…")
            }
        }

        Section(label = "Privacy posture") {
            Text(
                text = "On Android, haomad spawns its own tor child with cookie " +
                    "auth — the password override is only used if you point haomad " +
                    "at an external tor (rare). Stored in the vault, re-sealed on " +
                    "save; live haomad picks changes up immediately, no restart.",
                color = FG_DIM,
                fontSize = 12.sp,
            )
        }

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
                        onBack()
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
            color = C_SUCCESS,
            fontSize = 14.sp,
            fontWeight = FontWeight.SemiBold,
        )
        Text(
            text = "Password override: $overrideLabel.",
            color = FG_DIM,
            fontSize = 12.sp,
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
            surface = BG_BAR,
            onSurface = FG_PRIMARY,
            background = BG_BAR,
            onBackground = FG_PRIMARY,
        ),
    ) {
        AlertDialog(
            onDismissRequest = { if (!saving) onDismiss() },
            title = { Text("Change Tor password", color = FG_PRIMARY) },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    Text(
                        text = "New control-port password (leave blank to clear).",
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
                                    color = FG_DIM,
                                    fontSize = 13.sp,
                                )
                            },
                            colors = textFieldColors(),
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
private fun VaultUnavailableBanner() {
    Box(
        modifier = Modifier
            .fillMaxSize()
            .padding(24.dp),
        contentAlignment = Alignment.Center,
    ) {
        Text(
            text = "Vault session unavailable — re-unlock the app to edit Tor settings.",
            color = FG_DIM,
            fontSize = 13.sp,
        )
    }
}

@Composable
private fun textFieldColors() = OutlinedTextFieldDefaults.colors(
    focusedTextColor = FG_PRIMARY,
    unfocusedTextColor = FG_PRIMARY,
    cursorColor = FG_LINK,
    focusedBorderColor = FG_LINK,
    unfocusedBorderColor = DIVIDER,
)

private val BG_BASE = Color(0xFF1D2021)
private val BG_BAR = Color(0xFF282828)
private val DIVIDER = Color(0xFF3C3836)
private val FG_PRIMARY = Color(0xFFEBDBB2)
private val FG_DIM = Color(0xFF7C6F64)
private val FG_LINK = Color(0xFF83A598)
private val BTN_PRIMARY = Color(0xFF5FCC1A)
private val C_DANGER = Color(0xFFCC241D)
private val C_SUCCESS = Color(0xFF8EC07C)
