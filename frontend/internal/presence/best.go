package presence

func Best(snaps []Snapshot) Snapshot {
	var out Snapshot
	for _, s := range snaps {
		if s.Accepting {
			out.Accepting = true
		}
		if s.Sending {
			out.Sending = true
		}
		if chattyRank(s.Chatty) > chattyRank(out.Chatty) {
			out.Chatty = s.Chatty
		}
	}
	switch {
	case out.Chatty != "":
		out.Effective = out.Chatty
	case out.Accepting:
		out.Effective = EffectiveAccepting
	default:
		out.Effective = EffectiveUnknown
	}
	return out
}

func chattyRank(state string) int {
	switch state {
	case EffectiveBusy:
		return 3
	case EffectiveAvailable:
		return 2
	case EffectiveAway:
		return 1
	default:
		return 0
	}
}
