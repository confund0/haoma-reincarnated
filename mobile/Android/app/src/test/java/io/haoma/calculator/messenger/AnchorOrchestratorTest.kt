package io.haoma.calculator.messenger

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test


class AnchorOrchestratorTest {

    private fun makeEvent(msgId: String, displayTs: Long = 100L): TimelineEvent = TimelineEvent(
        recvSeq = displayTs,
        chatId = "chat-1",
        direction = EventDirection.IN,
        kind = EventKind.TEXT,
        displayTs = displayTs,
        senderTs = displayTs,
        recvTs = displayTs,
        senderSeq = displayTs,
        senderPeerId = "",
        envelopeId = "",
        msgId = msgId,
        decryptStatus = "ok",
        failReason = "",
        body = JSONObject().put("text", "hello"),
        deliveryState = "",
        expireSeconds = 0,
        readAt = 0L,
        editedAt = 0L,
        deletedAt = 0L,
    )

    @Test
    fun bumpAnchorSetsFreshSeq() {
        val store = MessengerStore()
        store.bumpAnchor("chat-1", "msg-1")
        val state = store.pendingAnchors.value["chat-1"]
        assertNotNull(state)
        assertEquals("msg-1", state!!.msgId)
        assertTrue("seq is positive on first bump", state.seq > 0L)
    }

    @Test
    fun bumpAnchorAlwaysIncrementsSeq() {
        val store = MessengerStore()
        store.bumpAnchor("chat-1", "msg-1")
        val first = store.pendingAnchors.value["chat-1"]!!.seq
        store.bumpAnchor("chat-1", "msg-1")
        val second = store.pendingAnchors.value["chat-1"]!!.seq
        store.bumpAnchor("chat-1", "msg-2")
        val third = store.pendingAnchors.value["chat-1"]!!.seq
        assertTrue("repeat tap same row gets a fresh seq", second > first)
        assertTrue("tap on different row still increments", third > second)
    }

    @Test
    fun bumpAnchorSeqStrictlyMonotonicAcrossConsumes() {
        
        
        val store = MessengerStore()
        store.bumpAnchor("chat-1", "msg-1")
        val first = store.pendingAnchors.value["chat-1"]!!.seq
        store.consumeAnchor("chat-1", first)
        assertNull(store.pendingAnchors.value["chat-1"])
        store.bumpAnchor("chat-1", "msg-1")
        val second = store.pendingAnchors.value["chat-1"]!!.seq
        assertTrue("re-tap after consume gets a strictly higher seq", second > first)
    }

    @Test
    fun bumpAnchorIndependentAcrossChats() {
        val store = MessengerStore()
        store.bumpAnchor("chat-a", "msg-1")
        store.bumpAnchor("chat-b", "msg-1")
        val a = store.pendingAnchors.value["chat-a"]!!.seq
        val b = store.pendingAnchors.value["chat-b"]!!.seq
        assertTrue("each chat carries its own published seq", a != b)
    }

    @Test
    fun scrollToMsgIdRejectsEmptyArgs() {
        val store = MessengerStore()
        store.scrollToMsgId("", "msg-1")
        store.scrollToMsgId("chat-1", "")
        assertTrue("no anchor written for empty args", store.pendingAnchors.value.isEmpty())
    }

    @Test
    fun scrollToMsgIdPresentInCacheNoMiss() {
        val store = MessengerStore()
        store._timelines.value = mapOf(
            "chat-1" to TimelineCache(
                chatId = "chat-1",
                events = listOf(makeEvent("msg-1"), makeEvent("msg-2", displayTs = 200L)),
                hasMore = true,
            ),
        )
        var missFired = false
        store.scrollToMsgId("chat-1", "msg-1") { missFired = true }
        assertFalse("present-in-cache must not fire onMiss", missFired)
        val state = store.pendingAnchors.value["chat-1"]
        assertNotNull("anchor still published for the surface to consume", state)
        assertEquals("msg-1", state!!.msgId)
    }

    @Test
    fun scrollToMsgIdDefinitiveMissFiresOnceAndClears() {
        val store = MessengerStore()
        store._timelines.value = mapOf(
            "chat-1" to TimelineCache(
                chatId = "chat-1",
                events = listOf(makeEvent("msg-other")),
                hasMore = false,
            ),
        )
        var missFired = 0
        store.scrollToMsgId("chat-1", "msg-gone") { missFired++ }
        assertEquals("miss fires exactly once", 1, missFired)
        assertNull(
            "anchor cleared on definitive miss so the surface doesn't pulse a wrong row",
            store.pendingAnchors.value["chat-1"],
        )
    }

    @Test
    fun scrollToMsgIdEmptyCacheDoesNotMissSynchronously() {
        
        
        val store = MessengerStore()
        var missFired = false
        store.scrollToMsgId("chat-1", "msg-gone") { missFired = true }
        assertFalse(
            "empty-but-pageable cache defers to the loop; no synchronous miss",
            missFired,
        )
        assertNotNull(store.pendingAnchors.value["chat-1"])
    }

    @Test
    fun consumeAnchorClearsWhenSeqMatches() {
        val store = MessengerStore()
        store.bumpAnchor("chat-1", "msg-1")
        val seq = store.pendingAnchors.value["chat-1"]!!.seq
        store.consumeAnchor("chat-1", seq)
        assertNull(store.pendingAnchors.value["chat-1"])
    }

    @Test
    fun consumeAnchorLeavesFresherTapAlone() {
        
        
        val store = MessengerStore()
        store.bumpAnchor("chat-1", "msg-1")
        val first = store.pendingAnchors.value["chat-1"]!!.seq
        store.bumpAnchor("chat-1", "msg-1")
        store.consumeAnchor("chat-1", first)
        val state = store.pendingAnchors.value["chat-1"]
        assertNotNull("fresher anchor survives the stale consume", state)
        assertTrue("survivor carries the newer seq", state!!.seq > first)
    }
}
