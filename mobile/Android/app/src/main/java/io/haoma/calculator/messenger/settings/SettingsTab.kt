package io.haoma.calculator.messenger.settings

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.haoma.calculator.messenger.HaomaPalette
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.openSettingsSection


@Composable
fun SettingsTab(store: MessengerStore) {
    Column(modifier = Modifier.fillMaxSize().background(HaomaPalette.BG_BASE)) {
        TabHeader(title = "Settings", store = store)
        LazyColumn(modifier = Modifier.fillMaxSize()) {
            items(SettingsDomains.Order, key = { it }) { domain ->
                SettingsRow(
                    label = SettingsDomains.Labels[domain] ?: domain,
                    hint = SettingsDomains.Hints[domain].orEmpty(),
                    onClick = { store.openSettingsSection(domain) },
                )
                HorizontalDivider(color = HaomaPalette.DIVIDER, thickness = 0.5.dp)
            }
        }
    }
}

@Composable
private fun TabHeader(title: String, store: MessengerStore) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .background(HaomaPalette.BG_BAR)
            .padding(horizontal = 16.dp, vertical = 10.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
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

@Composable
private fun SettingsRow(label: String, hint: String, onClick: () -> Unit) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .clickable(onClick = onClick)
            .padding(horizontal = 16.dp, vertical = 14.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Column(modifier = Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(2.dp)) {
            Text(
                text = label,
                color = HaomaPalette.FG_PRIMARY,
                fontSize = 16.sp,
                fontWeight = FontWeight.SemiBold,
            )
            if (hint.isNotEmpty()) {
                Text(
                    text = hint,
                    color = HaomaPalette.FG_SECONDARY,
                    fontSize = 13.sp,
                )
            }
        }
        Spacer(modifier = Modifier.width(12.dp))
        Text(
            text = "›",
            color = HaomaPalette.FG_LINK,
            fontSize = 22.sp,
            fontWeight = FontWeight.Bold,
        )
    }
}
