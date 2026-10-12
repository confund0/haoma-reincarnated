package ipc

import "time"

const JoinerDialTimeout = 120 * time.Second

const ReasonOnionAcceptFailed = "onion_accept_failed"

const ReasonJoinWordsInvalid = "join_words_invalid"

const ReasonJoinInvitationSpent = "join_invitation_spent"

func JoinDialFailed(code string) bool {
	return code == ReasonOnionAcceptFailed || code == ReasonBackendDown
}
