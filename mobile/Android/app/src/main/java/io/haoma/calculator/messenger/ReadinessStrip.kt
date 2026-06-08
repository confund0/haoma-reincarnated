package io.haoma.calculator.messenger

import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.darkColorScheme
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
import androidx.compose.ui.hapticfeedback.HapticFeedbackType
import androidx.compose.ui.platform.LocalHapticFeedback
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.PopupProperties
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import kotlinx.coroutines.delay


@OptIn(ExperimentalFoundationApi::class)
@Composable
fun ReadinessStrip(store: MessengerStore, onTap: () -> Unit = {}) {
    val health by store.health.collectAsStateWithLifecycle()
    val screen by store.current.collectAsStateWithLifecycle()
    val chats by store.chats.collectAsStateWithLifecycle()
    
    
    val notices by store.unsnoozedNotices.collectAsStateWithLifecycle()
    val noticeBorder = noticeBorderColor(notices)

    
    var nowSecs by remember { mutableLongStateOf(System.currentTimeMillis() / 1000L) }
    LaunchedEffect(Unit) {
        while (true) {
            nowSecs = System.currentTimeMillis() / 1000L
            delay(TICK_SECS * 1_000L)
        }
    }

    val selfPeerId = resolveSelfPeerId(screen, chats)
    val topColor = selfDotColor(health, selfPeerId, nowSecs)
    val midColor = externalDotColor(health, nowSecs)
    val botColor = localDotColor(health, nowSecs)

    var menuOpen by remember { mutableStateOf(false) }
    val haptic = LocalHapticFeedback.current

    Box(
        modifier = Modifier
            .width(STRIP_WIDTH)
            .combinedClickable(
                onClick = onTap,
                onLongClick = {
                    haptic.performHapticFeedback(HapticFeedbackType.LongPress)
                    menuOpen = true
                },
            ),
        contentAlignment = Alignment.Center,
    ) {
        Column(
            verticalArrangement = Arrangement.spacedBy(LED_GAP),
            horizontalAlignment = Alignment.CenterHorizontally,
            modifier = if (noticeBorder != null) {
                Modifier
                    .border(NOTICE_BORDER_WIDTH, noticeBorder, RoundedCornerShape(NOTICE_BORDER_CORNER))
                    .padding(NOTICE_BORDER_PADDING)
            } else {
                Modifier
            },
        ) {
            Led(color = topColor)
            Led(color = midColor)
            Led(color = botColor)
        }
        
        
        MaterialTheme(
            colorScheme = darkColorScheme(
                surface = MenuSurface,
                surfaceContainer = MenuSurface,
                onSurface = MenuAccent,
            ),
        ) {
            DropdownMenu(
                expanded = menuOpen,
                onDismissRequest = { menuOpen = false },
                
                
                properties = PopupProperties(focusable = false),
            ) {
                DropdownMenuItem(
                    text = { Text("Force new identity", color = MenuAccent) },
                    onClick = {
                        menuOpen = false
                        store.torRecover("newnym")
                    },
                )
                DropdownMenuItem(
                    text = { Text("Reset Tor", color = MenuAccent) },
                    onClick = {
                        menuOpen = false
                        store.torRecover("reset")
                    },
                )
            }
        }
    }
}

@Composable
private fun Led(color: Color) {
    Box(
        modifier = Modifier
            .width(LED_WIDTH)
            .height(LED_HEIGHT)
            .clip(RoundedCornerShape(LED_CORNER))
            .background(color),
    )
}


private fun resolveSelfPeerId(screen: Screen, chats: List<ChatEntry>): String? = when (screen) {
    is Screen.ChatDetail -> chats.firstOrNull { it.chatId == screen.chatId }?.peerId
    is Screen.ChatSettings -> chats.firstOrNull { it.chatId == screen.chatId }?.peerId
    is Screen.ContactDetail -> screen.peerId
    else -> null
}


private fun localDotColor(h: SystemHealth, now: Long): Color {
    if (h.tor.unreachable) return C_BAD
    if (h.backendStatusAt == 0L) return C_DIM
    val age = now - h.backendStatusAt
    val base = if (h.tor.ready) C_OK else C_WARN
    return when {
        age <= FRESH_LOCAL_SECS -> base
        age <= STALE_LOCAL_SECS -> C_WARN
        else -> C_DIM
    }
}

private fun externalDotColor(h: SystemHealth, now: Long): Color {
    val r = h.externalReach ?: return C_DIM
    val age = now - r.at
    return when {
        !r.ok && age <= STALE_REMOTE_SECS -> C_BAD
        age <= FRESH_REMOTE_SECS -> C_OK
        age <= STALE_REMOTE_SECS -> C_WARN
        else -> C_DIM
    }
}

private fun selfDotColor(h: SystemHealth, peerId: String?, now: Long): Color {
    val pick = if (peerId != null) h.selfReach[peerId]
    else h.selfReach.values.maxByOrNull { it.at }
    pick ?: return C_DIM
    val age = now - pick.at
    return when {
        !pick.ok && age <= STALE_REMOTE_SECS -> C_BAD
        age <= FRESH_REMOTE_SECS -> C_OK
        age <= STALE_REMOTE_SECS -> C_WARN
        else -> C_DIM
    }
}


private const val TICK_SECS = 10L

private const val FRESH_LOCAL_SECS = 15L
private const val STALE_LOCAL_SECS = 60L


private const val FRESH_REMOTE_SECS = 30L
private const val STALE_REMOTE_SECS = 180L

private val LED_WIDTH = 10.dp
private val LED_HEIGHT = 4.dp
private val LED_GAP = 2.dp
private val LED_CORNER = 1.dp
private val STRIP_WIDTH = 20.dp
private val NOTICE_BORDER_WIDTH = 1.dp
private val NOTICE_BORDER_CORNER = 3.dp
private val NOTICE_BORDER_PADDING = 2.dp
private val C_OK = Color(0xFF5FCC1A)
private val C_WARN = Color(0xFFFABD2F)
private val C_BAD = Color(0xFFCC241D)
private val C_DIM = Color(0xFF504945)

private fun noticeBorderColor(notices: List<Notice>): Color? {
    if (notices.isEmpty()) return null
    val sev = notices.maxOf { it.severity.ordinal }
    return when (sev) {
        NoticeSeverity.SEVERE.ordinal -> C_BAD
        NoticeSeverity.WARN.ordinal -> C_WARN
        else -> C_WARN
    }
}


private val MenuSurface = Color(0xFF32302F)
private val MenuAccent = Color(0xFFFABD2F)
