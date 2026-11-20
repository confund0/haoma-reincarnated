package io.haoma.calculator.messenger.calls

import android.app.Activity
import android.content.pm.ActivityInfo
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Call
import androidx.compose.material3.Icon
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import io.haoma.calculator.log.Logger
import io.haoma.calculator.messenger.CallAction
import io.haoma.calculator.messenger.CallEntry
import io.haoma.calculator.messenger.CallModality
import io.haoma.calculator.messenger.CallStatus
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.calls.video.CallVideoStage
import io.haoma.calculator.messenger.peerLabelFor
import io.haoma.calculator.messenger.respondCall
import io.haoma.calculator.messenger.switchCameraFacing
import io.haoma.calculator.messenger.toggleMute
import io.haoma.calculator.messenger.toggleVideoMute


@Composable
internal fun CallWindow(call: CallEntry, store: MessengerStore, onDismiss: () -> Unit) {
    LaunchedEffect(call.callId) {
        Logger.i(
            "call",
            "callwindow open call=${shortCallIdForLog(call.callId)} modality=video",
        )
    }

    
    val context = LocalContext.current
    DisposableEffect(call.callId) {
        val activity = context as? Activity
        val previous = activity?.requestedOrientation
        activity?.requestedOrientation = ActivityInfo.SCREEN_ORIENTATION_PORTRAIT
        onDispose {
            activity?.requestedOrientation =
                previous ?: ActivityInfo.SCREEN_ORIENTATION_UNSPECIFIED
        }
    }

    
    BackHandler(enabled = true) {
        Logger.i(
            "call",
            "callwindow dismiss call=${shortCallIdForLog(call.callId)} reason=back",
        )
        onDismiss()
    }

    Box(
        modifier = Modifier
            .fillMaxSize()
            .background(CallWindowTheme.WindowBg),
    ) {
        Column(modifier = Modifier.fillMaxSize()) {
            Header(label = store.peerLabelFor(call.peerId), store = store, call = call)
            PlaceholderArea(
                modifier = Modifier.weight(1f).fillMaxWidth(),
                store = store,
                callId = call.callId,
            )
            
            
            BottomBar(
                call = call,
                store = store,
                onEnd = {
                    Logger.i(
                        "call",
                        "callwindow end_pressed call=${shortCallIdForLog(call.callId)}",
                    )
                    store.respondCall(call.callId, CallAction.End)
                },
            )
        }
    }
}


@Composable
private fun Header(label: String, store: MessengerStore, call: CallEntry) {
    val accepted = call.status == CallStatus.Accepted
    val streamMap by store.callStreamState.collectAsStateWithLifecycle()
    val stream = streamMap[call.callId]
    val now = rememberTickingNowSeconds(call.callId)
    var statsOpen by remember { mutableStateOf(false) }

    Row(
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = 20.dp, vertical = 6.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Box(modifier = Modifier.weight(1f), contentAlignment = Alignment.CenterStart) {
            Text(
                text = label,
                color = CallWindowTheme.PeerNick,
                fontSize = 18.sp,
                fontWeight = FontWeight.SemiBold,
            )
        }
        Box(modifier = Modifier.weight(1f), contentAlignment = Alignment.Center) {
            
            
            if (accepted) {
                StreamStatsChip(
                    stream = stream,
                    nowMs = now * 1000L,
                    background = CallWindowTheme.ControlChipBg,
                    onLongPress = { statsOpen = true },
                )
            }
        }
        Box(modifier = Modifier.weight(1f), contentAlignment = Alignment.CenterEnd) {
            Text(
                text = if (accepted) {
                    formatDuration((now - call.startedAt).coerceAtLeast(0L))
                } else {
                    preAcceptLabel(call)
                },
                color = CallWindowTheme.Text,
                fontFamily = FontFamily.Monospace,
                fontSize = 13.sp,
                fontWeight = FontWeight.SemiBold,
            )
        }
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

@Composable
private fun PlaceholderArea(
    modifier: Modifier = Modifier,
    store: MessengerStore,
    callId: String,
) {
    Box(
        modifier = modifier
            .padding(horizontal = 12.dp, vertical = 6.dp)
            .clip(RoundedCornerShape(8.dp)),
    ) {
        CallVideoStage(store = store, callId = callId, modifier = Modifier.fillMaxSize())
    }
}


@Composable
private fun BottomBar(call: CallEntry, store: MessengerStore, onEnd: () -> Unit) {
    val hasVideo = CallModality.Video in call.modalities
    val accepted = call.status == CallStatus.Accepted
    val videoMuted by store.videoMutedCalls.collectAsStateWithLifecycle()
    val isVideoMuted = videoMuted[call.callId] == true
    val router = store.audioRouter
    val current = router?.currentDevice?.collectAsStateWithLifecycle()?.value
    val muted by store.mutedCalls.collectAsStateWithLifecycle()
    val isMuted = muted[call.callId] == true
    var pickerOpen by remember { mutableStateOf(false) }

    Row(
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = 12.dp, vertical = 10.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        BottomSlot {
            if (hasVideo) {
                
                
                BottomIconButton(
                    glyph = CallIcons.CameraRotate,
                    glyphColor = CallWindowTheme.Text,
                    background = CallWindowTheme.ControlChipBg,
                    contentDescription = "Switch camera",
                    onClick = { store.switchCameraFacing(call.callId) },
                )
            }
        }
        BottomSlot {
            if (hasVideo) {
                
                
                BottomIconButton(
                    glyph = CallIcons.VideoSlash,
                    glyphColor = if (isVideoMuted) CallWindowTheme.Accent else CallWindowTheme.Text,
                    background = if (isVideoMuted) {
                        CallWindowTheme.ControlChipMutedBg
                    } else {
                        CallWindowTheme.ControlChipBg
                    },
                    contentDescription = if (isVideoMuted) "Camera unmute" else "Camera mute",
                    onClick = { store.toggleVideoMute(call.callId) },
                )
            }
        }
        BottomSlot {
            
            
            Box(
                modifier = Modifier
                    .size(48.dp)
                    .clickable(onClickLabel = "End call") { onEnd() },
                contentAlignment = Alignment.Center,
            ) {
                Icon(
                    imageVector = Icons.Filled.Call,
                    contentDescription = "End call",
                    tint = CallWindowTheme.Accent,
                )
            }
        }
        BottomSlot {
            
            
            if (accepted) {
                val routeGlyph = glyphFor(current)
                BottomIconButton(
                    glyph = routeGlyph,
                    glyphColor = CallWindowTheme.Text,
                    background = CallWindowTheme.ControlChipBg,
                    contentDescription = "Audio route",
                    family = if (isBrandsGlyph(routeGlyph)) fontAwesomeBrands() else fontAwesomeSolid(),
                    onClick = { pickerOpen = true },
                )
            }
        }
        BottomSlot {
            if (accepted) {
                BottomIconButton(
                    glyph = if (isMuted) CallIcons.MicrophoneSlash else CallIcons.Microphone,
                    glyphColor = if (isMuted) CallWindowTheme.Accent else CallWindowTheme.Text,
                    background = if (isMuted) {
                        CallWindowTheme.ControlChipMutedBg
                    } else {
                        CallWindowTheme.ControlChipBg
                    },
                    contentDescription = if (isMuted) "Microphone unmute" else "Microphone mute",
                    onClick = { store.toggleMute(call.callId) },
                )
            }
        }
    }
    if (pickerOpen) {
        CallAudioDialog(store = store, onDismiss = { pickerOpen = false })
    }
}

@Composable
private fun RowScope.BottomSlot(content: @Composable () -> Unit) {
    Box(modifier = Modifier.weight(1f), contentAlignment = Alignment.Center) {
        content()
    }
}


@Composable
private fun BottomIconButton(
    glyph: String,
    glyphColor: Color,
    background: Color,
    contentDescription: String,
    family: FontFamily = fontAwesomeSolid(),
    onClick: () -> Unit,
) {
    Box(
        modifier = Modifier
            .size(48.dp)
            .clip(RoundedCornerShape(percent = 50))
            .background(background)
            .clickable(onClickLabel = contentDescription) { onClick() },
        contentAlignment = Alignment.Center,
    ) {
        Text(
            text = glyph,
            color = glyphColor,
            fontSize = 16.sp,
            fontFamily = family,
        )
    }
}

private fun shortCallIdForLog(callId: String): String =
    if (callId.length <= 8) callId else callId.take(8)
