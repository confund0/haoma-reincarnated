package io.haoma.calculator.messenger.calls

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import io.haoma.calculator.log.Logger
import io.haoma.calculator.messenger.CallAction
import io.haoma.calculator.messenger.CallEntry
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.calls.video.VideoTile
import io.haoma.calculator.messenger.peerLabelFor
import io.haoma.calculator.messenger.respondCall


@Composable
internal fun CallWindow(call: CallEntry, store: MessengerStore, onDismiss: () -> Unit) {
    LaunchedEffect(call.callId) {
        Logger.i(
            "call",
            "callwindow open call=${shortCallIdForLog(call.callId)} modality=video",
        )
    }

    Dialog(
        onDismissRequest = {
            Logger.i(
                "call",
                "callwindow dismiss call=${shortCallIdForLog(call.callId)} reason=back",
            )
            onDismiss()
        },
        properties = DialogProperties(
            dismissOnBackPress = true,
            dismissOnClickOutside = false,
            usePlatformDefaultWidth = false,
        ),
    ) {
        Box(
            modifier = Modifier
                .fillMaxSize()
                .background(CallWindowTheme.WindowBg)
                .safeDrawingPadding(),
        ) {
            Column(modifier = Modifier.fillMaxSize()) {
                Header(
                    label = store.peerLabelFor(call.peerId),
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
}

@Composable
private fun Header(label: String, onEnd: () -> Unit) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = 20.dp, vertical = 14.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(
            text = label,
            color = CallWindowTheme.Text,
            fontSize = 18.sp,
            fontWeight = FontWeight.SemiBold,
            modifier = Modifier.weight(1f),
        )
        Box(
            modifier = Modifier
                .clip(RoundedCornerShape(percent = 50))
                .background(CallWindowTheme.Accent)
                .clickable { onEnd() }
                .padding(horizontal = 18.dp, vertical = 10.dp),
            contentAlignment = Alignment.Center,
        ) {
            Text(
                text = "End call",
                color = CallWindowTheme.Text,
                fontWeight = FontWeight.SemiBold,
                fontSize = 14.sp,
            )
        }
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
        VideoTile(store = store, callId = callId, modifier = Modifier.fillMaxSize())
    }
}

private fun shortCallIdForLog(callId: String): String =
    if (callId.length <= 8) callId else callId.take(8)
