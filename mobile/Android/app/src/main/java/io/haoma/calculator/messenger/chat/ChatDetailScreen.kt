package io.haoma.calculator.messenger.chat

import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.snapshotFlow
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import io.haoma.calculator.messenger.*
import io.haoma.calculator.messenger.calls.InCallBar
import io.haoma.calculator.messenger.EventKind
import io.haoma.calculator.messenger.FileEventBody
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.Reaction
import io.haoma.calculator.messenger.TimelineEvent
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.drop
import kotlinx.coroutines.flow.filter
import kotlinx.coroutines.launch


@Composable
fun ChatDetailScreen(
    store: MessengerStore,
    chatId: String,
    onBack: () -> Unit,
) {
    val cache by store.timelineFor(chatId).collectAsStateWithLifecycle()
    val chats by store.chats.collectAsStateWithLifecycle()
    val presenceMap by store.presence.collectAsStateWithLifecycle()
    val activeCalls by store.activeCalls.collectAsStateWithLifecycle()
    val recordAudio by store.recordAudioGranted.collectAsStateWithLifecycle()
    val cameraGranted by store.cameraGranted.collectAsStateWithLifecycle()
    val drafts by store.drafts.collectAsStateWithLifecycle()
    val composeDraft = drafts[chatId] ?: ""
    val replyTargets by store.replyTargets.collectAsStateWithLifecycle()
    val replyTarget = replyTargets[chatId]
    val chat = chats.firstOrNull { it.chatId == chatId }
    val presence = chat?.peerId?.let { presenceMap[it] }
    
    
    val callHere = activeCalls.values
        .filter { it.chatId == chatId && !it.isTerminal }
        .maxByOrNull { it.startedAt }
    val callElsewhere = activeCalls.values.any {
        it.chatId != chatId && !it.isTerminal
    }
    var actionTarget by remember { mutableStateOf<TimelineEvent?>(null) }
    var reactPickerTarget by remember { mutableStateOf<TimelineEvent?>(null) }
    var editingTarget by remember { mutableStateOf<TimelineEvent?>(null) }
    var deleteConfirmTarget by remember { mutableStateOf<TimelineEvent?>(null) }
    var infoTarget by remember { mutableStateOf<TimelineEvent?>(null) }
    var filesPickerOpen by remember { mutableStateOf(false) }
    var fileActionTarget by remember { mutableStateOf<FileActionTarget?>(null) }
    var imageSaveTarget by remember { mutableStateOf<TimelineEvent?>(null) }
    
    
    var fullReplyTarget by remember { mutableStateOf<ReplyToSnapshot?>(null) }
    val clipboard = LocalClipboardManager.current
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    
    
    val saveImageLauncher = rememberLauncherForActivityResult(
        contract = remember { ActivityResultContracts.CreateDocument("image/*") },
    ) { uri ->
        val tgt = imageSaveTarget
        imageSaveTarget = null
        if (uri != null && tgt != null) {
            scope.launch { store.saveFileToUri(chatId, tgt.msgId, uri) }
        }
    }
    LaunchedEffect(imageSaveTarget) {
        val tgt = imageSaveTarget ?: return@LaunchedEffect
        val body = FileEventBody.fromJson(tgt.body)
        val name = body.name.ifEmpty { "image" }
        saveImageLauncher.launch(name)
    }
    
    
    val attachLauncher = rememberLauncherForActivityResult(
        contract = ActivityResultContracts.OpenDocument(),
    ) { uri ->
        if (uri != null) store.attachFromUri(chatId, uri)
    }

    LaunchedEffect(chatId) {
        store.loadTimeline(chatId)
        
        
        store.markRead(chatId)
        store.setClientFocus(chatId, 0)
    }
    DisposableEffect(chatId) {
        onDispose {
            store.setClientFocus("", 0)
            
            
            store.wipeAllOpenTransients()
        }
    }

    
    val viewerTarget by store.viewerTarget.collectAsStateWithLifecycle()
    viewerTarget?.let { target ->
        if (target.chatId == chatId) FullScreenImageViewer(store, target)
    }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .background(ChatPalette.Surface),
    ) {
        ChatTitleBar(
            chat = chat,
            chatId = chatId,
            presence = presence,
            inCall = callHere != null,
            otherChatInCall = callElsewhere,
            canCall = recordAudio,
            cameraGranted = cameraGranted,
            onBack = onBack,
            onPlaceCall = { store.startCall(chatId) },
            onPlaceVideoCall = {
                store.startCall(chatId, listOf(CallModality.Audio, CallModality.Video))
            },
            onRequestCamera = {
                
                
                (context as? io.haoma.calculator.MainActivity)?.requestCameraIfNeeded()
            },
            onHangup = { store.hangupCallInChat(chatId) },
            onRotateTor = { store.rotateTorForChat(chatId) },
            onNewTorCircuit = { store.newTorCircuitForChat(chatId) },
            onToggleMute = {
                val nextMuted = chat?.notificationsMuted != true
                store.setChatMute(chatId, nextMuted)
            },
            onOpenSettings = { store.openChatSettings(chatId) },
            onViewFiles = { filesPickerOpen = true },
        )
        callHere?.let { InCallBar(call = it, store = store) }
        Box(modifier = Modifier.weight(1f).fillMaxWidth()) {
            if (cache.events.isEmpty()) {
                EmptyChatHint(loading = cache.loading)
            } else {
                MessageList(
                    events = cache.events,
                    reactionsByTarget = cache.reactionsByTarget,
                    onLongPress = { actionTarget = it },
                    onTapReaction = { ev, emoji -> store.toggleReaction(chatId, ev.msgId, emoji) },
                    onTapImage = { ev ->
                        
                        
                        val name = FileEventBody.fromJson(ev.body).name
                        store.openImageViewer(chatId, ev.msgId, name)
                    },
                    onTapReplyChip = { snapshot -> fullReplyTarget = snapshot },
                    onScrolledToBottom = {
                        
                        
                        store.markRead(chatId)
                        store.setClientFocus(chatId, 0)
                    },
                )
            }
        }
        ChatInput(
            composeDraft = composeDraft,
            onComposeChange = { text -> store.setDraft(chatId, text) },
            onSend = { text -> store.sendText(chatId, text) },
            editingTarget = editingTarget,
            onSubmitEdit = { target, text ->
                store.sendEdit(chatId, target.msgId, text)
                editingTarget = null
            },
            onCancelEdit = { editingTarget = null },
            replyTarget = replyTarget,
            onCancelReply = { store.clearReplyTarget(chatId) },
            
            
            onAttach = { attachLauncher.launch(arrayOf("*/*")) },
        )
    }

    actionTarget?.let { target ->
        MessageActionSheet(
            target = target,
            onDismiss = { actionTarget = null },
            onReact = {
                reactPickerTarget = target
                actionTarget = null
            },
            onReply = {
                
                
                editingTarget = null
                store.setReplyTarget(chatId, target)
                actionTarget = null
            },
            onEdit = {
                
                store.clearReplyTarget(chatId)
                editingTarget = target
                actionTarget = null
            },
            onDelete = {
                deleteConfirmTarget = target
                actionTarget = null
            },
            onInfo = {
                infoTarget = target
                actionTarget = null
            },
            onCopy = {
                val text = target.bodyTextOrEmpty()
                if (text.isNotEmpty()) {
                    clipboard.setText(AnnotatedString(text))
                }
                actionTarget = null
            },
            onViewAttachment = {
                fileActionTarget = target.toFileActionTarget()
                actionTarget = null
            },
            onSaveImage = {
                imageSaveTarget = target
                actionTarget = null
            },
            onCopyImage = {
                scope.launch {
                    val res = store.openFile(chatId, target.msgId) ?: return@launch
                    copyImageToClipboard(context, res.path)
                }
                actionTarget = null
            },
        )
    }
    reactPickerTarget?.let { target ->
        EmojiPickerDialog(
            onPicked = { emoji ->
                store.toggleReaction(chatId, target.msgId, emoji)
                reactPickerTarget = null
            },
            onDismiss = { reactPickerTarget = null },
        )
    }
    deleteConfirmTarget?.let { target ->
        DeleteMessageDialog(
            preview = target.bodyTextOrEmpty(),
            onConfirm = {
                store.sendDelete(chatId, target.msgId)
                deleteConfirmTarget = null
            },
            onDismiss = { deleteConfirmTarget = null },
        )
    }
    infoTarget?.let { target ->
        MessageInfoSheet(
            target = target,
            chat = chat,
            reactions = cache.reactionsByTarget[target.msgId].orEmpty(),
            onDismiss = { infoTarget = null },
        )
    }
    if (filesPickerOpen) {
        FilesPickerDialog(
            chatId = chatId,
            store = store,
            onDismiss = { filesPickerOpen = false },
        )
    }
    fileActionTarget?.let { target ->
        FileActionDialog(
            chatId = chatId,
            target = target,
            store = store,
            onDismiss = { fileActionTarget = null },
        )
    }
    fullReplyTarget?.let { snapshot ->
        FullReplyOverlay(snapshot = snapshot, onDismiss = { fullReplyTarget = null })
    }
}

@Composable
private fun DeleteMessageDialog(
    preview: String,
    onConfirm: () -> Unit,
    onDismiss: () -> Unit,
) {
    val short = if (preview.length > 80) preview.take(80) + "…" else preview
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
            title = { Text("Delete message?", color = ChatPalette.Text) },
            text = {
                Text(
                    text = if (short.isEmpty())
                        "This removes the message for both ends. This can't be undone."
                    else
                        "“$short”\n\nThis removes the message for both ends. This can't be undone.",
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

@Composable
private fun MessageList(
    events: List<TimelineEvent>,
    reactionsByTarget: Map<String, Map<String, Reaction>>,
    onLongPress: (TimelineEvent) -> Unit,
    onTapReaction: (TimelineEvent, String) -> Unit,
    onTapImage: (TimelineEvent) -> Unit,
    onTapReplyChip: (ReplyToSnapshot) -> Unit,
    onScrolledToBottom: () -> Unit,
) {
    val state = rememberLazyListState()
    
    
    LaunchedEffect(events.size) {
        if (events.isNotEmpty()) state.scrollToItem(0)
    }
    
    
    LaunchedEffect(state) {
        snapshotFlow { state.firstVisibleItemIndex }
            .distinctUntilChanged()
            .drop(1)
            .filter { it == 0 }
            .collect { onScrolledToBottom() }
    }
    
    
    LazyColumn(
        state = state,
        modifier = Modifier.fillMaxSize(),
        reverseLayout = true,
        contentPadding = PaddingValues(vertical = 8.dp),
    ) {
        
        
        val newestFirst = events.asReversed().filter { it.kind != EventKind.REACTION }
        items(items = newestFirst, key = { rowKey(it) }) { ev ->
            when (ev.kind) {
                EventKind.TEXT, EventKind.FILE -> MessageBubble(
                    event = ev,
                    reactions = reactionsByTarget[ev.msgId].orEmpty(),
                    onLongPress = onLongPress,
                    onTapReaction = onTapReaction,
                    onTapImage = onTapImage,
                    onTapReplyChip = onTapReplyChip,
                )
                else -> SystemBreadcrumb(event = ev)
            }
        }
    }
}

private fun rowKey(ev: TimelineEvent): String =
    if (ev.msgId.isNotEmpty()) "msg:${ev.msgId}" else "seq:${ev.recvSeq}"


private fun TimelineEvent.toFileActionTarget(): FileActionTarget {
    val body = FileEventBody.fromJson(this.body)
    val nowSec = System.currentTimeMillis() / 1000L
    val withinWindow = nowSec - this.senderTs < 86_400L
    return FileActionTarget(
        msgId = this.msgId,
        displayName = body.name,
        mime = body.mime,
        state = body.state,
        isOutbound = this.isOutbound,
        deletable = this.isOutbound && !this.isTombstoned && withinWindow,
    )
}

@Composable
private fun EmptyChatHint(loading: Boolean) {
    Box(
        modifier = Modifier.fillMaxSize().padding(24.dp),
        contentAlignment = Alignment.Center,
    ) {
        Text(
            text = if (loading) "Loading history…" else "No messages yet — say hi.",
            color = ChatPalette.TextDim,
            fontSize = 14.sp,
        )
    }
}
