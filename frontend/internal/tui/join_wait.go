package tui

import (
	"fmt"
	"time"

	"haoma-frontend/internal/ipc"
)

const torSlowLine = "Tor is slow at the moment. Try again in a couple of minutes."

const joinTorSlowLine = "Tor is slow at the moment. Try again in a couple of minutes, or check the seven words."

const joinWordsWrongLine = "One of the words is wrong, so check the seven words and try again."

const joinInvitationSpentLine = "This invitation was already used or has run out, so ask for a new one."

const pairJoinNoAnswerLine = "No answer came back, so it is not known whether you are paired. Check your contacts before you try again."

func joinFailureLine(code string) (line string, keep, ok bool) {
	switch {
	case ipc.JoinDialFailed(code):
		return joinTorSlowLine, true, true
	case code == ipc.ReasonJoinWordsInvalid:
		return joinWordsWrongLine, true, true
	case code == ipc.ReasonJoinInvitationSpent:
		return joinInvitationSpentLine, true, true
	}
	return "", false, false
}

const joinWaitNoAnswerLine = "Waiting for Tor: no answer yet."

func joinSecondsLeft(elapsed time.Duration) int {
	left := ipc.JoinerDialTimeout - elapsed
	if left <= 0 {
		return 0
	}
	return int((left + time.Second - 1) / time.Second)
}

const joinWaitLinkNoAnswerLine = joinWaitNoAnswerLine + " Cancel does not stop a join that was already sent."

func joinWaitLinkLine(elapsed time.Duration) string {
	if joinSecondsLeft(elapsed) == 0 {
		return joinWaitLinkNoAnswerLine
	}
	return joinWaitLine(elapsed)
}

func joinWaitLine(elapsed time.Duration) string {
	switch left := joinSecondsLeft(elapsed); left {
	case 0:
		return joinWaitNoAnswerLine
	case 1:
		return "Waiting for Tor: 1 second left."
	default:
		return fmt.Sprintf("Waiting for Tor: %d seconds left.", left)
	}
}

func joinWaitStartLine() string {
	return fmt.Sprintf("Tor can take up to %d seconds to reach the other side.", int(ipc.JoinerDialTimeout/time.Second))
}
