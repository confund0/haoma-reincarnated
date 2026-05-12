package io.haoma.calculator.messenger.settings

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.haoma.calculator.messenger.*
import io.haoma.calculator.messenger.MessengerStore


@Composable
internal fun AdvancedSection(store: MessengerStore, onBack: () -> Unit) {
    val warnings = remember { store.loadSecurityWarnings() }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .background(BG_BASE_AS)
            .verticalScroll(rememberScrollState()),
    ) {
        SectionHeader(title = "Advanced", store = store, onBack = onBack)

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

private val BG_BASE_AS = Color(0xFF1D2021)
private val DIVIDER_AS = Color(0xFF3C3836)
private val FG_PRIMARY_AS = Color(0xFFEBDBB2)
private val FG_DIM_AS = Color(0xFF7C6F64)
