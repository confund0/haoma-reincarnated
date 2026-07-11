package io.haoma.calculator.messenger.chat

import org.junit.Assert.assertEquals
import org.junit.Test

class MessageStylingTest {

    private fun span(start: Int, end: Int, mark: TextStyleMark) = StyleSpan(start, end, mark)

    @Test fun plainTextUntouched() {
        val r = parseMessageStyling("hello world")
        assertEquals("hello world", r.text)
        assertEquals(emptyList<StyleSpan>(), r.spans)
    }

    @Test fun bold() {
        val r = parseMessageStyling("*bold*")
        assertEquals("bold", r.text)
        assertEquals(listOf(span(0, 4, TextStyleMark.BOLD)), r.spans)
    }

    @Test fun italic() {
        val r = parseMessageStyling("_italic_")
        assertEquals("italic", r.text)
        assertEquals(listOf(span(0, 6, TextStyleMark.ITALIC)), r.spans)
    }

    @Test fun strike() {
        val r = parseMessageStyling("~strike~")
        assertEquals("strike", r.text)
        assertEquals(listOf(span(0, 6, TextStyleMark.STRIKE)), r.spans)
    }

    @Test fun underlineDoubled() {
        val r = parseMessageStyling("__under__")
        assertEquals("under", r.text)
        assertEquals(listOf(span(0, 5, TextStyleMark.UNDERLINE)), r.spans)
    }

    @Test fun styledPhraseInSentence() {
        val r = parseMessageStyling("a *b c* d")
        assertEquals("a b c d", r.text)
        assertEquals(listOf(span(2, 5, TextStyleMark.BOLD)), r.spans)
    }

    @Test fun twoRuns() {
        val r = parseMessageStyling("*a* _b_")
        assertEquals("a b", r.text)
        assertEquals(
            listOf(span(0, 1, TextStyleMark.BOLD), span(2, 3, TextStyleMark.ITALIC)),
            r.spans,
        )
    }

    @Test fun nestedBoldItalic() {
        val r = parseMessageStyling("*_x_*")
        assertEquals("x", r.text)
        assertEquals(
            listOf(span(0, 1, TextStyleMark.ITALIC), span(0, 1, TextStyleMark.BOLD)),
            r.spans,
        )
    }

    @Test fun boldContainingItalic() {
        val r = parseMessageStyling("*bold _both_ end*")
        assertEquals("bold both end", r.text)
        assertEquals(
            listOf(span(5, 9, TextStyleMark.ITALIC), span(0, 13, TextStyleMark.BOLD)),
            r.spans,
        )
    }

    @Test fun underlineAroundBold() {
        val r = parseMessageStyling("__*x*__")
        assertEquals("x", r.text)
        assertEquals(
            listOf(span(0, 1, TextStyleMark.BOLD), span(0, 1, TextStyleMark.UNDERLINE)),
            r.spans,
        )
    }

    @Test fun unbalancedOpenIsLiteral() {
        val r = parseMessageStyling("*hello")
        assertEquals("*hello", r.text)
        assertEquals(emptyList<StyleSpan>(), r.spans)
    }

    @Test fun unbalancedCloseIsLiteral() {
        val r = parseMessageStyling("hello*")
        assertEquals("hello*", r.text)
        assertEquals(emptyList<StyleSpan>(), r.spans)
    }

    @Test fun mathAsterisksNotStyled() {
        val r = parseMessageStyling("2*3*4")
        assertEquals("2*3*4", r.text)
        assertEquals(emptyList<StyleSpan>(), r.spans)
    }

    @Test fun midWordUnderscoresNotStyled() {
        val r = parseMessageStyling("foo_bar_baz")
        assertEquals("foo_bar_baz", r.text)
        assertEquals(emptyList<StyleSpan>(), r.spans)
    }

    @Test fun whitespaceInsideNotStyled() {
        val r = parseMessageStyling("* x*")
        assertEquals("* x*", r.text)
        assertEquals(emptyList<StyleSpan>(), r.spans)
    }

    @Test fun emptyMarkersDropped() {
        val r = parseMessageStyling("**")
        assertEquals("", r.text)
        assertEquals(emptyList<StyleSpan>(), r.spans)
    }

    @Test fun improperOverlapDegradesGracefully() {
        
        
        val r = parseMessageStyling("*_x*_")
        assertEquals("*x*", r.text)
        assertEquals(listOf(span(1, 3, TextStyleMark.ITALIC)), r.spans)
    }

    @Test fun markerAtSentenceEndWithPunctuation() {
        val r = parseMessageStyling("say *hi*!")
        assertEquals("say hi!", r.text)
        assertEquals(listOf(span(4, 6, TextStyleMark.BOLD)), r.spans)
    }
}
