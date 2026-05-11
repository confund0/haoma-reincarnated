package io.haoma.calculator.messenger.chat

import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.sp
import io.haoma.calculator.messenger.DeliveryState


@Composable
internal fun DeliveryGlyph(state: String, modifier: Modifier = Modifier) {
    val (glyph, color) = glyphFor(state) ?: return
    Text(
        text = glyph,
        color = color,
        fontFamily = FontFamily.Monospace,
        fontSize = 11.sp,
        modifier = modifier,
    )
}

private fun glyphFor(state: String): Pair<String, Color>? = when (state) {
    DeliveryState.ENQUEUED, "" -> "…" to ChatPalette.TextDim
    DeliveryState.SENT, DeliveryState.DELIVERED -> "✓" to ChatPalette.Ok
    DeliveryState.READ -> "✓✓" to ChatPalette.Ok
    DeliveryState.FAILED -> "✗" to ChatPalette.Bad
    else -> null
}
