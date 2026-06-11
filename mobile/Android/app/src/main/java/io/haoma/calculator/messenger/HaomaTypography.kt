package io.haoma.calculator.messenger

import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.TextUnit
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp


data class HaomaTypography(
    val scale: Float = 1.0f,
) {
    
    val bubbleBody: TextUnit = (BASE_BODY * scale).sp

    
    val bubbleSmall: TextUnit = (BASE_SMALL * scale).sp

    
    val replyQuote: TextUnit = (BASE_REPLY * scale).sp

    
    val breadcrumb: TextUnit = (BASE_BREADCRUMB * scale).sp

    
    val pillText: TextUnit = (BASE_PILL_TEXT * scale).sp

    
    val pillSize: Dp = (BASE_PILL_SIZE * scale).dp

    companion object {
        private const val BASE_BODY = 14f
        private const val BASE_SMALL = 11f
        private const val BASE_REPLY = 12f
        private const val BASE_BREADCRUMB = 13f
        private const val BASE_PILL_TEXT = 14f
        private const val BASE_PILL_SIZE = 28f

        
        val STOPS: List<Float> = listOf(0.85f, 0.95f, 1.00f, 1.10f, 1.20f, 1.30f)

        
        fun snap(scale: Float): Float = STOPS.minByOrNull { kotlin.math.abs(it - scale) } ?: 1.0f

        
        fun stepUp(scale: Float): Float {
            val snapped = snap(scale)
            val idx = STOPS.indexOf(snapped).coerceAtLeast(0)
            return STOPS.getOrNull(idx + 1) ?: STOPS.last()
        }

        
        fun stepDown(scale: Float): Float {
            val snapped = snap(scale)
            val idx = STOPS.indexOf(snapped).coerceAtLeast(0)
            return STOPS.getOrNull(idx - 1) ?: STOPS.first()
        }
    }
}


val LocalHaomaTypography = staticCompositionLocalOf { HaomaTypography() }
