package io.haoma.calculator.messenger.chat

import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Call
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.haoma.calculator.log.Logger
import io.haoma.calculator.messenger.ChatEntry


@OptIn(ExperimentalFoundationApi::class)
@Composable
internal fun ChatTitleBar(
    chat: ChatEntry?,
    chatId: String,
    presence: String?,
    inCall: Boolean,
    otherChatInCall: Boolean,
    canCall: Boolean,
    cameraGranted: Boolean,
    onBack: () -> Unit,
    onPlaceCall: () -> Unit,
    onPlaceVideoCall: () -> Unit,
    onRequestCamera: () -> Unit,
    onHangup: () -> Unit,
    onRotateTor: () -> Unit,
    onNewTorCircuit: () -> Unit,
    onToggleMute: () -> Unit,
    onOpenSettings: () -> Unit,
    onViewFiles: () -> Unit,
) {
    val label = chat?.label.orEmpty().ifEmpty { chatId.take(8) }
    val effective = presence.orEmpty().ifEmpty { chat?.effective.orEmpty() }
    val muted = chat?.notificationsMuted == true
    var menuOpen by remember { mutableStateOf(false) }
    var startCallModeOpen by remember { mutableStateOf(false) }
    val (presenceGlyph, presenceColor) = presenceGlyphFor(effective)

    StartCallModeDialog(
        open = startCallModeOpen,
        chatId = chatId,
        cameraGranted = cameraGranted,
        onPlaceAudioCall = onPlaceCall,
        onPlaceVideoCall = onPlaceVideoCall,
        onRequestCamera = onRequestCamera,
        onDismiss = { startCallModeOpen = false },
    )

    Row(
        modifier = Modifier
            .fillMaxWidth()
            .background(ChatPalette.InboundBubble)
            .padding(horizontal = 12.dp, vertical = 6.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(4.dp),
    ) {
        Text(
            text = "‹",
            color = ChatPalette.Accent,
            fontSize = 22.sp,
            fontWeight = FontWeight.Bold,
            modifier = Modifier
                .clickable(onClick = onBack)
                .padding(horizontal = 8.dp, vertical = 4.dp),
        )
        Spacer(modifier = Modifier.width(12.dp))
        
        
        Text(
            text = presenceGlyph,
            color = presenceColor,
            fontSize = 16.sp,
            fontWeight = FontWeight.Bold,
        )
        Spacer(modifier = Modifier.width(8.dp))
        Text(
            text = label,
            
            
            color = if (inCall) ChatPalette.Bad else ChatPalette.Text,
            fontWeight = FontWeight.SemiBold,
            fontSize = 16.sp,
            modifier = Modifier.weight(1f),
        )
        
        
        val phoneTint = when {
            inCall -> ChatPalette.Bad
            canCall -> ChatPalette.Accent
            else -> ChatPalette.TextFaint
        }
        
        
        Box(
            modifier = Modifier
                .size(48.dp)
                .combinedClickable(
                    enabled = inCall || canCall,
                    onClick = { if (inCall) onHangup() else if (canCall) onPlaceCall() },
                    onLongClick = {
                        if (!inCall && canCall) {
                            Logger.d("call", "long-press call affordance chat=${chatId.take(8)}")
                            startCallModeOpen = true
                        }
                    },
                ),
            contentAlignment = Alignment.Center,
        ) {
            Icon(
                imageVector = Icons.Filled.Call,
                contentDescription = if (inCall) "Hang up" else "Voice call (long-press for modes)",
                tint = phoneTint,
            )
        }
        
        
        if (otherChatInCall) {
            io.haoma.calculator.messenger.calls.CallChipGlyph()
            Spacer(modifier = Modifier.width(4.dp))
        }
        Box {
            IconButton(onClick = { menuOpen = true }) {
                Icon(
                    imageVector = Icons.Filled.MoreVert,
                    contentDescription = "More actions",
                    tint = ChatPalette.Text,
                )
            }
            
            
            MaterialTheme(
                colorScheme = darkColorScheme(
                    surface = MenuSurface,
                    surfaceContainer = MenuSurface,
                    onSurface = ChatPalette.Accent,
                ),
            ) {
                DropdownMenu(
                    expanded = menuOpen,
                    onDismissRequest = { menuOpen = false },
                ) {
                    DropdownMenuItem(
                        text = {
                            Text(
                                text = if (muted) "🔕 Unmute" else "🔕 Mute",
                                color = ChatPalette.Accent,
                            )
                        },
                        onClick = {
                            menuOpen = false
                            onToggleMute()
                        },
                    )
                    DropdownMenuItem(
                        text = { Text("View files…", color = ChatPalette.Accent) },
                        onClick = {
                            menuOpen = false
                            onViewFiles()
                        },
                    )
                    DropdownMenuItem(
                        text = { Text("Details…", color = ChatPalette.Accent) },
                        onClick = {
                            menuOpen = false
                            onOpenSettings()
                        },
                    )
                    DropdownMenuItem(
                        text = { Text("New Tor circuit", color = ChatPalette.Accent) },
                        onClick = {
                            menuOpen = false
                            onNewTorCircuit()
                        },
                    )
                    DropdownMenuItem(
                        text = { Text("Rotate Tor", color = ChatPalette.Accent) },
                        onClick = {
                            menuOpen = false
                            onRotateTor()
                        },
                    )
                }
            }
        }
    }
}


private fun presenceGlyphFor(effective: String): Pair<String, Color> = when (effective) {
    "available" -> "●" to ChatPalette.Ok
    "away" -> "●" to Color(0xFFFABD2F)              
    "busy" -> "●" to ChatPalette.Bad
    "accepting" -> "◐" to Color(0xFF83A598)          
    else -> "○" to ChatPalette.TextDim
}

private val MenuSurface = Color(0xFF32302F) 
