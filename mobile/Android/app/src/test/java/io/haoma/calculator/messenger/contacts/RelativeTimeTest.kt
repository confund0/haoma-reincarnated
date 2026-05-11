package io.haoma.calculator.messenger.contacts

import org.junit.Assert.assertEquals
import org.junit.Test

class RelativeTimeTest {
    private val now = 1_715_000_000L

    @Test fun zeroOrNegativeRendersDash() {
        assertEquals("—", RelativeTime.format(now, 0L))
        assertEquals("—", RelativeTime.format(now, -1L))
    }

    @Test fun futureClampsToNow() {
        
        
        assertEquals("now", RelativeTime.format(now, now + 30))
    }

    @Test fun underOneMinute() {
        assertEquals("now", RelativeTime.format(now, now))
        assertEquals("now", RelativeTime.format(now, now - 1))
        assertEquals("now", RelativeTime.format(now, now - 59))
    }

    @Test fun minutesRange() {
        assertEquals("1min", RelativeTime.format(now, now - 60))
        assertEquals("3min", RelativeTime.format(now, now - 3 * 60))
        assertEquals("59min", RelativeTime.format(now, now - 59 * 60))
    }

    @Test fun hoursRange() {
        assertEquals("1h", RelativeTime.format(now, now - 60 * 60))
        assertEquals("5h", RelativeTime.format(now, now - 5 * 60 * 60))
        assertEquals("23h", RelativeTime.format(now, now - 23 * 60 * 60))
    }

    @Test fun daysCompound() {
        
        assertEquals("1d0h", RelativeTime.format(now, now - 24L * 3600))
        assertEquals("3d4h", RelativeTime.format(now, now - (3 * 86_400 + 4 * 3600)))
        assertEquals("29d23h", RelativeTime.format(now, now - (29 * 86_400 + 23 * 3600)))
    }

    @Test fun monthsCompound() {
        
        val month = 30L * 86_400
        assertEquals("1m0d", RelativeTime.format(now, now - month))
        assertEquals("1m7d", RelativeTime.format(now, now - (month + 7 * 86_400)))
        
        assertEquals("11m29d", RelativeTime.format(now, now - (11 * month + 29 * 86_400)))
    }

    @Test fun yearsCompound() {
        val year = 365L * 86_400
        val month = 30L * 86_400
        assertEquals("1y0m", RelativeTime.format(now, now - year))
        assertEquals("2y3m", RelativeTime.format(now, now - (2 * year + 3 * month)))
    }
}
