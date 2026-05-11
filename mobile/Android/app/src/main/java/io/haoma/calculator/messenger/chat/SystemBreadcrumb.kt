package io.haoma.calculator.messenger.chat

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.haoma.calculator.messenger.EventKind
import io.haoma.calculator.messenger.TimelineEvent


@Composable
internal fun SystemBreadcrumb(event: TimelineEvent, modifier: Modifier = Modifier) {
    val text = describe(event) ?: return
    Box(
        modifier = modifier
            .fillMaxWidth()
            .padding(horizontal = 24.dp, vertical = 4.dp),
        contentAlignment = Alignment.Center,
    ) {
        Text(
            text = text,
            color = ChatPalette.TextFaint,
            fontFamily = FontFamily.Monospace,
            fontStyle = FontStyle.Italic,
            fontSize = 12.sp,
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
        else -> "$prefix ${event.kind}"
    }
}
