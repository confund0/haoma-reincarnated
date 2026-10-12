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
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import io.haoma.calculator.messenger.*
import io.haoma.calculator.messenger.HaomaPalette
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.PeerEntry


@Composable
fun ContactsTab(store: MessengerStore) {
    val allPeers by store.peers.collectAsStateWithLifecycle()
    val presence by store.presence.collectAsStateWithLifecycle()

    val peers = remember(allPeers) { contactsToDraw(allPeers) }
    val activeCalls by store.activeCalls.collectAsStateWithLifecycle()
    val freshPeers by store.freshPeers.collectAsStateWithLifecycle()
    val nowSeconds = System.currentTimeMillis() / 1000L
    val inCallPeers = remember(activeCalls) {
        activeCalls.values
            .filter { it.status == CallStatus.Accepted }
            .map { it.peerId }
            .toHashSet()
    }

    
    val pairing by store.inheritedPairing.collectAsStateWithLifecycle()
    val invitations by store.invitations.collectAsStateWithLifecycle()
    val connected by store.connection.collectAsStateWithLifecycle()
    val siblingPresence = allPeers.firstOrNull { it.sibling }?.let { contactPresence(it, presence) }
    val acceptable = invitations.acceptable(nowSeconds)
    val showInvitations = acceptable.isNotEmpty() || invitations.listFailure.isNotEmpty() ||
        invitations.acceptingMsgId.isNotEmpty() || invitations.outcome != null

    Column(modifier = Modifier.fillMaxSize().background(HaomaPalette.BG_BASE)) {
        TabHeader(title = "Contacts", store = store)
        if (peers.isEmpty() && !pairing.showSection && !showInvitations) {
            EmptyContactsSurface()
            return@Column
        }
        
        
        val breakFollows = showInvitations || pairing.showSection
        LazyColumn(modifier = Modifier.fillMaxSize()) {
            if (peers.isEmpty()) {
                item(key = "no-paired-contacts") {
                    Text(
                        text = "No paired contacts on this device yet.",
                        color = HaomaPalette.FG_DIM,
                        fontSize = 14.sp,
                        modifier = Modifier.padding(horizontal = 16.dp, vertical = 12.dp),
                    )
                    if (!breakFollows) {
                        HorizontalDivider(color = HaomaPalette.DIVIDER, thickness = 0.5.dp)
                    }
                }
            }
            items(peers, key = { it.id }) { peer ->
                ContactRow(
                    peer = peer,
                    presence = contactPresence(peer, presence),
                    nowSeconds = nowSeconds,
                    inCall = peer.id in inCallPeers,
                    isFresh = peer.id in freshPeers,
                    onOpen = {
                        store.clearFreshPeer(peer.id)
                        store.openChatForPeer(peer.id)
                    },
                    onEdit = {
                        store.clearFreshPeer(peer.id)
                        store.openContactDetail(peer.id)
                    },
                )
                if (!breakFollows || peer.id != peers.last().id) {
                    HorizontalDivider(color = HaomaPalette.DIVIDER, thickness = 0.5.dp)
                }
            }
            if (showInvitations) {
                item(key = "invitations") {
                    SectionBreak()
                    InvitationsSection(
                        store = store,
                        view = invitations,
                        acceptable = acceptable,
                        connected = connected,
                        endLine = !pairing.showSection,
                    )
                }
            }
            
            
            if (pairing.showSection) {
                item(key = "linked-device") {
                    SectionBreak()
                    LinkedDeviceSection(
                        store = store,
                        pairing = pairing,
                        siblingPresence = siblingPresence,
                    )
                }
            }
        }
    }
}

@Composable
private fun TabHeader(title: String, store: MessengerStore) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .background(HaomaPalette.BG_BAR)
            .padding(horizontal = 16.dp, vertical = 10.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(
            text = title,
            color = HaomaPalette.FG_PRIMARY,
            fontWeight = FontWeight.SemiBold,
            fontSize = 17.sp,
            modifier = Modifier.weight(1f),
        )
        io.haoma.calculator.messenger.calls.CallChip(store = store)
    }
}

@Composable
private fun ContactRow(
    peer: PeerEntry,
    presence: PresenceView,
    nowSeconds: Long,
    inCall: Boolean,
    isFresh: Boolean,
    onOpen: () -> Unit,
    onEdit: () -> Unit,
) {
    val retired = peer.retiredAt != 0L
    val displayLabel = displayLabelFor(peer, retired)
    val labelColor = contactLabelColour(
        inCall = inCall,
        retired = retired,
        unreachable = peer.unreachable,
        isFresh = isFresh,
        chatty = presence.chatty,
        unnamed = peer.alias.isEmpty() && peer.nick.isEmpty(),
    )
    val labelStyle = if (peer.alias.isEmpty() && peer.nick.isEmpty()) FontStyle.Italic else FontStyle.Normal

    Row(
        modifier = Modifier
            .fillMaxWidth()
            .clickable(enabled = !retired, onClick = onOpen)
            .padding(horizontal = 12.dp, vertical = 10.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        PresenceArrows(view = presence, dim = retired)
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
                )
            }
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
            color = if (retired) HaomaPalette.FG_DIM else HaomaPalette.FG_LINK,
            fontSize = 14.sp,
            fontWeight = FontWeight.Medium,
            modifier = Modifier
                .clickable(onClick = onEdit)
                .padding(horizontal = 6.dp, vertical = 4.dp),
        )
    }
}

@Composable
private fun LastSeenLine(active: Long, passive: Long, nowSeconds: Long, retired: Boolean) {
    if (retired) {
        Text(
            text = "retired",
            color = HaomaPalette.FG_DIM,
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
        color = HaomaPalette.FG_DIM,
        fontSize = 12.sp,
        fontFamily = FontFamily.Monospace,
    )
}

@Composable
private fun EmptyContactsSurface() {
    Box(
        modifier = Modifier
            .fillMaxSize()
            .background(HaomaPalette.BG_BASE)
            .padding(24.dp),
        contentAlignment = Alignment.Center,
    ) {
        Column(
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            Text(
                text = "No contacts yet",
                color = HaomaPalette.FG_PRIMARY,
                fontWeight = FontWeight.SemiBold,
                fontSize = 18.sp,
            )
            Text(
                text = "Pair a peer in the Invites tab — paired contacts appear here.",
                color = HaomaPalette.FG_DIM,
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


internal fun contactLabelColour(
    inCall: Boolean,
    retired: Boolean,
    unreachable: Boolean,
    isFresh: Boolean,
    chatty: String,
    unnamed: Boolean,
): Color {
    val nameColour = presenceNameColour(chatty)
    return when {
        inCall -> FG_IN_CALL
        retired -> HaomaPalette.FG_DIM
        unreachable -> HaomaPalette.C_ATTENTION
        chatty == "busy" -> HaomaPalette.C_PRESENCE_BUSY
        isFresh -> FG_FRESH
        nameColour != null -> nameColour
        unnamed -> HaomaPalette.FG_DIM
        else -> HaomaPalette.FG_PRIMARY
    }
}

internal fun shortPeerId(id: String): String =
    if (id.length > 8) id.substring(0, 8) else id


private val FG_IN_CALL = Color(0xFFCC241D)


private val FG_FRESH = Color(0xFF5FCC1A)
