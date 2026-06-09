package io.haoma.calculator.messenger.calls

import android.app.Activity
import android.content.pm.ActivityInfo
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
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
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import io.haoma.calculator.log.Logger
import io.haoma.calculator.messenger.CallAction
import io.haoma.calculator.messenger.CallEntry
import io.haoma.calculator.messenger.CallModality
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.calls.video.CallVideoStage
import io.haoma.calculator.messenger.peerLabelFor
import io.haoma.calculator.messenger.respondCall
import io.haoma.calculator.messenger.switchCameraFacing
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
            Header(
                label = store.peerLabelFor(call.peerId),
                store = store,
                call = call,
                onEnd = {
                    Logger.i(
                        "call",
                        "callwindow end_pressed call=${shortCallIdForLog(call.callId)}",
                    )
                    store.respondCall(call.callId, CallAction.End)
                },
            )
            PlaceholderArea(
                modifier = Modifier.weight(1f).fillMaxWidth(),
                store = store,
                callId = call.callId,
            )
            
            
            InCallBar(call = call, store = store)
        }
    }
}

@Composable
private fun Header(
    label: String,
    store: MessengerStore,
    call: CallEntry,
    onEnd: () -> Unit,
) {
    val hasVideo = CallModality.Video in call.modalities
    val videoMuted by store.videoMutedCalls.collectAsStateWithLifecycle()
    val isVideoMuted = videoMuted[call.callId] == true
    val solid = fontAwesomeSolid()
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .background(CallWindowTheme.HeaderBg)
            .padding(horizontal = 20.dp, vertical = 6.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(
            text = label,
            color = CallWindowTheme.PeerNick,
            fontSize = 18.sp,
            fontWeight = FontWeight.SemiBold,
            modifier = Modifier.weight(1f),
        )
        
        
        Row(
            modifier = Modifier.weight(1f),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.SpaceEvenly,
        ) {
            if (hasVideo) {
                
                
                HeaderIconButton(
                    glyph = CallIcons.VideoSlash,
                    glyphColor = if (isVideoMuted) CallWindowTheme.Accent else CallWindowTheme.Text,
                    family = solid,
                    contentDescription = if (isVideoMuted) "Camera unmute" else "Camera mute",
                    onClick = { store.toggleVideoMute(call.callId) },
                )
                
                
                HeaderIconButton(
                    glyph = CallIcons.CameraRotate,
                    glyphColor = CallWindowTheme.Text,
                    family = solid,
                    contentDescription = "Switch camera",
                    onClick = { store.switchCameraFacing(call.callId) },
                )
            }
            
            
            Box(
                modifier = Modifier
                    .size(48.dp)
                    .clickable { onEnd() },
                contentAlignment = Alignment.Center,
            ) {
                Icon(
                    imageVector = Icons.Filled.Call,
                    contentDescription = "End call",
                    tint = CallWindowTheme.Accent,
                )
            }
        }
    }
}


@Composable
private fun HeaderIconButton(
    glyph: String,
    glyphColor: Color,
    family: androidx.compose.ui.text.font.FontFamily,
    contentDescription: String,
    onClick: () -> Unit,
) {
    Box(
        modifier = Modifier
            .size(width = 36.dp, height = 40.dp)
            .clickable(onClickLabel = contentDescription) { onClick() },
        contentAlignment = Alignment.Center,
    ) {
        Text(
            text = glyph,
            color = glyphColor,
            fontSize = 12.sp,
            fontFamily = family,
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
            .clip(RoundedCornerShape(8.dp))
            .background(CallWindowTheme.PlaceholderBg),
    ) {
        CallVideoStage(store = store, callId = callId, modifier = Modifier.fillMaxSize())
    }
}

private fun shortCallIdForLog(callId: String): String =
    if (callId.length <= 8) callId else callId.take(8)
