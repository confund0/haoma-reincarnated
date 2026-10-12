package io.haoma.calculator.messenger

import androidx.compose.ui.graphics.Color


data class PresenceView(
    val accepting: Boolean = false,
    val sending: Boolean = false,
    val chatty: String = "",
)


internal fun chatPresence(chat: ChatEntry): PresenceView =
    PresenceView(accepting = chat.accepting, sending = chat.sending, chatty = chat.chatty)


internal fun contactPresence(peer: PeerEntry, presence: Map<String, PresenceView>): PresenceView =
    presence[peer.id] ?: peerPresence(peer)


internal fun peerPresence(peer: PeerEntry): PresenceView =
    PresenceView(accepting = peer.accepting, sending = peer.sending, chatty = peer.chatty)


internal fun arrowColour(on: Boolean): Color =
    if (on) HaomaPalette.C_PRESENCE_ARROW_ON else HaomaPalette.C_PRESENCE_ARROW_OFF


internal fun presenceNameColour(chatty: String): Color? = when (chatty) {
    "available" -> HaomaPalette.C_PRESENCE_AVAILABLE
    "away" -> HaomaPalette.C_PRESENCE_AWAY
    "busy" -> HaomaPalette.C_PRESENCE_BUSY
    else -> null
}
