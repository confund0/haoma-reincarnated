package io.haoma.calculator.messenger.status

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class CommandParserTest {
    @Test
    fun blankInputReturnsNull() {
        assertNull(CommandParser.parse(""))
        assertNull(CommandParser.parse("   "))
        assertNull(CommandParser.parse("\n\t"))
    }

    @Test
    fun nonSlashRejected() {
        val r = CommandParser.parse("hello")
        assertTrue(r is Command.Unknown)
        assertTrue((r as Command.Unknown).reason.contains("'/'"))
    }

    @Test
    fun nickHappyPath() {
        assertEquals(Command.Nick("alice"), CommandParser.parse("/nick alice"))
        assertEquals(Command.Nick("alice b carol"), CommandParser.parse("/nick alice b carol"))
    }

    @Test
    fun nickRequiresArg() {
        val r = CommandParser.parse("/nick")
        assertTrue(r is Command.Unknown)
    }

    @Test
    fun inviteTorAliasOptional() {
        assertEquals(Command.InviteTor(""), CommandParser.parse("/invite-tor"))
        assertEquals(Command.InviteTor("bob"), CommandParser.parse("/invite-tor bob"))
        assertEquals(Command.InviteTor("bob senior"), CommandParser.parse("/invite-tor bob senior"))
    }

    @Test
    fun acceptTorRequiresSevenWords() {
        val short = CommandParser.parse("/accept-tor a b c d e f")
        assertTrue(short is Command.Unknown)
        assertTrue((short as Command.Unknown).reason.contains("7 words required"))
    }

    @Test
    fun acceptTorWithSevenWordsNoAlias() {
        val seven = listOf("a", "b", "c", "d", "e", "f", "g")
        val cmd = CommandParser.parse("/accept-tor a b c d e f g")
        assertEquals(Command.AcceptTor(seven, ""), cmd)
    }

    @Test
    fun acceptTorWithEighthArgIsAlias() {
        val seven = listOf("a", "b", "c", "d", "e", "f", "g")
        val cmd = CommandParser.parse("/accept-tor a b c d e f g alice senior")
        assertEquals(Command.AcceptTor(seven, "alice senior"), cmd)
    }

    @Test
    fun setTorPasswordHappyPath() {
        assertEquals(Command.SetTorPassword("hunter2"), CommandParser.parse("/set-tor-password hunter2"))
    }

    @Test
    fun setTorPasswordEmptyQuotesClears() {
        assertEquals(Command.SetTorPassword(""), CommandParser.parse("/set-tor-password \"\""))
        assertEquals(Command.SetTorPassword(""), CommandParser.parse("/set-tor-password ''"))
    }

    @Test
    fun setTorPasswordRequiresArg() {
        val r = CommandParser.parse("/set-tor-password")
        assertTrue(r is Command.Unknown)
    }

    @Test
    fun helpAccepted() {
        assertEquals(Command.Help, CommandParser.parse("/help"))
        assertEquals(Command.Help, CommandParser.parse("/?"))
    }

    @Test
    fun unknownCommand() {
        val r = CommandParser.parse("/no-such-command")
        assertTrue(r is Command.Unknown)
    }

    @Test
    fun caseInsensitiveHead() {
        
        assertEquals(Command.Nick("alice"), CommandParser.parse("/Nick alice"))
        assertEquals(Command.Nick("alice"), CommandParser.parse("/NICK alice"))
    }
}
