package io.haoma.calculator.messenger.calls

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import io.haoma.calculator.messenger.CallDirection
import io.haoma.calculator.messenger.CallStatus
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.peerLabelFor
import kotlinx.coroutines.delay


@Composable
fun CallChip(store: MessengerStore, modifier: Modifier = Modifier) {
    val active by store.activeCalls.collectAsStateWithLifecycle()
    val target = remember(active) {
        active.values
            .filter { !it.isTerminal }
            .minByOrNull { it.startedAt }
    } ?: return
    val accepted = target.status == CallStatus.Accepted

    
    var now by remember { mutableLongStateOf(System.currentTimeMillis() / 1000L) }
    LaunchedEffect(target.callId) {
        while (true) {
            now = System.currentTimeMillis() / 1000L
            delay(1_000L)
        }
    }
    val duration = (now - target.startedAt).coerceAtLeast(0L)
    val label = store.peerLabelFor(target.peerId)
    val suffix = when {
        accepted -> formatDuration(duration)
        target.direction == CallDirection.In && target.status == CallStatus.Ringing -> "Ringing…"
        else -> "Calling…"
    }

    var pickerOpen by remember { mutableLongStateOf(0L) }
    Box(
        modifier = modifier
            .clip(RoundedCornerShape(percent = 50))
            .background(ChipBg)
            .clickable(enabled = accepted) { pickerOpen = System.currentTimeMillis() }
            .padding(horizontal = 8.dp, vertical = 2.dp),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(
                text = "☎",
                color = ChipFg,
                fontSize = 12.sp,
                fontWeight = FontWeight.Bold,
            )
            Spacer(modifier = Modifier.width(4.dp))
            Text(
                text = "$label · $suffix",
                color = ChipFg,
                fontFamily = FontFamily.Monospace,
                fontSize = 12.sp,
                fontWeight = FontWeight.SemiBold,
            )
        }
    }
    if (pickerOpen != 0L) {
        CallAudioDialog(store = store, onDismiss = { pickerOpen = 0L })
    }
}


@Composable
fun CallChipGlyph(modifier: Modifier = Modifier) {
    Box(
        modifier = modifier
            .clip(RoundedCornerShape(percent = 50))
            .background(ChipBg)
            .padding(horizontal = 6.dp, vertical = 2.dp),
    ) {
        Text(
            text = "☎",
            color = ChipFg,
            fontSize = 12.sp,
            fontWeight = FontWeight.Bold,
        )
    }
}

internal fun formatDuration(seconds: Long): String {
    val m = seconds / 60L
    val s = seconds % 60L
    return "%02d:%02d".format(m, s)
}

internal val ChipBg = Color(0xFFCC241D) 
internal val ChipFg = Color(0xFFFBF1C7) 
