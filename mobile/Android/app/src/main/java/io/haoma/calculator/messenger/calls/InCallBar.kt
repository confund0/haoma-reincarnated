package io.haoma.calculator.messenger.calls

import androidx.compose.animation.core.LinearEasing
import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.Text
import androidx.compose.material3.rememberModalBottomSheetState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.lerp
import androidx.compose.ui.hapticfeedback.HapticFeedbackType
import androidx.compose.ui.platform.LocalHapticFeedback
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import io.haoma.calculator.messenger.CallDirection
import io.haoma.calculator.messenger.CallEntry
import io.haoma.calculator.messenger.CallStatus
import io.haoma.calculator.messenger.CallStreamSide
import io.haoma.calculator.messenger.CallStreamState
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.toggleMute
import kotlinx.coroutines.delay
import java.util.Locale


@Composable
fun InCallBar(call: CallEntry, store: MessengerStore) {
    val accepted = call.status == CallStatus.Accepted
    val router = store.audioRouter
    val current = router?.currentDevice?.collectAsStateWithLifecycle()?.value
    val muted by store.mutedCalls.collectAsStateWithLifecycle()
    val isMuted = muted[call.callId] == true
    val streamMap by store.callStreamState.collectAsStateWithLifecycle()
    val stream = streamMap[call.callId]
    var pickerOpen by remember { mutableStateOf(false) }
    var statsOpen by remember { mutableStateOf(false) }

    var now by remember { mutableLongStateOf(System.currentTimeMillis() / 1000L) }
    LaunchedEffect(call.callId) {
        while (true) {
            now = System.currentTimeMillis() / 1000L
            delay(1_000L)
        }
    }
    
    
    val duration = (now - call.startedAt).coerceAtLeast(0L)
    val preAcceptLabel = when {
        call.direction == CallDirection.Out && call.status == CallStatus.Offered -> "Calling…"
        call.direction == CallDirection.In && call.status == CallStatus.Ringing -> "Ringing…"
        call.status == CallStatus.Ringing -> "Ringing…" 
        call.status == CallStatus.Offered -> "Calling…"
        else -> ""
    }

    
    val pulse = rememberInfiniteTransition(label = "in-call pulse")
    val pulseSpec = infiniteRepeatable<Float>(
        animation = tween(durationMillis = 600, easing = LinearEasing),
        repeatMode = RepeatMode.Reverse,
    )
    val dotAlpha by pulse.animateFloat(
        initialValue = 1.0f,
        targetValue = 0.3f,
        animationSpec = pulseSpec,
        label = "in-call dot alpha",
    )
    val barTint by pulse.animateFloat(
        initialValue = 0f,
        targetValue = 1f,
        animationSpec = pulseSpec,
        label = "in-call bar tint",
    )
    val barColor = lerp(BarBg, BarBgPulse, barTint)

    Row(
        modifier = Modifier
            .fillMaxWidth()
            .background(barColor)
            .padding(horizontal = 12.dp, vertical = 6.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        Text(
            text = "●",
            color = BarAccent.copy(alpha = dotAlpha),
            fontSize = 12.sp,
            fontWeight = FontWeight.Bold,
        )
        if (accepted) {
            Text(
                text = formatDuration(duration),
                color = BarText,
                fontFamily = FontFamily.Monospace,
                fontSize = 13.sp,
                fontWeight = FontWeight.SemiBold,
            )
            
            
            StreamStatsChip(
                stream = stream,
                nowMs = now * 1000L,
                onLongPress = { statsOpen = true },
            )
            Spacer(modifier = Modifier.weight(1f))
            
            
            val routeGlyph = glyphFor(current)
            val solid = fontAwesomeSolid()
            val brands = fontAwesomeBrands()
            Box(
                modifier = Modifier
                    .clip(RoundedCornerShape(percent = 50))
                    .background(BarRouteBg)
                    .clickable { pickerOpen = true }
                    .padding(horizontal = 10.dp, vertical = 2.dp),
                contentAlignment = Alignment.Center,
            ) {
                Text(
                    text = routeGlyph,
                    color = BarText,
                    fontSize = 14.sp,
                    fontFamily = if (isBrandsGlyph(routeGlyph)) brands else solid,
                )
            }
            
            
            Box(
                modifier = Modifier
                    .clip(RoundedCornerShape(percent = 50))
                    .background(if (isMuted) BarMuteBg else BarRouteBg)
                    .clickable { store.toggleMute(call.callId) }
                    .padding(horizontal = 10.dp, vertical = 2.dp),
                contentAlignment = Alignment.Center,
            ) {
                Text(
                    text = if (isMuted) CallIcons.MicrophoneSlash else CallIcons.Microphone,
                    color = if (isMuted) BarAccent else BarText,
                    fontSize = 14.sp,
                    fontFamily = solid,
                )
            }
        } else {
            Text(
                text = preAcceptLabel,
                color = BarText,
                fontSize = 13.sp,
                fontWeight = FontWeight.SemiBold,
            )
            Spacer(modifier = Modifier.weight(1f))
        }
    }
    if (pickerOpen) {
        CallAudioDialog(store = store, onDismiss = { pickerOpen = false })
    }
    if (statsOpen) {
        CallStatsSheet(
            callId = call.callId,
            stream = stream,
            nowMs = now * 1000L,
            onDismiss = { statsOpen = false },
        )
    }
}

internal fun glyphFor(route: AudioRoute?): String = when (route?.kind) {
    AudioRoute.Kind.Speaker -> CallIcons.VolumeUp
    AudioRoute.Kind.Wired -> CallIcons.Headphones
    AudioRoute.Kind.Bluetooth -> CallIcons.Bluetooth
    
    
    else -> CallIcons.Headset
}


@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun StreamStatsChip(
    stream: CallStreamState?,
    nowMs: Long,
    onLongPress: () -> Unit,
) {
    val micColor = micArrowColor(stream?.mic, nowMs)
    val spkColor = spkArrowColor(stream?.spk, nowMs)
    val jitter = stream?.spk?.jitterMs
    val drops = stream?.dropped ?: 0L
    val haptic = LocalHapticFeedback.current
    val interactionSource = remember { MutableInteractionSource() }

    Box(
        modifier = Modifier
            .clip(RoundedCornerShape(percent = 50))
            .background(BarRouteBg)
            .combinedClickable(
                interactionSource = interactionSource,
                indication = null,
                onClick = {},
                onLongClick = {
                    haptic.performHapticFeedback(HapticFeedbackType.LongPress)
                    onLongPress()
                },
            )
            .padding(horizontal = 8.dp, vertical = 2.dp),
        contentAlignment = Alignment.Center,
    ) {
        Row(
            horizontalArrangement = Arrangement.spacedBy(6.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Text(
                text = "↑",
                color = micColor,
                fontSize = 14.sp,
                fontWeight = FontWeight.Black,
            )
            Text(
                text = "↓",
                color = spkColor,
                fontSize = 14.sp,
                fontWeight = FontWeight.Black,
            )
            Text(
                text = if (jitter != null) "${jitter.toInt()}ms" else "—",
                color = BarText,
                fontFamily = FontFamily.Monospace,
                fontSize = 12.sp,
            )
            Text(
                text = "${drops}drp",
                color = if (drops > 0) ArrowRed else BarText,
                fontFamily = FontFamily.Monospace,
                fontSize = 12.sp,
            )
        }
    }
}


@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun CallStatsSheet(
    callId: String,
    stream: CallStreamState?,
    nowMs: Long,
    onDismiss: () -> Unit,
) {
    val sheetState = rememberModalBottomSheetState()
    ModalBottomSheet(
        onDismissRequest = onDismiss,
        sheetState = sheetState,
        containerColor = SheetBg,
    ) {
        Column(
            modifier = Modifier
                .fillMaxWidth()
                .padding(horizontal = 24.dp, vertical = 8.dp)
                .padding(bottom = 24.dp),
            verticalArrangement = Arrangement.spacedBy(6.dp),
        ) {
            StatsTitle("Call diagnostics")
            StatsRow("call id", callId.take(16))
            StatsRow("drops", (stream?.dropped ?: 0L).toString())
            StatsTitle("mic ↑")
            StatsRow("frames out", (stream?.mic?.framesOut ?: 0L).toString())
            StatsRow("sample age", sampleAgeMs(stream?.mic?.lastSampleAtMs, nowMs))
            StatsRow("jitter", jitterMs(stream?.mic?.jitterMs))
            StatsTitle("spk ↓")
            StatsRow("frames out", (stream?.spk?.framesOut ?: 0L).toString())
            StatsRow("sample age", sampleAgeMs(stream?.spk?.lastSampleAtMs, nowMs))
            StatsRow("jitter", jitterMs(stream?.spk?.jitterMs))
        }
    }
}

@Composable
private fun StatsTitle(text: String) {
    Text(
        text = text,
        color = BarText,
        fontSize = 13.sp,
        fontWeight = FontWeight.SemiBold,
    )
}

@Composable
private fun StatsRow(label: String, value: String) {
    Row(
        modifier = Modifier.fillMaxWidth(),
        horizontalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        Text(text = label, color = BarTextDim, fontSize = 12.sp)
        Text(
            text = value,
            color = BarText,
            fontFamily = FontFamily.Monospace,
            fontSize = 12.sp,
            modifier = Modifier.weight(1f),
        )
    }
}

private fun sampleAgeMs(lastSampleAtMs: Long?, nowMs: Long): String =
    if (lastSampleAtMs == null || lastSampleAtMs <= 0L) "—"
    else "${nowMs - lastSampleAtMs}ms"

private fun jitterMs(j: Double?): String =
    if (j == null) "—" else String.format(Locale.US, "%.1fms", j)


private fun micArrowColor(mic: CallStreamSide?, nowMs: Long): Color {
    if (mic == null) return ArrowRed
    val ageMs = nowMs - mic.lastSampleAtMs
    if (ageMs > 5_000L) return ArrowRed
    val advanced = mic.framesOut > mic.prevFramesOut
    if (ageMs > 2_000L) return ArrowYellow
    if (!advanced && mic.prevFramesOut != 0L) return ArrowYellow
    return ArrowGreen
}


private fun spkArrowColor(spk: CallStreamSide?, nowMs: Long): Color {
    if (spk == null) return ArrowRed
    val ageMs = nowMs - spk.lastSampleAtMs
    if (ageMs > 5_000L) return ArrowRed
    if (spk.jitterMs > 200.0) return ArrowRed
    if (ageMs > 2_000L) return ArrowYellow
    if (spk.jitterMs > 80.0) return ArrowYellow
    return ArrowGreen
}


private val BarBg = Color(0xFFB05E0F)        
private val BarBgPulse = Color(0xFFD97A1F)   
private val BarAccent = Color(0xFFFB4934)    
private val BarText = Color(0xFFFBF1C7)      
private val BarTextDim = Color(0xFFFBEEC0)
private val BarRouteBg = Color(0x33000000)   
private val BarMuteBg = Color(0x66000000)    
private val SheetBg = Color(0xFF282828)      


private val ArrowGreen = Color(0xFF388E3C)   
private val ArrowYellow = Color(0xFFFABD2F)  
private val ArrowRed = Color(0xFFFB4934)     
