package io.haoma.calculator.messenger.chat

import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.Image
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.PlayArrow
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.Text
import androidx.compose.animation.core.Animatable
import androidx.compose.animation.core.tween
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.hapticfeedback.HapticFeedbackType
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalHapticFeedback
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.LinkAnnotation
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextLinkStyles
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.text.withLink
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import android.content.Intent
import android.net.Uri
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
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.filter


@OptIn(ExperimentalFoundationApi::class)
@Composable
internal fun MessageBubble(
    event: TimelineEvent,
    reactions: Map<String, Reaction> = emptyMap(),
    pulseEvents: Flow<String>? = null,
    onLongPress: (TimelineEvent) -> Unit = {},
    onTapReaction: (TimelineEvent, String) -> Unit = { _, _ -> },
    onTapImage: (TimelineEvent) -> Unit = {},
    onTapVideo: (TimelineEvent) -> Unit = {},
    onTapReplyChip: (ReplyToSnapshot) -> Unit = {},
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
    val tappableVideo = event.isReadyVideo()
    
    
    val pulseAlpha = remember { Animatable(0f) }
    if (pulseEvents != null) {
        val targetMsgId = event.msgId
        LaunchedEffect(pulseEvents, targetMsgId) {
            if (targetMsgId.isEmpty()) return@LaunchedEffect
            pulseEvents.filter { it == targetMsgId }.collect {
                pulseAlpha.snapTo(1f)
                pulseAlpha.animateTo(0f, animationSpec = tween(durationMillis = PULSE_DURATION_MS))
            }
        }
    }

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
                .background(ChatPalette.AnchorPulse.copy(alpha = pulseAlpha.value * ChatPalette.AnchorPulse.alpha))
                .combinedClickable(
                    interactionSource = interactionSource,
                    indication = null,
                    onClick = if (tappableImage) {
                        { onTapImage(event) }
                    } else if (tappableVideo) {
                        { onTapVideo(event) }
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
                val replyChip = event.replyToOrNull()
                if (replyChip != null) {
                    ReplyQuoteChip(snapshot = replyChip, onTap = { onTapReplyChip(replyChip) })
                    Spacer(modifier = Modifier.height(4.dp))
                }
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
private fun ReplyQuoteChip(snapshot: ReplyToSnapshot, onTap: () -> Unit) {
    val type = LocalHaomaTypography.current
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .heightIn(min = 28.dp)
            .clip(RoundedCornerShape(6.dp))
            .background(ChatPalette.TextFaint)
            .clickable(onClick = onTap),
    ) {
        
        Box(
            modifier = Modifier
                .width(3.dp)
                .heightIn(min = 28.dp)
                .background(ChatPalette.Accent),
        )
        Text(
            text = snapshot.text.ifEmpty { "(empty)" },
            color = ChatPalette.TextDim,
            fontSize = type.replyQuote,
            maxLines = 2,
            overflow = TextOverflow.Ellipsis,
            modifier = Modifier
                .padding(horizontal = 8.dp, vertical = 4.dp)
                .fillMaxWidth(),
        )
    }
}

@Composable
private fun MessageBody(event: TimelineEvent, textColor: androidx.compose.ui.graphics.Color) {
    val type = LocalHaomaTypography.current
    when {
        event.isTombstoned -> {
            Text(
                text = if (event.kind == EventKind.FILE) "[file deleted]" else "[message deleted]",
                color = ChatPalette.TextDim,
                fontStyle = FontStyle.Italic,
                fontSize = type.bubbleBody,
            )
        }
        event.decryptStatus == "failed" -> {
            if (event.failReason == "version_mismatch") {
                Text(
                    text = "peer too old, please upgrade",
                    color = ChatPalette.TextDim,
                    fontSize = type.bubbleBody,
                )
            } else {
                Text(
                    text = "[decrypt failed]",
                    color = ChatPalette.Bad,
                    fontWeight = FontWeight.SemiBold,
                    fontSize = type.bubbleBody,
                )
            }
        }
        event.kind == EventKind.FILE -> {
            val body = FileEventBody.fromJson(event.body)
            
            
            Column {
                when {
                    body.isImage && body.state == FileState.READY ->
                        ImageBody(event = event, body = body)
                    body.isVideo && body.state == FileState.READY ->
                        VideoBody(event = event, body = body)
                    else ->
                        FileCaption(body = body, textColor = textColor)
                }
                if (body.caption.isNotEmpty()) {
                    AttachmentCaption(text = body.caption, color = textColor)
                }
            }
        }
        else -> {
            val context = LocalContext.current
            val store = (context.applicationContext as HaomaApp).messengerStore
            val raw = event.bodyTextOrEmpty().ifEmpty { "(empty)" }
            val onUrl = remember(context, store) {
                { url: String ->
                    val viewIntent = Intent(Intent.ACTION_VIEW, Uri.parse(url))
                    val launchIntent = if (store.currentUrlForceChooser()) {
                        Intent.createChooser(viewIntent, "Open with")
                    } else {
                        viewIntent
                    }
                    runCatching { context.startActivity(launchIntent) }
                        .onFailure { t ->
                            Logger.w(
                                "message-bubble",
                                "url open failed url=$url err=${t.message ?: "?"}",
                            )
                        }
                    Unit
                }
            }
            val rendered = remember(raw) { styleChatText(raw, onUrl) }
            val jumboMult = remember(raw) { emojiOnlyJumboScale(parseMessageStyling(raw).text) }
            val bodyFontSize = if (jumboMult != null) {
                (type.bubbleBody.value * jumboMult).sp
            } else {
                type.bubbleBody
            }
            Text(
                text = rendered,
                color = textColor,
                fontSize = bodyFontSize,
            )
        }
    }
}


private val EMOJI_JUMBO_MULTIPLIERS = floatArrayOf(2.3f, 2.1f, 2.0f, 1.6f, 1.5f)


private fun emojiOnlyJumboScale(text: String): Float? {
    val trimmed = text.trim()
    if (trimmed.isEmpty()) return null
    val it = java.text.BreakIterator.getCharacterInstance()
    it.setText(trimmed)
    var start = it.first()
    var end = it.next()
    var visibleClusters = 0
    while (end != java.text.BreakIterator.DONE) {
        val cluster = trimmed.substring(start, end)
        if (!cluster.all { Character.isWhitespace(it) }) {
            if (!clusterIsEmoji(cluster)) return null
            visibleClusters++
            if (visibleClusters > EMOJI_JUMBO_MULTIPLIERS.size) return null
        }
        start = end
        end = it.next()
    }
    return when (visibleClusters) {
        in 1..EMOJI_JUMBO_MULTIPLIERS.size -> EMOJI_JUMBO_MULTIPLIERS[visibleClusters - 1]
        else -> null
    }
}


private fun clusterIsEmoji(cluster: String): Boolean {
    var i = 0
    var hasBase = false
    while (i < cluster.length) {
        val cp = cluster.codePointAt(i)
        when {
            cp == 0x200D -> {}                          
            cp == 0xFE0F || cp == 0xFE0E -> {}          
            cp == 0x20E3 -> {}                          
            cp in 0x1F3FB..0x1F3FF -> hasBase = true    
            cp in 0x1F1E6..0x1F1FF -> hasBase = true    
            isEmojiBaseCodepoint(cp) -> hasBase = true
            else -> return false
        }
        i += Character.charCount(cp)
    }
    return hasBase
}

private fun isEmojiBaseCodepoint(cp: Int): Boolean = when {
    cp in 0x2600..0x27BF -> true   
    cp in 0x2300..0x23FF -> true   
    cp in 0x2B00..0x2BFF -> true   
    cp in 0x1F000..0x1FAFF -> true 
    cp in 0x1F100..0x1F1FF -> true 
    else -> false
}


private val URL_REGEX = Regex("""https?://[^\s<>"'`)\]]+""")


private fun spanStyleFor(mark: TextStyleMark): SpanStyle = when (mark) {
    TextStyleMark.BOLD -> SpanStyle(fontWeight = FontWeight.Bold)
    TextStyleMark.ITALIC -> SpanStyle(fontStyle = FontStyle.Italic)
    TextStyleMark.UNDERLINE -> SpanStyle(textDecoration = TextDecoration.Underline)
    TextStyleMark.STRIKE -> SpanStyle(textDecoration = TextDecoration.LineThrough)
}


private fun styleChatText(raw: String, onUrl: (String) -> Unit): AnnotatedString {
    val styled = parseMessageStyling(raw)
    return buildAnnotatedString {
        linkifyInto(this, styled.text, onUrl)
        for (s in styled.spans) addStyle(spanStyleFor(s.mark), s.start, s.end)
    }
}


private fun linkifyInto(
    builder: AnnotatedString.Builder,
    text: String,
    onUrl: (String) -> Unit,
) = with(builder) {
    var i = 0
    for (match in URL_REGEX.findAll(text)) {
        if (match.range.first > i) append(text.substring(i, match.range.first))
        var url = match.value
        var trailing = ""
        while (url.isNotEmpty() && url.last() in ".,;:!?)") {
            trailing = url.last() + trailing
            url = url.dropLast(1)
        }
        if (url.isEmpty()) {
            append(match.value)
        } else {
            withLink(
                LinkAnnotation.Url(
                    url = url,
                    styles = TextLinkStyles(
                        style = SpanStyle(
                            color = ChatPalette.Link,
                            textDecoration = TextDecoration.Underline,
                        ),
                    ),
                    linkInteractionListener = { onUrl(url) },
                ),
            ) {
                append(url)
            }
            if (trailing.isNotEmpty()) append(trailing)
        }
        i = match.range.last + 1
    }
    if (i < text.length) append(text.substring(i))
}


@Composable
private fun ImageBody(event: TimelineEvent, body: FileEventBody) {
    val context = LocalContext.current
    val app = context.applicationContext as HaomaApp
    val store = app.messengerStore
    val bytesMap by store.imageBytesByMsgId.collectAsStateWithLifecycle()
    val bytes = bytesMap[event.msgId]

    LaunchedEffect(event.msgId) {
        if (bytes != null) return@LaunchedEffect
        store.openImageBytes(event.chatId, event.msgId)
    }

    val displayName = body.name.ifEmpty { "(image)" }
    if (bytes == null) {
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

    
    val request = remember(event.msgId, bytes) {
        ImageRequest.Builder(context)
            .data(bytes)
            .memoryCacheKey("image-bubble-${event.msgId}")
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
                "decode failed msg=${event.msgId} err=${it.result.throwable.message ?: "?"}",
            )
        },
    )
}


@Composable
private fun VideoBody(event: TimelineEvent, body: FileEventBody) {
    val context = LocalContext.current
    val app = context.applicationContext as HaomaApp
    val store = app.messengerStore
    val thumbs by store.videoThumbsByMsgId.collectAsStateWithLifecycle()
    val transients by store.videoTransientByMsgId.collectAsStateWithLifecycle()
    val thumb = thumbs[event.msgId]
    val transientReady = transients[event.msgId] != null
    val sentinel = thumbs.containsKey(event.msgId) && thumb == null

    LaunchedEffect(event.msgId) {
        if (!thumbs.containsKey(event.msgId)) {
            store.openVideoForBubble(event.chatId, event.msgId)
        }
    }

    Box(
        modifier = Modifier
            .widthIn(max = BubbleMaxWidth - BubbleHorizontalPadding * 2)
            .heightIn(max = ImageMaxHeight)
            .clip(ImageCornerShape)
            .background(ChatPalette.DecryptFailedBg.copy(alpha = 0.25f)),
        contentAlignment = Alignment.Center,
    ) {
        when {
            thumb != null -> {
                Image(
                    bitmap = thumb.asImageBitmap(),
                    contentDescription = body.name.ifEmpty { "(video)" },
                    contentScale = ContentScale.Fit,
                    modifier = Modifier.size(VideoThumbSize),
                )
            }
            sentinel -> {
                
                
                Box(
                    modifier = Modifier.size(VideoThumbSize),
                    contentAlignment = Alignment.Center,
                ) {
                    Text(
                        text = "video",
                        color = ChatPalette.TextDim,
                        fontSize = LocalHaomaTypography.current.replyQuote,
                    )
                }
            }
            else -> {
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
            }
        }
        
        
        if (transientReady) {
            Box(
                modifier = Modifier
                    .size(VideoPlayBadge)
                    .clip(CircleShape)
                    .background(Color.Black.copy(alpha = 0.55f)),
                contentAlignment = Alignment.Center,
            ) {
                Icon(
                    Icons.Filled.PlayArrow,
                    contentDescription = "Play",
                    tint = Color.White,
                    modifier = Modifier.size(VideoPlayIcon),
                )
            }
        }
    }
}


@Composable
private fun FileCaption(body: FileEventBody, textColor: androidx.compose.ui.graphics.Color) {
    val type = LocalHaomaTypography.current
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
            fontSize = type.bubbleBody,
            fontWeight = FontWeight.SemiBold,
        )
        if (parts.isNotEmpty()) {
            Text(
                text = parts.joinToString(" · "),
                color = stateColor,
                fontSize = type.bubbleSmall,
            )
        }
    }
}


@Composable
private fun AttachmentCaption(text: String, color: androidx.compose.ui.graphics.Color) {
    val type = LocalHaomaTypography.current
    Text(
        text = text,
        color = color,
        fontSize = type.bubbleBody,
        modifier = Modifier.padding(top = 4.dp),
    )
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
    val type = LocalHaomaTypography.current
    Row(
        modifier = Modifier.padding(top = 2.dp),
        horizontalArrangement = Arrangement.spacedBy(4.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(
            text = formatHm(event.displayTs),
            color = ChatPalette.TextDim,
            fontSize = type.bubbleSmall,
        )
        if (event.isEdited && !event.isTombstoned) {
            Text(
                text = "(edited)",
                color = ChatPalette.TextDim,
                fontStyle = FontStyle.Italic,
                fontSize = type.bubbleSmall,
            )
        }
        if (event.isOutbound) {
            DeliveryGlyph(state = event.deliveryState, isFile = event.kind == EventKind.FILE)
        }
    }
}

private const val PULSE_DURATION_MS = 1000

private val BubbleShape = RoundedCornerShape(12.dp)
private val BubbleMaxWidth = 280.dp
private val BubbleHorizontalPadding = 10.dp
private val BubblePadding = PaddingValues(horizontal = BubbleHorizontalPadding, vertical = 6.dp)
private val ReactionOverlap = (-10).dp
private val ImagePlaceholderSize = 180.dp
private val ImageMaxHeight = 360.dp
private val ImageCornerShape = RoundedCornerShape(8.dp)
private val VideoThumbSize = 240.dp
private val VideoPlayBadge = 56.dp
private val VideoPlayIcon = 36.dp

private val HM_FORMATTER = ThreadLocal.withInitial { SimpleDateFormat("HH:mm", Locale.US) }


internal fun formatHm(unixSeconds: Long): String =
    if (unixSeconds <= 0L) "" else HM_FORMATTER.get()!!.format(Date(unixSeconds * 1000L))
