package ids

import (
	"context"
	"sync"
	"time"
)

type ThresholdPerSource struct {
	RuleName         string
	Kind             EventKind
	Threshold        int
	Window           time.Duration
	Level            AlertLevel
	Message          string
	PanicOnThreshold bool

	mu       sync.Mutex
	counters map[string][]time.Time
}

func (r *ThresholdPerSource) Name() string { return r.RuleName }

func (r *ThresholdPerSource) Evaluate(_ context.Context, ev Event) []Action {
	if ev.Kind != r.Kind {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.counters == nil {
		r.counters = map[string][]time.Time{}
	}
	cutoff := ev.At.Add(-r.Window)

	old := r.counters[ev.SourceAddr]
	kept := old[:0]
	for _, t := range old {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	kept = append(kept, ev.At)
	r.counters[ev.SourceAddr] = kept

	actions := []Action{CounterBump{
		PeerID:     ev.PeerID,
		SourceAddr: ev.SourceAddr,
		Count:      1,
	}}
	if r.Threshold > 0 && len(kept) >= r.Threshold {
		actions = append(actions, AlertEmit{
			Level:       r.Level,
			Message:     r.Message,
			RelatedPeer: ev.PeerID,
		})
		if r.PanicOnThreshold {
			actions = append(actions, PanicRotate{
				Reason:      r.RuleName,
				RelatedPeer: ev.PeerID,
				SlotIdx:     ev.SlotIdx,
			})
		}
	}
	return actions
}

func Defaults() []Rule {
	return []Rule{
		&ThresholdPerSource{
			RuleName:  "unknown_source_flood",
			Kind:      EventUnknownSource,
			Threshold: 10,
			Window:    5 * time.Minute,
			Level:     AlertWarn,
			Message:   "10+ envelopes from unknown source within 5 minutes",
		},
		&ThresholdPerSource{
			RuleName:         "bad_mac_flood",
			Kind:             EventBadMAC,
			Threshold:        5,
			Window:           5 * time.Minute,
			Level:            AlertCritical,
			Message:          "5+ HMAC verification failures within 5 minutes — possible spoof or key compromise",
			PanicOnThreshold: true,
		},
	}
}
