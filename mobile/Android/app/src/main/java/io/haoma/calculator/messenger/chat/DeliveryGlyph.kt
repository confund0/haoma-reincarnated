package io.haoma.calculator.messenger.chat

import androidx.compose.foundation.layout.Row
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.sp
import io.haoma.calculator.messenger.DeliveryState


@Composable
internal fun DeliveryGlyph(
    state: String,
    isFile: Boolean = false,
    mirrored: Boolean = false,
    modifier: Modifier = Modifier,
) {
    val spans = deliveryGlyphSpans(state, isFile, mirrored)
    if (spans.isEmpty()) return
    Row(modifier = modifier) {
        for (span in spans) {
            Text(
                text = span.text,
                color = span.color,
                fontFamily = FontFamily.Monospace,
                fontSize = 11.sp,
            )
        }
    }
}


internal data class GlyphSpan(val text: String, val color: Color)


internal fun deliveryGlyphSpans(
    state: String,
    isFile: Boolean = false,
    mirrored: Boolean = false,
): List<GlyphSpan> {
    val pair = (if (isFile) fileGlyphFor(state) else glyphFor(state)) ?: return emptyList()
    val (glyph, color) = pair
    
    val mine = if (mirrored && color == ChatPalette.Ok) ChatPalette.Mirrored else color
    if (glyph.length == 1) return listOf(GlyphSpan(glyph, mine))
    
    
    return listOf(
        GlyphSpan(glyph.dropLast(1), mine),
        GlyphSpan(glyph.takeLast(1), color),
    )
}

private fun glyphFor(state: String): Pair<String, Color>? = when (state) {
    
    
    DeliveryState.ENQUEUED, "" -> "✓" to ChatPalette.TextDim
    DeliveryState.SENT, DeliveryState.DELIVERED -> "✓" to ChatPalette.Ok
    DeliveryState.READ -> "✓✓" to ChatPalette.Ok
    DeliveryState.FAILED -> "✗" to ChatPalette.Bad
    else -> null
}

private fun fileGlyphFor(state: String): Pair<String, Color>? = when (state) {
    
    
    DeliveryState.ENQUEUED, "" -> "…" to ChatPalette.TextDim
    
    DeliveryState.SENT -> "✓" to ChatPalette.TextDim
    
    DeliveryState.DELIVERED -> "✓" to ChatPalette.Ok
    DeliveryState.READ -> "✓✓" to ChatPalette.Ok
    DeliveryState.FAILED -> "✗" to ChatPalette.Bad
    else -> null
}
