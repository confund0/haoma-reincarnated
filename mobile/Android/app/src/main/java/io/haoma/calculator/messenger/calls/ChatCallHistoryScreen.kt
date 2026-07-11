package io.haoma.calculator.messenger.calls

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
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.material3.Text
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import io.haoma.calculator.messenger.EventKind
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.TimelineEvent
import io.haoma.calculator.messenger.chat.ChatPalette
import io.haoma.calculator.messenger.loadTimeline
import io.haoma.calculator.messenger.peerLabelFor
import io.haoma.calculator.messenger.timelineFor
import java.text.SimpleDateFormat
import java.util.Calendar
import java.util.Date
import java.util.Locale
import org.json.JSONObject


@Composable
internal fun ChatCallHistoryScreen(
    store: MessengerStore,
    chatId: String,
    onBack: () -> Unit,
) {
    val cache by remember(chatId) { store.timelineFor(chatId) }.collectAsStateWithLifecycle()
    val chats by store.chats.collectAsStateWithLifecycle()
    val peerLabel = run {
        val pid = chats.firstOrNull { it.chatId == chatId }?.peerId.orEmpty()
        if (pid.isEmpty()) "Calls" else store.peerLabelFor(pid)
    }
    
    
    val calls = remember(cache.events) {
        cache.events.asReversed().filter { it.kind == EventKind.CALL_SUMMARY }
    }
    val listState = rememberLazyListState()

    LaunchedEffect(chatId) {
        
        
        store.loadTimeline(chatId)
    }
    
    
    val approachingEnd by remember {
        derivedStateOf {
            val layout = listState.layoutInfo
            val lastVisible = layout.visibleItemsInfo.lastOrNull()?.index ?: -1
            lastVisible >= 0 && lastVisible >= layout.totalItemsCount - 4
        }
    }
    LaunchedEffect(approachingEnd, cache.hasMore, cache.loading) {
        if (approachingEnd && cache.hasMore && !cache.loading) {
            store.loadTimeline(chatId)
        }
    }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .background(ChatPalette.Surface),
    ) {
        Header(label = peerLabel, onBack = onBack)
        if (calls.isEmpty() && !cache.loading) {
            Box(modifier = Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
                Text(
                    text = "No calls yet",
                    color = ChatPalette.TextDim,
                    fontSize = 14.sp,
                )
            }
        } else {
            LazyColumn(
                state = listState,
                modifier = Modifier.fillMaxSize(),
            ) {
                items(items = calls, key = { it.msgId.ifEmpty { it.recvSeq.toString() } }) { ev ->
                    CallRow(ev)
                }
            }
        }
    }
}

@Composable
private fun Header(label: String, onBack: () -> Unit) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .background(ChatPalette.InboundBubble)
            .padding(horizontal = 12.dp, vertical = 6.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(4.dp),
    ) {
        Text(
            text = "‹",
            color = ChatPalette.Accent,
            fontSize = 22.sp,
            fontWeight = FontWeight.Bold,
            modifier = Modifier
                .clickable(onClick = onBack)
                .padding(horizontal = 8.dp, vertical = 4.dp),
        )
        Spacer(modifier = Modifier.width(12.dp))
        Text(
            text = "Calls — $label",
            color = ChatPalette.Text,
            fontWeight = FontWeight.SemiBold,
            fontSize = 16.sp,
            modifier = Modifier.weight(1f),
        )
    }
}

@Composable
private fun CallRow(event: TimelineEvent) {
    val body = event.body ?: return
    val direction = body.optString("direction")
    val outcome = body.optString("outcome")
    val isVideo = isVideoModality(body)
    val dirGlyph = if (direction == "in") "↓" else "↑"
    val modality = if (isVideo) "video" else "audio"
    val outcomeLabel = outcomeLabel(outcome, body)
    val accent = if (outcome == "completed") ChatPalette.CallSummaryOk else ChatPalette.CallSummaryBad
    val tsLabel = formatCallTimestamp(event.displayTs)

    Row(
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = 16.dp, vertical = 10.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(
            text = dirGlyph,
            color = accent,
            fontSize = 18.sp,
            fontWeight = FontWeight.Bold,
            modifier = Modifier.width(20.dp),
        )
        Column(modifier = Modifier.weight(1f)) {
            Text(
                text = "$modality · $outcomeLabel",
                color = ChatPalette.Text,
                fontSize = 14.sp,
                fontWeight = FontWeight.SemiBold,
            )
            Text(
                text = tsLabel,
                color = ChatPalette.TextDim,
                fontSize = 12.sp,
            )
        }
    }
}

private fun isVideoModality(body: JSONObject): Boolean {
    val modalities = body.optJSONArray("modalities") ?: return false
    for (i in 0 until modalities.length()) {
        if (modalities.optString(i) == "video") return true
    }
    return false
}

private fun outcomeLabel(outcome: String, body: JSONObject): String = when (outcome) {
    "completed" -> formatCallDuration(body.optLong("duration_seconds", 0L))
    "missed" -> "missed"
    "cancelled" -> "cancelled"
    "rejected" -> "declined"
    "failed" -> "failed"
    "disrupted" -> "disrupted"
    else -> outcome.ifEmpty { "—" }
}

private fun formatCallDuration(secs: Long): String {
    if (secs <= 0) return "0s"
    val h = secs / 3600
    val m = (secs % 3600) / 60
    val s = secs % 60
    return when {
        h > 0 -> "${h}h${m}m${s}s"
        m > 0 -> "${m}m${s}s"
        else -> "${s}s"
    }
}

private val HM_FORMATTER = ThreadLocal.withInitial { SimpleDateFormat("HH:mm", Locale.US) }
private val MONTH_DAY_FORMATTER = ThreadLocal.withInitial { SimpleDateFormat("MMM d, HH:mm", Locale.US) }
private val FULL_DATE_FORMATTER = ThreadLocal.withInitial { SimpleDateFormat("yyyy-MM-dd HH:mm", Locale.US) }


private fun formatCallTimestamp(unixSeconds: Long): String {
    if (unixSeconds <= 0L) return ""
    val now = Calendar.getInstance()
    val then = Calendar.getInstance().apply { time = Date(unixSeconds * 1000L) }
    val sameYear = now.get(Calendar.YEAR) == then.get(Calendar.YEAR)
    val sameDay = sameYear && now.get(Calendar.DAY_OF_YEAR) == then.get(Calendar.DAY_OF_YEAR)
    val yesterday = sameYear &&
        now.get(Calendar.DAY_OF_YEAR) - then.get(Calendar.DAY_OF_YEAR) == 1
    val hm = HM_FORMATTER.get()!!.format(then.time)
    return when {
        sameDay -> "Today $hm"
        yesterday -> "Yesterday $hm"
        sameYear -> MONTH_DAY_FORMATTER.get()!!.format(then.time)
        else -> FULL_DATE_FORMATTER.get()!!.format(then.time)
    }
}
