package io.haoma.calculator.messenger

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test


class DeliveryLadderTest {

    private fun row(
        direction: String,
        kind: String,
        deliveryState: String,
        envelopeId: String = "env-1",
    ): TimelineEvent = TimelineEvent(
        recvSeq = 1L,
        chatId = "chat-1",
        direction = direction,
        kind = kind,
        displayTs = 100L,
        senderTs = 100L,
        recvTs = 100L,
        senderSeq = 1L,
        senderPeerId = "",
        envelopeId = envelopeId,
        msgId = "msg-1",
        decryptStatus = if (direction == EventDirection.IN) "ok" else "",
        failReason = "",
        body = JSONObject().put("text", "hello"),
        deliveryState = deliveryState,
        expireSeconds = 0,
        readAt = 0L,
        editedAt = 0L,
        deletedAt = 0L,
    )

    @Test fun unsetIsBelowEveryStep() {
        assertEquals(0, DeliveryState.rank("")!!)
        assertTrue(DeliveryState.outranks("", DeliveryState.ENQUEUED))
        assertTrue(DeliveryState.outranks("", DeliveryState.READ))
    }

    @Test fun theLadderClimbsInOneOrder() {
        val steps = listOf(
            "",
            DeliveryState.ENQUEUED,
            DeliveryState.SENT,
            DeliveryState.DELIVERED,
            DeliveryState.READ,
        )
        for (i in steps.indices) {
            for (j in steps.indices) {
                val expected = j > i
                assertEquals(
                    "outranks(${steps[i]}, ${steps[j]})",
                    expected,
                    DeliveryState.outranks(steps[i], steps[j]),
                )
            }
        }
    }

    @Test fun pendingAndEnqueuedShareAStep() {
        assertEquals(DeliveryState.rank(DeliveryState.ENQUEUED), DeliveryState.rank(DeliveryState.PENDING))
        assertFalse(DeliveryState.outranks(DeliveryState.PENDING, DeliveryState.ENQUEUED))
        assertFalse(DeliveryState.outranks(DeliveryState.ENQUEUED, DeliveryState.PENDING))
    }

    
    @Test fun failureIsNotOnTheLadder() {
        assertNull(DeliveryState.rank(DeliveryState.FAILED))
        assertFalse(DeliveryState.outranks(DeliveryState.SENT, DeliveryState.FAILED))
        assertFalse(DeliveryState.outranks(DeliveryState.FAILED, DeliveryState.SENT))
    }

    
    @Test fun transferStatesAreNotOnTheLadder() {
        for (s in listOf("downloading", "awaiting_key", "ready", "failed_transient", "failed_permanent")) {
            assertNull("rank($s)", DeliveryState.rank(s))
        }
    }

    
    @Test fun aRowAtSentTakesAFailure() {
        assertTrue(DeliveryState.advances(DeliveryState.SENT, DeliveryState.FAILED))
    }

    @Test fun aFailureTakesOverEveryStepBelowAPeersWord() {
        for (cur in listOf("", DeliveryState.PENDING, DeliveryState.ENQUEUED, DeliveryState.SENT)) {
            assertTrue("advances($cur, failed)", DeliveryState.advances(cur, DeliveryState.FAILED))
        }
    }

    
    @Test fun aFailureIsRefusedOnceAPeerHasSpoken() {
        assertFalse(DeliveryState.advances(DeliveryState.DELIVERED, DeliveryState.FAILED))
        assertFalse(DeliveryState.advances(DeliveryState.READ, DeliveryState.FAILED))
    }

    @Test fun anOutboxReportMayOnlyClimb() {
        assertTrue(DeliveryState.advances(DeliveryState.ENQUEUED, DeliveryState.SENT))
        assertFalse(DeliveryState.advances(DeliveryState.DELIVERED, DeliveryState.SENT))
        assertFalse(DeliveryState.advances(DeliveryState.READ, DeliveryState.SENT))
        assertFalse(DeliveryState.advances(DeliveryState.READ, DeliveryState.DELIVERED))
    }

    @Test fun aRepeatedReportIsNotAMove() {
        assertFalse(DeliveryState.advances(DeliveryState.SENT, DeliveryState.SENT))
        assertFalse(DeliveryState.advances(DeliveryState.FAILED, DeliveryState.FAILED))
    }

    
    @Test fun anEmptyReportIsRefused() {
        assertFalse(DeliveryState.advances(DeliveryState.SENT, ""))
    }

    
    @Test fun anUnsetRowTakesTheFirstThingThatArrives() {
        assertTrue(DeliveryState.advances("", DeliveryState.FAILED))
        assertTrue(DeliveryState.advances("", DeliveryState.ENQUEUED))
    }

    
    @Test fun applyDeliveryStatusLandsAFailureOnASentRow() {
        var cache = TimelineCache(chatId = "chat-1")
        cache = mergeTimelineEvent(
            cache,
            row(EventDirection.OUT, EventKind.TEXT, DeliveryState.SENT),
        )
        cache = applyDeliveryStatus(cache, "env-1", DeliveryState.FAILED)
        assertEquals(DeliveryState.FAILED, cache.events[0].deliveryState)
    }

    
    @Test fun applyDeliveryStatusRefusesALateSentOverDelivered() {
        var cache = TimelineCache(chatId = "chat-1")
        cache = mergeTimelineEvent(
            cache,
            row(EventDirection.OUT, EventKind.FILE, DeliveryState.DELIVERED),
        )
        cache = applyDeliveryStatus(cache, "env-1", DeliveryState.SENT)
        assertEquals(DeliveryState.DELIVERED, cache.events[0].deliveryState)
    }

    
    @Test fun aFailureCrossingFromASiblingLandsOnAMirroredRow() {
        var cache = TimelineCache(chatId = "chat-1")
        val mirrored = row(EventDirection.OUT, EventKind.TEXT, DeliveryState.SENT)
        cache = mergeTimelineEvent(cache, mirrored)
        cache = mergeTimelineEvent(cache, mirrored.copy(deliveryState = DeliveryState.FAILED))
        assertEquals(DeliveryState.FAILED, cache.events[0].deliveryState)
    }

    
    @Test fun aStaleStepDownIsRefusedOnTheMergePath() {
        var cache = TimelineCache(chatId = "chat-1")
        val held = row(EventDirection.OUT, EventKind.FILE, DeliveryState.DELIVERED)
        cache = mergeTimelineEvent(cache, held)
        cache = mergeTimelineEvent(cache, held.copy(deliveryState = DeliveryState.SENT))
        assertEquals(DeliveryState.DELIVERED, cache.events[0].deliveryState)
    }

    
    @Test fun anInboundFileTransferStateStillMoves() {
        var cache = TimelineCache(chatId = "chat-1")
        val inbound = row(EventDirection.IN, EventKind.FILE, "downloading", envelopeId = "")
        cache = mergeTimelineEvent(cache, inbound)
        cache = mergeTimelineEvent(cache, inbound.copy(deliveryState = "ready"))
        assertEquals("ready", cache.events[0].deliveryState)
    }
}
