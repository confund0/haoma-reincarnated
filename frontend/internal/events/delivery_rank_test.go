package events

import "testing"

func TestDeliveryAdvances_OutboxReportNeverLowersAStep(t *testing.T) {
	for _, cur := range []string{DeliveryDelivered, DeliveryRead} {
		for _, report := range []string{DeliveryEnqueued, DeliverySent} {
			if DeliveryAdvances(cur, report) {
				t.Errorf("outbox report %q was allowed over %q — the reconcile "+
					"downgrades a row the peer already acked", report, cur)
			}
		}
	}
}

func TestDeliveryAdvances_ClimbsAndStartsFromUnset(t *testing.T) {
	climbs := []struct{ cur, next string }{
		{"", DeliveryEnqueued},
		{"", DeliveryFailed},
		{DeliveryEnqueued, DeliverySent},
		{DeliverySent, DeliveryDelivered},
		{DeliveryDelivered, DeliveryRead},
		{DeliverySent, DeliveryRead},
	}
	for _, c := range climbs {
		if !DeliveryAdvances(c.cur, c.next) {
			t.Errorf("DeliveryAdvances(%q, %q) = false, want true", c.cur, c.next)
		}
	}
}

func TestDeliveryAdvances_FailureYieldsToPeerEvidence(t *testing.T) {
	if !DeliveryAdvances(DeliverySent, DeliveryFailed) {
		t.Error("a failure must land on a row the outbox is still working")
	}
	for _, cur := range []string{DeliveryDelivered, DeliveryRead} {
		if DeliveryAdvances(cur, DeliveryFailed) {
			t.Errorf("failure was allowed over %q — the peer already confirmed arrival", cur)
		}
	}
}

func TestDeliveryRank_InboundFileStatesAreNotSteps(t *testing.T) {
	for _, s := range []string{"downloading", "awaiting_key", "ready", "failed_transient", DeliveryFailed} {
		if _, ok := DeliveryRank(s); ok {
			t.Errorf("%q ranked as a ladder step", s)
		}
	}
	for _, s := range []string{"", DeliveryEnqueued, DeliverySent, DeliveryDelivered, DeliveryRead} {
		if _, ok := DeliveryRank(s); !ok {
			t.Errorf("%q did not rank as a ladder step", s)
		}
	}
}

func TestDeliverySeedFromSibling_LiftsEveryBottomStep(t *testing.T) {
	for _, proposed := range []string{"", DeliveryPending, DeliveryEnqueued} {
		if got := DeliverySeedFromSibling(proposed); got != DeliverySiblingFloor {
			t.Errorf("a sibling row proposing %q was seeded at %q, want the floor %q — "+
				"it renders as a message that never left the outbox",
				proposed, got, DeliverySiblingFloor)
		}
	}
}

func TestDeliverySeedFromSibling_KeepsAHigherStep(t *testing.T) {
	for _, proposed := range []string{DeliverySent, DeliveryDelivered, DeliveryRead} {
		if got := DeliverySeedFromSibling(proposed); got != proposed {
			t.Errorf("a sibling row arriving at %q was rewritten to %q", proposed, got)
		}
	}
}

func TestDeliverySeedFromSibling_LeavesNonStepsAlone(t *testing.T) {
	for _, proposed := range []string{DeliveryFailed, "downloading", "awaiting_key", "ready"} {
		if got := DeliverySeedFromSibling(proposed); got != proposed {
			t.Errorf("non-step %q was rewritten to %q by the floor", proposed, got)
		}
	}
}

func TestDeliveryOutranks_RefusesNonStepsBothSides(t *testing.T) {
	if DeliveryOutranks(DeliveryFailed, DeliveryDelivered) {
		t.Error("a non-step current state must not be climbed from")
	}
	if DeliveryOutranks(DeliverySent, "downloading") {
		t.Error("an inbound transfer state must not be treated as a step")
	}
	if DeliveryOutranks(DeliveryRead, DeliveryRead) {
		t.Error("equal states do not outrank each other")
	}
}
