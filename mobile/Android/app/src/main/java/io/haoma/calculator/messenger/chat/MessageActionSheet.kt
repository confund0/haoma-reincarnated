package io.haoma.calculator.messenger.chat

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.Text
import androidx.compose.material3.rememberModalBottomSheetState
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.haoma.calculator.messenger.EventKind
import io.haoma.calculator.messenger.TimelineEvent


@OptIn(ExperimentalMaterial3Api::class)
@Composable
internal fun MessageActionSheet(
    target: TimelineEvent,
    onDismiss: () -> Unit,
    onReact: () -> Unit,
    onReply: () -> Unit,
    onEdit: () -> Unit,
    onDelete: () -> Unit,
    onCopy: () -> Unit,
    onInfo: () -> Unit,
    onViewAttachment: () -> Unit = {},
    onSaveImage: () -> Unit = {},
    onCopyImage: () -> Unit = {},
    onSaveVideo: () -> Unit = {},
    onViewMedia: () -> Unit = {},
    onShareAttachment: () -> Unit = {},
) {
    val sheetState = rememberModalBottomSheetState()
    val imageRow = target.isReadyImage()
    val videoRow = target.isReadyVideo()
    ModalBottomSheet(
        onDismissRequest = onDismiss,
        sheetState = sheetState,
        containerColor = ChatPalette.Surface,
    ) {
        Column(modifier = Modifier.padding(bottom = 16.dp)) {
            when {
                imageRow -> {
                    
                    
                    ActionItem(glyph = "😀", label = "React", enabled = imageRow, onClick = onReact)
                    ActionItem(glyph = "▷", label = "View", enabled = imageRow, onClick = onViewMedia)
                    ActionItem(
                        glyph = "⤓",
                        label = "Save as",
                        enabled = imageRow && !target.isOutbound,
                        onClick = onSaveImage,
                    )
                    ActionItem(glyph = "↗", label = "Share", enabled = imageRow, onClick = onShareAttachment)
                    ActionItem(
                        glyph = "✕",
                        label = "Delete",
                        enabled = canDelete(target),
                        onClick = onDelete,
                        destructive = true,
                    )
                    HorizontalDivider(color = ChatPalette.TextFaint, thickness = 0.5.dp)
                    ActionItem(
                        glyph = "⧉",
                        label = "Copy image",
                        enabled = imageRow,
                        onClick = onCopyImage,
                    )
                    ActionItem(glyph = "≡", label = "Info", enabled = true, onClick = onInfo)
                }
                videoRow -> {
                    
                    
                    ActionItem(glyph = "😀", label = "React", enabled = videoRow, onClick = onReact)
                    ActionItem(glyph = "▷", label = "View", enabled = videoRow, onClick = onViewMedia)
                    ActionItem(
                        glyph = "⤓",
                        label = "Save as",
                        enabled = videoRow && !target.isOutbound,
                        onClick = onSaveVideo,
                    )
                    ActionItem(glyph = "↗", label = "Share", enabled = videoRow, onClick = onShareAttachment)
                    ActionItem(
                        glyph = "✕",
                        label = "Delete",
                        enabled = canDelete(target),
                        onClick = onDelete,
                        destructive = true,
                    )
                    HorizontalDivider(color = ChatPalette.TextFaint, thickness = 0.5.dp)
                    ActionItem(glyph = "≡", label = "Info", enabled = true, onClick = onInfo)
                }
                target.kind == EventKind.FILE -> {
                    
                    
                    ActionItem(glyph = "😀", label = "React", enabled = canReact(target), onClick = onReact)
                    ActionItem(
                        glyph = "▷",
                        label = "View attachment",
                        enabled = !target.isTombstoned,
                        onClick = onViewAttachment,
                    )
                    ActionItem(
                        glyph = "↗",
                        label = "Share",
                        enabled = !target.isTombstoned,
                        onClick = onShareAttachment,
                    )
                    ActionItem(
                        glyph = "✕",
                        label = "Delete",
                        enabled = canDelete(target),
                        onClick = onDelete,
                        destructive = true,
                    )
                    HorizontalDivider(color = ChatPalette.TextFaint, thickness = 0.5.dp)
                    ActionItem(glyph = "≡", label = "Info", enabled = true, onClick = onInfo)
                }
                else -> {
                    ActionItem(glyph = "😀", label = "React", enabled = canReact(target), onClick = onReact)
                    ActionItem(glyph = "↩", label = "Reply", enabled = canReply(target), onClick = onReply)
                    ActionItem(glyph = "✎", label = "Edit", enabled = canEdit(target), onClick = onEdit)
                    ActionItem(
                        glyph = "✕",
                        label = "Delete",
                        enabled = canDelete(target),
                        onClick = onDelete,
                        destructive = true,
                    )
                    HorizontalDivider(color = ChatPalette.TextFaint, thickness = 0.5.dp)
                    ActionItem(glyph = "⧉", label = "Copy text", enabled = canCopy(target), onClick = onCopy)
                    ActionItem(glyph = "≡", label = "Info", enabled = true, onClick = onInfo)
                }
            }
        }
    }
}


private const val MUTATION_WINDOW_SEC = 86_400L

private fun nowSec(): Long = System.currentTimeMillis() / 1000L

private fun canReact(t: TimelineEvent): Boolean =
    (t.kind == EventKind.TEXT || t.kind == EventKind.FILE) && !t.isTombstoned


private fun canReply(t: TimelineEvent): Boolean =
    t.kind == EventKind.TEXT && !t.isTombstoned

private fun canEdit(t: TimelineEvent): Boolean {
    if (t.kind != EventKind.TEXT) return false
    if (!t.isOutbound) return false
    if (t.isTombstoned) return false
    return nowSec() - t.senderTs < MUTATION_WINDOW_SEC
}

private fun canDelete(t: TimelineEvent): Boolean {
    if (t.kind != EventKind.TEXT && t.kind != EventKind.FILE) return false
    if (!t.isOutbound) return false
    if (t.isTombstoned) return false
    return nowSec() - t.senderTs < MUTATION_WINDOW_SEC
}

private fun canCopy(t: TimelineEvent): Boolean =
    t.kind == EventKind.TEXT && !t.isTombstoned && t.bodyTextOrEmpty().isNotEmpty()

@Composable
private fun ActionItem(
    label: String,
    enabled: Boolean,
    onClick: () -> Unit,
    destructive: Boolean = false,
    glyph: String = "",
) {
    val color = when {
        !enabled -> ChatPalette.TextDim
        destructive -> ChatPalette.Bad
        else -> ChatPalette.Text
    }
    
    
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .background(ChatPalette.Surface)
            .clickable(enabled = enabled, onClick = onClick)
            .padding(horizontal = 24.dp, vertical = 14.dp),
        horizontalArrangement = Arrangement.spacedBy(14.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(
            text = glyph,
            color = color,
            fontSize = 18.sp,
            textAlign = TextAlign.Center,
            modifier = Modifier.width(22.dp),
        )
        Text(
            text = label,
            color = color,
            fontSize = 16.sp,
        )
    }
}
