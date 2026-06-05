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
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.haoma.calculator.messenger.*
import io.haoma.calculator.messenger.AdvancedSettings
import io.haoma.calculator.messenger.MessengerStore
import kotlinx.coroutines.launch


@Composable
internal fun AdvancedSection(store: MessengerStore, onBack: () -> Unit) {
    val initial = remember { store.loadAdvancedSettings() }
    val warnings = remember { store.loadSecurityWarnings() }
    val coroutineScope = rememberCoroutineScope()

    Column(
        modifier = Modifier
            .fillMaxSize()
            .background(BG_BASE_AS)
            .verticalScroll(rememberScrollState()),
    ) {
        SectionHeader(title = "Advanced", store = store, onBack = onBack)

        if (initial == null) {
            VaultUnavailableBanner()
            return@Column
        }

        var urlForceChooser by remember { mutableStateOf(initial.urlForceChooser) }
        var saving by remember { mutableStateOf(false) }
        var error by remember { mutableStateOf<String?>(null) }

        val current by remember(urlForceChooser) {
            derivedStateOf { AdvancedSettings(urlForceChooser = urlForceChooser) }
        }
        val dirty by remember(current, initial) {
            derivedStateOf { current != initial }
        }

        InfoSection(label = "Links") {
            ToggleRow(
                label = "Always choose how to open links",
                hint = "Show app picker instead of using your default browser.",
                checked = urlForceChooser,
                onCheckedChange = { urlForceChooser = it; if (error != null) error = null },
            )
        }

        Spacer(modifier = Modifier.height(4.dp))

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
                        val result = store.saveAdvancedSettings(snapshot)
                        saving = false
                        result.onSuccess { onBack() }
                        result.onFailure { t -> error = t.message ?: "save failed" }
                    }
                },
                colors = ButtonDefaults.buttonColors(
                    containerColor = BTN_PRIMARY_AS,
                    contentColor = BG_BASE_AS,
                    disabledContainerColor = BTN_DIM_AS,
                    disabledContentColor = FG_DIM_AS,
                ),
            ) {
                Text(if (saving) "Saving…" else "Save")
            }
            Spacer(modifier = Modifier.width(12.dp))
            TextButton(
                enabled = dirty && !saving,
                onClick = {
                    urlForceChooser = initial.urlForceChooser
                    error = null
                },
            ) {
                Text("Reset", color = if (dirty && !saving) FG_LINK_AS else FG_DIM_AS)
            }
            if (saving) {
                Spacer(modifier = Modifier.width(12.dp))
                CircularProgressIndicator(
                    color = BTN_PRIMARY_AS,
                    strokeWidth = 2.dp,
                    modifier = Modifier
                        .height(20.dp)
                        .width(20.dp),
                )
                Spacer(modifier = Modifier.width(8.dp))
                Text(
                    text = "re-sealing vault (1–3s)…",
                    color = FG_DIM_AS,
                    fontSize = 12.sp,
                )
            }
        }

        error?.let { message ->
            Spacer(modifier = Modifier.height(10.dp))
            Text(
                text = message,
                color = C_DANGER_AS,
                fontSize = 13.sp,
                modifier = Modifier.padding(horizontal = 16.dp),
            )
        }

        Spacer(modifier = Modifier.height(12.dp))

        InfoSection(label = "Security warnings") {
            when {
                warnings == null -> Text(
                    text = "Vault session unavailable — re-unlock the app to see security warnings.",
                    color = FG_DIM_AS,
                    fontSize = 13.sp,
                )

                warnings.isEmpty() -> Text(
                    text = "None.",
                    color = FG_DIM_AS,
                    fontSize = 14.sp,
                )

                else -> Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
                    warnings.forEach { w ->
                        Text(
                            text = "• $w",
                            color = FG_PRIMARY_AS,
                            fontSize = 14.sp,
                        )
                    }
                }
            }
        }

        InfoSection(label = "About") {
            Text(
                text = "Warnings are emitted when a tunable (PIN validity, idle " +
                    "timeout, etc.) sits outside its recommended range. " +
                    "The producer that fills this list lands with the " +
                    "Security Health screen — until then it stays empty.",
                color = FG_DIM_AS,
                fontSize = 13.sp,
            )
        }

        Spacer(modifier = Modifier.height(24.dp))
    }
}

@Composable
private fun InfoSection(label: String, content: @Composable () -> Unit) {
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = 16.dp, vertical = 14.dp),
        verticalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        Text(
            text = label.uppercase(),
            color = FG_DIM_AS,
            fontSize = 11.sp,
            fontWeight = FontWeight.SemiBold,
        )
        content()
    }
    HorizontalDivider(color = DIVIDER_AS, thickness = 0.5.dp)
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
            .clickable { onCheckedChange(!checked) },
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Checkbox(
            checked = checked,
            onCheckedChange = onCheckedChange,
            colors = CheckboxDefaults.colors(
                checkedColor = BTN_PRIMARY_AS,
                uncheckedColor = FG_DIM_AS,
                checkmarkColor = BG_BASE_AS,
            ),
        )
        Spacer(modifier = Modifier.width(8.dp))
        Column(modifier = Modifier.weight(1f)) {
            Text(text = label, color = FG_PRIMARY_AS, fontSize = 14.sp)
            if (hint.isNotEmpty()) {
                Text(text = hint, color = FG_DIM_AS, fontSize = 12.sp)
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
            text = "Vault session unavailable — re-unlock the app to edit Advanced settings.",
            color = FG_DIM_AS,
            fontSize = 13.sp,
        )
    }
}

private val BG_BASE_AS = Color(0xFF1D2021)
private val DIVIDER_AS = Color(0xFF3C3836)
private val FG_PRIMARY_AS = Color(0xFFEBDBB2)
private val FG_DIM_AS = Color(0xFF7C6F64)
private val FG_LINK_AS = Color(0xFF83A598)
private val BTN_PRIMARY_AS = Color(0xFF5FCC1A)
private val BTN_DIM_AS = Color(0xFF504945)
private val C_DANGER_AS = Color(0xFFCC241D)
