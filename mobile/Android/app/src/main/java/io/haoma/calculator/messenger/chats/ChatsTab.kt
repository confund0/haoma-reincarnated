package io.haoma.calculator.messenger.chats

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import io.haoma.calculator.messenger.*
import io.haoma.calculator.messenger.ChatEntry
import io.haoma.calculator.messenger.ChatKind
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.contacts.RelativeTime
import io.haoma.calculator.messenger.contacts.shortPeerId


@Composable
fun ChatsTab(store: MessengerStore) {
    val chats by store.chats.collectAsStateWithLifecycle()
    val presence by store.presence.collectAsStateWithLifecycle()
    val activeCalls by store.activeCalls.collectAsStateWithLifecycle()
    val drafts by store.drafts.collectAsStateWithLifecycle()
    val nowSeconds = System.currentTimeMillis() / 1000L
    
    
    val inCallPeers = remember(activeCalls) {
        activeCalls.values
            .filter { it.status == CallStatus.Accepted }
            .map { it.peerId }
            .toHashSet()
    }

    Column(modifier = Modifier.fillMaxSize().background(BG_BASE)) {
        TabHeader(title = "Conversations", store = store)
        if (chats.isEmpty()) {
            EmptyChatsSurface()
            return@Column
        }
        val sorted = remember(chats) { sortChats(chats) }
        LazyColumn(modifier = Modifier.fillMaxSize()) {
            items(sorted, key = { it.chatId }) { chat ->
                ChatRow(
                    chat = chat,
                    presenceLabel = presence[chat.peerId].orEmpty(),
                    nowSeconds = nowSeconds,
                    inCall = chat.peerId.isNotEmpty() && chat.peerId in inCallPeers,
                    hasDraft = chat.chatId in drafts,
                    onOpen = { store.openChatDetail(chat.chatId) },
                    onEdit = { store.openChatSettings(chat.chatId) },
                )
                HorizontalDivider(color = DIVIDER, thickness = 0.5.dp)
            }
        }
    }
}

@Composable
private fun TabHeader(title: String, store: MessengerStore) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .background(BG_BAR)
            .padding(horizontal = 16.dp, vertical = 10.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(
            text = title,
            color = FG_PRIMARY,
            fontWeight = FontWeight.SemiBold,
            fontSize = 17.sp,
            modifier = Modifier.weight(1f),
        )
        io.haoma.calculator.messenger.calls.CallChip(store = store)
    }
}

@Composable
private fun ChatRow(
    chat: ChatEntry,
    presenceLabel: String,
    nowSeconds: Long,
    inCall: Boolean,
    hasDraft: Boolean,
    onOpen: () -> Unit,
    onEdit: () -> Unit,
) {
    val hasUnread = chat.unreadCount > 0
    val isDirect = chat.kind == ChatKind.Direct
    val displayLabel = renderChatLabel(chat)
    
    
    val labelColor = when {
        inCall -> FG_IN_CALL
        hasUnread -> FG_UNREAD
        else -> FG_PRIMARY
    }
    val labelStyle = if (chat.label.isEmpty() && chat.groupAlias.isEmpty() && chat.groupName.isEmpty()) {
        FontStyle.Italic
    } else {
        FontStyle.Normal
    }

    Row(
        modifier = Modifier
            .fillMaxWidth()
            .clickable(onClick = onOpen)
            .padding(horizontal = 12.dp, vertical = 10.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        if (isDirect) {
            PresenceDot(label = presenceLabel)
        } else {
            Spacer(modifier = Modifier.size(10.dp))
        }
        Spacer(modifier = Modifier.width(10.dp))
        Column(
            modifier = Modifier.weight(1f),
            verticalArrangement = Arrangement.spacedBy(2.dp),
        ) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                if (inCall) {
                    
                    
                    Text(
                        text = "☎",
                        color = FG_IN_CALL,
                        fontSize = 14.sp,
                        fontWeight = FontWeight.Bold,
                    )
                    Spacer(modifier = Modifier.width(6.dp))
                }
                Text(
                    text = displayLabel,
                    color = labelColor,
                    fontStyle = labelStyle,
                    fontWeight = FontWeight.SemiBold,
                    fontSize = 15.sp,
                    modifier = Modifier.weight(1f, fill = false),
                )
                if (hasDraft) {
                    
                    
                    Spacer(modifier = Modifier.width(6.dp))
                    Text(
                        text = "✏️",
                        fontSize = 12.sp,
                    )
                }
                if (chat.notificationsMuted) {
                    Spacer(modifier = Modifier.width(6.dp))
                    Text(
                        text = "🔕",
                        color = FG_DIM,
                        fontSize = 12.sp,
                    )
                }
            }
            LastActivityLine(
                ts = chat.lastActivityAt,
                nowSeconds = nowSeconds,
                hasUnread = hasUnread,
            )
        }
        Spacer(modifier = Modifier.width(10.dp))
        if (hasUnread) {
            UnreadBadge(count = chat.unreadCount)
            Spacer(modifier = Modifier.width(8.dp))
        }
        Text(
            text = "Edit",
            color = FG_LINK,
            fontSize = 14.sp,
            fontWeight = FontWeight.Medium,
            modifier = Modifier
                .clickable(onClick = onEdit)
                .padding(horizontal = 6.dp, vertical = 4.dp),
        )
    }
}

@Composable
private fun PresenceDot(label: String) {
    val color = presenceColor(label.ifEmpty { "unknown" })
    Box(
        modifier = Modifier
            .size(10.dp)
            .clip(CircleShape)
            .background(color),
    )
}

@Composable
private fun LastActivityLine(ts: Long, nowSeconds: Long, hasUnread: Boolean) {
    val text = if (ts == 0L) "—" else RelativeTime.format(nowSeconds, ts)
    Text(
        text = text,
        color = if (hasUnread) FG_UNREAD else FG_DIM,
        fontSize = 12.sp,
        fontFamily = FontFamily.Monospace,
    )
}

@Composable
private fun UnreadBadge(count: Long) {
    Box(
        modifier = Modifier
            .clip(RoundedCornerShape(50))
            .background(FG_UNREAD)
            .padding(horizontal = 8.dp, vertical = 2.dp),
    ) {
        Text(
            text = "$count *",
            color = BG_BASE,
            fontSize = 12.sp,
            fontWeight = FontWeight.Bold,
            fontFamily = FontFamily.Monospace,
        )
    }
}

@Composable
private fun EmptyChatsSurface() {
    Box(
        modifier = Modifier
            .fillMaxSize()
            .background(BG_BASE)
            .padding(24.dp),
        contentAlignment = Alignment.Center,
    ) {
        Column(
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            Text(
                text = "No chats yet",
                color = FG_PRIMARY,
                fontWeight = FontWeight.SemiBold,
                fontSize = 18.sp,
            )
            Text(
                text = "Pair a peer in the Invites tab, then start a chat from Contacts.",
                color = FG_DIM,
                fontSize = 14.sp,
            )
        }
    }
}


private fun renderChatLabel(chat: ChatEntry): String {
    if (chat.label.isNotEmpty()) return chat.label
    return when (chat.kind) {
        ChatKind.Group -> when {
            chat.groupAlias.isNotEmpty() -> chat.groupAlias
            chat.groupName.isNotEmpty() -> chat.groupName
            else -> shortPeerId(chat.chatId)
        }
        ChatKind.Direct -> shortPeerId(chat.peerId.ifEmpty { chat.chatId })
        else -> shortPeerId(chat.chatId)
    }
}


private fun sortChats(chats: List<ChatEntry>): List<ChatEntry> =
    chats.sortedByDescending { it.lastActivityAt }


private fun presenceColor(label: String): Color = when (label) {
    "available" -> C_AVAILABLE
    "away" -> C_AWAY
    "busy" -> C_BUSY
    "accepting" -> C_ACCEPTING
    else -> C_UNKNOWN
}


private val BG_BASE = Color(0xFF1D2021)
private val BG_BAR = Color(0xFF282828)
private val DIVIDER = Color(0xFF3C3836)
private val FG_PRIMARY = Color(0xFFEBDBB2)
private val FG_DIM = Color(0xFF7C6F64)
private val FG_LINK = Color(0xFF83A598)
private val FG_UNREAD = Color(0xFFB16286) 
private val FG_IN_CALL = Color(0xFFCC241D) 

private val C_AVAILABLE = Color(0xFF5FCC1A) 
private val C_AWAY = Color(0xFFFABD2F) 
private val C_BUSY = Color(0xFFCC241D)
private val C_ACCEPTING = Color(0xFF5F87FF)
private val C_UNKNOWN = Color(0xFF504945)
