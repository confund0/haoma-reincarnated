package io.haoma.calculator.notifications

import io.haoma.calculator.messenger.NotificationSettings
import io.haoma.calculator.messenger.THREAT_PRESET_BUNDLES
import io.haoma.calculator.messenger.ThreatPresetBundle
import io.haoma.calculator.messenger.TIER_DOMESTIC
import io.haoma.calculator.messenger.TIER_FORCE
import io.haoma.calculator.messenger.TIER_NOBODY
import io.haoma.calculator.messenger.TIER_POWER
import io.haoma.calculator.notifications.NotificationPoster.Companion.planCallBanner
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test


class CallBannerPlanTest {

    private fun notifsOf(b: ThreatPresetBundle) = NotificationSettings(
        shellEnabled = b.notifyShellEnabled,
        showSender = b.notifyShowSender,
        showBody = b.notifyShowBody,
        onLock = b.notificationsOnLock,
        disguiseEnabled = b.notifyDisguiseEnabled,
        noisy = b.notifyNoisy,
        persistUntilOpen = b.notifyPersistUntilOpen,
        deepLink = b.notifyDeepLink,
        showFailedDestroyAttempts = b.showFailedDestroyAttempts,
    )

    
    private val open = NotificationSettings(
        shellEnabled = true,
        showSender = true,
        showBody = true,
        onLock = true,
        disguiseEnabled = false,
        noisy = true,
        persistUntilOpen = false,
        deepLink = false,
        showFailedDestroyAttempts = false,
    )

    @Test
    fun `only the no-one preset rings`() {
        for ((tier, b) in THREAT_PRESET_BUNDLES) {
            val plan = planCallBanner(notifsOf(b), softLocked = false, chatMuted = false)
            assertEquals("tier $tier", tier == TIER_NOBODY, plan.ring)
        }
    }

    @Test
    fun `the no-one preset posts a named ringing call banner`() {
        val plan = planCallBanner(notifsOf(THREAT_PRESET_BUNDLES.getValue(TIER_NOBODY)), false, false)
        assertNull(plan.skipReason)
        assertFalse(plan.disguise)
        assertTrue(plan.showName)
        assertTrue(plan.ring)
    }

    @Test
    fun `the message gates stop the call banner`() {
        assertEquals("no-settings", planCallBanner(null, false, false).skipReason)
        assertEquals(
            "notifications-off",
            planCallBanner(open.copy(shellEnabled = false), false, false).skipReason,
        )
        assertEquals(
            "soft-locked+notifications_on_lock=false",
            planCallBanner(open.copy(onLock = false), softLocked = true, chatMuted = false).skipReason,
        )
        assertNull(planCallBanner(open, softLocked = true, chatMuted = false).skipReason)
        assertEquals("chat-muted", planCallBanner(open, false, chatMuted = true).skipReason)
    }

    @Test
    fun `disguise hides the call, its name and its ring`() {
        val s = open.copy(disguiseEnabled = true, showSender = false, showBody = false)
        val plan = planCallBanner(s, false, false)
        assertNull(plan.skipReason)
        assertTrue(plan.disguise)
        assertFalse(plan.showName)
        assertFalse(plan.ring)
    }

    @Test
    fun `disguise wins over a show flag an old vault left on`() {
        val plan = planCallBanner(open.copy(disguiseEnabled = true), false, false)
        assertTrue(plan.disguise)
        assertFalse(plan.showName)
        assertFalse(plan.ring)
    }

    @Test
    fun `show sender off drops the name`() {
        val plan = planCallBanner(open.copy(showSender = false), false, false)
        assertFalse(plan.disguise)
        assertFalse(plan.showName)
    }

    @Test
    fun `noisy notifications on rings on the ringing calls channel`() {
        val plan = planCallBanner(open.copy(noisy = true), false, false)
        assertTrue(plan.ring)
        assertEquals("haoma_calls_ring", plan.channelId)
    }

    @Test
    fun `noisy notifications off keeps the quiet calls channel`() {
        val plan = planCallBanner(open.copy(noisy = false), false, false)
        assertFalse(plan.ring)
        assertEquals("haoma_calls", plan.channelId)
    }

    @Test
    fun `a disguised call goes to the channel a disguised message goes to`() {
        val disguised = open.copy(disguiseEnabled = true, showSender = false, showBody = false)
        assertEquals(
            "haoma_messages_noisy",
            planCallBanner(disguised.copy(noisy = true), false, false).channelId,
        )
        assertEquals(
            "haoma_messages",
            planCallBanner(disguised.copy(noisy = false), false, false).channelId,
        )
        assertEquals("haoma_messages_noisy", NotificationPoster.messageChannelId(true))
        assertEquals("haoma_messages", NotificationPoster.messageChannelId(false))
    }

    @Test
    fun `a skipped call has no channel`() {
        assertEquals("", planCallBanner(open, false, chatMuted = true).channelId)
    }

    
    @Test
    fun `each preset maps to its call banner`() {
        data class Row(
            val tier: String,
            val locked: Boolean,
            val skip: String?,
            val disguise: Boolean,
            val name: Boolean,
            val ring: Boolean,
            val channel: String,
        )
        val rows = listOf(
            Row(TIER_NOBODY, false, null, false, true, true, "haoma_calls_ring"),
            Row(TIER_NOBODY, true, null, false, true, true, "haoma_calls_ring"),
            Row(TIER_DOMESTIC, false, null, true, false, false, "haoma_messages"),
            Row(TIER_DOMESTIC, true, null, true, false, false, "haoma_messages"),
            Row(TIER_POWER, false, null, true, false, false, "haoma_messages"),
            Row(TIER_POWER, true, "soft-locked+notifications_on_lock=false", false, false, false, ""),
            Row(TIER_FORCE, false, "notifications-off", false, false, false, ""),
            Row(TIER_FORCE, true, "notifications-off", false, false, false, ""),
        )
        assertEquals(THREAT_PRESET_BUNDLES.keys, rows.map { it.tier }.toSet())
        for (r in rows) {
            val plan = planCallBanner(notifsOf(THREAT_PRESET_BUNDLES.getValue(r.tier)), r.locked, false)
            val got = Row(r.tier, r.locked, plan.skipReason, plan.disguise, plan.showName, plan.ring, plan.channelId)
            assertEquals(r, got)
        }
    }
}
