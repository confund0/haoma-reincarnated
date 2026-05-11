package io.haoma.calculator.messenger

import io.haoma.calculator.messenger.status.Command
import io.haoma.calculator.messenger.status.CommandParser
import kotlinx.coroutines.launch


fun MessengerStore.runCommand(input: String) {
    val cmd = CommandParser.parse(input) ?: return
    appendStatus("> $input")
    when (cmd) {
        is Command.Nick -> setSelfNick(cmd.name)
        is Command.InviteTor -> inviteOnion(cmd.alias)
        is Command.AcceptTor -> scope.launch {
            val result = acceptOnion(cmd.words, cmd.alias)
            if (result is AcceptResult.Error) {
                appendStatus("accept-tor failed: ${result.message}", level = StatusLevel.WARN)
            }
        }
        is Command.SetTorPassword -> setTorPassword(cmd.password)
        Command.Help -> appendHelp()
        is Command.Unknown -> appendStatus(cmd.reason, level = StatusLevel.WARN)
    }
}

private fun MessengerStore.appendHelp() {
    for (line in CommandParser.helpText().split('\n')) {
        appendStatus(line)
    }
}
