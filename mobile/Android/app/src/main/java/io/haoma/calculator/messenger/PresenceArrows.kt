package io.haoma.calculator.messenger

import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.TextUnit
import androidx.compose.ui.unit.sp


@Composable
internal fun PresenceArrows(
    view: PresenceView,
    modifier: Modifier = Modifier,
    fontSize: TextUnit = 14.sp,
    dim: Boolean = false,
) {
    val text = buildAnnotatedString {
        withStyle(SpanStyle(color = if (dim) HaomaPalette.FG_DIM else arrowColour(view.accepting))) { append("↑") }
        withStyle(SpanStyle(color = if (dim) HaomaPalette.FG_DIM else arrowColour(view.sending))) { append("↓") }
    }
    Text(
        text = text,
        fontSize = fontSize,
        fontWeight = FontWeight.Bold,
        modifier = modifier,
    )
}
