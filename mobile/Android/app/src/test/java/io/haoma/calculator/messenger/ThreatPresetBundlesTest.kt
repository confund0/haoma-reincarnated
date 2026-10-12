package io.haoma.calculator.messenger

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test


class ThreatPresetBundlesTest {

    private val nobody = ThreatPresetBundle(
        showLoginForm = false,
        sayPassphraseIsDefault = false,
        bypassSlotEnabled = true,
        idleAction = "soft-lock",
        idleTimeoutSeconds = 900,
        pinValiditySec = 0,
        panicAction = "safe-lock",
        defaultRetentionSec = 28L * 24 * 3600,
        notifyShellEnabled = true,
        notifyShowSender = true,
        notifyShowBody = false,
        notifyDisguiseEnabled = false,
        notifyNoisy = true,
        notifyPersistUntilOpen = true,
        notifyDeepLink = true,
        notificationsOnLock = true,
        shareTargetEnabled = true,
        showFailedDestroyAttempts = true,
    )

    private val domestic = ThreatPresetBundle(
        showLoginForm = true,
        sayPassphraseIsDefault = true,
        bypassSlotEnabled = true,
        idleAction = "soft-lock",
        idleTimeoutSeconds = 300,
        pinValiditySec = 86400,
        panicAction = "safe-lock",
        defaultRetentionSec = 7L * 24 * 3600,
        notifyShellEnabled = true,
        notifyShowSender = false,
        notifyShowBody = false,
        notifyDisguiseEnabled = true,
        notifyNoisy = false,
        notifyPersistUntilOpen = true,
        notifyDeepLink = false,
        notificationsOnLock = true,
        shareTargetEnabled = false,
        showFailedDestroyAttempts = false,
    )

    private val power = ThreatPresetBundle(
        showLoginForm = true,
        sayPassphraseIsDefault = true,
        bypassSlotEnabled = false,
        idleAction = "safe-lock",
        idleTimeoutSeconds = 60,
        pinValiditySec = 300,
        panicAction = "hard-lock",
        defaultRetentionSec = 24L * 3600,
        notifyShellEnabled = true,
        notifyShowSender = false,
        notifyShowBody = false,
        notifyDisguiseEnabled = true,
        notifyNoisy = false,
        notifyPersistUntilOpen = false,
        notifyDeepLink = false,
        notificationsOnLock = false,
        shareTargetEnabled = false,
        showFailedDestroyAttempts = false,
    )

    private val force = ThreatPresetBundle(
        showLoginForm = true,
        sayPassphraseIsDefault = true,
        bypassSlotEnabled = false,
        idleAction = "hard-lock",
        idleTimeoutSeconds = 30,
        pinValiditySec = 0,
        panicAction = "self-destruct",
        defaultRetentionSec = 3600L,
        notifyShellEnabled = false,
        notifyShowSender = false,
        notifyShowBody = false,
        notifyDisguiseEnabled = true,
        notifyNoisy = false,
        notifyPersistUntilOpen = false,
        notifyDeepLink = false,
        notificationsOnLock = false,
        shareTargetEnabled = false,
        showFailedDestroyAttempts = false,
    )

    @Test
    fun `every tier writes the values the matrix gives it`() {
        assertEquals(nobody, THREAT_PRESET_BUNDLES[TIER_NOBODY])
        assertEquals(domestic, THREAT_PRESET_BUNDLES[TIER_DOMESTIC])
        assertEquals(power, THREAT_PRESET_BUNDLES[TIER_POWER])
        assertEquals(force, THREAT_PRESET_BUNDLES[TIER_FORCE])
        assertEquals(4, THREAT_PRESET_BUNDLES.size)
    }

    @Test
    fun `the retired names have no bundle`() {
        for (retired in listOf("minimal", "privacy", "activist")) {
            assertNull("$retired is retired and must have no bundle", THREAT_PRESET_BUNDLES[retired])
        }
    }

    @Test
    fun `an empty profile means the picker has not run, so it is not a tier`() {
        assertNull(THREAT_PRESET_BUNDLES[""])
    }

    @Test
    fun `nobody carries the baseline an empty profile used to stand for`() {
        val b = THREAT_PRESET_BUNDLES.getValue(TIER_NOBODY)
        assertTrue("the share sheet is offered at nobody", b.shareTargetEnabled)
        assertTrue("failed destroy attempts are told at nobody", b.showFailedDestroyAttempts)
        assertTrue("the bypass slot is on at nobody", b.bypassSlotEnabled)
        assertFalse("the login form is skipped at nobody", b.showLoginForm)
    }

    @Test
    fun `no tier shows the message body and no tier keeps messages for ever`() {
        for ((tier, b) in THREAT_PRESET_BUNDLES) {
            assertFalse("$tier shows the message body", b.notifyShowBody)
            assertTrue("$tier keeps messages for ever", b.defaultRetentionSec > 0)
        }
    }

    @Test
    fun `the four tiers do not collapse into each other`() {
        val distinct = THREAT_PRESET_BUNDLES.values.toSet()
        assertEquals("two tiers hold the same bundle", THREAT_PRESET_BUNDLES.size, distinct.size)
    }

    @Test
    fun `only the higher tiers turn the share sheet and the bypass slot off`() {
        assertNotNull(THREAT_PRESET_BUNDLES[TIER_NOBODY])
        for (tier in listOf(TIER_DOMESTIC, TIER_POWER, TIER_FORCE)) {
            assertFalse("$tier offers the share sheet", THREAT_PRESET_BUNDLES.getValue(tier).shareTargetEnabled)
        }
        assertTrue(THREAT_PRESET_BUNDLES.getValue(TIER_DOMESTIC).bypassSlotEnabled)
        assertFalse(THREAT_PRESET_BUNDLES.getValue(TIER_POWER).bypassSlotEnabled)
        assertFalse(THREAT_PRESET_BUNDLES.getValue(TIER_FORCE).bypassSlotEnabled)
    }
}
