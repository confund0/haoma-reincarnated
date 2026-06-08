package io.haoma.calculator.messenger

import androidx.compose.material3.AlertDialog
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import io.haoma.calculator.messenger.calls.CallIcons
import io.haoma.calculator.messenger.calls.fontAwesomeSolid


@Composable
internal fun RingerDialogHost(store: MessengerStore) {
    val activeCalls by store.activeCalls.collectAsStateWithLifecycle()
    val ringing = remember(activeCalls) {
        activeCalls.values
            .filter { it.direction == CallDirection.In && it.status == CallStatus.Ringing }
            .maxByOrNull { it.startedAt }
    } ?: return

    val peerLabel = remember(ringing.peerId, ringing.callId) {
        store.peerLabelFor(ringing.peerId)
    }
    val hasVideo = CallModality.Video in ringing.modalities
    val glyph = if (hasVideo) CallIcons.Video else CallIcons.Headset
    val titleText = if (hasVideo) "Incoming video call" else "Incoming audio call"

    MaterialTheme(
        colorScheme = darkColorScheme(
            surface = DialogSurface,
            surfaceContainerHigh = DialogSurface,
            onSurface = DialogText,
            primary = DialogAccent,
        ),
    ) {
        AlertDialog(
            onDismissRequest = {
                
                
            },
            icon = {
                Text(
                    text = glyph,
                    color = ModalityGlyph,
                    fontSize = 26.sp,
                    fontFamily = fontAwesomeSolid(),
                )
            },
            title = {
                Text(
                    text = titleText,
                    color = DialogText,
                    fontSize = 22.sp,
                )
            },
            text = {
                Text(text = "From: $peerLabel", color = DialogText)
            },
            confirmButton = {
                TextButton(onClick = {
                    store.respondCall(ringing.callId, CallAction.Accept)
                    
                    store.notificationPoster?.cancelCall(ringing.callId)
                }) {
                    Text("Answer", color = DialogAccent)
                }
            },
            dismissButton = {
                TextButton(onClick = {
                    store.respondCall(ringing.callId, CallAction.Reject)
                    store.notificationPoster?.cancelCall(ringing.callId)
                }) {
                    Text("Decline", color = DialogReject)
                }
            },
            containerColor = DialogSurface,
        )
    }
}

private val DialogSurface = Color(0xFF32302F)   
private val DialogText = Color(0xFFEBDBB2)      
private val DialogAccent = Color(0xFFB8BB26)    
private val DialogReject = Color(0xFFFB4934)    
private val ModalityGlyph = Color(0xFFD79921)   
