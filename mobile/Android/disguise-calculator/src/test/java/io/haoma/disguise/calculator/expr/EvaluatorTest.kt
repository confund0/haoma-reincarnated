package io.haoma.disguise.calculator.expr

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class EvaluatorTest {

    private fun ok(input: String): Double {
        val r = evaluate(input)
        assertTrue("expected Ok, got $r", r is EvalResult.Ok)
        return (r as EvalResult.Ok).value
    }

    private fun err(input: String): String {
        val r = evaluate(input)
        assertTrue("expected Err, got $r", r is EvalResult.Err)
        return (r as EvalResult.Err).message
    }

    
    @Test fun `addition`() = assertEquals(5.0, ok("2+3"), 0.0)
    @Test fun `subtraction`() = assertEquals(-1.0, ok("2-3"), 0.0)
    @Test fun `multiplication ascii`() = assertEquals(6.0, ok("2*3"), 0.0)
    @Test fun `multiplication unicode`() = assertEquals(6.0, ok("2×3"), 0.0)
    @Test fun `division ascii`() = assertEquals(4.0, ok("8/2"), 0.0)
    @Test fun `division unicode`() = assertEquals(4.0, ok("8÷2"), 0.0)
    @Test fun `decimals`() = assertEquals(3.7, ok("1.2+2.5"), 1e-9)

    
    @Test fun `times before plus`() = assertEquals(7.0, ok("1+2*3"), 0.0)
    @Test fun `parens force order`() = assertEquals(9.0, ok("(1+2)*3"), 0.0)
    @Test fun `nested parens`() = assertEquals(20.0, ok("((1+1)*2)*5"), 0.0)
    @Test fun `unary minus`() = assertEquals(-5.0, ok("-5"), 0.0)
    @Test fun `unary minus inside expr`() = assertEquals(-1.0, ok("2+-3"), 0.0)

    
    @Test fun `power basic`() = assertEquals(8.0, ok("2^3"), 0.0)
    @Test fun `power right associative`() = assertEquals(512.0, ok("2^3^2"), 0.0) 
    @Test fun `power binds tighter than mul`() = assertEquals(18.0, ok("2*3^2"), 0.0)

    
    @Test fun `sqrt basic`() = assertEquals(3.0, ok("√9"), 0.0)
    @Test fun `sqrt of expr`() = assertEquals(5.0, ok("√(16+9)"), 0.0)
    @Test fun `sqrt of negative is error`() {
        val msg = err("√(-4)")
        assertTrue(msg.contains("√"))
    }

    
    @Test fun `percent standalone`() = assertEquals(0.1, ok("10%"), 1e-9)
    @Test fun `percent in plus is percent-of-left`() = assertEquals(55.0, ok("50+10%"), 1e-9)
    @Test fun `percent in minus is percent-of-left`() = assertEquals(45.0, ok("50-10%"), 1e-9)
    @Test fun `percent in mul is just over-100`() = assertEquals(5.0, ok("50*10%"), 1e-9)
    @Test fun `percent in div is just over-100`() = assertEquals(500.0, ok("50/10%"), 1e-9)

    
    @Test fun `divide by zero`() {
        val msg = err("1/0")
        assertTrue(msg.contains("zero", ignoreCase = true))
    }

    @Test fun `divide by zero via parens`() {
        val msg = err("1/(2-2)")
        assertTrue(msg.contains("zero", ignoreCase = true))
    }

    @Test fun `unmatched lparen`() {
        val msg = err("(1+2")
        assertTrue(msg.contains("Expected", ignoreCase = true))
    }

    @Test fun `unmatched rparen`() {
        val msg = err("1+2)")
        assertTrue(msg.isNotEmpty())
    }

    @Test fun `lone dot`() {
        val msg = err(".")
        assertTrue(msg.isNotEmpty())
    }

    @Test fun `empty input`() {
        val msg = err("")
        assertTrue(msg.isNotEmpty())
    }

    
    @Test fun `whitespace ignored`() = assertEquals(5.0, ok("  2 +   3  "), 0.0)
}
