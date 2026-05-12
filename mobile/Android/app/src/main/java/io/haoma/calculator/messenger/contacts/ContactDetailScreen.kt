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
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Checkbox
import androidx.compose.material3.CheckboxDefaults
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import io.haoma.calculator.messenger.*
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.PeerAction


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

    Column(
        modifier = Modifier
            .fillMaxSize()
            .background(BG_BASE)
            .verticalScroll(rememberScrollState()),
    ) {
        Header(
            title = peer.label.ifEmpty { shortPeerId(peer.id) },
            retired = retired,
            inCall = inCall,
            store = store,
            onBack = onBack,
        )

        IdentityFooter(nick = peer.nick)

        Section(label = "Alias (local)") {
            OutlinedTextField(
                value = alias,
                onValueChange = { alias = it },
                singleLine = true,
                enabled = !retired,
                placeholder = {
                    Text(
                        text = "(no alias — falls back to nick / id)",
                        color = FG_DIM,
                        fontSize = 13.sp,
                    )
                },
                colors = textFieldColors(),
                modifier = Modifier.fillMaxWidth(),
            )
            Spacer(modifier = Modifier.height(8.dp))
            Row {
                Button(
                    enabled = !retired && aliasChanged,
                    onClick = {
                        store.setAlias(peerId, alias)
                    },
                    colors = ButtonDefaults.buttonColors(
                        containerColor = BTN_PRIMARY,
                        contentColor = BG_BASE,
                        disabledContainerColor = BTN_DIM,
                        disabledContentColor = FG_DIM,
                    ),
                ) {
                    Text("Save alias")
                }
                Spacer(modifier = Modifier.width(12.dp))
                TextButton(
                    enabled = aliasChanged,
                    onClick = { alias = peer.alias },
                ) {
                    Text("Reset", color = if (aliasChanged) FG_LINK else FG_DIM)
                }
            }
        }

        Section(label = "Peer ID") {
            
            
            Text(
                text = peer.id,
                color = FG_VALUE_DIM,
                fontFamily = FontFamily.Monospace,
                fontSize = 13.sp,
                modifier = Modifier.fillMaxWidth(),
            )
            Spacer(modifier = Modifier.height(6.dp))
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
                color = if (fpReady) FG_VALUE_DIM else FG_DIM,
                fontFamily = FontFamily.Monospace,
                fontSize = 13.sp,
                modifier = Modifier.fillMaxWidth(),
            )
            if (fpReady) {
                Spacer(modifier = Modifier.height(6.dp))
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
            .background(BG_BAR)
            .padding(horizontal = 12.dp, vertical = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(
            text = "‹",
            color = FG_LINK,
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
                color = FG_IN_CALL,
                fontSize = 16.sp,
                fontWeight = FontWeight.Bold,
            )
            Spacer(modifier = Modifier.width(8.dp))
        }
        Text(
            text = title + if (retired) " (retired)" else "",
            color = if (inCall) FG_IN_CALL else FG_PRIMARY,
            fontWeight = FontWeight.SemiBold,
            fontSize = 17.sp,
            modifier = Modifier.weight(1f),
        )
        io.haoma.calculator.messenger.calls.CallChip(store = store)
    }
}

@Composable
private fun Section(label: String, content: @Composable () -> Unit) {
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = 16.dp, vertical = 14.dp),
    ) {
        Text(
            text = label.uppercase(),
            color = FG_DIM,
            fontSize = 11.sp,
            fontWeight = FontWeight.SemiBold,
        )
        Spacer(modifier = Modifier.height(8.dp))
        content()
    }
    HorizontalDivider(color = DIVIDER, thickness = 0.5.dp)
}

@Composable
private fun IdentityFooter(nick: String) {
    if (nick.isEmpty()) return
    Section(label = "Peer-declared nick") {
        Text(
            text = nick,
            color = FG_PRIMARY,
            fontSize = 14.sp,
        )
    }
}

@Composable
private fun CopyLink(label: String, onClick: () -> Unit) {
    Text(
        text = label,
        color = FG_LINK,
        fontSize = 13.sp,
        fontWeight = FontWeight.Medium,
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
            .padding(horizontal = 16.dp, vertical = 14.dp),
    ) {
        Text(
            text = "DANGER",
            color = C_DANGER,
            fontSize = 11.sp,
            fontWeight = FontWeight.SemiBold,
        )
        Spacer(modifier = Modifier.height(8.dp))
        Row(verticalAlignment = Alignment.CenterVertically) {
            Checkbox(
                checked = risksAcked,
                onCheckedChange = onRiskCheck,
                colors = CheckboxDefaults.colors(
                    checkedColor = C_DANGER,
                    uncheckedColor = FG_DIM,
                    checkmarkColor = BG_BASE,
                ),
            )
            Spacer(modifier = Modifier.width(4.dp))
            Text(
                text = "I understand risks",
                color = FG_PRIMARY,
                fontSize = 14.sp,
            )
        }
        Spacer(modifier = Modifier.height(12.dp))
        if (!retired) {
            DangerButton(
                label = "Unpair",
                enabled = risksAcked,
                onClick = onUnpair,
            )
            Spacer(modifier = Modifier.height(8.dp))
        }
        DangerButton(
            label = "Delete peer",
            enabled = risksAcked,
            onClick = onDelete,
        )
    }
}

@Composable
private fun DangerButton(label: String, enabled: Boolean, onClick: () -> Unit) {
    Button(
        enabled = enabled,
        onClick = onClick,
        colors = ButtonDefaults.buttonColors(
            containerColor = C_DANGER,
            contentColor = BG_BASE,
            disabledContainerColor = BTN_DIM,
            disabledContentColor = FG_DIM,
        ),
        modifier = Modifier.fillMaxWidth(),
    ) {
        Text(label)
    }
}

@Composable
private fun textFieldColors() = OutlinedTextFieldDefaults.colors(
    focusedTextColor = FG_PRIMARY,
    unfocusedTextColor = FG_PRIMARY,
    disabledTextColor = FG_DIM,
    cursorColor = FG_LINK,
    focusedBorderColor = FG_LINK,
    unfocusedBorderColor = DIVIDER,
    disabledBorderColor = DIVIDER,
)


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


private val BG_BASE = Color(0xFF1D2021)
private val BG_BAR = Color(0xFF282828)
private val DIVIDER = Color(0xFF3C3836)
private val FG_PRIMARY = Color(0xFFEBDBB2)
private val FG_DIM = Color(0xFF7C6F64)
private val FG_VALUE_DIM = Color(0xFFA89984) 
private val FG_LINK = Color(0xFF83A598)
private val FG_IN_CALL = Color(0xFFCC241D)
private val BTN_PRIMARY = Color(0xFF5FCC1A)
private val BTN_DIM = Color(0xFF504945)
private val C_DANGER = Color(0xFFCC241D) 
