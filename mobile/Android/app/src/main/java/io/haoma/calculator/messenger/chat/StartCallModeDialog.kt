package io.haoma.calculator.messenger.chat

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.haoma.calculator.log.Logger
import io.haoma.calculator.messenger.calls.CallIcons
import io.haoma.calculator.messenger.calls.fontAwesomeSolid


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
                        glyph = CallIcons.Headset,
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
                        glyph = CallIcons.Video,
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
    glyph: String,
    label: String,
    sublabel: String,
    onClick: () -> Unit,
) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .clip(RoundedCornerShape(6.dp))
            .background(ChatPalette.Surface)
            .clickable(onClick = onClick)
            .padding(start = 24.dp, end = 12.dp, top = 10.dp, bottom = 10.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(
            text = glyph,
            color = ModeGlyph,
            fontSize = 22.sp,
            fontFamily = fontAwesomeSolid(),
        )
        Spacer(modifier = Modifier.width(44.dp))
        Column {
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
}

private val StartCallModeSurface = Color(0xFF32302F) 
private val ModeGlyph = Color(0xFF8EC07C)            
