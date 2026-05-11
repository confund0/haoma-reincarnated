package io.haoma.calculator.messenger.chat

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.Text
import androidx.compose.material3.rememberModalBottomSheetState
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.haoma.calculator.messenger.ChatEntry
import io.haoma.calculator.messenger.DeliveryState
import io.haoma.calculator.messenger.Reaction
import io.haoma.calculator.messenger.TimelineEvent
import io.haoma.calculator.HaomaApp
import androidx.compose.ui.platform.LocalContext
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.compose.runtime.getValue
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale


@OptIn(ExperimentalMaterial3Api::class)
@Composable
internal fun MessageInfoSheet(
    target: TimelineEvent,
    chat: ChatEntry?,
    reactions: Map<String, Reaction>,
    onDismiss: () -> Unit,
) {
    val sheetState = rememberModalBottomSheetState()
    val peerLabel = chat?.label?.ifEmpty { null } ?: chat?.peerId.orEmpty().take(12)
    val context = LocalContext.current
    val app = context.applicationContext as HaomaApp
    val dimsMap by app.messengerStore.imageDimsByMsgId.collectAsStateWithLifecycle()
    val dims = dimsMap[target.msgId]
    ModalBottomSheet(
        onDismissRequest = onDismiss,
        sheetState = sheetState,
        containerColor = ChatPalette.Surface,
    ) {
        Column(
            modifier = Modifier
                .fillMaxWidth()
                .padding(horizontal = 24.dp, vertical = 8.dp)
                .padding(bottom = 24.dp),
            verticalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            SheetTitle(text = if (target.isOutbound) "Sent by you" else "Received from $peerLabel")
            FieldRow("Message ID", target.msgId.ifEmpty { "—" }, mono = true)
            FieldRow("Sender clock", formatLong(target.senderTs))
            if (target.isInbound) {
                FieldRow("Received here", formatLong(target.recvTs))
            }
            if (target.isOutbound) {
                FieldRow("Delivery state", target.deliveryState.ifEmpty { DeliveryState.ENQUEUED })
            }
            if (target.readAt > 0L) {
                val label = if (target.isInbound) "Marked read" else "Peer read"
                FieldRow(label, formatLong(target.readAt))
            }
            if (target.editedAt > 0L) {
                FieldRow("Edited", formatLong(target.editedAt))
            }
            if (target.deletedAt > 0L) {
                FieldRow("Deleted", formatLong(target.deletedAt))
            }
            if (dims != null) {
                FieldRow("Dimensions", "${dims.first} × ${dims.second}")
            }
            HorizontalDivider(color = ChatPalette.TextFaint, thickness = 0.5.dp)
            ReactionsBlock(reactions = reactions, peerLabel = peerLabel)
        }
    }
}

@Composable
private fun SheetTitle(text: String) {
    Text(
        text = text,
        color = ChatPalette.Text,
        fontSize = 14.sp,
        fontWeight = FontWeight.SemiBold,
    )
}

@Composable
private fun FieldRow(label: String, value: String, mono: Boolean = false) {
    Row(
        modifier = Modifier.fillMaxWidth(),
        horizontalArrangement = Arrangement.spacedBy(12.dp),
        verticalAlignment = Alignment.Top,
    ) {
        Text(
            text = label,
            color = ChatPalette.TextDim,
            fontSize = 12.sp,
            modifier = Modifier.padding(top = 1.dp),
        )
        Text(
            text = value,
            color = ChatPalette.Text,
            fontSize = 12.sp,
            fontFamily = if (mono) FontFamily.Monospace else FontFamily.Default,
            modifier = Modifier.weight(1f),
        )
    }
}

@Composable
private fun ReactionsBlock(reactions: Map<String, Reaction>, peerLabel: String) {
    if (reactions.isEmpty()) {
        Text(
            text = "No reactions",
            color = ChatPalette.TextDim,
            fontSize = 12.sp,
        )
        return
    }
    Column(verticalArrangement = Arrangement.spacedBy(2.dp)) {
        Text(
            text = "Reactions",
            color = ChatPalette.TextDim,
            fontSize = 12.sp,
        )
        for ((peerId, r) in reactions.entries.sortedBy { it.key }) {
            val who = if (peerId.isEmpty()) "you" else peerLabel.ifEmpty { peerId.take(12) }
            Text(
                text = "  $who: ${r.emoji}",
                color = ChatPalette.Text,
                fontSize = 13.sp,
            )
        }
    }
}

private val LONG_FMT = ThreadLocal.withInitial {
    SimpleDateFormat("yyyy-MM-dd HH:mm:ss", Locale.US)
}

private fun formatLong(unixSeconds: Long): String =
    if (unixSeconds <= 0L) "—" else LONG_FMT.get()!!.format(Date(unixSeconds * 1000L))
