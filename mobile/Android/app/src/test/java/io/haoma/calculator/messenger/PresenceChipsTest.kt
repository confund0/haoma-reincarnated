package io.haoma.calculator.messenger

import io.haoma.calculator.core.ipc.Frame
import io.haoma.calculator.core.ipc.FrameType
import org.json.JSONArray
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test


class PresenceChipsTest {

    private val aliceChat = "chat-alice"
    private val alicePc = "000000000000000000000000000a11ce"
    private val alicePhone = "000000000000000000000000000a1fff"
    private val carolChat = "chat-carol"
    private val carol = "00000000000000000000000000ca401e"

    private fun chatJson(chatId: String, peerId: String, members: List<String>) = JSONObject()
        .put("chat_id", chatId)
        .put("kind", "direct")
        .put("peer_id", peerId)
        .put("members", JSONArray(members))
        .put("effective", "unknown")

    private fun peerJson(id: String, chatId: String) = JSONObject()
        .put("id", id)
        .put("chat_id", chatId)
        .put("effective", "unknown")

    private fun store(): MessengerStore {
        val s = MessengerStore()
        s.upsertChat(ChatEntry.fromJson(chatJson(aliceChat, alicePc, listOf(alicePc, alicePhone))))
        s.upsertChat(ChatEntry.fromJson(chatJson(carolChat, carol, listOf(carol))))
        s.upsertPeer(PeerEntry.fromJson(peerJson(alicePc, aliceChat)))
        s.upsertPeer(PeerEntry.fromJson(peerJson(alicePhone, aliceChat)))
        s.upsertPeer(PeerEntry.fromJson(peerJson(carol, carolChat)))
        return s
    }

    private fun peerFrame(peerId: String, v: PresenceView) = Frame(
        type = FrameType.PresenceChanged,
        payload = JSONObject().put("peer_id", peerId).put("accepting", v.accepting)
            .put("sending", v.sending).put("chatty", v.chatty).put("effective", v.chatty.ifEmpty { "unknown" }),
    )

    private fun chatFrame(chatId: String, v: PresenceView) = Frame(
        type = FrameType.ChatPresenceChanged,
        payload = JSONObject().put("chat_id", chatId).put("accepting", v.accepting)
            .put("sending", v.sending).put("chatty", v.chatty).put("effective", v.chatty.ifEmpty { "unknown" }),
    )

    private fun MessengerStore.chat(id: String) = chats.value.first { it.chatId == id }
    private fun MessengerStore.peer(id: String) = peers.value.first { it.id == id }
    private fun MessengerStore.contact(id: String) = contactPresence(peer(id), presence.value)

    @Test
    fun `the chat's presence lands on its chat and nowhere else`() {
        val s = store()
        val busyBoth = PresenceView(accepting = true, sending = true, chatty = "busy")
        s.dispatch(chatFrame(aliceChat, busyBoth))

        assertEquals(busyBoth, chatPresence(s.chat(aliceChat)))
        assertEquals(PresenceView(), chatPresence(s.chat(carolChat)))
        
        assertEquals(PresenceView(), s.contact(alicePc))
        assertEquals(PresenceView(), s.contact(alicePhone))
    }

    @Test
    fun `a pairing's own presence lands on its contacts row and not on the chat`() {
        val s = store()
        s.dispatch(chatFrame(aliceChat, PresenceView(sending = true, chatty = "available")))
        s.dispatch(peerFrame(alicePc, PresenceView(accepting = true)))
        s.dispatch(peerFrame(alicePhone, PresenceView(sending = true, chatty = "available")))

        assertEquals(PresenceView(accepting = true), s.contact(alicePc))
        assertEquals(PresenceView(sending = true, chatty = "available"), s.contact(alicePhone))
        
        assertEquals(PresenceView(sending = true, chatty = "available"), chatPresence(s.chat(aliceChat)))
    }

    @Test
    fun `a chat row from the daemon carries the chat's presence`() {
        val s = store()
        s.upsertChat(
            ChatEntry.fromJson(
                chatJson(aliceChat, alicePc, listOf(alicePc, alicePhone))
                    .put("sending", true).put("chatty", "away").put("effective", "away"),
            ),
        )
        assertEquals(PresenceView(sending = true, chatty = "away"), chatPresence(s.chat(aliceChat)))
        assertEquals(PresenceView(), s.contact(alicePc))
    }

    @Test
    fun `a peer row from the daemon carries its own arrows`() {
        val s = store()
        s.upsertPeer(PeerEntry.fromJson(peerJson(alicePhone, aliceChat).put("sending", true).put("effective", "unknown")))
        assertEquals(PresenceView(sending = true), s.contact(alicePhone))
    }

    @Test
    fun `a peer list after a reconnect replaces a stale arrow`() {
        val s = store()
        s.dispatch(peerFrame(alicePhone, PresenceView(sending = true, chatty = "busy")))
        s.dispatch(peerFrame(alicePc, PresenceView(accepting = true)))
        assertEquals(PresenceView(sending = true, chatty = "busy"), s.contact(alicePhone))

        
        s.applyPeersSnapshot(
            listOf(
                PeerEntry.fromJson(JSONObject().put("id", alicePc).put("chat_id", aliceChat).put("accepting", true).put("effective", "accepting")),
                PeerEntry.fromJson(JSONObject().put("id", alicePhone).put("chat_id", aliceChat)),
            ),
        )
        assertEquals(PresenceView(), s.contact(alicePhone))
        assertEquals(PresenceView(accepting = true), s.contact(alicePc))
        
        assertNull(s.presence.value[carol])
    }

    @Test
    fun `busy wins over the just-paired green, the other states do not`() {
        fun colour(chatty: String, fresh: Boolean) = io.haoma.calculator.messenger.contacts.contactLabelColour(
            inCall = false, retired = false, unreachable = false, isFresh = fresh, chatty = chatty, unnamed = false,
        )
        val freshGreen = androidx.compose.ui.graphics.Color(0xFF5FCC1A)
        assertEquals(HaomaPalette.C_PRESENCE_BUSY, colour("busy", fresh = true))
        assertEquals(HaomaPalette.C_PRESENCE_BUSY, colour("busy", fresh = false))
        assertEquals(freshGreen, colour("away", fresh = true))
        assertEquals(HaomaPalette.C_PRESENCE_AWAY, colour("away", fresh = false))
        assertEquals(freshGreen, colour("", fresh = true))
        assertEquals(HaomaPalette.FG_PRIMARY, colour("", fresh = false))
        
        assertEquals(
            HaomaPalette.C_ATTENTION,
            io.haoma.calculator.messenger.contacts.contactLabelColour(
                inCall = false, retired = false, unreachable = true, isFresh = false, chatty = "busy", unnamed = false,
            ),
        )
    }

    @Test
    fun `a presence for a chat this client does not hold is dropped`() {
        val s = store()
        s.dispatch(chatFrame("chat-gone", PresenceView(chatty = "busy")))
        assertEquals(listOf(aliceChat, carolChat), s.chats.value.map { it.chatId })
    }

    @Test
    fun `each arrow is blue when on and dark grey when off`() {
        assertEquals(HaomaPalette.C_PRESENCE_ARROW_ON, arrowColour(true))
        assertEquals(HaomaPalette.C_PRESENCE_ARROW_OFF, arrowColour(false))
        assertEquals(androidx.compose.ui.graphics.Color(0xFF5F87FF), HaomaPalette.C_PRESENCE_ARROW_ON)
        org.junit.Assert.assertNotEquals(arrowColour(true), arrowColour(false))
    }

    @Test
    fun `the name takes the colour of the human state`() {
        assertEquals(HaomaPalette.C_PRESENCE_AVAILABLE, presenceNameColour("available"))
        assertEquals(HaomaPalette.C_PRESENCE_AWAY, presenceNameColour("away"))
        assertEquals(HaomaPalette.C_PRESENCE_BUSY, presenceNameColour("busy"))
        assertNull(presenceNameColour(""))
        assertNull(presenceNameColour("accepting"))
        assertNull(presenceNameColour("a-state-this-build-does-not-know"))
    }
}
