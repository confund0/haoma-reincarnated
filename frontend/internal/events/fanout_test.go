package events_test

import (
	"encoding/json"
	"testing"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
)

func TestLess_CanonicalOrder(t *testing.T) {

	older := events.Event{DisplayTs: 10, MsgID: "zzz", RecvSeq: 9}
	newer := events.Event{DisplayTs: 20, MsgID: "aaa", RecvSeq: 1}
	if !events.Less(older, newer) {
		t.Error("older display_ts must sort first regardless of msg_id/recv_seq")
	}
	if events.Less(newer, older) {
		t.Error("newer display_ts must not sort before older")
	}

	a := events.Event{DisplayTs: 10, MsgID: "aaa", RecvSeq: 99}
	b := events.Event{DisplayTs: 10, MsgID: "bbb", RecvSeq: 1}
	if !events.Less(a, b) {
		t.Error("equal ts: lower msg_id must sort first even with higher recv_seq")
	}

	lo := events.Event{DisplayTs: 10, MsgID: "", RecvSeq: 3}
	hi := events.Event{DisplayTs: 10, MsgID: "", RecvSeq: 5}
	if !events.Less(lo, hi) {
		t.Error("equal ts + empty msg_id: lower recv_seq must sort first")
	}
	if events.Less(hi, lo) {
		t.Error("recv_seq tiebreak not antisymmetric")
	}
}

func TestAppendOutbound_MultiEnvelopeIndexesEveryID(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))

	body, _ := json.Marshal(events.TextBody{Text: "fanned"})
	fanIDs := []string{"env-devA", "env-devB", "env-devC"}
	got, err := l.AppendOutbound(events.OutboundParams{
		ChatID:      chat.ChatID("peer-alice"),
		Kind:        events.KindText,
		SenderSeq:   1,
		EnvelopeIDs: fanIDs,
		MsgID:       "m1",
		Body:        body,
	})
	if err != nil {
		t.Fatal(err)
	}

	if got.EnvelopeID != "env-devA" {
		t.Errorf("primary EnvelopeID = %q, want env-devA", got.EnvelopeID)
	}
	if len(got.EnvelopeIDs) != 3 {
		t.Fatalf("EnvelopeIDs = %v, want 3 entries", got.EnvelopeIDs)
	}

	rows, err := l.List(chat.ChatID("peer-alice"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("List returned %d rows, want 1 (deduped to a single row)", len(rows))
	}

	for _, eid := range fanIDs {
		upd, err := l.UpdateDeliveryState(eid, "sent")
		if err != nil {
			t.Fatalf("UpdateDeliveryState(%q) did not resolve the shared row: %v", eid, err)
		}
		if upd.MsgID != "m1" {
			t.Errorf("envelope %q resolved to msg_id %q, want m1", eid, upd.MsgID)
		}
	}
}

func TestAppendOutbound_SingularConvenienceIndexed(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))

	body, _ := json.Marshal(events.TextBody{Text: "solo"})
	got, err := l.AppendOutbound(events.OutboundParams{
		ChatID:     chat.ChatID("peer-bob"),
		Kind:       events.KindText,
		SenderSeq:  1,
		EnvelopeID: "env-solo",
		MsgID:      "m2",
		Body:       body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.EnvelopeID != "env-solo" || len(got.EnvelopeIDs) != 1 || got.EnvelopeIDs[0] != "env-solo" {
		t.Fatalf("singular convenience not normalised to a 1-list: id=%q ids=%v", got.EnvelopeID, got.EnvelopeIDs)
	}
	if _, err := l.UpdateDeliveryState("env-solo", "sent"); err != nil {
		t.Fatalf("singular envelope not indexed: %v", err)
	}
}
