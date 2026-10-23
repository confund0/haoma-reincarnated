package io.haoma.calculator.messenger

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test


class AdvancedSettingsLogFieldsTest {

    @Test
    fun `an absent key reads as no override and call stats off`() {
        val snap = JSONObject()
        assertEquals("", snap.optString("log_level", ""))
        assertFalse(snap.optBoolean("call_stats", false))
    }

    @Test
    fun `a saved payload round-trips through the same keys`() {
        val settings = AdvancedSettings(urlForceChooser = true, logLevel = "debug", callStats = true)

        val payload = JSONObject()
        payload.put("url_force_chooser", settings.urlForceChooser)
        payload.put("log_level", settings.logLevel)
        payload.put("call_stats", settings.callStats)

        assertTrue(payload.optBoolean("url_force_chooser", false))
        assertEquals("debug", payload.optString("log_level", ""))
        assertTrue(payload.optBoolean("call_stats", false))
    }

    @Test
    fun `the data class keeps the no-override default for old vaults`() {
        
        
        val settings = AdvancedSettings(urlForceChooser = true)
        assertEquals("", settings.logLevel)
        assertFalse(settings.callStats)
    }
}
