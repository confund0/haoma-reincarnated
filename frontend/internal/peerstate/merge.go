package peerstate

func stampWins(inAt, localAt int64) (take, settled bool) {
	switch {
	case inAt > localAt:
		return true, true
	case inAt < localAt:
		return false, true
	default:
		return false, false
	}
}

func stringWins(inAt, localAt int64, in, local string) bool {
	take, settled := stampWins(inAt, localAt)
	if settled {
		return take && in != local
	}
	return in > local
}

func takeString(val *string, at *int64, in string, inAt int64) (moved bool) {
	if stringWins(inAt, *at, in, *val) {
		*val, moved = in, true
	}
	if inAt > *at {
		*at, moved = inAt, true
	}
	return moved
}

func bumpPast(ts, held int64) int64 {
	if ts <= held {
		return held + 1
	}
	return ts
}
