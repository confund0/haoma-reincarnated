package io.haoma.disguise.calculator.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class CalculatorStateTest {

    private fun seq(vararg actions: CalcAction): CalculatorState {
        var s = CalculatorState()
        for (a in actions) s = reduce(s, a)
        return s
    }

    @Test fun `initial state shows zero`() {
        val s = CalculatorState()
        assertEquals("", s.input)
        assertEquals("0", s.display)
        assertFalse(s.error)
    }

    @Test fun `digit appends to buffer and display`() {
        val s = seq(CalcAction.Char("1"), CalcAction.Char("2"), CalcAction.Char("3"))
        assertEquals("123", s.input)
        assertEquals("123", s.display)
    }

    @Test fun `equals evaluates and replaces with result`() {
        val s = seq(
            CalcAction.Char("2"), CalcAction.Char("+"), CalcAction.Char("3"),
            CalcAction.Equals,
        )
        assertEquals("5", s.input)
        assertEquals("5", s.display)
        assertFalse(s.error)
    }

    @Test fun `equals on empty input is no-op`() {
        val s = seq(CalcAction.Equals)
        assertEquals("", s.input)
        assertEquals("0", s.display)
        assertFalse(s.error)
    }

    @Test fun `chain after equals continues from result`() {
        
        val s = seq(
            CalcAction.Char("5"), CalcAction.Char("+"), CalcAction.Char("3"),
            CalcAction.Equals,
            CalcAction.Char("+"), CalcAction.Char("2"),
            CalcAction.Equals,
        )
        assertEquals("10", s.display)
    }

    @Test fun `bad input goes to error display`() {
        val s = seq(
            CalcAction.Char("1"), CalcAction.Char("/"), CalcAction.Char("0"),
            CalcAction.Equals,
        )
        assertTrue(s.error)
        assertEquals("Error", s.display)
    }

    @Test fun `clear resets after error`() {
        val s = seq(
            CalcAction.Char("1"), CalcAction.Char("/"), CalcAction.Char("0"),
            CalcAction.Equals,
            CalcAction.Clear,
        )
        assertFalse(s.error)
        assertEquals("0", s.display)
        assertEquals("", s.input)
    }

    @Test fun `digit after error starts fresh`() {
        val s = seq(
            CalcAction.Char("1"), CalcAction.Char("/"), CalcAction.Char("0"),
            CalcAction.Equals,
            CalcAction.Char("7"),
        )
        assertFalse(s.error)
        assertEquals("7", s.input)
        assertEquals("7", s.display)
    }

    @Test fun `backspace removes last char`() {
        val s = seq(
            CalcAction.Char("1"), CalcAction.Char("2"), CalcAction.Char("3"),
            CalcAction.Backspace,
        )
        assertEquals("12", s.input)
        assertEquals("12", s.display)
    }

    @Test fun `backspace on empty buffer shows zero`() {
        val s = seq(CalcAction.Backspace)
        assertEquals("", s.input)
        assertEquals("0", s.display)
    }

    @Test fun `clear from arbitrary state`() {
        val s = seq(
            CalcAction.Char("1"), CalcAction.Char("+"), CalcAction.Char("2"),
            CalcAction.Clear,
        )
        assertEquals(CalculatorState(), s)
    }
}
