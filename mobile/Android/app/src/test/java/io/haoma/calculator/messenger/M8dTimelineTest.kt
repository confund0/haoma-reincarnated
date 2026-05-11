package io.haoma.calculator.messenger

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test


class M8dTimelineTest {

    private fun makeEvent(
        recvSeq: Long,
        displayTs: Long,
        chatId: String = "chat-1",
        direction: String = EventDirection.IN,
        kind: String = EventKind.TEXT,
        msgId: String = "msg-$recvSeq",
        envelopeId: String = "",
        text: String = "hello",
        deliveryState: String = "",
        deletedAt: Long = 0L,
        editedAt: Long = 0L,
    ): TimelineEvent = TimelineEvent(
        recvSeq = recvSeq,
        chatId = chatId,
        direction = direction,
        kind = kind,
        displayTs = displayTs,
        senderTs = displayTs,
        recvTs = displayTs,
        senderSeq = recvSeq,
        senderPeerId = "",
        envelopeId = envelopeId,
        msgId = msgId,
        decryptStatus = if (direction == EventDirection.IN) "ok" else "",
        body = JSONObject().put("text", text),
        deliveryState = deliveryState,
        expireSeconds = 0,
        readAt = 0L,
        editedAt = editedAt,
        deletedAt = deletedAt,
    )

    @Test fun mergeAppendsByDisplayTs() {
        var cache = TimelineCache(chatId = "chat-1")
        cache = mergeTimelineEvent(cache, makeEvent(recvSeq = 1, displayTs = 100))
        cache = mergeTimelineEvent(cache, makeEvent(recvSeq = 3, displayTs = 300))
        cache = mergeTimelineEvent(cache, makeEvent(recvSeq = 2, displayTs = 200))
        assertEquals(listOf(1L, 2L, 3L), cache.events.map { it.recvSeq })
        assertEquals(100L, cache.oldestDisplayTs)
    }

    @Test fun mergeBreaksTieByRecvSeq() {
        var cache = TimelineCache(chatId = "chat-1")
        cache = mergeTimelineEvent(cache, makeEvent(recvSeq = 5, displayTs = 100))
        cache = mergeTimelineEvent(cache, makeEvent(recvSeq = 4, displayTs = 100))
        assertEquals(listOf(4L, 5L), cache.events.map { it.recvSeq })
    }

    @Test fun mergeUpsertsByMsgId() {
        var cache = TimelineCache(chatId = "chat-1")
        cache = mergeTimelineEvent(
            cache,
            makeEvent(recvSeq = 1, displayTs = 100, msgId = "abc", text = "first"),
        )
        
        cache = mergeTimelineEvent(
            cache,
            makeEvent(recvSeq = 1, displayTs = 100, msgId = "abc", text = "second", editedAt = 999L),
        )
        assertEquals(1, cache.events.size)
        assertEquals("second", cache.events[0].bodyTextOrEmpty())
        assertTrue(cache.events[0].isEdited)
    }

    @Test fun mergeMonotonicProgressNoDowngrade() {
        var cache = TimelineCache(chatId = "chat-1")
        val first = makeEvent(
            recvSeq = 1, displayTs = 100, direction = EventDirection.OUT,
            envelopeId = "env-1", deliveryState = DeliveryState.READ,
        )
        cache = mergeTimelineEvent(cache, first)
        
        val stale = first.copy(deliveryState = DeliveryState.SENT)
        cache = mergeTimelineEvent(cache, stale)
        assertEquals(DeliveryState.READ, cache.events[0].deliveryState)
    }

    @Test fun applyDeliveryStatusUpdatesByEnvelope() {
        var cache = TimelineCache(chatId = "chat-1")
        cache = mergeTimelineEvent(
            cache,
            makeEvent(
                recvSeq = 1, displayTs = 100, direction = EventDirection.OUT,
                envelopeId = "env-1", deliveryState = DeliveryState.ENQUEUED,
            ),
        )
        cache = applyDeliveryStatus(cache, "env-1", DeliveryState.SENT)
        assertEquals(DeliveryState.SENT, cache.events[0].deliveryState)
    }

    @Test fun applyDeliveryStatusRefusesDowngradeFromRead() {
        var cache = TimelineCache(chatId = "chat-1")
        cache = mergeTimelineEvent(
            cache,
            makeEvent(
                recvSeq = 1, displayTs = 100, direction = EventDirection.OUT,
                envelopeId = "env-1", deliveryState = DeliveryState.READ,
            ),
        )
        
        val sameRef = cache
        cache = applyDeliveryStatus(cache, "env-1", DeliveryState.DELIVERED)
        assertSame(sameRef, cache)
        
        cache = applyDeliveryStatus(cache, "env-1", DeliveryState.FAILED)
        assertEquals(DeliveryState.READ, cache.events[0].deliveryState)
    }

    @Test fun applyDeliveryStatusUnknownEnvelopeNoOp() {
        val cache = TimelineCache(chatId = "chat-1")
        val same = applyDeliveryStatus(cache, "env-unknown", DeliveryState.SENT)
        assertSame(cache, same)
    }

    @Test fun removeTimelineEventDropsByRecvSeq() {
        var cache = TimelineCache(chatId = "chat-1")
        cache = mergeTimelineEvent(cache, makeEvent(recvSeq = 1, displayTs = 100))
        cache = mergeTimelineEvent(cache, makeEvent(recvSeq = 2, displayTs = 200))
        cache = removeTimelineEvent(cache, recvSeq = 1L)
        assertEquals(listOf(2L), cache.events.map { it.recvSeq })
        assertEquals(200L, cache.oldestDisplayTs)
    }

    @Test fun removeTimelineEventUnknownNoOp() {
        var cache = TimelineCache(chatId = "chat-1")
        cache = mergeTimelineEvent(cache, makeEvent(recvSeq = 1, displayTs = 100))
        val before = cache
        cache = removeTimelineEvent(cache, recvSeq = 999L)
        assertSame(before, cache)
    }

    @Test fun mergeTimelinePageMergesAndStampsHasMore() {
        val page = TimelinePageResponse(
            peerId = "peer-1",
            events = listOf(
                makeEvent(recvSeq = 3, displayTs = 300),
                makeEvent(recvSeq = 1, displayTs = 100),
                makeEvent(recvSeq = 2, displayTs = 200),
            ),
            hasMore = false,
        )
        var cache = TimelineCache(chatId = "chat-1", loading = true)
        cache = mergeTimelinePage(cache, page)
        assertEquals(listOf(1L, 2L, 3L), cache.events.map { it.recvSeq })
        assertFalse(cache.loading)
        assertFalse(cache.hasMore)
        assertEquals(100L, cache.oldestDisplayTs)
    }

    @Test fun timelineEventDecodesFromWire() {
        val raw = JSONObject(
            """
            {
              "recv_seq": 7,
              "chat_id": "chat-1",
              "direction": "in",
              "kind": "text",
              "display_ts": 1700000000,
              "msg_id": "abc",
              "body": {"text": "hi"},
              "decrypt_status": "ok"
            }
            """.trimIndent(),
        )
        val ev = TimelineEvent.fromJson(raw)
        assertEquals(7L, ev.recvSeq)
        assertEquals("hi", ev.bodyTextOrEmpty())
        assertTrue(ev.isInbound)
        assertNotNull(ev.body)
    }

    @Test fun deliveryStatusPayloadDecodesAllFields() {
        val raw = JSONObject(
            """{"envelope_id":"env-9","state":"sent","at":12345,"attempts":1}""",
        )
        val u = DeliveryStatusPayload.fromJson(raw)
        assertEquals("env-9", u.envelopeId)
        assertEquals("sent", u.state)
        assertEquals(12345L, u.at)
        assertEquals(1, u.attempts)
        assertEquals("", u.lastError)
    }

    @Test fun listTimelineRequestEncodesPeerIdAndOptionals() {
        val first = ListTimelineRequest(peerId = "p", limit = 50, beforeDisplayTs = 0L).toJson()
        assertEquals("p", first.getString("peer_id"))
        assertEquals(50, first.getInt("limit"))
        
        assertFalse(first.has("before_display_ts"))

        val paged = ListTimelineRequest(peerId = "p", limit = 25, beforeDisplayTs = 1700L).toJson()
        assertEquals(1700L, paged.getLong("before_display_ts"))
        assertEquals(25, paged.getInt("limit"))
    }

    @Test fun timelineEventBodyTextNullableSafe() {
        val noBody = TimelineEvent.fromJson(
            JSONObject("""{"recv_seq":1,"chat_id":"c","direction":"in","kind":"text","display_ts":1}"""),
        )
        assertNull(noBody.body)
        assertEquals("", noBody.bodyTextOrEmpty())
    }
}
