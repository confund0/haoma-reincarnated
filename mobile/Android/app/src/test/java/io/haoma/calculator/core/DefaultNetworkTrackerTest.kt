package io.haoma.calculator.core

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class DefaultNetworkTrackerTest {
    @Test
    fun theReportAtRegistrationIsNoChange() {
        val t = DefaultNetworkTracker(initial = 100L)
        assertFalse(t.onAvailable(100L))
    }

    @Test
    fun anotherNetworkIsAChangeOnce() {
        val t = DefaultNetworkTracker(initial = 100L)
        assertFalse(t.onAvailable(100L))
        assertTrue(t.onAvailable(200L))
        assertFalse(t.onAvailable(200L))
        assertTrue(t.onAvailable(100L))
    }

    @Test
    fun theFirstNetworkAfterNoneIsAChange() {
        val t = DefaultNetworkTracker(initial = null)
        assertTrue(t.onAvailable(100L))
        assertFalse(t.onAvailable(100L))
    }
}
