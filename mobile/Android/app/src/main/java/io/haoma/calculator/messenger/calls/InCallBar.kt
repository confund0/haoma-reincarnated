package io.haoma.calculator.messenger.calls

import androidx.compose.animation.core.LinearEasing
import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
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
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import io.haoma.calculator.messenger.CallDirection
import io.haoma.calculator.messenger.CallEntry
import io.haoma.calculator.messenger.CallStatus
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.toggleMute
import kotlinx.coroutines.delay


@Composable
fun InCallBar(call: CallEntry, store: MessengerStore) {
    val accepted = call.status == CallStatus.Accepted
    val router = store.audioRouter
    val current = router?.currentDevice?.collectAsStateWithLifecycle()?.value
    val muted by store.mutedCalls.collectAsStateWithLifecycle()
    val isMuted = muted[call.callId] == true
    val jitterMap by store.callJitter.collectAsStateWithLifecycle()
    val jitterMs = jitterMap[call.callId]
    var pickerOpen by remember { mutableStateOf(false) }

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
            
            
            if (jitterMs != null) {
                Text(text = "·", color = BarTextDim, fontSize = 13.sp)
                Text(
                    text = "${jitterMs.toInt()}ms",
                    color = BarTextDim,
                    fontFamily = FontFamily.Monospace,
                    fontSize = 12.sp,
                )
            }
            Text(text = "·", color = BarTextDim, fontSize = 13.sp)
            
            
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
            Spacer(modifier = Modifier.weight(1f))
            
            
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
}

internal fun glyphFor(route: AudioRoute?): String = when (route?.kind) {
    AudioRoute.Kind.Speaker -> CallIcons.VolumeUp
    AudioRoute.Kind.Wired -> CallIcons.Headphones
    AudioRoute.Kind.Bluetooth -> CallIcons.Bluetooth
    
    
    else -> CallIcons.Headset
}


private val BarBg = Color(0xFFB05E0F)        
private val BarBgPulse = Color(0xFFD97A1F)   
private val BarAccent = Color(0xFFFB4934)    
private val BarText = Color(0xFFFBF1C7)      
private val BarTextDim = Color(0xFFFBEEC0)
private val BarRouteBg = Color(0x33000000)   
private val BarMuteBg = Color(0x66000000)    
