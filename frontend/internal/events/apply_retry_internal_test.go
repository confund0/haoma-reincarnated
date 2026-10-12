package events

import (
	"encoding/json"
	"testing"
	"time"

	badger "github.com/dgraph-io/badger/v4"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/store"
)

func newRetryTestLog(t *testing.T) (*Log, *store.Store) {
	t.Helper()
	st := newTestStore(t)
	now := time.Unix(1742643890, 0)
	l := New(st, NewBus(), func() time.Time { return now })
	return l, st
}

func disruptRowIdentical(t *testing.T, st *store.Store, msgID string) {
	t.Helper()
	if err := st.Update(func(txn *badger.Txn) error {
		idxItem, err := txn.Get([]byte(msgIDPrefix + msgID))
		if err != nil {
			return err
		}
		var key []byte
		if err := idxItem.Value(func(v []byte) error {
			key = append([]byte(nil), v...)
			return nil
		}); err != nil {
			return err
		}
		rowItem, err := txn.Get(key)
		if err != nil {
			return err
		}
		var raw []byte
		if err := rowItem.Value(func(v []byte) error {
			raw = append([]byte(nil), v...)
			return nil
		}); err != nil {
			return err
		}
		return txn.Set(key, raw)
	}); err != nil {
		t.Fatalf("disruptor (identical rewrite) failed: %v", err)
	}
}

func disruptRowStampReceiptClaim(t *testing.T, st *store.Store, msgID string, at int64) {
	t.Helper()
	if err := st.Update(func(txn *badger.Txn) error {
		idxItem, err := txn.Get([]byte(msgIDPrefix + msgID))
		if err != nil {
			return err
		}
		var key []byte
		if err := idxItem.Value(func(v []byte) error {
			key = append([]byte(nil), v...)
			return nil
		}); err != nil {
			return err
		}
		rowItem, err := txn.Get(key)
		if err != nil {
			return err
		}
		var ev Event
		if err := rowItem.Value(func(v []byte) error {
			return json.Unmarshal(v, &ev)
		}); err != nil {
			return err
		}
		ev.ReadReceiptSentAt = at
		ev.ReceiptClaimOrigin = false
		raw, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		return txn.Set(key, raw)
	}); err != nil {
		t.Fatalf("disruptor (stamp receipt claim) failed: %v", err)
	}
}

func TestApplyReceiptSentClaim_RetryReportsTheSecondAttemptsVerdict(t *testing.T) {
	l, st := newRetryTestLog(t)
	body, err := json.Marshal(TextBody{Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.AppendInbound(InboundParams{
		ChatID: chat.ChatID("alice"), Kind: KindText, SenderTs: 1742643880,
		MsgID: "in-1", SenderPeerID: "peer-1", Status: DecryptOK, Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	const earlierClaim = 1742643850
	const laterClaim = 1742643900

	attempts := 0
	l.retryTestHook = func() {
		attempts++
		if attempts == 1 {
			disruptRowStampReceiptClaim(t, st, "in-1", earlierClaim)
		}
	}

	updated, changed, err := l.ApplyReceiptSentClaim("in-1", laterClaim, 0)
	if err != nil {
		t.Fatal(err)
	}
	if attempts < 2 {
		t.Fatalf("hook ran %d time(s), want at least 2 — the disruptor did not force a retry", attempts)
	}
	if changed {
		t.Fatal("changed = true, want false — attempt two correctly found the row already claimed and did nothing; " +
			"reporting true means attempt one's discarded verdict leaked through")
	}
	if updated.ReadReceiptSentAt != earlierClaim {
		t.Fatalf("returned ReadReceiptSentAt = %d, want %d (the earlier, real claim) — "+
			"attempt one's write must not have landed", updated.ReadReceiptSentAt, earlierClaim)
	}
	got, err := l.GetByMsgID("in-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ReadReceiptSentAt != earlierClaim {
		t.Fatalf("persisted ReadReceiptSentAt = %d, want %d", got.ReadReceiptSentAt, earlierClaim)
	}
}

func TestApplyReceiptSentClaim_RetrySurvivesAndLandsTheClaim(t *testing.T) {
	l, st := newRetryTestLog(t)
	body, _ := json.Marshal(TextBody{Text: "hi"})
	if _, err := l.AppendInbound(InboundParams{
		ChatID: chat.ChatID("alice"), Kind: KindText, SenderTs: 1742643880,
		MsgID: "in-1", SenderPeerID: "peer-1", Status: DecryptOK, Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	attempts := 0
	l.retryTestHook = func() {
		attempts++
		if attempts == 1 {
			disruptRowIdentical(t, st, "in-1")
		}
	}

	updated, changed, err := l.ApplyReceiptSentClaim("in-1", 1742643900, 0)
	if err != nil {
		t.Fatal(err)
	}
	if attempts < 2 {
		t.Fatalf("hook ran %d time(s), want at least 2", attempts)
	}
	if !changed {
		t.Fatal("changed = false, want true — the claim was dropped on retry")
	}
	if updated.ReadReceiptSentAt != 1742643900 {
		t.Fatalf("returned ReadReceiptSentAt = %d, want 1742643900", updated.ReadReceiptSentAt)
	}
	got, err := l.GetByMsgID("in-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ReadReceiptSentAt != 1742643900 {
		t.Fatalf("persisted ReadReceiptSentAt = %d, want 1742643900", got.ReadReceiptSentAt)
	}
}

func TestApplyReadReceipt_RetrySurvivesAndLandsTheClaim(t *testing.T) {
	l, st := newRetryTestLog(t)
	body, _ := json.Marshal(TextBody{Text: "hi"})
	if _, err := l.AppendOutbound(OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: KindText, MsgID: "out-1",
		EnvelopeID: "env-1", Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	attempts := 0
	l.retryTestHook = func() {
		attempts++
		if attempts == 1 {
			disruptRowIdentical(t, st, "out-1")
		}
	}

	updated, changed, err := l.ApplyReadReceipt("out-1", 1742643900, chat.ChatID("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if attempts < 2 {
		t.Fatalf("hook ran %d time(s), want at least 2", attempts)
	}
	if !changed {
		t.Fatal("changed = false, want true — the read receipt was dropped on retry")
	}
	if updated.ReadAt != 1742643900 {
		t.Fatalf("returned ReadAt = %d, want 1742643900", updated.ReadAt)
	}
	got, err := l.GetByMsgID("out-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ReadAt != 1742643900 {
		t.Fatalf("persisted ReadAt = %d, want 1742643900", got.ReadAt)
	}
}

func TestApplyInboundReadAt_RetrySurvivesAndLandsTheClaim(t *testing.T) {
	l, st := newRetryTestLog(t)
	body, _ := json.Marshal(TextBody{Text: "hi"})
	if _, err := l.AppendInbound(InboundParams{
		ChatID: chat.ChatID("alice"), Kind: KindText, SenderTs: 1742643880,
		MsgID: "in-1", SenderPeerID: "peer-1", Status: DecryptOK, Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	attempts := 0
	l.retryTestHook = func() {
		attempts++
		if attempts == 1 {
			disruptRowIdentical(t, st, "in-1")
		}
	}

	updated, changed, err := l.ApplyInboundReadAt("in-1", 1742643900)
	if err != nil {
		t.Fatal(err)
	}
	if attempts < 2 {
		t.Fatalf("hook ran %d time(s), want at least 2", attempts)
	}
	if !changed {
		t.Fatal("changed = false, want true — the read time was dropped on retry")
	}
	if updated.ReadAt != 1742643900 {
		t.Fatalf("returned ReadAt = %d, want 1742643900", updated.ReadAt)
	}
	got, err := l.GetByMsgID("in-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ReadAt != 1742643900 {
		t.Fatalf("persisted ReadAt = %d, want 1742643900", got.ReadAt)
	}
}

func disruptRowStampReadAt(t *testing.T, st *store.Store, msgID string, readAt int64) {
	t.Helper()
	if err := st.Update(func(txn *badger.Txn) error {
		idxItem, err := txn.Get([]byte(msgIDPrefix + msgID))
		if err != nil {
			return err
		}
		var key []byte
		if err := idxItem.Value(func(v []byte) error {
			key = append([]byte(nil), v...)
			return nil
		}); err != nil {
			return err
		}
		rowItem, err := txn.Get(key)
		if err != nil {
			return err
		}
		var ev Event
		if err := rowItem.Value(func(v []byte) error {
			return json.Unmarshal(v, &ev)
		}); err != nil {
			return err
		}
		ev.ReadAt = readAt
		raw, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		return txn.Set(key, raw)
	}); err != nil {
		t.Fatalf("disruptor (stamp read at) failed: %v", err)
	}
}

func TestApplyInboundReadAt_RetryReportsTheSecondAttemptsVerdict(t *testing.T) {
	l, st := newRetryTestLog(t)
	body, _ := json.Marshal(TextBody{Text: "hi"})
	if _, err := l.AppendInbound(InboundParams{
		ChatID: chat.ChatID("alice"), Kind: KindText, SenderTs: 1742643880,
		MsgID: "in-1", SenderPeerID: "peer-1", Status: DecryptOK, Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	const earlierRead = 1742643850
	const laterRead = 1742643900

	attempts := 0
	l.retryTestHook = func() {
		attempts++
		if attempts == 1 {
			disruptRowStampReadAt(t, st, "in-1", earlierRead)
		}
	}

	updated, changed, err := l.ApplyInboundReadAt("in-1", laterRead)
	if err != nil {
		t.Fatal(err)
	}
	if attempts < 2 {
		t.Fatalf("hook ran %d time(s), want at least 2 — the disruptor did not force a retry", attempts)
	}
	if changed {
		t.Fatal("changed = true, want false — attempt two found the row already read earlier and did nothing; " +
			"reporting true means attempt one's discarded verdict leaked through")
	}
	if updated.ReadAt != earlierRead {
		t.Fatalf("returned ReadAt = %d, want %d (the earlier, real read time) — "+
			"attempt one's write must not have landed", updated.ReadAt, earlierRead)
	}
	got, err := l.GetByMsgID("in-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ReadAt != earlierRead {
		t.Fatalf("persisted ReadAt = %d, want %d", got.ReadAt, earlierRead)
	}
}

func TestApplyDeliverySnapshot_RetrySurvivesAndLandsTheClaim(t *testing.T) {
	l, st := newRetryTestLog(t)
	body, _ := json.Marshal(TextBody{Text: "hi"})
	if _, err := l.AppendOutbound(OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: KindFile, MsgID: "out-1",
		EnvelopeID: "env-1", Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	attempts := 0
	l.retryTestHook = func() {
		attempts++
		if attempts == 1 {
			disruptRowIdentical(t, st, "out-1")
		}
	}

	updated, changed, err := l.ApplyDeliverySnapshot("out-1", DeliveryDelivered, false)
	if err != nil {
		t.Fatal(err)
	}
	if attempts < 2 {
		t.Fatalf("hook ran %d time(s), want at least 2", attempts)
	}
	if !changed {
		t.Fatal("changed = false, want true — the delivery rank was dropped on retry")
	}
	if updated.DeliveryState != DeliveryDelivered {
		t.Fatalf("returned delivery_state = %q, want %q", updated.DeliveryState, DeliveryDelivered)
	}
	got, err := l.GetByMsgID("out-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.DeliveryState != DeliveryDelivered {
		t.Fatalf("persisted delivery_state = %q, want %q", got.DeliveryState, DeliveryDelivered)
	}
}

func disruptRowStampDeliveryState(t *testing.T, st *store.Store, msgID, state string) {
	t.Helper()
	if err := st.Update(func(txn *badger.Txn) error {
		idxItem, err := txn.Get([]byte(msgIDPrefix + msgID))
		if err != nil {
			return err
		}
		var key []byte
		if err := idxItem.Value(func(v []byte) error {
			key = append([]byte(nil), v...)
			return nil
		}); err != nil {
			return err
		}
		rowItem, err := txn.Get(key)
		if err != nil {
			return err
		}
		var ev Event
		if err := rowItem.Value(func(v []byte) error {
			return json.Unmarshal(v, &ev)
		}); err != nil {
			return err
		}
		ev.DeliveryState = state
		raw, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		return txn.Set(key, raw)
	}); err != nil {
		t.Fatalf("disruptor (stamp delivery state) failed: %v", err)
	}
}

func TestApplyDeliverySnapshot_RetryReportsTheSecondAttemptsVerdict(t *testing.T) {
	l, st := newRetryTestLog(t)
	body, _ := json.Marshal(TextBody{Text: "hi"})
	if _, err := l.AppendOutbound(OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: KindFile, MsgID: "out-1",
		EnvelopeID: "env-1", Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	attempts := 0
	l.retryTestHook = func() {
		attempts++
		if attempts == 1 {
			disruptRowStampDeliveryState(t, st, "out-1", DeliveryDelivered)
		}
	}

	updated, changed, err := l.ApplyDeliverySnapshot("out-1", DeliveryDelivered, false)
	if err != nil {
		t.Fatal(err)
	}
	if attempts < 2 {
		t.Fatalf("hook ran %d time(s), want at least 2 — the disruptor did not force a retry", attempts)
	}
	if changed {
		t.Fatal("changed = true, want false — attempt two found the rank already landed and did nothing; " +
			"reporting true means attempt one's discarded verdict leaked through")
	}
	if updated.DeliveryState != DeliveryDelivered {
		t.Fatalf("returned delivery_state = %q, want %q", updated.DeliveryState, DeliveryDelivered)
	}
}

func disruptRowEdit(t *testing.T, st *store.Store, msgID string, edit func(*Event)) {
	t.Helper()
	if err := st.Update(func(txn *badger.Txn) error {
		idxItem, err := txn.Get([]byte(msgIDPrefix + msgID))
		if err != nil {
			return err
		}
		var key []byte
		if err := idxItem.Value(func(v []byte) error {
			key = append([]byte(nil), v...)
			return nil
		}); err != nil {
			return err
		}
		rowItem, err := txn.Get(key)
		if err != nil {
			return err
		}
		var ev Event
		if err := rowItem.Value(func(v []byte) error { return json.Unmarshal(v, &ev) }); err != nil {
			return err
		}
		edit(&ev)
		raw, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		return txn.Set(key, raw)
	}); err != nil {
		t.Fatalf("disruptor (edit) failed: %v", err)
	}
}

func appendTwoEnvelopeRow(t *testing.T, l *Log, msgID string) {
	t.Helper()
	body, err := json.Marshal(TextBody{Text: "to both"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.AppendOutbound(OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: KindText, SenderSeq: 1,
		EnvelopeIDs: []string{"env-1-" + msgID, "env-2-" + msgID}, MsgID: msgID, Body: body,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateDeliveryState_RetryDoesNotKeepTheFailedList(t *testing.T) {
	l, st := newRetryTestLog(t)
	appendTwoEnvelopeRow(t, l, "m-r1")
	attempts := 0
	l.retryTestHook = func() {
		attempts++
		if attempts == 1 {
			disruptRowIdentical(t, st, "m-r1")
		}
	}
	if _, _, err := l.UpdateDeliveryState("env-2-m-r1", DeliveryFailed); err != nil {
		t.Fatal(err)
	}
	if attempts < 2 {
		t.Fatalf("hook ran %d time(s), want at least 2 — the disruptor did not force a retry", attempts)
	}
	got, err := l.GetByMsgID("m-r1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.FailedEnvelopeIDs) != 1 || got.FailedEnvelopeIDs[0] != "env-2-m-r1" {
		t.Fatalf("stored failed list = %v, want [env-2-m-r1] — attempt one's list leaked into attempt two", got.FailedEnvelopeIDs)
	}
}

func TestUpdateDeliveryState_RetryReportsTheSecondAttemptsMoved(t *testing.T) {
	l, st := newRetryTestLog(t)
	appendTwoEnvelopeRow(t, l, "m-r2")
	if _, _, err := l.UpdateDeliveryState("env-1-m-r2", DeliveryFailed); err != nil {
		t.Fatal(err)
	}
	attempts := 0
	l.retryTestHook = func() {
		attempts++
		if attempts == 1 {
			disruptRowEdit(t, st, "m-r2", func(ev *Event) {
				ev.DeliveryState = DeliveryFailed
				ev.FailedEnvelopeIDs = []string{"env-1-m-r2", "env-2-m-r2"}
			})
		}
	}
	row, moved, err := l.UpdateDeliveryState("env-2-m-r2", DeliveryFailed)
	if err != nil {
		t.Fatal(err)
	}
	if attempts < 2 {
		t.Fatalf("hook ran %d time(s), want at least 2 — the disruptor did not force a retry", attempts)
	}
	if moved {
		t.Fatal("moved = true, want false — attempt two found the row failed already; attempt one's verdict leaked through")
	}
	if row.DeliveryState != DeliveryFailed {
		t.Fatalf("state = %q, want failed", row.DeliveryState)
	}
}
