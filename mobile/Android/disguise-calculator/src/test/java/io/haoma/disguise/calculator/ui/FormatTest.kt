package io.haoma.disguise.calculator.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class FormatTest {
    @Test fun `zero`() = assertEquals("0", formatNumber(0.0))
    @Test fun `whole number`() = assertEquals("42", formatNumber(42.0))
    @Test fun `negative whole`() = assertEquals("-7", formatNumber(-7.0))
    @Test fun `simple decimal`() = assertEquals("3.14", formatNumber(3.14))
    @Test fun `trailing zeros stripped`() = assertEquals("1.5", formatNumber(1.5000000000))

    @Test fun `large number flips to scientific`() {
        val s = formatNumber(1.23e15)
        assertTrue("expected scientific notation, got $s", s.contains("e"))
    }

    @Test fun `tiny number flips to scientific`() {
        val s = formatNumber(1.23e-12)
        assertTrue("expected scientific notation, got $s", s.contains("e"))
    }

    @Test fun `nan is error`() = assertEquals("Error", formatNumber(Double.NaN))
    @Test fun `infinity is error`() = assertEquals("Error", formatNumber(Double.POSITIVE_INFINITY))
}
