package io.haoma.calculator.messenger.chat

import android.net.Uri
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.Send
import androidx.compose.material.icons.filled.Close
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.IconButtonDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import coil.compose.AsyncImage
import coil.request.ImageRequest
import io.haoma.calculator.saf.ImageDims
import io.haoma.calculator.saf.SafBridge
import io.haoma.calculator.saf.UriMetadata
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import java.util.Locale
import kotlin.math.max


@Composable
internal fun AttachPreviewScreen(
    uri: Uri,
    peerLabel: String,
    onPickAgain: () -> Unit,
    onSend: (compressed: Boolean) -> Unit,
) {
    val context = LocalContext.current
    var meta by remember(uri) { mutableStateOf<UriMetadata?>(null) }
    var dims by remember(uri) { mutableStateOf<ImageDims?>(null) }
    
    var compressed by remember(uri) { mutableStateOf(true) }

    LaunchedEffect(uri) {
        meta = withContext(Dispatchers.IO) { SafBridge.peekMetadata(context, uri) }
        dims = withContext(Dispatchers.IO) { SafBridge.peekImageDims(context, uri) }
    }

    val sizeBytes = meta?.sizeBytes ?: 0L
    val toggleEnabled = isCompressibleImage(meta) && sizeBytes >= MIN_TOGGLE_BYTES
    
    
    val effectiveCompressed = if (toggleEnabled) compressed else true
    val estimatedBytes = if (effectiveCompressed && isCompressibleImage(meta))
        estimateCompressedBytes(sizeBytes, dims) else null

    FullScreenOverlay(
        onDismiss = onPickAgain,
        background = ChatPalette.Surface,
    ) {
        Column(modifier = Modifier.fillMaxSize()) {
            HeaderRow(peerLabel = peerLabel, onPickAgain = onPickAgain)
            Box(
                modifier = Modifier
                    .weight(1f)
                    .fillMaxWidth(),
                contentAlignment = Alignment.Center,
            ) {
                PreviewBody(uri = uri, meta = meta)
            }
            MetaFooter(
                meta = meta,
                compressed = effectiveCompressed,
                estimatedBytes = estimatedBytes,
                toggleEnabled = toggleEnabled,
                onToggle = { compressed = !compressed },
            )
            SendRow(onSend = { onSend(effectiveCompressed) })
        }
    }
}

@Composable
private fun HeaderRow(peerLabel: String, onPickAgain: () -> Unit) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .background(ChatPalette.InboundBubble)
            .padding(horizontal = 12.dp, vertical = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.SpaceBetween,
    ) {
        IconButton(onClick = onPickAgain) {
            Icon(
                imageVector = Icons.Filled.Close,
                contentDescription = "Pick a different file",
                tint = ChatPalette.Accent,
            )
        }
        
        
        Text(
            text = peerLabel,
            color = ChatPalette.Text,
            fontWeight = FontWeight.SemiBold,
            fontSize = 16.sp,
            modifier = Modifier.padding(end = 12.dp),
        )
    }
}

@Composable
private fun PreviewBody(uri: Uri, meta: UriMetadata?) {
    val context = LocalContext.current
    val mime = (meta?.mime ?: "").lowercase(Locale.US)
    val isImage = mime.startsWith("image/") && mime != "image/svg+xml"
    val isVideo = mime.startsWith("video/")

    when {
        isImage -> {
            
            
            val request = remember(uri) {
                ImageRequest.Builder(context)
                    .data(uri)
                    .crossfade(false)
                    .build()
            }
            AsyncImage(
                model = request,
                contentDescription = meta?.displayName,
                contentScale = ContentScale.Fit,
                modifier = Modifier
                    .fillMaxSize()
                    .padding(16.dp)
                    .clip(RoundedCornerShape(8.dp)),
            )
        }
        isVideo -> NonImagePlaceholder(label = "VIDEO", accent = VideoAccent)
        else -> NonImagePlaceholder(label = extensionFor(meta), accent = FileAccent)
    }
}

@Composable
private fun NonImagePlaceholder(label: String, accent: Color) {
    Box(
        modifier = Modifier
            .size(width = 200.dp, height = 200.dp)
            .clip(RoundedCornerShape(12.dp))
            .background(ChatPalette.InboundBubble),
        contentAlignment = Alignment.Center,
    ) {
        Text(
            text = label,
            color = accent,
            fontFamily = FontFamily.Monospace,
            fontWeight = FontWeight.Bold,
            fontSize = 28.sp,
        )
    }
}

@Composable
private fun MetaFooter(
    meta: UriMetadata?,
    compressed: Boolean,
    estimatedBytes: Long?,
    toggleEnabled: Boolean,
    onToggle: () -> Unit,
) {
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = 24.dp, vertical = 12.dp),
    ) {
        Text(
            text = meta?.displayName ?: "Reading…",
            color = ChatPalette.Text,
            fontSize = 15.sp,
            fontWeight = FontWeight.SemiBold,
        )
        Spacer(modifier = Modifier.height(4.dp))
        Text(
            text = subtitleFor(meta, compressed, estimatedBytes),
            color = ChatPalette.TextDim,
            
            
            fontSize = 14.sp,
        )
        if (toggleEnabled) {
            Spacer(modifier = Modifier.height(6.dp))
            SizeToggleRow(compressed = compressed, onToggle = onToggle)
        }
    }
}

@Composable
private fun SizeToggleRow(compressed: Boolean, onToggle: () -> Unit) {
    val label = if (compressed) "size: compressed (tap for original)" else "size: original (tap for compressed)"
    Text(
        text = label,
        color = ChatPalette.Accent,
        fontSize = 13.sp,
        fontWeight = FontWeight.SemiBold,
        modifier = Modifier
            .fillMaxWidth()
            .clickable(onClick = onToggle)
            .padding(vertical = 4.dp),
    )
}

@Composable
private fun SendRow(onSend: () -> Unit) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = 16.dp, vertical = 12.dp),
        horizontalArrangement = Arrangement.End,
    ) {
        IconButton(
            onClick = onSend,
            modifier = Modifier
                .size(48.dp)
                .clip(CircleShape),
            colors = IconButtonDefaults.iconButtonColors(
                contentColor = ChatPalette.Surface,
                containerColor = SendButtonContainer,
            ),
        ) {
            Icon(
                imageVector = Icons.AutoMirrored.Filled.Send,
                contentDescription = "Send attachment",
            )
        }
    }
}

internal fun subtitleFor(meta: UriMetadata?, compressed: Boolean, estimatedBytes: Long?): String {
    if (meta == null) return ""
    val mimeStr = meta.mime.ifEmpty { "unknown" }
    val sizeStr = when {
        compressed && estimatedBytes != null && estimatedBytes > 0L -> "~${humanBytesShort(estimatedBytes)}"
        else -> humanBytesShort(meta.sizeBytes)
    }
    return if (sizeStr.isEmpty()) mimeStr else "$sizeStr · $mimeStr"
}

internal fun humanBytesShort(bytes: Long): String {
    if (bytes <= 0L) return ""
    if (bytes < 1024L) return "${bytes} B"
    val units = arrayOf("KB", "MB", "GB", "TB")
    var v = bytes.toDouble() / 1024.0
    var i = 0
    while (v >= 1024.0 && i < units.size - 1) {
        v /= 1024.0
        i++
    }
    return String.format(Locale.US, "%.1f %s", v, units[i])
}


private const val MIN_TOGGLE_BYTES = 500_000L


private const val DAEMON_LONG_EDGE = 1920


internal fun isCompressibleImage(meta: UriMetadata?): Boolean {
    val mime = meta?.mime?.lowercase(Locale.US) ?: return false
    return when (mime) {
        "image/jpeg", "image/jpg", "image/png", "image/webp",
        "image/heic", "image/heif", "image/avif" -> true
        else -> false
    }
}


internal fun estimateCompressedBytes(sourceBytes: Long, dims: ImageDims?): Long? {
    if (sourceBytes <= 0L) return null
    if (dims == null) return null
    val longEdge = max(dims.width, dims.height)
    if (longEdge <= 0) return null
    return if (longEdge <= DAEMON_LONG_EDGE) {
        (sourceBytes * 0.95).toLong()
    } else {
        val ratio = DAEMON_LONG_EDGE.toDouble() / longEdge
        (sourceBytes * ratio * ratio).toLong()
    }
}

private fun extensionFor(meta: UriMetadata?): String {
    val name = meta?.displayName ?: return "FILE"
    val dot = name.lastIndexOf('.')
    if (dot <= 0 || dot >= name.length - 1) return "FILE"
    val ext = name.substring(dot + 1).uppercase(Locale.US).take(5)
    return ext
}

private val SendButtonContainer = Color(0xFFD79921) 
private val VideoAccent = Color(0xFF83A598)         
private val FileAccent = Color(0xFFFABD2F)          
