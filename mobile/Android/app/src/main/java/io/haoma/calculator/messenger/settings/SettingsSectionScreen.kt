package io.haoma.calculator.messenger.settings

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.haoma.calculator.messenger.MessengerStore


@Composable
fun SettingsSectionScreen(
    store: MessengerStore,
    domain: String,
    onBack: () -> Unit,
) {
    when (domain) {
        SettingsDomains.Profile -> ProfileSection(store, onBack)
        SettingsDomains.Defaults -> ChatDefaultsSection(store, onBack)
        SettingsDomains.Files -> FilesSection(onBack)
        SettingsDomains.Notifications -> NotificationsSection(store, onBack)
        SettingsDomains.Tor -> TorSection(store, onBack)
        SettingsDomains.Lock -> LockSection(store, onBack)
        SettingsDomains.Advanced -> AdvancedSection(store, onBack)
        else -> PendingSection(domain = domain, onBack = onBack)
    }
}

@Composable
private fun PendingSection(domain: String, onBack: () -> Unit) {
    val title = SettingsDomains.Labels[domain] ?: domain
    Column(modifier = Modifier.fillMaxSize().background(BG_BASE_LOCAL)) {
        SectionHeader(title = title, onBack = onBack)
        Box(
            modifier = Modifier.fillMaxSize().padding(24.dp),
            contentAlignment = Alignment.Center,
        ) {
            Text(
                text = "Lands in a follow-up M-8g slice.",
                color = FG_DIM_LOCAL,
                fontSize = 14.sp,
            )
        }
    }
}


@Composable
internal fun SectionHeader(title: String, onBack: () -> Unit) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .background(BG_BAR_LOCAL)
            .padding(horizontal = 12.dp, vertical = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(
            text = "‹",
            color = FG_LINK_LOCAL,
            fontSize = 22.sp,
            fontWeight = FontWeight.Bold,
            modifier = Modifier
                .clickable(onClick = onBack)
                .padding(horizontal = 8.dp, vertical = 4.dp),
        )
        Spacer(modifier = Modifier.width(20.dp))
        Text(
            text = title,
            color = FG_PRIMARY_LOCAL,
            fontWeight = FontWeight.SemiBold,
            fontSize = 17.sp,
            modifier = Modifier.weight(1f),
        )
    }
}

private val BG_BASE_LOCAL = Color(0xFF1D2021)
private val BG_BAR_LOCAL = Color(0xFF282828)
private val FG_PRIMARY_LOCAL = Color(0xFFEBDBB2)
private val FG_DIM_LOCAL = Color(0xFF7C6F64)
private val FG_LINK_LOCAL = Color(0xFF83A598)
