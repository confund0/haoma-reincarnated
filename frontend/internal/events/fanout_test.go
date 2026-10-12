package events_test

import (
	"encoding/json"
	"errors"
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
		upd, _, err := l.UpdateDeliveryState(eid, "sent")
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
	if _, _, err := l.UpdateDeliveryState("env-solo", "sent"); err != nil {
		t.Fatalf("singular envelope not indexed: %v", err)
	}
}

func TestUpdateDeliveryState_SecondDeviceMovesTheRowOnce(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	chatID := chat.ChatID("peer-alice")
	body, _ := json.Marshal(events.TextBody{Text: "to both"})
	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID:      chatID,
		Kind:        events.KindText,
		SenderSeq:   1,
		EnvelopeIDs: []string{"env-desktop", "env-phone"},
		MsgID:       "m-fold",
		Body:        body,
	}); err != nil {
		t.Fatal(err)
	}

	row, moved, err := l.UpdateDeliveryState("env-phone", events.DeliverySent)
	if err != nil {
		t.Fatal(err)
	}
	if !moved {
		t.Fatal("the second device's ack did not move the row")
	}
	if row.EnvelopeID != "env-desktop" || row.DeliveryState != events.DeliverySent {
		t.Fatalf("row = (%q, %q), want (env-desktop, sent)", row.EnvelopeID, row.DeliveryState)
	}

	row, moved, err = l.UpdateDeliveryState("env-desktop", events.DeliverySent)
	if err != nil {
		t.Fatal(err)
	}
	if moved {
		t.Error("the first device's late ack moved a row that was already sent")
	}
	if row.DeliveryState != events.DeliverySent {
		t.Errorf("state after the late ack = %q, want sent", row.DeliveryState)
	}

	if _, _, err := l.ApplyReadReceipt("m-fold", 1742643999, chatID); err != nil {
		t.Fatal(err)
	}
	for _, report := range []string{events.DeliveryEnqueued, events.DeliverySent} {
		row, moved, err = l.UpdateDeliveryState("env-phone", report)
		if err != nil {
			t.Fatal(err)
		}
		if moved || row.DeliveryState != events.DeliveryRead {
			t.Errorf("report %q on a read row: moved=%v state=%q, want no move and read",
				report, moved, row.DeliveryState)
		}
	}
}

func TestUpdateDeliveryState_UnindexedEnvelopeMovesNothing(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	chatID := chat.ChatID("peer-alice")
	body, _ := json.Marshal(events.TextBody{Text: "from my other device"})
	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID:              chatID,
		Kind:                events.KindText,
		SenderSeq:           1,
		MsgID:               "m-mirrored",
		Body:                body,
		MirroredFromSibling: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, moved, err := l.UpdateDeliveryState("env-rail-copy", events.DeliveryFailed); !errors.Is(err, events.ErrEventNotFound) || moved {
		t.Fatalf("rail envelope report: moved=%v err=%v, want ErrEventNotFound", moved, err)
	}
	got, err := l.GetByMsgID("m-mirrored")
	if err != nil {
		t.Fatal(err)
	}
	if got.DeliveryState != events.DeliverySiblingFloor {
		t.Errorf("mirrored row state = %q, want %q", got.DeliveryState, events.DeliverySiblingFloor)
	}
}

func appendTwoDeviceRow(t *testing.T, l *events.Log, msgID, state string) {
	t.Helper()
	body, _ := json.Marshal(events.TextBody{Text: msgID})
	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID:        chat.ChatID("peer-alice"),
		Kind:          events.KindText,
		SenderSeq:     1,
		EnvelopeIDs:   []string{"env-desktop-" + msgID, "env-phone-" + msgID},
		MsgID:         msgID,
		Body:          body,
		DeliveryState: state,
	}); err != nil {
		t.Fatal(err)
	}
}

func report(t *testing.T, l *events.Log, envID, state string) (events.Event, bool) {
	t.Helper()
	row, moved, err := l.UpdateDeliveryState(envID, state)
	if err != nil {
		t.Fatalf("UpdateDeliveryState(%s, %s): %v", envID, state, err)
	}
	return row, moved
}

func TestUpdateDeliveryState_FailedOnlyWhenEveryEnvelopeFailed(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	appendTwoDeviceRow(t, l, "m1", "")

	row, moved := report(t, l, "env-phone-m1", events.DeliveryFailed)
	if moved || row.DeliveryState != events.DeliveryEnqueued {
		t.Fatalf("one failed envelope of two: moved=%v state=%q, want no move, enqueued", moved, row.DeliveryState)
	}
	held, err := l.GetByMsgID("m1")
	if err != nil {
		t.Fatal(err)
	}
	if len(held.FailedEnvelopeIDs) != 1 || held.FailedEnvelopeIDs[0] != "env-phone-m1" {
		t.Fatalf("stored failed list = %v, want [env-phone-m1]", held.FailedEnvelopeIDs)
	}

	row, moved = report(t, l, "env-desktop-m1", events.DeliveryFailed)
	if !moved || row.DeliveryState != events.DeliveryFailed || row.EnvelopeID != "env-desktop-m1" {
		t.Fatalf("every envelope failed: moved=%v row=(%q, %q), want moved, (env-desktop-m1, failed)",
			moved, row.EnvelopeID, row.DeliveryState)
	}

}

func TestUpdateDeliveryState_FailureAfterAnAckDoesNotMove(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	appendTwoDeviceRow(t, l, "m2", "")
	report(t, l, "env-desktop-m2", events.DeliverySent)
	row, moved := report(t, l, "env-phone-m2", events.DeliveryFailed)
	if moved || row.DeliveryState != events.DeliverySent {
		t.Fatalf("failure after an ack: moved=%v state=%q, want no move, sent", moved, row.DeliveryState)
	}
}

func TestUpdateDeliveryState_SingleEnvelopeFailsAsBefore(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	body, _ := json.Marshal(events.TextBody{Text: "solo"})
	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("peer-bob"), Kind: events.KindText, SenderSeq: 1,
		EnvelopeID: "env-solo", MsgID: "m-solo", Body: body,
	}); err != nil {
		t.Fatal(err)
	}
	row, moved := report(t, l, "env-solo", events.DeliveryFailed)
	if !moved || row.DeliveryState != events.DeliveryFailed {
		t.Fatalf("single envelope failed: moved=%v state=%q, want moved, failed", moved, row.DeliveryState)
	}
}

func TestUpdateDeliveryState_FailureWinsOverAnEchoedFloor(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	body, _ := json.Marshal(events.TextBody{Text: "doomed"})
	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("peer-bob"), Kind: events.KindText, SenderSeq: 1,
		EnvelopeID: "env-doomed", MsgID: "m-doomed", Body: body,
	}); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := l.ApplyDeliverySnapshot("m-doomed", events.DeliverySent, false); err != nil || !changed {
		t.Fatalf("echoed floor: changed=%v err=%v, want the rank taken", changed, err)
	}
	row, moved := report(t, l, "env-doomed", events.DeliveryFailed)
	if !moved || row.DeliveryState != events.DeliveryFailed {
		t.Fatalf("failure over an echoed floor: moved=%v state=%q, want moved, failed", moved, row.DeliveryState)
	}
}
