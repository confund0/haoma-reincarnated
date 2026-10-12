package io.haoma.calculator.messenger

import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.unit.sp
import kotlinx.coroutines.delay


@Composable
fun JoinCountdown(startedAtMs: Long, link: Boolean = false) {
    var nowMs by remember(startedAtMs) { mutableLongStateOf(System.currentTimeMillis()) }
    LaunchedEffect(startedAtMs) {
        while (joinSecondsLeft(startedAtMs, nowMs) > 0L) {
            delay(1_000L)
            nowMs = System.currentTimeMillis()
        }
    }
    Text(
        text = joinWaitLine(startedAtMs, nowMs, link),
        color = HaomaPalette.FG_SECONDARY,
        fontSize = 13.sp,
        lineHeight = 18.sp,
    )
}
