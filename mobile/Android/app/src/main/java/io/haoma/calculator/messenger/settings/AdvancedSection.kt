package io.haoma.calculator.messenger.settings

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.haoma.calculator.messenger.AdvancedSettings
import io.haoma.calculator.messenger.DirtyBar
import io.haoma.calculator.messenger.HaomaPalette
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.Section
import io.haoma.calculator.messenger.loadAdvancedSettings
import io.haoma.calculator.messenger.loadSecurityWarnings
import io.haoma.calculator.messenger.saveAdvancedSettings
import kotlinx.coroutines.launch


@Composable
internal fun AdvancedSection(store: MessengerStore, onBack: () -> Unit) {
    var initial by remember { mutableStateOf(store.loadAdvancedSettings()) }
    val warnings = remember { store.loadSecurityWarnings() }
    val coroutineScope = rememberCoroutineScope()
    val scrollState = rememberScrollState()

    val snapshot = initial
    if (snapshot == null) {
        Column(modifier = Modifier.fillMaxSize().background(HaomaPalette.BG_BASE)) {
            SectionHeader(title = "Advanced", store = store, onBack = onBack)
            VaultUnavailableBanner(message = "Vault session unavailable — re-unlock the app to edit Advanced settings.")
        }
        return
    }

    var urlForceChooser by remember(snapshot) { mutableStateOf(snapshot.urlForceChooser) }
    var saving by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }

    val current by remember(urlForceChooser) {
        derivedStateOf { AdvancedSettings(urlForceChooser = urlForceChooser) }
    }
    val dirty by remember(current, snapshot) { derivedStateOf { current != snapshot } }

    Column(modifier = Modifier.fillMaxSize().background(HaomaPalette.BG_BASE)) {
        SectionHeader(title = "Advanced", store = store, onBack = onBack)
        DirtyBar(
            visible = dirty && !saving,
            onTap = { coroutineScope.launch { scrollState.animateScrollTo(scrollState.maxValue) } },
        )

        Column(modifier = Modifier.weight(1f).verticalScroll(scrollState)) {
            Section(label = "Links") {
                ToggleRow(
                    label = "Always choose how to open links",
                    hint = "Show app picker instead of using your default browser.",
                    checked = urlForceChooser,
                    onCheckedChange = { urlForceChooser = it; if (error != null) error = null },
                )
            }

            Spacer(modifier = Modifier.height(4.dp))

            SaveResetRow(
                dirty = dirty,
                saving = saving,
                onSave = {
                    val saveSnapshot = current
                    saving = true
                    error = null
                    coroutineScope.launch {
                        val result = store.saveAdvancedSettings(saveSnapshot)
                        saving = false
                        result.onSuccess { initial = store.loadAdvancedSettings() }
                        result.onFailure { t -> error = t.message ?: "save failed" }
                    }
                },
                onReset = {
                    urlForceChooser = snapshot.urlForceChooser
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

            Spacer(modifier = Modifier.height(12.dp))

            Section(label = "Security warnings") {
                when {
                    warnings == null -> Text(
                        text = "Vault session unavailable — re-unlock the app to see security warnings.",
                        color = HaomaPalette.FG_SECONDARY,
                        fontSize = 13.sp,
                    )

                    warnings.isEmpty() -> Text(
                        text = "None.",
                        color = HaomaPalette.FG_SECONDARY,
                        fontSize = 14.sp,
                    )

                    else -> Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
                        warnings.forEach { w ->
                            Text(
                                text = "• $w",
                                color = HaomaPalette.FG_PRIMARY,
                                fontSize = 14.sp,
                            )
                        }
                    }
                }
            }

            Section(
                label = "About",
                description = "Warnings are emitted when a tunable (PIN validity, idle timeout, etc.) sits outside its recommended range. The producer that fills this list lands with the Security Health screen — until then it stays empty.",
            ) {}

            Spacer(modifier = Modifier.height(24.dp))
        }
    }
}
