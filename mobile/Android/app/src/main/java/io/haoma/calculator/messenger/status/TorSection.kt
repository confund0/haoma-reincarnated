package io.haoma.calculator.messenger.status

import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.background
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.TorHealth
import io.haoma.calculator.messenger.TorSlot


@OptIn(ExperimentalFoundationApi::class)
@Composable
fun TorSection(store: MessengerStore) {
    val snapshot by store.torInfoSnapshot.collectAsStateWithLifecycle()
    val clipboard = LocalClipboardManager.current

    LaunchedEffect(Unit) { store.refreshTorInfo() }

    val health = snapshot?.health
    val slots = snapshot?.slots.orEmpty()

    Column(
        modifier = Modifier
            .fillMaxSize()
            .background(BG_BASE)
            .padding(horizontal = 12.dp, vertical = 8.dp),
        verticalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        HealthLine(health = health)
        Row(
            modifier = Modifier.fillMaxWidth(),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.SpaceBetween,
        ) {
            Text(
                text = "Onion slots (${slots.size})",
                color = FG_LOG,
                fontFamily = FontFamily.Monospace,
                fontWeight = FontWeight.SemiBold,
                fontSize = 13.sp,
            )
            TextButton(onClick = { store.refreshTorInfo() }) {
                Text(
                    text = "Refresh",
                    color = ACCENT,
                    fontFamily = FontFamily.Monospace,
                    fontSize = 13.sp,
                )
            }
        }
        Box(modifier = Modifier.weight(1f).fillMaxWidth()) {
            when {
                snapshot == null -> EmptyLine(
                    text = "(fetching tor info…)",
                )
                slots.isEmpty() -> EmptyLine(
                    text = "No onions published yet — pair a peer to mint one.",
                )
                else -> SlotTable(
                    slots = slots,
                    onLongPress = { url ->
                        clipboard.setText(AnnotatedString(url))
                    },
                )
            }
        }
    }
}

@Composable
private fun HealthLine(health: TorHealth?) {
    val (text, color) = when {
        health == null -> "tor: …" to FG_DIM
        health.unreachable -> "tor: unreachable" to C_BAD
        !health.ready -> "tor: bootstrapping ${health.bootstrap}%" to C_WARN
        else -> "tor: ready (100%)" to C_OK
    }
    Text(
        text = text,
        color = color,
        fontFamily = FontFamily.Monospace,
        fontWeight = FontWeight.SemiBold,
        fontSize = 14.sp,
    )
}

@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun SlotTable(slots: List<TorSlot>, onLongPress: (String) -> Unit) {
    LazyColumn(
        modifier = Modifier.fillMaxSize(),
        contentPadding = PaddingValues(vertical = 4.dp),
    ) {
        items(slots, key = { it.slot.toString() + ":" + it.serviceId }) { slot ->
            SlotRow(slot = slot, onLongPress = onLongPress)
            HorizontalDivider(color = DIVIDER, thickness = 0.5.dp)
        }
    }
}

@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun SlotRow(slot: TorSlot, onLongPress: (String) -> Unit) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .combinedClickable(
                onClick = {},
                onLongClick = { onLongPress(slot.url) },
            )
            .padding(vertical = 8.dp, horizontal = 4.dp),
        verticalAlignment = Alignment.Top,
    ) {
        Text(
            text = "#${slot.slot}",
            color = FG_DIM,
            fontFamily = FontFamily.Monospace,
            fontSize = 12.sp,
            modifier = Modifier.padding(end = 12.dp),
        )
        Column(modifier = Modifier.weight(1f)) {
            Text(
                text = slot.serviceId,
                color = FG_LOG,
                fontFamily = FontFamily.Monospace,
                fontSize = 12.sp,
                fontWeight = FontWeight.SemiBold,
            )
            Text(
                text = slot.url,
                color = FG_DIM,
                fontFamily = FontFamily.Monospace,
                fontSize = 11.sp,
            )
        }
    }
}

@Composable
private fun EmptyLine(text: String) {
    Box(modifier = Modifier.fillMaxSize(), contentAlignment = Alignment.TopStart) {
        Text(
            text = text,
            color = FG_DIM,
            fontFamily = FontFamily.Monospace,
            fontSize = 13.sp,
        )
    }
}

private val BG_BASE = Color(0xFF1D2021)
private val DIVIDER = Color(0xFF3C3836)
private val FG_LOG = Color(0xFFD5C4A1)
private val FG_DIM = Color(0xFF7C6F64)
private val ACCENT = Color(0xFF83A598)
private val C_OK = Color(0xFF5FCC1A)
private val C_WARN = Color(0xFFFABD2F)
private val C_BAD = Color(0xFFCC241D)
