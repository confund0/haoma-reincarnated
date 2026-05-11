package ids

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestIDS_NoRules_Observe(t *testing.T) {
	i := New()
	actions := i.Observe(context.Background(), Event{Kind: EventUnknownSource})
	if len(actions) != 0 {
		t.Errorf("actions = %v, want empty", actions)
	}
	snap := i.Snapshot()
	if snap.EventCounts["unknown_source"] != 1 {
		t.Errorf("EventCounts = %v", snap.EventCounts)
	}
}

type constantRule struct {
	name    string
	kind    EventKind
	actions []Action
	seen    int
	mu      sync.Mutex
}

func (r *constantRule) Name() string { return r.name }
func (r *constantRule) Evaluate(_ context.Context, ev Event) []Action {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ev.Kind != r.kind {
		return nil
	}
	r.seen++
	return r.actions
}

func TestIDS_AggregatesRuleActions(t *testing.T) {
	i := New()
	i.AddRule(&constantRule{
		name: "r1", kind: EventUnknownSource,
		actions: []Action{CounterBump{PeerID: "p"}},
	})
	i.AddRule(&constantRule{
		name: "r2", kind: EventUnknownSource,
		actions: []Action{AlertEmit{Level: AlertWarn, Message: "from r2"}},
	})

	actions := i.Observe(context.Background(), Event{Kind: EventUnknownSource})
	if len(actions) != 2 {
		t.Fatalf("got %d actions, want 2 (one per rule)", len(actions))
	}
	if _, ok := actions[0].(CounterBump); !ok {
		t.Errorf("actions[0] = %T, want CounterBump", actions[0])
	}
	if _, ok := actions[1].(AlertEmit); !ok {
		t.Errorf("actions[1] = %T, want AlertEmit", actions[1])
	}

	snap := i.Snapshot()
	if snap.ActionCounts["counter_bump"] != 1 || snap.ActionCounts["alert"] != 1 {
		t.Errorf("ActionCounts = %v", snap.ActionCounts)
	}
}

func TestIDS_Observe_FillsAtWhenZero(t *testing.T) {
	i := New()
	fixed := time.Unix(1_700_000_000, 0)
	i.now = func() time.Time { return fixed }

	i.Observe(context.Background(), Event{Kind: EventUnknownSource})
	snap := i.Snapshot()
	if !snap.LastEventAt.Equal(fixed) {
		t.Errorf("LastEventAt = %v, want %v (fills from i.now when Event.At zero)", snap.LastEventAt, fixed)
	}
}

func TestThresholdPerSource_BelowThreshold_OnlyBumps(t *testing.T) {
	r := &ThresholdPerSource{
		RuleName: "test", Kind: EventUnknownSource,
		Threshold: 5, Window: time.Minute, Level: AlertWarn, Message: "test",
	}
	base := time.Unix(1_700_000_000, 0)
	for i := 0; i < 4; i++ {
		actions := r.Evaluate(context.Background(), Event{
			Kind: EventUnknownSource, At: base.Add(time.Duration(i) * time.Second), SourceAddr: "src",
		})
		if len(actions) != 1 {
			t.Fatalf("iter %d: got %d actions, want 1 (CounterBump only)", i, len(actions))
		}
		if _, ok := actions[0].(CounterBump); !ok {
			t.Errorf("iter %d: action = %T, want CounterBump", i, actions[0])
		}
	}
}

func TestThresholdPerSource_AtThreshold_Alerts(t *testing.T) {
	r := &ThresholdPerSource{
		RuleName: "test", Kind: EventUnknownSource,
		Threshold: 3, Window: time.Minute, Level: AlertCritical, Message: "boom",
	}
	base := time.Unix(1_700_000_000, 0)
	var alertSeen int
	for i := 0; i < 5; i++ {
		actions := r.Evaluate(context.Background(), Event{
			Kind: EventUnknownSource, At: base.Add(time.Duration(i) * time.Second), SourceAddr: "src",
		})
		for _, a := range actions {
			if al, ok := a.(AlertEmit); ok {
				alertSeen++
				if al.Level != AlertCritical || al.Message != "boom" {
					t.Errorf("alert fields wrong: %+v", al)
				}
			}
		}
	}

	if alertSeen != 3 {
		t.Errorf("alerts seen = %d, want 3 (iters 3,4,5 all above threshold)", alertSeen)
	}
}

func TestThresholdPerSource_WindowExpires(t *testing.T) {
	r := &ThresholdPerSource{
		RuleName: "test", Kind: EventUnknownSource,
		Threshold: 3, Window: time.Minute, Level: AlertWarn, Message: "x",
	}
	base := time.Unix(1_700_000_000, 0)

	r.Evaluate(context.Background(), Event{Kind: EventUnknownSource, At: base, SourceAddr: "src"})
	r.Evaluate(context.Background(), Event{Kind: EventUnknownSource, At: base.Add(10 * time.Second), SourceAddr: "src"})
	actions := r.Evaluate(context.Background(), Event{Kind: EventUnknownSource, At: base.Add(10 * time.Minute), SourceAddr: "src"})

	for _, a := range actions {
		if _, ok := a.(AlertEmit); ok {
			t.Errorf("unexpected AlertEmit after window expiry: %+v", a)
		}
	}
}

func TestThresholdPerSource_PerSourceIsolated(t *testing.T) {
	r := &ThresholdPerSource{
		RuleName: "test", Kind: EventUnknownSource,
		Threshold: 3, Window: time.Minute, Level: AlertWarn, Message: "x",
	}
	base := time.Unix(1_700_000_000, 0)

	for i := 0; i < 2; i++ {
		r.Evaluate(context.Background(), Event{
			Kind: EventUnknownSource, At: base, SourceAddr: "srcA",
		})
		r.Evaluate(context.Background(), Event{
			Kind: EventUnknownSource, At: base, SourceAddr: "srcB",
		})
	}

	actions := r.Evaluate(context.Background(), Event{Kind: EventUnknownSource, At: base, SourceAddr: "srcA"})
	var gotAlert bool
	for _, a := range actions {
		if _, ok := a.(AlertEmit); ok {
			gotAlert = true
		}
	}
	if !gotAlert {
		t.Errorf("srcA reached threshold but no AlertEmit emitted")
	}
}

func TestThresholdPerSource_IgnoresOtherKinds(t *testing.T) {
	r := &ThresholdPerSource{
		RuleName: "test", Kind: EventBadMAC,
		Threshold: 1, Window: time.Minute, Level: AlertWarn, Message: "x",
	}
	actions := r.Evaluate(context.Background(), Event{Kind: EventUnknownSource, At: time.Now()})
	if len(actions) != 0 {
		t.Errorf("rule fired on non-matching Kind: %v", actions)
	}
}

func TestDefaults_InstallsExpectedRules(t *testing.T) {
	rules := Defaults()
	names := make([]string, len(rules))
	for i, r := range rules {
		names[i] = r.Name()
	}
	want := []string{"unknown_source_flood", "bad_mac_flood"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("Defaults rule names = %v, want %v", names, want)
	}
}

func TestIDS_Subscribe_ReceivesNotifications(t *testing.T) {
	i := New()
	i.AddRule(&constantRule{
		name: "r1", kind: EventUnknownSource,
		actions: []Action{CounterBump{SourceAddr: "x"}},
	})
	ch, cancel := i.Subscribe(4)
	defer cancel()

	i.Observe(context.Background(), Event{Kind: EventUnknownSource, SourceAddr: "addr1"})
	i.Observe(context.Background(), Event{Kind: EventUnknownSource, SourceAddr: "addr2"})

	for want := 2; want > 0; want-- {
		select {
		case n := <-ch:
			if n.Event.Kind != EventUnknownSource {
				t.Errorf("notification Kind = %v", n.Event.Kind)
			}
			if len(n.Actions) != 1 {
				t.Errorf("notification Actions = %v", n.Actions)
			}
		case <-time.After(200 * time.Millisecond):
			t.Fatalf("subscriber didn't receive all notifications (still want %d)", want)
		}
	}
}

func TestIDS_Subscribe_Cancel_StopsDelivery(t *testing.T) {
	i := New()
	ch, cancel := i.Subscribe(2)

	cancel()
	i.Observe(context.Background(), Event{Kind: EventUnknownSource})

	if _, ok := <-ch; ok {
		t.Error("channel should be closed after cancel")
	}

	cancel()
}

func TestIDS_Subscribe_DropsOnSlowConsumer(t *testing.T) {
	i := New()
	_, cancel := i.Subscribe(1)
	defer cancel()

	for k := 0; k < 5; k++ {
		i.Observe(context.Background(), Event{Kind: EventUnknownSource})
	}

	snap := i.Snapshot()
	if snap.EventCounts["unknown_source"] != 5 {
		t.Errorf("EventCounts = %v, want 5 (observer must not block on slow subscriber)", snap.EventCounts)
	}
}

func TestIDS_Subscribe_MultipleSubscribers(t *testing.T) {
	i := New()
	ch1, cancel1 := i.Subscribe(4)
	defer cancel1()
	ch2, cancel2 := i.Subscribe(4)
	defer cancel2()

	i.Observe(context.Background(), Event{Kind: EventUnknownSource})

	for _, ch := range []<-chan Notification{ch1, ch2} {
		select {
		case <-ch:

		case <-time.After(200 * time.Millisecond):
			t.Errorf("one subscriber didn't receive")
		}
	}
}

func TestThresholdPerSource_PanicOnThreshold_EmitsPanicRotate(t *testing.T) {
	r := &ThresholdPerSource{
		RuleName: "bad_mac_flood", Kind: EventBadMAC,
		Threshold: 3, Window: time.Minute,
		Level:            AlertCritical,
		Message:          "attack",
		PanicOnThreshold: true,
	}
	base := time.Unix(1_700_000_000, 0)
	var lastActions []Action
	for i := 0; i < 4; i++ {
		lastActions = r.Evaluate(context.Background(), Event{
			Kind:       EventBadMAC,
			At:         base.Add(time.Duration(i) * time.Second),
			SourceAddr: "attacker-onion",
			PeerID:     "peer-id-1",
			SlotIdx:    1,
		})
	}

	var sawPanic bool
	for _, a := range lastActions {
		if pr, ok := a.(PanicRotate); ok {
			sawPanic = true
			if pr.SlotIdx != 1 {
				t.Errorf("PanicRotate.SlotIdx = %d, want 1 (from triggering event)", pr.SlotIdx)
			}
			if pr.RelatedPeer != "peer-id-1" {
				t.Errorf("PanicRotate.RelatedPeer = %q, want peer-id-1", pr.RelatedPeer)
			}
			if pr.Reason != "bad_mac_flood" {
				t.Errorf("PanicRotate.Reason = %q, want rule name", pr.Reason)
			}
		}
	}
	if !sawPanic {
		t.Errorf("PanicRotate not emitted above threshold; actions=%+v", lastActions)
	}
}

func TestThresholdPerSource_PanicOnThreshold_NotEmittedBelow(t *testing.T) {
	r := &ThresholdPerSource{
		RuleName: "bad_mac_flood", Kind: EventBadMAC,
		Threshold: 3, Window: time.Minute,
		Level:            AlertCritical,
		Message:          "attack",
		PanicOnThreshold: true,
	}
	actions := r.Evaluate(context.Background(), Event{
		Kind: EventBadMAC, At: time.Unix(1, 0), SourceAddr: "src", SlotIdx: 0,
	})
	for _, a := range actions {
		if _, ok := a.(PanicRotate); ok {
			t.Errorf("PanicRotate emitted below threshold: %+v", actions)
		}
	}
}

func TestThresholdPerSource_PanicOnThreshold_DisabledByDefault(t *testing.T) {
	r := &ThresholdPerSource{
		RuleName: "no_panic", Kind: EventBadMAC,
		Threshold: 2, Window: time.Minute,
		Level:   AlertCritical,
		Message: "just alert",
	}
	base := time.Unix(1, 0)
	r.Evaluate(context.Background(), Event{Kind: EventBadMAC, At: base, SourceAddr: "src"})
	actions := r.Evaluate(context.Background(), Event{Kind: EventBadMAC, At: base.Add(time.Second), SourceAddr: "src"})
	for _, a := range actions {
		if _, ok := a.(PanicRotate); ok {
			t.Errorf("PanicRotate emitted despite PanicOnThreshold=false: %+v", actions)
		}
	}
}

func TestDefaults_BadMACRuleEmitsPanicRotate(t *testing.T) {

	defaults := Defaults()
	var badMACRule *ThresholdPerSource
	for _, r := range defaults {
		if tps, ok := r.(*ThresholdPerSource); ok && tps.Kind == EventBadMAC {
			badMACRule = tps
			break
		}
	}
	if badMACRule == nil {
		t.Fatal("bad-MAC ThresholdPerSource rule missing from Defaults()")
	}
	if !badMACRule.PanicOnThreshold {
		t.Error("bad-MAC rule has PanicOnThreshold=false; P11S8 wiring missing")
	}
	if badMACRule.Level != AlertCritical {
		t.Errorf("bad-MAC rule Level = %v, want AlertCritical", badMACRule.Level)
	}
}
