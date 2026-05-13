package io.haoma.calculator.messenger

import io.haoma.calculator.messenger.status.Command
import io.haoma.calculator.messenger.status.CommandParser
import kotlinx.coroutines.launch


fun MessengerStore.runCommand(input: String) {
    val cmd = CommandParser.parse(input) ?: return
    appendStatus("> $input", source = StatusSource.Cli)
    when (cmd) {
        is Command.Nick -> setSelfNick(cmd.name)
        is Command.InviteTor -> inviteOnion(cmd.alias)
        is Command.AcceptTor -> scope.launch {
            val result = acceptOnion(cmd.words, cmd.alias)
            if (result is AcceptResult.Error) {
                appendStatus("accept-tor failed: ${result.message}", level = StatusLevel.WARN, source = StatusSource.Cli)
            }
        }
        is Command.SetTorPassword -> setTorPassword(cmd.password)
        is Command.Call -> dispatchCall(cmd.target)
        Command.Answer -> dispatchRespond(CallAction.Accept, "/answer", "no incoming call to answer")
        Command.Decline -> dispatchRespond(CallAction.Reject, "/decline", "no incoming call to decline")
        Command.Hangup -> dispatchHangup()
        Command.Help -> appendHelp()
        is Command.Unknown -> appendStatus(cmd.reason, level = StatusLevel.WARN, source = StatusSource.Cli)
    }
}

private fun MessengerStore.appendHelp() {
    for (line in CommandParser.helpText().split('\n')) {
        appendStatus(line, source = StatusSource.Cli)
    }
}


private fun MessengerStore.dispatchCall(target: String) {
    val chatId = resolveChatByAlias(target)
    if (chatId == null) {
        appendStatus(
            "/call: no unique match for \"$target\" (try a longer prefix or full peer-id)",
            level = StatusLevel.WARN,
            source = StatusSource.Cli,
        )
        return
    }
    startCall(chatId)
}

private fun MessengerStore.dispatchRespond(action: String, verb: String, noneMessage: String) {
    val ringing = findRingingIncoming()
    when (ringing.size) {
        0 -> appendStatus("$verb: $noneMessage", level = StatusLevel.WARN, source = StatusSource.Cli)
        1 -> respondCall(ringing.first().callId, action)
        else -> {
            appendStatus(
                "$verb: ${ringing.size} incoming calls — be more specific (GUI lands in M-CALLS-C):",
                level = StatusLevel.WARN,
                source = StatusSource.Cli,
            )
            for (c in ringing) {
                appendStatus(
                    "  • from ${peerLabelFor(c.peerId)} (call_id=${shortCallId(c.callId)})",
                    source = StatusSource.Cli,
                )
            }
        }
    }
}

private fun MessengerStore.dispatchHangup() {
    val active = findActiveCalls()
    when (active.size) {
        0 -> appendStatus("/hangup: no active call", level = StatusLevel.WARN, source = StatusSource.Cli)
        1 -> respondCall(active.first().callId, CallAction.End)
        else -> {
            appendStatus(
                "/hangup: ${active.size} active calls — be more specific:",
                level = StatusLevel.WARN,
                source = StatusSource.Cli,
            )
            for (c in active) {
                appendStatus(
                    "  • ${c.status} with ${peerLabelFor(c.peerId)} (call_id=${shortCallId(c.callId)})",
                    source = StatusSource.Cli,
                )
            }
        }
    }
}
