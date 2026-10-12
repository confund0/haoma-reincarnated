package io.haoma.calculator.messenger.chat

import io.haoma.calculator.messenger.DeliveryState
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test


class DeliveryGlyphMirroredTest {

    private val everyState = listOf(
        DeliveryState.ENQUEUED,
        DeliveryState.SENT,
        DeliveryState.DELIVERED,
        DeliveryState.READ,
        DeliveryState.FAILED,
    )

    @Test
    fun anUnmirroredRowCarriesNoPurpleAnywhere() {
        for (state in everyState) {
            val spans = deliveryGlyphSpans(state, isFile = false, mirrored = false)
            assertTrue(
                "$state is not mirrored and must carry no mirrored colour",
                spans.none { it.color == ChatPalette.Mirrored },
            )
        }
    }

    
    @Test
    fun theMarkReplacesGreenAndNeverAddsAGlyph() {
        for (state in everyState) {
            val plain = deliveryGlyphSpans(state, isFile = false, mirrored = false)
            val marked = deliveryGlyphSpans(state, isFile = false, mirrored = true)
            assertEquals("$state changed shape when mirrored", plain.size, marked.size)
            assertEquals(
                "$state changed what the marker says when mirrored",
                plain.joinToString("") { it.text },
                marked.joinToString("") { it.text },
            )
        }
    }

    
    @Test
    fun theReadGlyphIsNeverTintedByWhichDeviceSent() {
        val plain = deliveryGlyphSpans(DeliveryState.READ, mirrored = false)
        val marked = deliveryGlyphSpans(DeliveryState.READ, mirrored = true)
        assertEquals(2, marked.size)
        assertEquals(
            "the peer's own marker took a colour from my side",
            plain[1].color,
            marked[1].color,
        )
        assertEquals(ChatPalette.Mirrored, marked[0].color)
        assertEquals(ChatPalette.Ok, marked[1].color)
    }

    
    @Test
    fun aMirroredReachedRowIsOnePurpleTick() {
        val spans = deliveryGlyphSpans(DeliveryState.SENT, mirrored = true)
        assertEquals(1, spans.size)
        assertEquals("✓", spans[0].text)
        assertEquals(ChatPalette.Mirrored, spans[0].color)
    }

    
    @Test
    fun notReachedAndFailedKeepTheirOwnColourWhenMirrored() {
        for (state in listOf(DeliveryState.ENQUEUED, DeliveryState.FAILED)) {
            val plain = deliveryGlyphSpans(state, mirrored = false).single()
            val marked = deliveryGlyphSpans(state, mirrored = true).single()
            assertEquals(
                "$state lost its own colour to the mirrored mark",
                plain.color,
                marked.color,
            )
            assertNotEquals(ChatPalette.Mirrored, marked.color)
        }
    }

    
    @Test
    fun theMarkRidesTheFileLadderAsWell() {
        val spans = deliveryGlyphSpans(DeliveryState.DELIVERED, isFile = true, mirrored = true)
        assertEquals(ChatPalette.Mirrored, spans[0].color)
    }

    
    @Test
    fun aStateThatDrawsNothingStillDrawsNothing() {
        assertEquals(emptyList<GlyphSpan>(), deliveryGlyphSpans("nonsense", mirrored = true))
    }

    
    @Test
    fun aMessageThatHasNotReachedThePeerDrawsAGrayTick() {
        val spans = deliveryGlyphSpans(DeliveryState.ENQUEUED)
        assertEquals(1, spans.size)
        assertEquals("✓", spans[0].text)
        assertEquals(ChatPalette.TextDim, spans[0].color)
    }

    
    @Test
    fun theTickShapeIsTheSameNotReachedAndReached() {
        val notReached = deliveryGlyphSpans(DeliveryState.ENQUEUED).single()
        val reached = deliveryGlyphSpans(DeliveryState.SENT).single()
        assertEquals("the shape must not change between these two", notReached.text, reached.text)
        assertNotEquals("only the colour may say which is which", notReached.color, reached.color)
    }

    
    @Test
    fun theFileLadderKeepsTwoDistinctMarksForItsFirstTwoSteps() {
        val enqueued = deliveryGlyphSpans(DeliveryState.ENQUEUED, isFile = true).single()
        val sent = deliveryGlyphSpans(DeliveryState.SENT, isFile = true).single()
        assertNotEquals(
            "a file that was never sent and one waiting to be pulled must not read alike",
            enqueued.text to enqueued.color,
            sent.text to sent.color,
        )
    }
}
