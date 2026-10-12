package calls

func SetBeforeCommitForTest(fn func(callID string)) (restore func()) {
	prev := beforeCommit
	beforeCommit = fn
	return func() { beforeCommit = prev }
}

const UpdateRetries = updateRetries
