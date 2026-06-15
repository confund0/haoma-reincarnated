package io.haoma.calculator.messenger.chat

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import io.haoma.calculator.messenger.EventKind
import io.haoma.calculator.messenger.LocalHaomaTypography
import io.haoma.calculator.messenger.TimelineEvent


@Composable
internal fun SystemBreadcrumb(event: TimelineEvent, modifier: Modifier = Modifier) {
    val text = describe(event) ?: return
    
    
    val alignment = if (event.kind == EventKind.CALL_SUMMARY || event.kind == EventKind.ONION_ROTATION) {
        if (event.isOutbound) Alignment.CenterEnd else Alignment.CenterStart
    } else {
        Alignment.Center
    }
    Box(
        modifier = modifier
            .fillMaxWidth()
            .padding(horizontal = 24.dp, vertical = 4.dp),
        contentAlignment = alignment,
    ) {
        Text(
            text = text,
            color = breadcrumbColor(event),
            fontFamily = FontFamily.Monospace,
            fontSize = LocalHaomaTypography.current.breadcrumb,
        )
    }
}

private fun describe(event: TimelineEvent): String? {
    if (event.kind == EventKind.REACTION) return null
    val body = event.body ?: return "* ${event.kind}"
    val ts = formatHm(event.displayTs)
    val prefix = if (ts.isEmpty()) "*" else "* $ts"
    return when (event.kind) {
        EventKind.TIMER_CHANGE -> {
            val to = body.optInt("to", 0)
            val label = if (to <= 0) "off" else "${to}s"
            "$prefix retention → $label"
        }
        EventKind.FILE -> "$prefix file (rendering parked — Files-1 mobile slice)"
        EventKind.CALL_SUMMARY -> "$prefix ${describeCallSummary(body)}"
        EventKind.ONION_ROTATION -> "$prefix ${describeOnionRotation(event, body)}"
        else -> "$prefix ${event.kind}"
    }
}

private fun describeOnionRotation(event: TimelineEvent, body: org.json.JSONObject): String {
    val dirGlyph = if (event.isOutbound) "↑" else "↓"
    return when (body.optString("stage")) {
        "started" -> if (event.isOutbound) {
            "$dirGlyph rotating our Tor address"
        } else {
            "$dirGlyph they're rotating their Tor address"
        }
        "succeeded" -> if (event.isOutbound) {
            "$dirGlyph rotated our Tor address"
        } else {
            "$dirGlyph they rotated their Tor address"
        }
        "failed" -> {
            val reason = body.optString("reason").ifEmpty { "unknown" }
            "$dirGlyph rotation failed: $reason"
        }
        else -> "$dirGlyph rotation ${body.optString("stage")}"
    }
}

private fun describeCallSummary(body: org.json.JSONObject): String {
    val direction = body.optString("direction")
    val outcome = body.optString("outcome")
    val modalities = body.optJSONArray("modalities")
    val isVideo = modalities != null && (0 until modalities.length()).any { i ->
        modalities.optString(i) == "video"
    }
    val modality = if (isVideo) "video" else "audio"
    
    
    val dirGlyph = if (direction == "in") "↓" else "↑"
    val tail = when (outcome) {
        "completed" -> formatCallDuration(body.optLong("duration_seconds", 0L))
        "missed" -> "missed"
        "cancelled" -> "cancelled"
        "rejected" -> "declined"
        "failed" -> "failed"
        "disrupted" -> "disrupted"
        else -> ""
    }
    return if (tail.isEmpty()) "$dirGlyph $modality" else "$dirGlyph $modality · $tail"
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

private fun breadcrumbColor(event: TimelineEvent): Color {
    if (event.kind == EventKind.CALL_SUMMARY) {
        val outcome = event.body?.optString("outcome")
        return if (outcome == "completed") ChatPalette.CallSummaryOk else ChatPalette.CallSummaryBad
    }
    if (event.kind == EventKind.ONION_ROTATION) {
        val stage = event.body?.optString("stage")
        return if (stage == "failed") ChatPalette.OnionRotationBad else ChatPalette.OnionRotationOk
    }
    return ChatPalette.TextFaint
}

