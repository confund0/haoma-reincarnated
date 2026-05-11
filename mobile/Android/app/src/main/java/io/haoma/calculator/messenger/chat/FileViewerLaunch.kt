package io.haoma.calculator.messenger.chat

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.content.Intent
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.text.font.FontWeight
import io.haoma.calculator.log.Logger
import io.haoma.calculator.saf.SafBridge


internal fun launchView(context: Context, path: String, mime: String) {
    try {
        val intent = SafBridge.viewIntent(context, path, mime)
        context.startActivity(Intent.createChooser(intent, "Open with"))
    } catch (t: Throwable) {
        Logger.w("file-action", "launchView failed: ${t.message ?: "?"}")
    }
}


internal fun copyImageToClipboard(context: Context, path: String) {
    try {
        val uri = SafBridge.fileProviderUri(context, path)
        val clipboard = context.getSystemService(Context.CLIPBOARD_SERVICE) as? ClipboardManager
        if (clipboard == null) {
            Logger.w("image-action", "copyImage: clipboard service unavailable")
            return
        }
        val clip = ClipData.newUri(context.contentResolver, "image", uri)
        clipboard.setPrimaryClip(clip)
    } catch (t: Throwable) {
        Logger.w("image-action", "copyImage failed: ${t.message ?: "?"}")
    }
}

internal data class OpenWarnState(val path: String, val sniffedMime: String)


@Composable
internal fun OpenMimeWarnDialog(
    claimedMime: String,
    sniffedMime: String,
    onConfirm: () -> Unit,
    onDismiss: () -> Unit,
) {
    MaterialTheme(
        colorScheme = darkColorScheme(
            surface = ChatPalette.InboundBubble,
            onSurface = ChatPalette.Text,
            background = ChatPalette.InboundBubble,
            onBackground = ChatPalette.Text,
        ),
    ) {
        AlertDialog(
            onDismissRequest = onDismiss,
            title = { Text("Type mismatch", color = ChatPalette.Text) },
            text = {
                Text(
                    text = "Sender claimed ${claimedMime.ifEmpty { "?" }}, " +
                        "bytes look like ${sniffedMime.ifEmpty { "?" }}. Open anyway?",
                    color = ChatPalette.TextDim,
                )
            },
            confirmButton = {
                TextButton(onClick = onConfirm) {
                    Text("Open", color = ChatPalette.Bad, fontWeight = FontWeight.SemiBold)
                }
            },
            dismissButton = {
                TextButton(onClick = onDismiss) {
                    Text("Cancel", color = ChatPalette.Text)
                }
            },
            containerColor = ChatPalette.InboundBubble,
        )
    }
}
