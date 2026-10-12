package events

import "slices"

func (ev Event) envelopeSet() []string {
	if len(ev.EnvelopeIDs) > 0 {
		return ev.EnvelopeIDs
	}
	if ev.EnvelopeID != "" {
		return []string{ev.EnvelopeID}
	}
	return nil
}

func (ev *Event) noteEnvelopeOutcome(envelopeID, state string) bool {
	if envelopeID == "" || state != DeliveryFailed || slices.Contains(ev.FailedEnvelopeIDs, envelopeID) {
		return false
	}
	ev.FailedEnvelopeIDs = append(ev.FailedEnvelopeIDs, envelopeID)
	return true
}

func (ev Event) allEnvelopesFailed() bool {
	set := ev.envelopeSet()
	if len(set) == 0 {
		return false
	}
	for _, id := range set {
		if !slices.Contains(ev.FailedEnvelopeIDs, id) {
			return false
		}
	}
	return true
}

func deliveryFoldAdvances(ev Event, state string) bool {
	if state == DeliveryFailed {
		return ev.mayFail()
	}
	return DeliveryAdvances(ev.DeliveryState, state)
}

func (ev Event) mayFail() bool {
	if !DeliveryAdvances(ev.DeliveryState, DeliveryFailed) {
		return false
	}
	if len(ev.envelopeSet()) == 0 {
		return true
	}
	return ev.allEnvelopesFailed()
}
