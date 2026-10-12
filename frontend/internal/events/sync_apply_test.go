package events_test

import (
	"testing"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
)

func TestApplyInboundReadAt_StampsAnUnreadRow(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	if _, err := l.AppendInbound(events.InboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderTs: 1742643880,
		MsgID: "in-1", SenderPeerID: "peer-1", Status: events.DecryptOK, Body: textBody(t, "hi"),
	}); err != nil {
		t.Fatal(err)
	}

	updated, changed, err := l.ApplyInboundReadAt("in-1", 1742643900)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("changed = false, want true — the row was unread before this call")
	}
	if updated.ReadAt != 1742643900 {
		t.Errorf("ReadAt = %d, want 1742643900", updated.ReadAt)
	}

	stored, err := l.GetByMsgID("in-1")
	if err != nil {
		t.Fatal(err)
	}
	if stored.ReadAt != 1742643900 {
		t.Fatalf("persisted ReadAt = %d, want 1742643900", stored.ReadAt)
	}
}

func TestApplyInboundReadAt_EarliestWins(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	if _, err := l.AppendInbound(events.InboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderTs: 1742643880,
		MsgID: "in-1", SenderPeerID: "peer-1", Status: events.DecryptOK, Body: textBody(t, "hi"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.ApplyInboundReadAt("in-1", 1742643900); err != nil {
		t.Fatal(err)
	}

	updated, changed, err := l.ApplyInboundReadAt("in-1", 1742643999)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("changed = true, want false — a later claim must not overwrite the earlier read time")
	}
	if updated.ReadAt != 1742643900 {
		t.Errorf("ReadAt = %d, want 1742643900 (earliest wins)", updated.ReadAt)
	}

	if got, err := l.GetByMsgID("in-1"); err != nil {
		t.Fatal(err)
	} else if got.ReadAt != 1742643900 {
		t.Fatalf("persisted ReadAt = %d, want 1742643900", got.ReadAt)
	}
}

func TestApplyInboundReadAt_WrongShapeIsANoop(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, MsgID: "out-1", Body: textBody(t, "hi"),
	}); err != nil {
		t.Fatal(err)
	}

	if _, changed, err := l.ApplyInboundReadAt("out-1", 1742643900); err != nil {
		t.Fatal(err)
	} else if changed {
		t.Error("an outbound row accepted an inbound read-time claim")
	}
	if _, _, err := l.ApplyInboundReadAt("out-1", 0); err == nil {
		t.Error("a zero read time was accepted")
	}
	if _, _, err := l.ApplyInboundReadAt("does-not-exist", 1742643900); err != events.ErrEventNotFound {
		t.Errorf("err = %v, want ErrEventNotFound", err)
	}
}

func seedOutboundFile(t *testing.T, l *events.Log, msgID string) {
	t.Helper()
	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindFile, MsgID: msgID,
		EnvelopeID: "env-" + msgID, Body: textBody(t, "file"),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestApplyDeliverySnapshot_RankClimbsNeverFalls(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	seedOutboundFile(t, l, "f-1")

	updated, changed, err := l.ApplyDeliverySnapshot("f-1", events.DeliveryDelivered, false)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || updated.DeliveryState != events.DeliveryDelivered {
		t.Fatalf("delivery_state = %q, changed=%v, want %q / true", updated.DeliveryState, changed, events.DeliveryDelivered)
	}

	_, changed, err = l.ApplyDeliverySnapshot("f-1", events.DeliveryEnqueued, false)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("changed = true, want false — a lower rank must not downgrade the row")
	}

	if got, err := l.GetByMsgID("f-1"); err != nil {
		t.Fatal(err)
	} else if got.DeliveryState != events.DeliveryDelivered {
		t.Fatalf("persisted delivery_state = %q, want %q", got.DeliveryState, events.DeliveryDelivered)
	}
}

func TestApplyDeliverySnapshot_FailureNeverOverwritesDeliveredOrRead(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	seedOutboundFile(t, l, "f-2")
	if _, _, err := l.ApplyDeliverySnapshot("f-2", events.DeliveryDelivered, false); err != nil {
		t.Fatal(err)
	}

	_, changed, err := l.ApplyDeliverySnapshot("f-2", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("changed = true, want false — a stale failure must not overwrite a delivered row")
	}
	got, err := l.GetByMsgID("f-2")
	if err != nil {
		t.Fatal(err)
	}
	if got.DeliveryState != events.DeliveryDelivered {
		t.Fatalf("delivery_state = %q, want %q — a sibling's stale failure claim overwrote a delivered row",
			got.DeliveryState, events.DeliveryDelivered)
	}
}

func TestApplyDeliverySnapshot_FailureAppliesOverTheSiblingFloor(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindFile, MsgID: "f-3",
		Body: textBody(t, "file"), MirroredFromSibling: true,
	}); err != nil {
		t.Fatal(err)
	}

	updated, changed, err := l.ApplyDeliverySnapshot("f-3", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || updated.DeliveryState != events.DeliveryFailed {
		t.Fatalf("delivery_state = %q, changed=%v, want %q / true", updated.DeliveryState, changed, events.DeliveryFailed)
	}
}

func TestApplyDeliverySnapshot_FailureNeverOverwritesOwnSent(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	seedOutboundFile(t, l, "f-4")
	if _, _, err := l.ApplyDeliverySnapshot("f-4", events.DeliverySent, false); err != nil {
		t.Fatal(err)
	}
	updated, changed, err := l.ApplyDeliverySnapshot("f-4", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if changed || updated.DeliveryState != events.DeliverySent {
		t.Fatalf("delivery_state = %q, changed=%v, want %q / false", updated.DeliveryState, changed, events.DeliverySent)
	}
}

func TestApplyDeliverySnapshot_InboundRowIsANoop(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	if _, err := l.AppendInbound(events.InboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindFile, SenderTs: 1742643880,
		MsgID: "in-file", Status: events.DecryptOK, Body: textBody(t, "hi"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := l.ApplyDeliverySnapshot("in-file", events.DeliveryRead, false); err != nil {
		t.Fatal(err)
	} else if changed {
		t.Error("an inbound row's local file-transfer state accepted an outbound delivery rank claim")
	}
}

func TestApplyReceiptSentClaim_AppliesToAFreshRow(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	if _, err := l.AppendInbound(events.InboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderTs: 1742643880,
		MsgID: "in-1", SenderPeerID: "peer-1", Status: events.DecryptOK, Body: textBody(t, "hi"),
	}); err != nil {
		t.Fatal(err)
	}

	updated, changed, err := l.ApplyReceiptSentClaim("in-1", 1742643900, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || updated.ReadReceiptSentAt != 1742643900 {
		t.Fatalf("ReadReceiptSentAt = %d, changed=%v, want 1742643900 / true", updated.ReadReceiptSentAt, changed)
	}
	if updated.ReceiptClaimOrigin {
		t.Error("an inherited claim stamped ReceiptClaimOrigin=true — it would re-export past its origin")
	}
}

func TestApplyReceiptSentClaim_StickyOverAnExistingClaim(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	if _, err := l.AppendInbound(events.InboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderTs: 1742643880,
		MsgID: "in-1", SenderPeerID: "peer-1", Status: events.DecryptOK, Body: textBody(t, "hi"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := l.MarkReadReceiptSent([]string{"in-1"}, 1742643888); err != nil {
		t.Fatal(err)
	}

	_, changed, err := l.ApplyReceiptSentClaim("in-1", 1742643900, 0)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("changed = true, want false — a row with its own honest claim is already resolved")
	}
	got, err := l.GetByMsgID("in-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ReadReceiptSentAt != 1742643888 || !got.ReceiptClaimOrigin {
		t.Fatalf("own claim was overwritten: sent_at=%d origin=%v", got.ReadReceiptSentAt, got.ReceiptClaimOrigin)
	}
}

func TestApplyReceiptSentClaim_NeverOverwritesSuppression(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	if _, err := l.AppendInbound(events.InboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderTs: 1742643880,
		MsgID: "in-1", SenderPeerID: "peer-1", Status: events.DecryptOK, Body: textBody(t, "hi"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := l.MarkReadReceiptSent([]string{"in-1"}, events.ReadReceiptSuppressedSentinel); err != nil {
		t.Fatal(err)
	}

	_, changed, err := l.ApplyReceiptSentClaim("in-1", 1742643900, 0)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("changed = true, want false — a sibling's claim must never un-suppress a locally disabled chat")
	}
	got, err := l.GetByMsgID("in-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ReadReceiptSentAt != events.ReadReceiptSuppressedSentinel {
		t.Fatalf("ReadReceiptSentAt = %d, want the suppression sentinel preserved", got.ReadReceiptSentAt)
	}
}

func TestApplyReceiptSentClaim_StaleGenerationLoses(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	if _, err := l.AppendInbound(events.InboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderTs: 1742643880,
		MsgID: "in-1", SenderPeerID: "peer-1", Status: events.DecryptOK, Body: textBody(t, "hi"),
	}); err != nil {
		t.Fatal(err)
	}

	if err := l.MarkReadReceiptSent([]string{"in-1"}, 1742643889); err != nil {
		t.Fatal(err)
	}
	if err := l.RecordReceiptEnvelope("env-1", chat.ChatID("alice"), []string{"in-1"}, 1742643889); err != nil {
		t.Fatal(err)
	}
	if found, cleared, err := l.ClearReceiptEnvelope("env-1"); err != nil {
		t.Fatal(err)
	} else if !found || cleared != 1 {
		t.Fatalf("withdrawal did not clear the row: found=%v cleared=%d", found, cleared)
	}

	afterWithdraw, err := l.GetByMsgID("in-1")
	if err != nil {
		t.Fatal(err)
	}
	if afterWithdraw.ReceiptClaimGeneration == 0 {
		t.Fatal("withdrawal did not raise ReceiptClaimGeneration — the stale-claim race is unguarded")
	}

	_, changed, err := l.ApplyReceiptSentClaim("in-1", 1742643890, 0)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("changed = true, want false — a claim predating the withdrawal must lose")
	}
	got, err := l.GetByMsgID("in-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ReadReceiptSentAt != 0 {
		t.Fatalf("ReadReceiptSentAt = %d, want 0 — the withdrawal must survive the stale claim", got.ReadReceiptSentAt)
	}

	current, changed, err := l.ApplyReceiptSentClaim("in-1", 1742643999, afterWithdraw.ReceiptClaimGeneration)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || current.ReadReceiptSentAt != 1742643999 {
		t.Fatalf("a claim at the current generation was refused: changed=%v sent_at=%d", changed, current.ReadReceiptSentAt)
	}
}

func TestApplyReceiptSentClaim_OutboundRowIsANoop(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, MsgID: "out-1", Body: textBody(t, "hi"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := l.ApplyReceiptSentClaim("out-1", 1742643900, 0); err != nil {
		t.Fatal(err)
	} else if changed {
		t.Error("an outbound row accepted an inbound-only receipt claim")
	}
}

func TestApplyDeliverySnapshot_FailureNeverOverwritesOwnEnqueued(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	seedOutboundFile(t, l, "f-5")
	updated, changed, err := l.ApplyDeliverySnapshot("f-5", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if changed || updated.DeliveryState != events.DeliveryEnqueued {
		t.Fatalf("delivery_state = %q, changed=%v, want %q / false", updated.DeliveryState, changed, events.DeliveryEnqueued)
	}
}
