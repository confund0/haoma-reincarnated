package io.haoma.calculator.messenger.contacts

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Checkbox
import androidx.compose.material3.CheckboxDefaults
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import io.haoma.calculator.messenger.CallStatus
import io.haoma.calculator.messenger.CtaButton
import io.haoma.calculator.messenger.DangerButton
import io.haoma.calculator.messenger.HaomaPalette
import io.haoma.calculator.messenger.HaomaTextField
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.PeerAction
import io.haoma.calculator.messenger.Section
import io.haoma.calculator.messenger.getPeerFingerprint
import io.haoma.calculator.messenger.peerAction
import io.haoma.calculator.messenger.setAlias


@Composable
fun ContactDetailScreen(
    store: MessengerStore,
    peerId: String,
    onBack: () -> Unit,
) {
    val peers by store.peers.collectAsStateWithLifecycle()
    val activeCalls by store.activeCalls.collectAsStateWithLifecycle()
    val peer = peers.firstOrNull { it.id == peerId }
    val inCall = activeCalls.values.any {
        it.peerId == peerId && it.status == CallStatus.Accepted
    }

    if (peer == null) {
        LaunchedEffect(Unit) { onBack() }
        return
    }

    val retired = peer.retiredAt != 0L
    val clipboard = LocalClipboardManager.current
    var alias by remember(peer.alias) { mutableStateOf(peer.alias) }
    var risksAcked by remember { mutableStateOf(false) }
    var fingerprint by remember(peerId) { mutableStateOf<String?>(null) }
    var fingerprintLoaded by remember(peerId) { mutableStateOf(false) }

    LaunchedEffect(peerId) {
        fingerprint = store.getPeerFingerprint(peerId)
        fingerprintLoaded = true
    }

    val aliasChanged by remember(peer.alias, alias) {
        derivedStateOf { alias != peer.alias }
    }

    Column(modifier = Modifier.fillMaxSize().background(HaomaPalette.BG_BASE)) {
        Header(
            title = peer.label.ifEmpty { shortPeerId(peer.id) },
            retired = retired,
            inCall = inCall,
            store = store,
            onBack = onBack,
        )

        
        Column(modifier = Modifier.weight(1f).verticalScroll(rememberScrollState())) {
            IdentityFooter(nick = peer.nick)

            Section(
                label = "Alias (local)",
                description = if (retired) {
                    "Retired peers can't be renamed. Re-pair to give them a new local label."
                } else {
                    "Only stored on this device. The peer never sees it; the daemon falls back to their declared nick or short id when empty."
                },
            ) {
                HaomaTextField(
                    value = alias,
                    onValueChange = { alias = it },
                    placeholder = "(no alias — falls back to nick / id)",
                    modifier = Modifier.fillMaxWidth(),
                )
                Spacer(modifier = Modifier.height(12.dp))
                Row(
                    horizontalArrangement = Arrangement.spacedBy(12.dp),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    CtaButton(
                        label = "Save alias",
                        accent = HaomaPalette.BTN_PRIMARY,
                        enabled = !retired && aliasChanged,
                    ) {
                        
                        
                        store.setAlias(peerId, alias)
                    }
                    Text(
                        text = "Reset",
                        color = if (aliasChanged) HaomaPalette.FG_LINK else HaomaPalette.FG_DIM,
                        fontSize = 13.sp,
                        fontWeight = FontWeight.SemiBold,
                        modifier = Modifier
                            .clickable(enabled = aliasChanged) { alias = peer.alias }
                            .padding(horizontal = 8.dp, vertical = 10.dp),
                    )
                }
            }

            Section(label = "Peer ID") {
                Text(
                    text = peer.id,
                    color = HaomaPalette.FG_SECONDARY,
                    fontFamily = FontFamily.Monospace,
                    fontSize = 13.sp,
                    modifier = Modifier.fillMaxWidth(),
                )
                Spacer(modifier = Modifier.height(8.dp))
                CopyLink(label = "Copy peer ID") {
                    clipboard.setText(AnnotatedString(peer.id))
                }
            }

            Section(label = "Fingerprint") {
                val fpDisplay = when {
                    !fingerprintLoaded -> "(loading…)"
                    fingerprint == null -> "(unavailable)"
                    fingerprint!!.isEmpty() -> "(no session yet — exchange a message first)"
                    else -> formatFingerprint(fingerprint!!)
                }
                val fpReady = fingerprintLoaded && !fingerprint.isNullOrEmpty()
                Text(
                    text = fpDisplay,
                    color = if (fpReady) HaomaPalette.FG_SECONDARY else HaomaPalette.FG_DIM,
                    fontFamily = FontFamily.Monospace,
                    fontSize = 13.sp,
                    modifier = Modifier.fillMaxWidth(),
                )
                if (fpReady) {
                    Spacer(modifier = Modifier.height(8.dp))
                    CopyLink(label = "Copy fingerprint") {
                        clipboard.setText(AnnotatedString(formatFingerprint(fingerprint!!)))
                    }
                }
            }

            DangerSection(
                retired = retired,
                risksAcked = risksAcked,
                onRiskCheck = { risksAcked = it },
                onUnpair = {
                    store.peerAction(peerId, PeerAction.Retire)
                    onBack()
                },
                onDelete = {
                    store.peerAction(peerId, PeerAction.Delete)
                    onBack()
                },
            )

            Spacer(modifier = Modifier.height(24.dp))
        }
    }
}

@Composable
private fun Header(
    title: String,
    retired: Boolean,
    inCall: Boolean,
    store: MessengerStore,
    onBack: () -> Unit,
) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .background(HaomaPalette.BG_BAR)
            .padding(horizontal = 12.dp, vertical = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(
            text = "‹",
            color = HaomaPalette.FG_LINK,
            fontSize = 22.sp,
            fontWeight = FontWeight.Bold,
            modifier = Modifier
                .clickable(onClick = onBack)
                .padding(horizontal = 8.dp, vertical = 4.dp),
        )
        Spacer(modifier = Modifier.width(20.dp))
        if (inCall) {
            Text(
                text = "☎",
                color = HaomaPalette.C_DANGER,
                fontSize = 16.sp,
                fontWeight = FontWeight.Bold,
            )
            Spacer(modifier = Modifier.width(8.dp))
        }
        Text(
            text = title + if (retired) " (retired)" else "",
            color = if (inCall) HaomaPalette.C_DANGER else HaomaPalette.FG_PRIMARY,
            fontWeight = FontWeight.SemiBold,
            fontSize = 17.sp,
            modifier = Modifier.weight(1f),
        )
        io.haoma.calculator.messenger.calls.CallChip(store = store)
    }
}

@Composable
private fun IdentityFooter(nick: String) {
    if (nick.isEmpty()) return
    Section(label = "Peer-declared nick") {
        Text(
            text = nick,
            color = HaomaPalette.BTN_GIVE,
            fontSize = 14.sp,
            fontWeight = FontWeight.SemiBold,
        )
    }
}

@Composable
private fun CopyLink(label: String, onClick: () -> Unit) {
    Text(
        text = label,
        color = HaomaPalette.FG_LINK,
        fontSize = 13.sp,
        fontWeight = FontWeight.SemiBold,
        modifier = Modifier
            .clickable(onClick = onClick)
            .padding(vertical = 4.dp),
    )
}

@Composable
private fun DangerSection(
    retired: Boolean,
    risksAcked: Boolean,
    onRiskCheck: (Boolean) -> Unit,
    onUnpair: () -> Unit,
    onDelete: () -> Unit,
) {
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = 16.dp, vertical = 16.dp),
    ) {
        Text(
            text = "DANGER",
            color = HaomaPalette.C_DANGER,
            fontSize = 13.sp,
            fontWeight = FontWeight.SemiBold,
        )
        Spacer(modifier = Modifier.height(10.dp))
        Row(verticalAlignment = Alignment.CenterVertically) {
            Checkbox(
                checked = risksAcked,
                onCheckedChange = onRiskCheck,
                colors = CheckboxDefaults.colors(
                    checkedColor = HaomaPalette.C_DANGER,
                    uncheckedColor = HaomaPalette.FG_DIM,
                    checkmarkColor = HaomaPalette.BG_BASE,
                ),
            )
            Spacer(modifier = Modifier.width(8.dp))
            Text(
                text = "I understand risks",
                color = HaomaPalette.FG_PRIMARY,
                fontSize = 14.sp,
            )
        }
        Spacer(modifier = Modifier.height(12.dp))
        if (!retired) {
            DangerButton(
                label = "Unpair",
                enabled = risksAcked,
                modifier = Modifier.fillMaxWidth(),
                onClick = onUnpair,
            )
            Spacer(modifier = Modifier.height(8.dp))
        }
        DangerButton(
            label = "Delete peer",
            enabled = risksAcked,
            modifier = Modifier.fillMaxWidth(),
            onClick = onDelete,
        )
    }
    HorizontalDivider(color = HaomaPalette.DIVIDER, thickness = 0.5.dp)
}


internal fun formatFingerprint(hex: String): String {
    val groupSize = 6
    val groups = ArrayList<String>(hex.length / groupSize + 1)
    var i = 0
    while (i < hex.length) {
        val end = minOf(i + groupSize, hex.length)
        groups += hex.substring(i, end)
        i = end
    }
    return groups.joinToString(" ")
}
