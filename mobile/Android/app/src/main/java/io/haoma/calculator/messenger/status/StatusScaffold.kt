package io.haoma.calculator.messenger.status

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Tab
import androidx.compose.material3.TabRow
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.haoma.calculator.messenger.MessengerStore


@Composable
fun StatusScaffold(store: MessengerStore) {
    var selected by rememberSaveable { mutableStateOf(StatusSection.Home) }

    Column(
        modifier = Modifier.fillMaxSize().background(BG_BASE),
    ) {
        SectionTabRow(selected = selected, onSelect = { selected = it })
        Box(modifier = Modifier.fillMaxSize()) {
            when (selected) {
                StatusSection.Home -> HomeSection(store = store)
                StatusSection.Cmd -> CmdSection(store = store)
                StatusSection.Tor -> TorSection(store = store)
            }
        }
    }
}

@Composable
private fun SectionTabRow(selected: StatusSection, onSelect: (StatusSection) -> Unit) {
    TabRow(
        selectedTabIndex = selected.ordinal,
        containerColor = BG_BAR,
        contentColor = FG_BAR_SELECTED,
        divider = { HorizontalDivider(color = BORDER_DIM, thickness = 0.5.dp) },
    ) {
        StatusSection.entries.forEach { section ->
            Tab(
                selected = selected == section,
                onClick = { onSelect(section) },
                selectedContentColor = FG_BAR_SELECTED,
                unselectedContentColor = FG_BAR,
            ) {
                Text(
                    text = section.label,
                    fontSize = 13.sp,
                    fontWeight = FontWeight.SemiBold,
                    modifier = Modifier.padding(vertical = 14.dp),
                )
            }
        }
    }
}

private val BG_BASE = Color(0xFF1D2021)
private val BG_BAR = Color(0xFF282828)
private val FG_BAR = Color(0xFF928374)
private val FG_BAR_SELECTED = Color(0xFFEBDBB2)
private val BORDER_DIM = Color(0xFF3C3836)
