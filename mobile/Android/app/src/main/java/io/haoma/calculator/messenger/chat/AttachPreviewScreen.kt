package io.haoma.calculator.messenger.chat

import android.net.Uri
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawing
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.windowInsetsPadding
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
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import coil.compose.AsyncImage
import coil.request.ImageRequest
import io.haoma.calculator.saf.SafBridge
import io.haoma.calculator.saf.UriMetadata
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import java.util.Locale


@Composable
internal fun AttachPreviewScreen(
    uri: Uri,
    peerLabel: String,
    onPickAgain: () -> Unit,
    onSend: () -> Unit,
) {
    val context = LocalContext.current
    var meta by remember(uri) { mutableStateOf<UriMetadata?>(null) }
    LaunchedEffect(uri) {
        meta = withContext(Dispatchers.IO) { SafBridge.peekMetadata(context, uri) }
    }

    
    Dialog(
        onDismissRequest = onPickAgain,
        properties = DialogProperties(
            usePlatformDefaultWidth = false,
            decorFitsSystemWindows = false,
        ),
    ) {
        Box(
            modifier = Modifier
                .fillMaxSize()
                .background(ChatPalette.Surface),
        ) {
            
            
            Column(
                modifier = Modifier
                    .fillMaxSize()
                    .windowInsetsPadding(WindowInsets.safeDrawing),
            ) {
                HeaderRow(peerLabel = peerLabel, onPickAgain = onPickAgain)
                Box(
                    modifier = Modifier
                        .weight(1f)
                        .fillMaxWidth(),
                    contentAlignment = Alignment.Center,
                ) {
                    PreviewBody(uri = uri, meta = meta)
                }
                MetaFooter(meta = meta)
                SendRow(onSend = onSend)
            }
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
private fun MetaFooter(meta: UriMetadata?) {
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
            text = subtitleFor(meta),
            color = ChatPalette.TextDim,
            fontSize = 12.sp,
        )
    }
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

private fun subtitleFor(meta: UriMetadata?): String {
    if (meta == null) return ""
    val sizeStr = humanBytesShort(meta.sizeBytes)
    val mimeStr = meta.mime.ifEmpty { "unknown" }
    return if (sizeStr.isEmpty()) mimeStr else "$sizeStr · $mimeStr"
}

private fun humanBytesShort(bytes: Long): String {
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
