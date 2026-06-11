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
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.haoma.calculator.messenger.HaomaPalette
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
        SettingsDomains.Files -> FilesSection(store, onBack)
        SettingsDomains.Notifications -> NotificationsSection(store, onBack)
        SettingsDomains.Tor -> TorSection(store, onBack)
        SettingsDomains.Lock -> LockSection(store, onBack)
        SettingsDomains.Advanced -> AdvancedSection(store, onBack)
        else -> PendingSection(store = store, domain = domain, onBack = onBack)
    }
}

@Composable
private fun PendingSection(store: MessengerStore, domain: String, onBack: () -> Unit) {
    val title = SettingsDomains.Labels[domain] ?: domain
    Column(modifier = Modifier.fillMaxSize().background(HaomaPalette.BG_BASE)) {
        SectionHeader(title = title, store = store, onBack = onBack)
        Box(
            modifier = Modifier.fillMaxSize().padding(24.dp),
            contentAlignment = Alignment.Center,
        ) {
            Text(
                text = "Lands in a follow-up M-8g slice.",
                color = HaomaPalette.FG_DIM,
                fontSize = 14.sp,
            )
        }
    }
}


@Composable
internal fun SectionHeader(title: String, store: MessengerStore, onBack: () -> Unit) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .background(HaomaPalette.BG_BAR)
            .padding(horizontal = 12.dp, vertical = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(
            text = "‹",
            color = HaomaPalette.FG_LINK,
            fontSize = 22.sp,
            fontWeight = FontWeight.Bold,
            modifier = Modifier
                .clickable(onClick = onBack)
                .padding(horizontal = 8.dp, vertical = 4.dp),
        )
        Spacer(modifier = Modifier.width(20.dp))
        Text(
            text = title,
            color = HaomaPalette.FG_PRIMARY,
            fontWeight = FontWeight.SemiBold,
            fontSize = 17.sp,
            modifier = Modifier.weight(1f),
        )
        io.haoma.calculator.messenger.calls.CallChip(store = store)
    }
}
