package io.haoma.calculator.messenger.contacts

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
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
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
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.PeerEntry


@Composable
fun ContactsTab(store: MessengerStore) {
    val peers by store.peers.collectAsStateWithLifecycle()
    val presence by store.presence.collectAsStateWithLifecycle()
    val nowSeconds = System.currentTimeMillis() / 1000L

    Column(modifier = Modifier.fillMaxSize().background(BG_BASE)) {
        TabHeader(title = "Contacts")
        if (peers.isEmpty()) {
            EmptyContactsSurface()
            return@Column
        }
        LazyColumn(modifier = Modifier.fillMaxSize()) {
            items(peers, key = { it.id }) { peer ->
                ContactRow(
                    peer = peer,
                    presenceLabel = presence[peer.id] ?: peer.effective.ifEmpty { "unknown" },
                    nowSeconds = nowSeconds,
                    onOpen = { store.openChatForPeer(peer.id) },
                    onEdit = { store.openContactDetail(peer.id) },
                )
                HorizontalDivider(color = DIVIDER, thickness = 0.5.dp)
            }
        }
    }
}

@Composable
private fun TabHeader(title: String) {
    Box(
        modifier = Modifier
            .fillMaxWidth()
            .background(BG_BAR)
            .padding(horizontal = 16.dp, vertical = 10.dp),
    ) {
        Text(
            text = title,
            color = FG_PRIMARY,
            fontWeight = FontWeight.SemiBold,
            fontSize = 17.sp,
        )
    }
}

@Composable
private fun ContactRow(
    peer: PeerEntry,
    presenceLabel: String,
    nowSeconds: Long,
    onOpen: () -> Unit,
    onEdit: () -> Unit,
) {
    val retired = peer.retiredAt != 0L
    val displayLabel = displayLabelFor(peer, retired)
    val labelColor = when {
        retired -> FG_DIM
        peer.alias.isEmpty() && peer.nick.isEmpty() -> FG_DIM
        else -> FG_PRIMARY
    }
    val labelStyle = if (peer.alias.isEmpty() && peer.nick.isEmpty()) FontStyle.Italic else FontStyle.Normal

    Row(
        modifier = Modifier
            .fillMaxWidth()
            .clickable(enabled = !retired, onClick = onOpen)
            .padding(horizontal = 12.dp, vertical = 10.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        PresenceDot(label = presenceLabel, retired = retired)
        Spacer(modifier = Modifier.width(10.dp))
        Column(
            modifier = Modifier.weight(1f),
            verticalArrangement = Arrangement.spacedBy(2.dp),
        ) {
            Text(
                text = displayLabel,
                color = labelColor,
                fontStyle = labelStyle,
                fontWeight = FontWeight.SemiBold,
                fontSize = 15.sp,
            )
            LastSeenLine(
                active = peer.lastActiveAt,
                passive = peer.lastPassiveAt,
                nowSeconds = nowSeconds,
                retired = retired,
            )
        }
        Spacer(modifier = Modifier.width(10.dp))
        Text(
            text = "Edit",
            color = if (retired) FG_DIM else FG_LINK,
            fontSize = 14.sp,
            fontWeight = FontWeight.Medium,
            modifier = Modifier
                .clickable(onClick = onEdit)
                .padding(horizontal = 6.dp, vertical = 4.dp),
        )
    }
}

@Composable
private fun PresenceDot(label: String, retired: Boolean) {
    val color = when {
        retired -> FG_DIM
        else -> presenceColor(label)
    }
    Box(
        modifier = Modifier
            .size(10.dp)
            .clip(CircleShape)
            .background(color),
    )
}

@Composable
private fun LastSeenLine(active: Long, passive: Long, nowSeconds: Long, retired: Boolean) {
    if (retired) {
        Text(
            text = "retired",
            color = FG_DIM,
            fontSize = 12.sp,
            fontFamily = FontFamily.Monospace,
        )
        return
    }
    val activeText = RelativeTime.format(nowSeconds, active)
    val passiveText = RelativeTime.format(nowSeconds, passive)
    
    
    if (active == 0L && passive == 0L) return
    Text(
        text = "act:$activeText · psv:$passiveText",
        color = FG_DIM,
        fontSize = 12.sp,
        fontFamily = FontFamily.Monospace,
    )
}

@Composable
private fun EmptyContactsSurface() {
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
                text = "No contacts yet",
                color = FG_PRIMARY,
                fontWeight = FontWeight.SemiBold,
                fontSize = 18.sp,
            )
            Text(
                text = "Pair a peer in the Invites tab — paired contacts appear here.",
                color = FG_DIM,
                fontSize = 14.sp,
            )
        }
    }
}


private fun displayLabelFor(peer: PeerEntry, retired: Boolean): String {
    val resolved = peer.label.ifEmpty {
        when {
            peer.alias.isNotEmpty() -> peer.alias
            peer.nick.isNotEmpty() -> peer.nick
            else -> shortPeerId(peer.id)
        }
    }
    return if (retired) "$resolved (retired)" else resolved
}

internal fun shortPeerId(id: String): String =
    if (id.length > 8) id.substring(0, 8) else id

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


private val C_AVAILABLE = Color(0xFF5FCC1A) 
private val C_AWAY = Color(0xFFFABD2F) 
private val C_BUSY = Color(0xFFCC241D) 
private val C_ACCEPTING = Color(0xFF5F87FF) 
private val C_UNKNOWN = Color(0xFF504945) 
