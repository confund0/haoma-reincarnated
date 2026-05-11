package io.haoma.calculator.messenger.chat

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.Text
import androidx.compose.material3.rememberModalBottomSheetState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.haoma.calculator.messenger.*
import io.haoma.calculator.messenger.EventDirection
import io.haoma.calculator.messenger.FileEntry
import io.haoma.calculator.messenger.FileState
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.humanBytes


@OptIn(ExperimentalMaterial3Api::class)
@Composable
internal fun FilesPickerDialog(
    chatId: String,
    store: MessengerStore,
    onDismiss: () -> Unit,
) {
    val sheetState = rememberModalBottomSheetState()
    var entries by remember { mutableStateOf<List<FileEntry>>(emptyList()) }
    var loading by remember { mutableStateOf(true) }
    var actionTarget by remember { mutableStateOf<FileActionTarget?>(null) }

    LaunchedEffect(chatId) {
        loading = true
        entries = store.listFilesFor(chatId)
        loading = false
    }

    ModalBottomSheet(
        onDismissRequest = onDismiss,
        sheetState = sheetState,
        containerColor = ChatPalette.Surface,
    ) {
        Column(modifier = Modifier.padding(bottom = 16.dp)) {
            Text(
                text = "Files",
                color = ChatPalette.Text,
                fontWeight = FontWeight.SemiBold,
                fontSize = 16.sp,
                modifier = Modifier.padding(horizontal = 24.dp, vertical = 12.dp),
            )
            HorizontalDivider(color = ChatPalette.TextFaint, thickness = 0.5.dp)
            when {
                loading -> EmptyMessage("Loading…")
                entries.isEmpty() -> EmptyMessage("No attachments in this chat.")
                else -> LazyColumn(
                    modifier = Modifier.heightIn(max = 480.dp),
                ) {
                    items(items = entries, key = { it.msgId }) { entry ->
                        FileRow(entry = entry, onClick = {
                            actionTarget = entry.toActionTarget()
                        })
                    }
                }
            }
        }
    }

    actionTarget?.let { target ->
        FileActionDialog(
            chatId = chatId,
            target = target,
            store = store,
            onDismiss = { actionTarget = null },
        )
    }
}

@Composable
private fun EmptyMessage(text: String) {
    Box(
        modifier = Modifier
            .fillMaxWidth()
            .padding(vertical = 24.dp, horizontal = 24.dp),
        contentAlignment = Alignment.Center,
    ) {
        Text(text = text, color = ChatPalette.TextDim, fontSize = 14.sp)
    }
}

@Composable
private fun FileRow(entry: FileEntry, onClick: () -> Unit) {
    val arrow = if (entry.direction == EventDirection.OUT) "→" else "←"
    val stateColor = when (entry.state) {
        FileState.READY -> ChatPalette.TextDim
        FileState.DOWNLOADING, FileState.AWAITING_KEY, FileState.PENDING -> ChatPalette.Accent
        FileState.FAILED_TRANSIENT, FileState.FAILED_PERMANENT, FileState.EXPIRED -> ChatPalette.Bad
        else -> ChatPalette.TextDim
    }
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .background(ChatPalette.Surface)
            .clickable(onClick = onClick)
            .padding(horizontal = 24.dp, vertical = 10.dp),
    ) {
        Row(
            modifier = Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.spacedBy(8.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Text(
                text = formatHm(entry.displayTs),
                color = ChatPalette.TextFaint,
                fontSize = 11.sp,
            )
            Text(text = arrow, color = ChatPalette.TextDim, fontSize = 13.sp)
            Text(
                text = entry.originalName.ifEmpty { "(unnamed)" },
                color = ChatPalette.Text,
                fontSize = 14.sp,
                modifier = Modifier.weight(1f),
                fontWeight = FontWeight.SemiBold,
            )
            Text(
                text = humanBytes(entry.size),
                color = ChatPalette.TextDim,
                fontSize = 11.sp,
            )
        }
        Row(
            modifier = Modifier.fillMaxWidth().padding(top = 2.dp),
            horizontalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            if (entry.mime.isNotEmpty()) {
                Text(text = entry.mime, color = ChatPalette.TextDim, fontSize = 11.sp)
            }
            Text(text = entry.state.ifEmpty { "?" }, color = stateColor, fontSize = 11.sp)
        }
    }
}

private fun FileEntry.toActionTarget(): FileActionTarget = FileActionTarget(
    msgId = msgId,
    displayName = originalName,
    mime = mime,
    state = state,
    isOutbound = direction == EventDirection.OUT,
    deletable = deletable,
)
