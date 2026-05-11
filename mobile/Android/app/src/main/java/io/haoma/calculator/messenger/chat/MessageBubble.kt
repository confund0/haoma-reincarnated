package io.haoma.calculator.messenger.chat

import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.background
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.hapticfeedback.HapticFeedbackType
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalHapticFeedback
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import coil.compose.AsyncImage
import coil.request.ImageRequest
import io.haoma.calculator.HaomaApp
import io.haoma.calculator.log.Logger
import io.haoma.calculator.messenger.*
import io.haoma.calculator.messenger.EventKind
import io.haoma.calculator.messenger.FileEventBody
import io.haoma.calculator.messenger.FileState
import io.haoma.calculator.messenger.Reaction
import io.haoma.calculator.messenger.TimelineEvent
import io.haoma.calculator.messenger.humanBytes
import java.io.File
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale


@OptIn(ExperimentalFoundationApi::class)
@Composable
internal fun MessageBubble(
    event: TimelineEvent,
    reactions: Map<String, Reaction> = emptyMap(),
    onLongPress: (TimelineEvent) -> Unit = {},
    onTapReaction: (TimelineEvent, String) -> Unit = { _, _ -> },
    onTapImage: (TimelineEvent) -> Unit = {},
    modifier: Modifier = Modifier,
) {
    val outbound = event.isOutbound
    val alignment = if (outbound) Alignment.End else Alignment.Start
    val bubbleColor = when {
        event.decryptStatus == "failed" -> ChatPalette.DecryptFailedBg
        outbound -> ChatPalette.OutboundBubble
        else -> ChatPalette.InboundBubble
    }
    val textColor = if (event.decryptStatus == "failed") ChatPalette.Bad else ChatPalette.Text
    val haptic = LocalHapticFeedback.current
    val interactionSource = remember { MutableInteractionSource() }
    
    
    val tappableImage = event.isReadyImage()

    Column(
        modifier = modifier
            .fillMaxWidth()
            .padding(horizontal = 8.dp, vertical = 2.dp),
        horizontalAlignment = alignment,
    ) {
        Box(
            modifier = Modifier
                .widthIn(max = BubbleMaxWidth)
                .clip(BubbleShape)
                .background(bubbleColor)
                .combinedClickable(
                    interactionSource = interactionSource,
                    indication = null,
                    onClick = if (tappableImage) {
                        { onTapImage(event) }
                    } else {
                        {}
                    },
                    onLongClick = {
                        haptic.performHapticFeedback(HapticFeedbackType.LongPress)
                        onLongPress(event)
                    },
                )
                .padding(BubblePadding),
        ) {
            Column {
                MessageBody(event = event, textColor = textColor)
                MessageFooter(event = event)
            }
        }
        if (reactions.isNotEmpty() && !event.isTombstoned) {
            
            
            ReactionPills(
                reactions = reactions,
                onTap = { emoji -> onTapReaction(event, emoji) },
                modifier = Modifier
                    .widthIn(max = BubbleMaxWidth)
                    .offset(y = ReactionOverlap),
            )
        }
    }
}

@Composable
private fun MessageBody(event: TimelineEvent, textColor: androidx.compose.ui.graphics.Color) {
    when {
        event.isTombstoned -> {
            Text(
                text = if (event.kind == EventKind.FILE) "[file deleted]" else "[message deleted]",
                color = ChatPalette.TextDim,
                fontStyle = FontStyle.Italic,
                fontSize = 14.sp,
            )
        }
        event.decryptStatus == "failed" -> {
            Text(
                text = "[decrypt failed]",
                color = ChatPalette.Bad,
                fontWeight = FontWeight.SemiBold,
                fontSize = 14.sp,
            )
        }
        event.kind == EventKind.FILE -> {
            val body = FileEventBody.fromJson(event.body)
            if (body.isImage && body.state == FileState.READY) {
                ImageBody(event = event, body = body)
            } else {
                FileCaption(body = body, textColor = textColor)
            }
        }
        else -> {
            Text(
                text = event.bodyTextOrEmpty().ifEmpty { "(empty)" },
                color = textColor,
                fontSize = 14.sp,
            )
        }
    }
}


@Composable
private fun ImageBody(event: TimelineEvent, body: FileEventBody) {
    val context = LocalContext.current
    val app = context.applicationContext as HaomaApp
    val store = app.messengerStore
    val pathMap by store.imagePathByMsgId.collectAsStateWithLifecycle()
    val path = pathMap[event.msgId]

    LaunchedEffect(event.msgId) {
        if (path != null) return@LaunchedEffect
        val res = store.openFile(event.chatId, event.msgId) ?: return@LaunchedEffect
        store.recordImagePath(event.msgId, res.path)
    }

    val displayName = body.name.ifEmpty { "(image)" }
    if (path == null) {
        Box(
            modifier = Modifier.size(ImagePlaceholderSize),
            contentAlignment = Alignment.Center,
        ) {
            CircularProgressIndicator(
                color = ChatPalette.Accent,
                strokeWidth = 2.dp,
                modifier = Modifier.size(24.dp),
            )
        }
        return
    }

    val request = remember(path) {
        ImageRequest.Builder(context)
            .data(File(path))
            .crossfade(false)
            .build()
    }
    AsyncImage(
        model = request,
        contentDescription = displayName,
        contentScale = ContentScale.Fit,
        modifier = Modifier
            .widthIn(max = BubbleMaxWidth - BubbleHorizontalPadding * 2)
            .heightIn(max = ImageMaxHeight)
            .clip(ImageCornerShape),
        onSuccess = { state ->
            val size = state.painter.intrinsicSize
            if (size.width > 0f && size.height > 0f) {
                store.recordImageDims(event.msgId, size.width.toInt(), size.height.toInt())
            }
        },
        onError = {
            Logger.w(
                "image-bubble",
                "decode failed msg=${event.msgId} path=$path err=${it.result.throwable.message ?: "?"}",
            )
        },
    )
}


@Composable
private fun FileCaption(body: FileEventBody, textColor: androidx.compose.ui.graphics.Color) {
    val displayName = body.name.ifEmpty { "(unnamed)" }
    val parts = mutableListOf<String>()
    if (body.size > 0L) parts += humanBytes(body.size)
    if (body.mime.isNotEmpty()) parts += body.mime
    val stateLabel = renderStateLabel(body)
    if (stateLabel.isNotEmpty()) parts += stateLabel
    val stateColor = stateColorFor(body.state)
    Column {
        Text(
            text = "📎 $displayName",
            color = textColor,
            fontSize = 14.sp,
            fontWeight = FontWeight.SemiBold,
        )
        if (parts.isNotEmpty()) {
            Text(
                text = parts.joinToString(" · "),
                color = stateColor,
                fontSize = 11.sp,
            )
        }
    }
}

private fun renderStateLabel(body: FileEventBody): String = when (body.state) {
    FileState.READY -> "ready"
    FileState.DOWNLOADING -> {
        val total = body.size
        val recv = body.bytesReceived
        if (total > 0L && recv in 1L..total) {
            val pct = (recv * 100L / total).toInt()
            "downloading $pct%"
        } else "downloading"
    }
    FileState.AWAITING_KEY -> "awaiting key"
    FileState.PENDING -> "pending"
    FileState.FAILED_TRANSIENT -> "failed (transient)"
    FileState.FAILED_PERMANENT -> "failed"
    FileState.EXPIRED -> "expired"
    "" -> ""
    else -> body.state
}

private fun stateColorFor(state: String): androidx.compose.ui.graphics.Color = when (state) {
    FileState.READY, "" -> ChatPalette.TextDim
    FileState.DOWNLOADING, FileState.AWAITING_KEY, FileState.PENDING -> ChatPalette.Accent
    FileState.FAILED_TRANSIENT, FileState.FAILED_PERMANENT, FileState.EXPIRED -> ChatPalette.Bad
    else -> ChatPalette.TextDim
}

@Composable
private fun MessageFooter(event: TimelineEvent) {
    Row(
        modifier = Modifier.padding(top = 2.dp),
        horizontalArrangement = Arrangement.spacedBy(4.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(
            text = formatHm(event.displayTs),
            color = ChatPalette.TextDim,
            fontSize = 11.sp,
        )
        if (event.isEdited && !event.isTombstoned) {
            Text(
                text = "(edited)",
                color = ChatPalette.TextDim,
                fontStyle = FontStyle.Italic,
                fontSize = 11.sp,
            )
        }
        if (event.isOutbound) {
            DeliveryGlyph(state = event.deliveryState)
        }
    }
}

private val BubbleShape = RoundedCornerShape(12.dp)
private val BubbleMaxWidth = 280.dp
private val BubbleHorizontalPadding = 10.dp
private val BubblePadding = PaddingValues(horizontal = BubbleHorizontalPadding, vertical = 6.dp)
private val ReactionOverlap = (-10).dp
private val ImagePlaceholderSize = 180.dp
private val ImageMaxHeight = 360.dp
private val ImageCornerShape = RoundedCornerShape(8.dp)

private val HM_FORMATTER = ThreadLocal.withInitial { SimpleDateFormat("HH:mm", Locale.US) }


internal fun formatHm(unixSeconds: Long): String =
    if (unixSeconds <= 0L) "" else HM_FORMATTER.get()!!.format(Date(unixSeconds * 1000L))
