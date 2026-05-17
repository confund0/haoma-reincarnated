package io.haoma.calculator.messenger.chat

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.haoma.calculator.log.Logger


@Composable
internal fun StartCallModeDialog(
    open: Boolean,
    chatId: String,
    cameraGranted: Boolean,
    onPlaceAudioCall: () -> Unit,
    onPlaceVideoCall: () -> Unit,
    onRequestCamera: () -> Unit,
    onDismiss: () -> Unit,
) {
    if (!open) return
    MaterialTheme(
        colorScheme = darkColorScheme(
            surface = StartCallModeSurface,
            surfaceContainer = StartCallModeSurface,
            onSurface = ChatPalette.Accent,
        ),
    ) {
        AlertDialog(
            onDismissRequest = onDismiss,
            confirmButton = {},
            title = {
                Text(
                    text = "Start call",
                    color = ChatPalette.Accent,
                    fontWeight = FontWeight.SemiBold,
                    fontSize = 16.sp,
                )
            },
            text = {
                Column(modifier = Modifier.fillMaxWidth()) {
                    ModeRow(
                        label = "Voice call",
                        sublabel = "audio only",
                        onClick = {
                            Logger.d("call", "start-mode picked=audio chat=${chatId.take(8)}")
                            onDismiss()
                            onPlaceAudioCall()
                        },
                    )
                    Spacer(modifier = Modifier.padding(vertical = 4.dp))
                    ModeRow(
                        label = "Video call",
                        sublabel = if (cameraGranted) "audio + video" else "tap to grant camera",
                        onClick = {
                            if (cameraGranted) {
                                Logger.d("call", "start-mode picked=video chat=${chatId.take(8)}")
                                onDismiss()
                                onPlaceVideoCall()
                            } else {
                                Logger.d("call", "start-mode picked=video (pre-grant) chat=${chatId.take(8)}")
                                onDismiss()
                                onRequestCamera()
                            }
                        },
                    )
                }
            },
        )
    }
}

@Composable
private fun ModeRow(
    label: String,
    sublabel: String,
    onClick: () -> Unit,
) {
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .clip(RoundedCornerShape(6.dp))
            .background(ChatPalette.Surface)
            .clickable(onClick = onClick)
            .padding(horizontal = 12.dp, vertical = 10.dp),
    ) {
        Text(
            text = label,
            color = ChatPalette.Accent,
            fontWeight = FontWeight.SemiBold,
            fontSize = 15.sp,
        )
        Text(
            text = sublabel,
            color = ChatPalette.TextDim,
            fontSize = 12.sp,
        )
    }
}

private val StartCallModeSurface = androidx.compose.ui.graphics.Color(0xFF32302F) 
