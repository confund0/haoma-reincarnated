package io.haoma.disguise.calculator

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class TokenAccumulatorTest {
    @Test
    fun `seeded with trigger key — starts empty`() {
        val acc = TokenAccumulator("5")
        assertTrue(acc.isEmpty)
        assertEquals("", acc.token)
    }

    @Test
    fun `slide path captures distinct adjacent keys after seed`() {
        val acc = TokenAccumulator("5")
        listOf("7", "8", "9", "6", "3").forEach(acc::visit)
        assertFalse(acc.isEmpty)
        assertEquals("78963", acc.token)
    }

    @Test
    fun `revisiting the seed mid-slide is included in the token`() {
        
        val acc = TokenAccumulator("5")
        listOf("7", "8", "5", "6").forEach(acc::visit)
        assertEquals("7856", acc.token)
    }

    @Test
    fun `repeated visits of the same key collapse`() {
        val acc = TokenAccumulator("5")
        listOf("7", "7", "7", "8", "8", "9").forEach(acc::visit)
        assertEquals("789", acc.token)
    }

    @Test
    fun `holding trigger and lifting without sliding stays empty`() {
        
        val acc = TokenAccumulator("5")
        repeat(10) { acc.visit("5") }
        assertTrue(acc.isEmpty)
    }

    @Test
    fun `non-digit keys can appear in the path — gesture engine is shape-agnostic`() {
        
        val acc = TokenAccumulator("5")
        listOf("4", "7", "^", "%", "9").forEach(acc::visit)
        assertEquals("47^%9", acc.token)
    }
}
