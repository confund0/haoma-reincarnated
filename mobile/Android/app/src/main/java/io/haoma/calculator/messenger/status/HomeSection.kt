package io.haoma.calculator.messenger.status

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableLongStateOf
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
import io.haoma.calculator.core.BinaryFingerprints
import io.haoma.calculator.core.FingerprintRow
import io.haoma.calculator.messenger.ExternalReach
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.PeerEntry
import io.haoma.calculator.messenger.SelfReach
import io.haoma.calculator.messenger.SystemHealth
import io.haoma.calculator.messenger.SystemInfoComponent
import io.haoma.calculator.messenger.SystemInfoResponse
import io.haoma.calculator.messenger.TorHealth
import kotlinx.coroutines.delay
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import java.util.TimeZone


@Composable
fun HomeSection(store: MessengerStore) {
    val health by store.health.collectAsStateWithLifecycle()
    val connected by store.connection.collectAsStateWithLifecycle()
    val systemInfo by store.systemInfo.collectAsStateWithLifecycle()
    val fingerprints by store.fingerprints.collectAsStateWithLifecycle()
    val peers by store.peers.collectAsStateWithLifecycle()

    
    var now by remember { mutableLongStateOf(System.currentTimeMillis()) }
    LaunchedEffect(Unit) {
        while (true) {
            now = System.currentTimeMillis()
            delay(UPTIME_TICK_MS)
        }
    }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .background(BG_BASE)
            .verticalScroll(rememberScrollState())
            .padding(horizontal = 12.dp, vertical = 8.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        ConnectivityCard(connected = connected, health = health, peers = peers, nowMs = now)
        IdentityCard(health = health, systemInfo = systemInfo, nowMs = now)
        FingerprintCard(snapshot = fingerprints)
    }
}


@Composable
private fun ConnectivityCard(connected: Boolean, health: SystemHealth, peers: List<PeerEntry>, nowMs: Long) {
    Card(title = "Connectivity") {
        Row2(
            label = "IPC ↔ haoma",
            text = if (connected) "ok" else "down",
            tone = if (connected) Tone.OK else Tone.BAD,
        )
        Row2(
            label = "haomad reachable",
            text = if (health.backendReachable) "ok" else "unreachable",
            tone = if (health.backendReachable) Tone.OK else Tone.BAD,
        )
        val torText = when {
            health.tor.unreachable -> "unreachable"
            !health.tor.ready -> "bootstrapping ${health.tor.bootstrap}%"
            else -> "ready (100%)"
        }
        val torTone = when {
            health.tor.unreachable -> Tone.BAD
            !health.tor.ready -> Tone.WARN
            else -> Tone.OK
        }
        Row2(label = "Tor bootstrap", text = torText, tone = torTone)
        ExternalProbeRow(reach = health.externalReach, nowMs = nowMs)
        SelfProbeRow(reach = health.selfReach, peers = peers, nowMs = nowMs)
    }
}

@Composable
private fun ExternalProbeRow(reach: ExternalReach?, nowMs: Long) {
    if (reach == null) {
        Row2(label = "Tor external probe", text = "pending", tone = Tone.WARN)
        return
    }
    val ageSecs = ((nowMs / 1000L) - reach.at).coerceAtLeast(0)
    val tone = if (reach.ok) Tone.OK else Tone.BAD
    val verb = if (reach.ok) "ok" else "failed"
    Row2(
        label = "Tor external probe",
        text = "$verb · ${humanAge(ageSecs)} ago",
        tone = tone,
    )
}

@Composable
private fun SelfProbeRow(reach: Map<String, SelfReach>, peers: List<PeerEntry>, nowMs: Long) {
    if (reach.isEmpty()) {
        val label = if (peers.none { it.retiredAt == 0L }) {
            "no peers paired yet"
        } else {
            "pending"
        }
        Row2(label = "Tor self-probe", text = label, tone = Tone.WARN)
        return
    }
    val freshest = reach.entries.maxByOrNull { it.value.at } ?: return
    val peer = peers.firstOrNull { it.id == freshest.key }
    val peerLabel = peer?.label?.takeIf { it.isNotEmpty() }
        ?: freshest.key.take(8)
    val ageSecs = ((nowMs / 1000L) - freshest.value.at).coerceAtLeast(0)
    val tone = if (freshest.value.ok) Tone.OK else Tone.BAD
    val verb = if (freshest.value.ok) "ok" else "failed"
    Row2(
        label = "Tor self-probe ($peerLabel)",
        text = "$verb · ${humanAge(ageSecs)} ago",
        tone = tone,
    )
}


@Composable
private fun IdentityCard(health: SystemHealth, systemInfo: SystemInfoResponse?, nowMs: Long) {
    Card(title = "Identity & versions") {
        val nick = health.selfNick.ifEmpty { "(unset)" }
        Row2(
            label = "Self-nick",
            text = nick,
            tone = if (health.selfNick.isEmpty() || health.selfNickIsDefault) Tone.WARN else Tone.OK,
        )
        VersionRow(label = "haoma", c = systemInfo?.haoma, nowMs = nowMs)
        VersionRow(label = "haomad", c = systemInfo?.haomad, nowMs = nowMs)
    }
}

@Composable
private fun VersionRow(label: String, c: SystemInfoComponent?, nowMs: Long) {
    if (c == null || c.version.isEmpty()) {
        Row2(label = label, text = "(unwired)", tone = Tone.DIM)
        return
    }
    val startedMs = parseRfc3339OrZero(c.startedAt)
    val uptimeText = if (startedMs == 0L) "?" else humanAge(((nowMs - startedMs) / 1000L).coerceAtLeast(0))
    Row2(
        label = label,
        text = "${c.version} · up $uptimeText",
        tone = Tone.OK,
    )
}


@Composable
private fun FingerprintCard(snapshot: BinaryFingerprints?) {
    val clipboard = LocalClipboardManager.current
    Card(title = "Binaries (SHA-256)") {
        if (snapshot == null) {
            EmptyLine("(computing…)")
            return@Card
        }
        snapshot.rows.forEach { row ->
            FingerprintRowView(row = row, onTapCopy = { clipboard.setText(AnnotatedString(it)) })
        }
    }
}

@Composable
private fun FingerprintRowView(row: FingerprintRow, onTapCopy: (String) -> Unit) {
    val tap = if (row.sha256.isNotEmpty()) Modifier.clickable { onTapCopy(row.sha256) } else Modifier
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .then(tap)
            .padding(vertical = 4.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(
            text = row.name,
            color = FG_LABEL,
            fontFamily = FontFamily.Monospace,
            fontSize = 12.sp,
            modifier = Modifier.weight(0.42f),
        )
        Text(
            text = shortHash(row.sha256),
            color = if (row.sha256.isEmpty()) FG_DIM else FG_HASH,
            fontFamily = FontFamily.Monospace,
            fontSize = 12.sp,
            fontWeight = FontWeight.SemiBold,
            modifier = Modifier.weight(0.58f),
        )
    }
}


@Composable
private fun Card(title: String, content: @Composable () -> Unit) {
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .background(color = BG_CARD, shape = RoundedCornerShape(8.dp))
            .padding(horizontal = 12.dp, vertical = 10.dp),
        verticalArrangement = Arrangement.spacedBy(2.dp),
    ) {
        Text(
            text = title,
            color = FG_TITLE,
            fontFamily = FontFamily.Monospace,
            fontWeight = FontWeight.SemiBold,
            fontSize = 13.sp,
            modifier = Modifier.padding(bottom = 4.dp),
        )
        content()
    }
}

@Composable
private fun Row2(label: String, text: String, tone: Tone) {
    Row(
        modifier = Modifier.fillMaxWidth().padding(vertical = 3.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(
            text = label,
            color = FG_LABEL,
            fontFamily = FontFamily.Monospace,
            fontSize = 12.sp,
            modifier = Modifier.weight(0.45f),
        )
        Text(
            text = text,
            color = toneColor(tone),
            fontFamily = FontFamily.Monospace,
            fontSize = 12.sp,
            fontWeight = FontWeight.SemiBold,
            modifier = Modifier.weight(0.55f),
        )
    }
}

@Composable
private fun EmptyLine(text: String) {
    Box(modifier = Modifier.fillMaxWidth(), contentAlignment = Alignment.CenterStart) {
        Text(
            text = text,
            color = FG_DIM,
            fontFamily = FontFamily.Monospace,
            fontSize = 12.sp,
        )
    }
}

private enum class Tone { OK, WARN, BAD, DIM }

private fun toneColor(t: Tone): Color = when (t) {
    Tone.OK -> C_OK
    Tone.WARN -> C_WARN
    Tone.BAD -> C_BAD
    Tone.DIM -> FG_DIM
}

private fun shortHash(full: String): String {
    if (full.isEmpty()) return "---"
    if (full.length < 12) return full
    return full.substring(0, 8) + "…" + full.substring(full.length - 4)
}

private fun humanAge(seconds: Long): String {
    if (seconds < 60) return "${seconds}s"
    if (seconds < 3600) return "${seconds / 60}m"
    if (seconds < 86_400) return "${seconds / 3600}h"
    return "${seconds / 86_400}d"
}

private val rfc3339Formatters: List<SimpleDateFormat> by lazy {
    listOf(
        SimpleDateFormat("yyyy-MM-dd'T'HH:mm:ss'Z'", Locale.US),
        SimpleDateFormat("yyyy-MM-dd'T'HH:mm:ssXXX", Locale.US),
    ).onEach { it.timeZone = TimeZone.getTimeZone("UTC") }
}

private fun parseRfc3339OrZero(s: String): Long {
    if (s.isEmpty()) return 0L
    for (fmt in rfc3339Formatters) {
        try {
            return fmt.parse(s)?.time ?: continue
        } catch (_: Throwable) {
            
        }
    }
    return 0L
}

private const val UPTIME_TICK_MS = 30_000L

private val BG_BASE = Color(0xFF1D2021)
private val BG_CARD = Color(0xFF282828)
private val FG_TITLE = Color(0xFFEBDBB2)
private val FG_LABEL = Color(0xFFD5C4A1)
private val FG_HASH = Color(0xFF83A598)
private val FG_DIM = Color(0xFF7C6F64)
private val C_OK = Color(0xFF5FCC1A)
private val C_WARN = Color(0xFFFABD2F)
private val C_BAD = Color(0xFFCC241D)
