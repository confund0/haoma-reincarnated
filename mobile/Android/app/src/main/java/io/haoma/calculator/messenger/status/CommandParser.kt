package io.haoma.calculator.messenger.status


sealed interface Command {
    data class Nick(val name: String) : Command
    data class InviteTor(val alias: String) : Command
    data class AcceptTor(val words: List<String>, val alias: String) : Command
    data class SetTorPassword(val password: String) : Command
    data class Call(val target: String) : Command
    data object Answer : Command
    data object Decline : Command
    data object Hangup : Command
    data object Help : Command
    data class Unknown(val reason: String) : Command
}

object CommandParser {
    

    fun parse(raw: String): Command? {
        val trimmed = raw.trim()
        if (trimmed.isEmpty()) return null
        if (!trimmed.startsWith("/")) {
            return Command.Unknown("commands must start with '/' — try /help")
        }
        val tokens = trimmed.split(WHITESPACE).filter { it.isNotEmpty() }
        val head = tokens.first().lowercase()
        val args = tokens.drop(1)
        return when (head) {
            "/help", "/?" -> Command.Help
            "/nick" -> parseNick(args)
            "/invite-tor" -> parseInviteTor(args)
            "/accept-tor" -> parseAcceptTor(args)
            "/set-tor-password" -> parseSetTorPassword(args)
            "/call" -> parseCall(args)
            "/answer" -> Command.Answer
            "/decline", "/reject" -> Command.Decline
            "/hangup" -> Command.Hangup
            else -> Command.Unknown("unknown command $head — try /help")
        }
    }

    fun helpText(): String = buildString {
        appendLine("/nick <name>                       — set your displayed nick")
        appendLine("/invite-tor [alias]                — start an onion invite")
        appendLine("/accept-tor <w1..w7> [alias]       — accept a 7-word onion invite")
        appendLine("/set-tor-password <pw>             — store + apply Tor control-port password")
        appendLine("/call <peer-alias-or-id-prefix>    — place an outbound call")
        appendLine("/answer                            — accept the ringing inbound call")
        appendLine("/decline                           — reject the ringing inbound call")
        appendLine("/hangup                            — end the active call")
        append("/help                              — show this list")
    }

    private fun parseNick(args: List<String>): Command {
        if (args.isEmpty()) return Command.Unknown("/nick: name required")
        
        val name = args.joinToString(" ")
        return Command.Nick(name)
    }

    private fun parseInviteTor(args: List<String>): Command {
        
        
        val alias = args.joinToString(" ")
        return Command.InviteTor(alias)
    }

    private fun parseAcceptTor(args: List<String>): Command {
        if (args.size < 7) {
            return Command.Unknown("/accept-tor: 7 words required (got ${args.size})")
        }
        val words = args.take(7)
        
        val alias = if (args.size > 7) args.drop(7).joinToString(" ") else ""
        return Command.AcceptTor(words = words, alias = alias)
    }

    private fun parseSetTorPassword(args: List<String>): Command {
        if (args.isEmpty()) {
            return Command.Unknown("/set-tor-password: password required (use empty quotes \"\" to clear)")
        }
        
        
        val pw = args.joinToString(" ")
        
        
        val cleaned = if (pw == "\"\"" || pw == "''") "" else pw
        return Command.SetTorPassword(cleaned)
    }

    private fun parseCall(args: List<String>): Command {
        if (args.isEmpty()) {
            return Command.Unknown("/call: peer alias or id-prefix required")
        }
        
        return Command.Call(args.joinToString(" "))
    }

    private val WHITESPACE = Regex("\\s+")
}
