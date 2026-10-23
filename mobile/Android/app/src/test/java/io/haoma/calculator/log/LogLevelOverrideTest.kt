package io.haoma.calculator.log

import org.junit.Assert.assertEquals
import org.junit.Test


class LogLevelOverrideTest {

    @Test
    fun `a known override wins over the build default`() {
        assertEquals("debug", Logger.resolveLogLevel(debugBuild = false, override = "debug"))
        assertEquals("error", Logger.resolveLogLevel(debugBuild = true, override = "error"))
    }

    @Test
    fun `no override falls back to the build-type default`() {
        assertEquals("debug", Logger.resolveLogLevel(debugBuild = true, override = null))
        assertEquals("warn", Logger.resolveLogLevel(debugBuild = false, override = null))
    }

    @Test
    fun `a blank override falls back to the build-type default`() {
        assertEquals("debug", Logger.resolveLogLevel(debugBuild = true, override = ""))
        assertEquals("warn", Logger.resolveLogLevel(debugBuild = false, override = ""))
    }

    @Test
    fun `an unknown override is refused, not guessed at`() {
        assertEquals("warn", Logger.resolveLogLevel(debugBuild = false, override = "bogus-value"))
        assertEquals("debug", Logger.resolveLogLevel(debugBuild = true, override = "bogus-value"))
    }

    @Test
    fun `every known level round-trips`() {
        for (level in listOf("error", "warn", "info", "debug")) {
            assertEquals(level, Logger.resolveLogLevel(debugBuild = false, override = level))
            assertEquals(level, Logger.resolveLogLevel(debugBuild = true, override = level))
        }
    }
}
