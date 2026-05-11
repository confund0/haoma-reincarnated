package io.haoma.calculator.messenger.chat

import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.haoma.calculator.messenger.*
import io.haoma.calculator.messenger.MessengerStore
import kotlinx.coroutines.launch


data class FileActionTarget(
    val msgId: String,
    val displayName: String,
    val mime: String,
    val state: String,
    val isOutbound: Boolean,
    val deletable: Boolean,
)

@Composable
internal fun FileActionDialog(
    chatId: String,
    target: FileActionTarget,
    store: MessengerStore,
    onDismiss: () -> Unit,
) {
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    var confirmDelete by remember { mutableStateOf(false) }
    var openWarn by remember { mutableStateOf<OpenWarnState?>(null) }

    
    val mimeKey = target.mime.ifEmpty { "*/*" }
    val saveLauncher = rememberLauncherForActivityResult(
        contract = remember(mimeKey) { ActivityResultContracts.CreateDocument(mimeKey) },
    ) { uri ->
        if (uri != null) scope.launch { store.saveFileToUri(chatId, target.msgId, uri) }
    }

    val isReady = target.state == io.haoma.calculator.messenger.FileState.READY
    val canSaveAs = isReady && !target.isOutbound
    val canOpen = isReady

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
            title = {
                Text(
                    text = target.displayName.ifEmpty { "Attachment" },
                    color = ChatPalette.Text,
                    fontWeight = FontWeight.SemiBold,
                )
            },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(4.dp)) {
                    if (target.mime.isNotEmpty()) {
                        Text(
                            text = target.mime,
                            color = ChatPalette.TextDim,
                            fontSize = 12.sp,
                        )
                    }
                    if (!isReady) {
                        Text(
                            text = "State: ${target.state.ifEmpty { "unknown" }}",
                            color = ChatPalette.Bad,
                            fontSize = 12.sp,
                        )
                    }
                }
            },
            confirmButton = {
                Row(
                    modifier = Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.End,
                ) {
                    if (canSaveAs) {
                        ActionButton("Save as") {
                            saveLauncher.launch(target.displayName.ifEmpty { "attachment" })
                            onDismiss()
                        }
                    }
                    if (canOpen) {
                        ActionButton("Open") {
                            scope.launch {
                                val res = store.openFile(chatId, target.msgId) ?: run {
                                    onDismiss()
                                    return@launch
                                }
                                if (!res.matches) {
                                    openWarn = OpenWarnState(
                                        path = res.path,
                                        sniffedMime = res.sniffedMime,
                                    )
                                } else {
                                    launchView(context, res.path, target.mime)
                                    onDismiss()
                                }
                            }
                        }
                    }
                    if (target.deletable) {
                        ActionButton("Delete", destructive = true) {
                            confirmDelete = true
                        }
                    }
                }
            },
            dismissButton = {
                TextButton(onClick = onDismiss) {
                    Text("Close", color = ChatPalette.Text)
                }
            },
            containerColor = ChatPalette.InboundBubble,
        )
    }

    if (confirmDelete) {
        DeleteFileDialog(
            displayName = target.displayName,
            onConfirm = {
                store.sendDelete(chatId, target.msgId)
                confirmDelete = false
                onDismiss()
            },
            onDismiss = { confirmDelete = false },
        )
    }
    openWarn?.let { state ->
        OpenMimeWarnDialog(
            claimedMime = target.mime,
            sniffedMime = state.sniffedMime,
            onConfirm = {
                launchView(context, state.path, target.mime)
                openWarn = null
                onDismiss()
            },
            onDismiss = { openWarn = null },
        )
    }
}

@Composable
private fun ActionButton(
    label: String,
    destructive: Boolean = false,
    onClick: () -> Unit,
) {
    TextButton(onClick = onClick) {
        Text(
            text = label,
            color = if (destructive) ChatPalette.Bad else ChatPalette.Accent,
            fontWeight = FontWeight.SemiBold,
        )
    }
}

@Composable
private fun DeleteFileDialog(
    displayName: String,
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
            title = { Text("Delete attachment?", color = ChatPalette.Text) },
            text = {
                Text(
                    text = if (displayName.isEmpty())
                        "This removes the file for both ends. This can't be undone."
                    else
                        "“$displayName”\n\nThis removes the file for both ends. This can't be undone.",
                    color = ChatPalette.TextDim,
                )
            },
            confirmButton = {
                TextButton(onClick = onConfirm) {
                    Text("Delete", color = ChatPalette.Bad, fontWeight = FontWeight.SemiBold)
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

