package io.haoma.calculator.messenger.contacts

import org.junit.Assert.assertEquals
import org.junit.Test


class FormatFingerprintTest {
    @Test fun standardSixtySixHex() {
        
        
        val hex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef01"
        assertEquals(66, hex.length)
        val out = formatFingerprint(hex)
        
        assertEquals(76, out.length)
        assertEquals("012345", out.substring(0, 6))
        
        
        assertEquals(11, out.split(" ").size)
    }

    @Test fun shortInputDoesNotCrash() {
        
        val out = formatFingerprint("abcdefgh")
        assertEquals("abcdef gh", out)
    }
}
