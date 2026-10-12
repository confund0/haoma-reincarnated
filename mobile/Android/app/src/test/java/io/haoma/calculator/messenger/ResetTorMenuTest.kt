package io.haoma.calculator.messenger

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test


class ResetTorMenuTest {

    @Test
    fun `the menu offers only Reset Tor`() {
        assertEquals(listOf("Reset Tor"), RECOVERY_MENU)
        assertFalse(RECOVERY_MENU.any { it.contains("identity", ignoreCase = true) })
    }

    @Test
    fun `the confirmation says what a reset does`() {
        assertEquals("Reset Tor?", RESET_TOR_TITLE)
        assertTrue(RESET_TOR_BODY.contains("restart"))
        assertTrue(RESET_TOR_BODY.contains("cannot reach"))
        assertEquals("Reset", RESET_TOR_ACTION)
    }
}
