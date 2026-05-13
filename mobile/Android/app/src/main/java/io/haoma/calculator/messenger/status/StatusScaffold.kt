package io.haoma.calculator.messenger.status

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontFamily
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
                StatusSection.Logs -> LogsSection()
            }
        }
    }
}

@Composable
private fun SectionTabRow(selected: StatusSection, onSelect: (StatusSection) -> Unit) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = 12.dp, vertical = 8.dp),
        horizontalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        StatusSection.values().forEach { section ->
            SectionButton(
                section = section,
                isSelected = section == selected,
                onClick = { onSelect(section) },
            )
        }
    }
}

@Composable
private fun SectionButton(section: StatusSection, isSelected: Boolean, onClick: () -> Unit) {
    val bg = if (isSelected) BG_BAR_SELECTED else BG_BAR
    val fg = if (isSelected) FG_BAR_SELECTED else FG_BAR
    val border = if (isSelected) BORDER_SEL else BORDER_DIM
    Box(
        modifier = Modifier
            .background(color = bg, shape = RoundedCornerShape(percent = 50))
            .border(width = 1.dp, color = border, shape = RoundedCornerShape(percent = 50))
            .clickable(onClick = onClick)
            .padding(horizontal = 16.dp, vertical = 6.dp),
        contentAlignment = Alignment.Center,
    ) {
        Text(
            text = "[${section.label}]",
            color = fg,
            fontFamily = FontFamily.Monospace,
            fontWeight = if (isSelected) FontWeight.SemiBold else FontWeight.Normal,
            fontSize = 13.sp,
        )
    }
}

private val BG_BASE = Color(0xFF1D2021)
private val BG_BAR = Color(0xFF282828)
private val BG_BAR_SELECTED = Color(0xFF3C3836)
private val FG_BAR = Color(0xFF928374)
private val FG_BAR_SELECTED = Color(0xFFEBDBB2)
private val BORDER_DIM = Color(0xFF3C3836)
private val BORDER_SEL = Color(0xFF83A598)
