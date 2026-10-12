package io.haoma.calculator.messenger.settings

import io.haoma.calculator.messenger.LockSettings
import io.haoma.calculator.messenger.NotificationSettings
import io.haoma.calculator.messenger.THREAT_PRESET_BUNDLES
import io.haoma.calculator.messenger.ThreatPresetBundle
import io.haoma.calculator.messenger.TIER_DOMESTIC
import io.haoma.calculator.messenger.TIER_FORCE
import io.haoma.calculator.messenger.TIER_NOBODY
import io.haoma.calculator.messenger.TIER_POWER
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test


class LockDriftTest {

    private fun lockOf(b: ThreatPresetBundle) = LockSettings(
        threatProfile = "",
        idleAction = b.idleAction,
        idleTimeoutSeconds = b.idleTimeoutSeconds,
        pinValiditySec = b.pinValiditySec,
        panicAction = b.panicAction,
    )

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

    @Test
    fun `a tier straight after its apply does not read as changed`() {
        for ((tier, b) in THREAT_PRESET_BUNDLES) {
            val label = buildDriftStatus(tier, lockOf(b), notifsOf(b))
            assertFalse(
                "$tier reads as changed straight after an apply: $label",
                label.contains("changed since"),
            )
        }
    }

    
    @Test
    fun `each of the eleven cells is seen when it changes`() {
        val b = THREAT_PRESET_BUNDLES.getValue(TIER_POWER)
        val lock = lockOf(b)
        val notifs = notifsOf(b)
        val flips: List<Pair<String, () -> String>> = listOf(
            "idle_action" to {
                buildDriftStatus(TIER_POWER, lock.copy(idleAction = "soft-lock"), notifs)
            },
            "idle_timeout_seconds" to {
                buildDriftStatus(TIER_POWER, lock.copy(idleTimeoutSeconds = b.idleTimeoutSeconds + 1), notifs)
            },
            "pin_validity_sec" to {
                buildDriftStatus(TIER_POWER, lock.copy(pinValiditySec = b.pinValiditySec + 1), notifs)
            },
            "panic_action" to {
                buildDriftStatus(TIER_POWER, lock.copy(panicAction = "safe-lock"), notifs)
            },
            "notify_shell_enabled" to {
                buildDriftStatus(TIER_POWER, lock, notifs.copy(shellEnabled = !b.notifyShellEnabled))
            },
            "notify_show_sender" to {
                buildDriftStatus(TIER_POWER, lock, notifs.copy(showSender = !b.notifyShowSender))
            },
            "notify_show_body" to {
                buildDriftStatus(TIER_POWER, lock, notifs.copy(showBody = !b.notifyShowBody))
            },
            "notifications_on_lock" to {
                buildDriftStatus(TIER_POWER, lock, notifs.copy(onLock = !b.notificationsOnLock))
            },
            "notify_persist_until_open" to {
                buildDriftStatus(TIER_POWER, lock, notifs.copy(persistUntilOpen = !b.notifyPersistUntilOpen))
            },
            "notify_deep_link" to {
                buildDriftStatus(TIER_POWER, lock, notifs.copy(deepLink = !b.notifyDeepLink))
            },
            "show_failed_destroy_attempts" to {
                buildDriftStatus(
                    TIER_POWER,
                    lock,
                    notifs.copy(showFailedDestroyAttempts = !b.showFailedDestroyAttempts),
                )
            },
        )
        assertEquals("the compared list is eleven cells", 11, flips.size)
        for ((cell, run) in flips) {
            assertTrue(
                "$cell changed and the screen still reads clean",
                run().contains("changed since"),
            )
        }
    }

    
    @Test
    fun `the three cells the wire drops are not compared`() {
        val b = THREAT_PRESET_BUNDLES.getValue(TIER_POWER)
        val lock = lockOf(b)
        val notifs = notifsOf(b)
        val unseen = mapOf(
            "notify_disguise_enabled" to notifs.copy(disguiseEnabled = !b.notifyDisguiseEnabled),
            "notify_noisy" to notifs.copy(noisy = !b.notifyNoisy),
        )
        for ((cell, changed) in unseen) {
            assertFalse(
                "$cell is compared here and cannot be compared on the desktop",
                buildDriftStatus(TIER_POWER, lock, changed).contains("changed since"),
            )
        }
    }

    @Test
    fun `an unanswered picker has nothing to compare`() {
        assertEquals("Not picked yet", buildDriftStatus("", lockOf(THREAT_PRESET_BUNDLES.getValue(TIER_NOBODY)), null))
    }

    
    @Test
    fun `only the two answers that turn the bypass slot off clear the key`() {
        assertFalse("No one keeps the bypass key", presetClearsBypassSlot(TIER_NOBODY))
        assertFalse("People at home keep the bypass key", presetClearsBypassSlot(TIER_DOMESTIC))
        assertTrue("People with power lose the bypass key", presetClearsBypassSlot(TIER_POWER))
        assertTrue("People with force lose the bypass key", presetClearsBypassSlot(TIER_FORCE))
        assertFalse("clearing the answer writes no cell at all", presetClearsBypassSlot(""))
    }
}


class BypassGoingAwayNoteTest {

    @Test
    fun `names the key the person actually presses`() {
        assertTrue(bypassGoingAwayNote("1").contains("Long-press 1 "))
        assertTrue(bypassGoingAwayNote("7").contains("Long-press 7 "))
    }

    @Test
    fun `never says 0000`() {
        assertFalse(bypassGoingAwayNote("1").contains("0000"))
    }

    @Test
    fun `never says the PIN is being changed`() {
        val note = bypassGoingAwayNote("1").lowercase()
        for (claim in listOf("setting your pin", "set to", "resets", "changes your pin")) {
            assertFalse("the note must not claim an apply touches the PIN: $claim", note.contains(claim))
        }
    }

    @Test
    fun `says the factory PIN as the fallback`() {
        val note = bypassGoingAwayNote("1")
        assertTrue(note.contains(io.haoma.calculator.unlock.PatternConfig.FACTORY_DEFAULT))
        assertTrue(note.contains("If you have not set your own PIN"))
    }

    @Test
    fun `gives the three steps in the order they are pressed`() {
        val note = bypassGoingAwayNote("1")
        val press = note.indexOf("Long-press 1")
        val type = note.indexOf("type your PIN")
        val submit = note.indexOf("long-press =")
        assertTrue(press in 0 until type)
        assertTrue(type in 0 until submit)
    }
}
