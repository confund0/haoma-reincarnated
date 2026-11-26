package io.haoma.calculator.messenger

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test


class CallStreamSideTest {

    private fun stats(framesIn: Long, framesOut: Long = 0L) = CallStreamEventPayload(
        callId = "c1",
        side = "spk",
        type = "stats",
        jitterMs = 0.0,
        framesIn = framesIn,
        framesOut = framesOut,
        framesDropped = 0L,
        reason = "",
    )

    @Test
    fun firstSampleWithFramesIsNotStopped() {
        val first = CallStreamSide.next(null, stats(framesIn = 40, framesOut = 40), 1_000L)
        assertFalse(first.hasPrev)
        assertFalse(first.framesInStopped)
        assertFalse(first.framesOutStopped)
    }

    @Test
    fun secondSampleWithSameCountIsStopped() {
        val first = CallStreamSide.next(null, stats(framesIn = 40, framesOut = 40), 1_000L)
        val second = CallStreamSide.next(first, stats(framesIn = 40, framesOut = 40), 2_000L)
        assertTrue(second.framesInStopped)
        assertTrue(second.framesOutStopped)
    }

    @Test
    fun movingCountIsNotStopped() {
        val first = CallStreamSide.next(null, stats(framesIn = 40, framesOut = 40), 1_000L)
        val second = CallStreamSide.next(first, stats(framesIn = 90, framesOut = 90), 2_000L)
        assertFalse(second.framesInStopped)
        assertFalse(second.framesOutStopped)
    }

    @Test
    fun noFramesYetIsNotStopped() {
        val first = CallStreamSide.next(null, stats(framesIn = 0), 1_000L)
        val second = CallStreamSide.next(first, stats(framesIn = 0), 2_000L)
        assertFalse(second.framesInStopped)
        assertFalse(second.framesOutStopped)
    }
}
