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
import androidx.lifecycle.compose.collectAsStateWithLifecycle


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
            title = {
                Text(text = "Incoming call", color = DialogText)
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
