package io.haoma.calculator.messenger

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test


class PassphraseNoticeGateTest {

    @Test
    fun `a quiet device does not apply the notice`() {
        assertFalse(
            "a device whose preset says nothing must stay quiet",
            passphraseNoticeApplies(isDefault = true, saysSo = false),
        )
    }

    @Test
    fun `a loud device with the shipped passphrase applies it`() {
        assertTrue(passphraseNoticeApplies(isDefault = true, saysSo = true))
    }

    @Test
    fun `a real passphrase applies nothing however loud the preset`() {
        assertFalse(
            "the preset must not invent a default passphrase",
            passphraseNoticeApplies(isDefault = false, saysSo = true),
        )
    }

    @Test
    fun `an applied notice reaches the producer and a quiet one does not`() {
        val loud = produceNotices(
            passphraseNoticeApplies(isDefault = true, saysSo = true),
            destroyAttempts = null,
            snooze = emptyMap(),
        )
        assertEquals(1, loud.size)
        assertEquals(NOTICE_PASSPHRASE_IS_DEFAULT, loud[0].id)

        val quiet = produceNotices(
            passphraseNoticeApplies(isDefault = true, saysSo = false),
            destroyAttempts = null,
            snooze = emptyMap(),
        )
        assertEquals("a quiet device must produce nothing", 0, quiet.size)
    }

    @Test
    fun `only the lowest tier stays quiet`() {
        assertFalse(
            "nobody keeps the default as a choice, so it says nothing",
            THREAT_PRESET_BUNDLES.getValue("nobody").sayPassphraseIsDefault,
        )
        for (tier in listOf("domestic", "power", "force")) {
            assertTrue(
                "$tier must say the passphrase is still the default",
                THREAT_PRESET_BUNDLES.getValue(tier).sayPassphraseIsDefault,
            )
        }
    }
}


class SaysPassphraseIsDefaultUpgradeTest {

    @Test
    fun `the cell decides when it is present`() {
        val quiet = org.json.JSONObject()
            .put("say_passphrase_is_default", false)
            .put("threat_profile", "force")
        assertFalse("the cell must beat the tier", saysPassphraseIsDefault(quiet))

        val loud = org.json.JSONObject()
            .put("say_passphrase_is_default", true)
            .put("threat_profile", TIER_NOBODY)
        assertTrue("the cell must beat the tier", saysPassphraseIsDefault(loud))
    }

    @Test
    fun `an old vault at the lowest tier stays quiet without re-picking it`() {
        val old = org.json.JSONObject().put("threat_profile", TIER_NOBODY)
        assertFalse(
            "upgrading must not make a nobody device warn until the tier is re-applied",
            saysPassphraseIsDefault(old),
        )
    }

    @Test
    fun `an old vault at every other tier still warns`() {
        for (tier in listOf("domestic", "power", "force")) {
            assertTrue(
                "$tier must keep warning across an upgrade",
                saysPassphraseIsDefault(org.json.JSONObject().put("threat_profile", tier)),
            )
        }
    }

    @Test
    fun `a vault with no tier at all warns`() {
        assertTrue(
            "never picking a tier is not choosing the default passphrase",
            saysPassphraseIsDefault(org.json.JSONObject()),
        )
    }
}
