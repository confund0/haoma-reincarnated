package events_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/store"
)

func init() {
	store.DefaultKDFParams = store.KDFParams{
		Time: 1, Memory: 8 * 1024, Threads: 2, KeyLen: 32, SaltLen: 16,
	}
}

func fixedClock(unix int64) func() time.Time {
	t := time.Unix(unix, 0)
	return func() time.Time { return t }
}

func newLog(t *testing.T, now func() time.Time) (*events.Log, *events.Bus) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Lock() })
	bus := events.NewBus()
	return events.New(st, bus, now), bus
}

func TestAppendOutbound_StoresAndPushesOnBus(t *testing.T) {
	clock := fixedClock(1742643890)
	l, bus := newLog(t, clock)

	ch, cancel := bus.Subscribe(4)
	defer cancel()

	body, _ := json.Marshal(events.TextBody{Text: "hello"})
	got, err := l.AppendOutbound(events.OutboundParams{
		ChatID:     chat.ChatID("alice"),
		Kind:       events.KindText,
		SenderSeq:  1,
		EnvelopeID: "env-1",
		Body:       body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Direction != events.DirOut {
		t.Errorf("direction = %q, want out", got.Direction)
	}
	if got.RecvSeq != 1 {
		t.Errorf("recv_seq = %d, want 1", got.RecvSeq)
	}
	if got.DisplayTs != 1742643890 {
		t.Errorf("display_ts = %d, want clock value", got.DisplayTs)
	}

	select {
	case ev := <-ch:
		if ev.RecvSeq != 1 {
			t.Errorf("bus event recv_seq = %d, want 1", ev.RecvSeq)
		}
	case <-time.After(time.Second):
		t.Fatal("no event delivered to bus subscriber")
	}

	rows, err := l.List(chat.ChatID("alice"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("List returned %d rows, want 1", len(rows))
	}
}

func TestAppendInbound_DecryptOK_StoresBody(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)

	body, _ := json.Marshal(events.TextBody{Text: "from bob"})
	got, err := l.AppendInbound(events.InboundParams{
		ChatID:    chat.ChatID("bob"),
		Kind:      events.KindText,
		SenderTs:  1742643885,
		SenderSeq: 7,
		Status:    events.DecryptOK,
		Body:      body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Direction != events.DirIn {
		t.Errorf("direction = %q, want in", got.Direction)
	}
	if got.DecryptStatus != events.DecryptOK {
		t.Errorf("decrypt_status = %q, want ok", got.DecryptStatus)
	}
	var tb events.TextBody
	if err := json.Unmarshal(got.Body, &tb); err != nil {
		t.Fatal(err)
	}
	if tb.Text != "from bob" {
		t.Errorf("text = %q, want 'from bob'", tb.Text)
	}
}

func TestAppendInbound_DecryptFailed_StoresRawBlob(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)

	got, err := l.AppendInbound(events.InboundParams{
		ChatID:     chat.ChatID("bob"),
		Kind:       events.KindText,
		SenderTs:   0,
		EnvelopeID: "env-bad",
		Status:     events.DecryptFailed,
		RawBlob:    []byte{0xDE, 0xAD, 0xBE, 0xEF},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.DecryptStatus != events.DecryptFailed {
		t.Errorf("decrypt_status = %q, want failed", got.DecryptStatus)
	}
	if string(got.RawBlob) != "\xde\xad\xbe\xef" {
		t.Errorf("raw_blob round-trip drift")
	}
	if got.DisplayTs != 1742643890 {
		t.Errorf("display_ts = %d, want clock value (sender_ts was 0)", got.DisplayTs)
	}
}

func TestAppendInbound_GroupStoresSenderPeerID(t *testing.T) {

	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)

	body, _ := json.Marshal(events.TextBody{Text: "hi group"})
	got, err := l.AppendInbound(events.InboundParams{
		ChatID:       chat.ChatID("group-xyz"),
		Kind:         events.KindText,
		SenderTs:     1742643885,
		SenderPeerID: "peer-alice",
		Status:       events.DecryptOK,
		Body:         body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.SenderPeerID != "peer-alice" {
		t.Errorf("SenderPeerID = %q, want 'peer-alice'", got.SenderPeerID)
	}
}

func TestRecvSeq_MonotonicAcrossChats(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)

	body, _ := json.Marshal(events.TextBody{Text: "x"})
	for i := 0; i < 3; i++ {
		_, err := l.AppendOutbound(events.OutboundParams{ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderSeq: uint64(i + 1), Body: body})
		if err != nil {
			t.Fatal(err)
		}
	}
	bobEv, err := l.AppendInbound(events.InboundParams{ChatID: chat.ChatID("bob"), Kind: events.KindText, SenderTs: 1742643890, Status: events.DecryptOK, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if bobEv.RecvSeq != 4 {
		t.Errorf("bob recv_seq = %d, want 4 (daemon-monotonic, not per-chat)", bobEv.RecvSeq)
	}
}

func TestRecvSeq_PersistsAcrossReopen(t *testing.T) {
	clock := fixedClock(1742643890)
	dir := t.TempDir()
	st, err := store.Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	l := events.New(st, bus, clock)

	body, _ := json.Marshal(events.TextBody{Text: "x"})
	for i := 0; i < 3; i++ {
		_, err := l.AppendOutbound(events.OutboundParams{ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderSeq: uint64(i + 1), Body: body})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Lock(); err != nil {
		t.Fatal(err)
	}

	st2, err := store.Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st2.Lock() })
	l2 := events.New(st2, events.NewBus(), clock)
	got, err := l2.AppendOutbound(events.OutboundParams{ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderSeq: 4, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if got.RecvSeq != 4 {
		t.Errorf("after reopen, recv_seq = %d, want 4 (counter must persist)", got.RecvSeq)
	}
}

func TestRecvSeq_Concurrent_NoDuplicates(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)
	body, _ := json.Marshal(events.TextBody{Text: "x"})

	const N = 30
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		seen = make(map[uint64]int, N)
	)
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			ev, err := l.AppendOutbound(events.OutboundParams{
				ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderSeq: uint64(i + 1), Body: body,
			})
			if err != nil {
				t.Errorf("append: %v", err)
				return
			}
			mu.Lock()
			seen[ev.RecvSeq]++
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	if len(seen) != N {
		t.Fatalf("got %d distinct recv_seq values, want %d", len(seen), N)
	}
	for v := uint64(1); v <= N; v++ {
		if seen[v] != 1 {
			t.Errorf("recv_seq %d appeared %d times, want 1", v, seen[v])
		}
	}
}

func TestClampDisplayTs_WithinWindow_Preserved(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)
	body, _ := json.Marshal(events.TextBody{Text: "x"})
	ev, err := l.AppendInbound(events.InboundParams{
		ChatID: chat.ChatID("bob"), Kind: events.KindText,
		SenderTs: 1742643890 - 30, Status: events.DecryptOK, Body: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ev.DisplayTs != 1742643890-30 {
		t.Errorf("display_ts = %d, want unchanged sender_ts (1742643860)", ev.DisplayTs)
	}
}

func TestClampDisplayTs_TooFarPast_PinnedToFloor(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)
	body, _ := json.Marshal(events.TextBody{Text: "x"})
	ev, err := l.AppendInbound(events.InboundParams{
		ChatID: chat.ChatID("bob"), Kind: events.KindText,
		SenderTs: 1,
		Status:   events.DecryptOK,
		Body:     body,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantFloor := int64(1742643890) - events.SkewMaxPastSec
	if ev.DisplayTs != wantFloor {
		t.Errorf("display_ts = %d, want floor %d", ev.DisplayTs, wantFloor)
	}
	if ev.SenderTs != 1 {
		t.Errorf("sender_ts = %d, want 1 (raw claim preserved)", ev.SenderTs)
	}
}

func TestClampDisplayTs_TooFarFuture_PinnedToCeiling(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)
	body, _ := json.Marshal(events.TextBody{Text: "x"})
	ev, err := l.AppendInbound(events.InboundParams{
		ChatID: chat.ChatID("bob"), Kind: events.KindText,
		SenderTs: 1742643890 + 99999,
		Status:   events.DecryptOK,
		Body:     body,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantCeil := int64(1742643890) + events.SkewMaxFutureSec
	if ev.DisplayTs != wantCeil {
		t.Errorf("display_ts = %d, want ceiling %d", ev.DisplayTs, wantCeil)
	}
}

func TestList_OrderByDisplayTs_AscendingPerChat(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)
	body, _ := json.Marshal(events.TextBody{Text: "x"})

	tsA := int64(1742643890 - 100)
	tsB := int64(1742643890 - 50)
	tsC := int64(1742643890 - 200)
	for _, ts := range []int64{tsA, tsB, tsC} {
		if _, err := l.AppendInbound(events.InboundParams{
			ChatID: chat.ChatID("bob"), Kind: events.KindText, SenderTs: ts, Status: events.DecryptOK, Body: body,
		}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := l.AppendOutbound(events.OutboundParams{ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderSeq: 1, Body: body}); err != nil {
		t.Fatal(err)
	}

	rows, err := l.List(chat.ChatID("bob"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("List(bob) = %d rows, want 3", len(rows))
	}
	want := []int64{tsC, tsA, tsB}
	for i, ev := range rows {
		if ev.DisplayTs != want[i] {
			t.Errorf("row %d display_ts = %d, want %d", i, ev.DisplayTs, want[i])
		}
	}
}

func TestList_SinceFilter_ExcludesOlder(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)
	body, _ := json.Marshal(events.TextBody{Text: "x"})

	tsA := int64(1742643890 - 100)
	tsB := int64(1742643890 - 50)
	tsC := int64(1742643890 - 25)
	for _, ts := range []int64{tsA, tsB, tsC} {
		if _, err := l.AppendInbound(events.InboundParams{
			ChatID: chat.ChatID("bob"), Kind: events.KindText, SenderTs: ts, Status: events.DecryptOK, Body: body,
		}); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := l.List(chat.ChatID("bob"), tsA, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("List(bob, since=tsA) = %d rows, want 2", len(rows))
	}
	if rows[0].DisplayTs != tsB || rows[1].DisplayTs != tsC {
		t.Errorf("List filtered wrong: %+v", rows)
	}
}

func TestBus_CancelClosesChannel(t *testing.T) {
	bus := events.NewBus()
	ch, cancel := bus.Subscribe(2)
	if bus.SubscriberCount() != 1 {
		t.Errorf("subscriber count = %d, want 1", bus.SubscriberCount())
	}
	cancel()
	if bus.SubscriberCount() != 0 {
		t.Errorf("subscriber count after cancel = %d, want 0", bus.SubscriberCount())
	}
	_, ok := <-ch
	if ok {
		t.Error("channel still open after cancel")
	}
}

func TestBus_CancelIdempotent(t *testing.T) {
	bus := events.NewBus()
	_, cancel := bus.Subscribe(2)
	cancel()
	cancel()
}

func TestBus_SlowSubscriberDropsButDoesNotBlock(t *testing.T) {
	clock := fixedClock(1742643890)
	l, bus := newLog(t, clock)
	body, _ := json.Marshal(events.TextBody{Text: "x"})

	ch, cancel := bus.Subscribe(1)
	defer cancel()

	for i := 0; i < 5; i++ {
		_, err := l.AppendOutbound(events.OutboundParams{ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderSeq: uint64(i + 1), Body: body})
		if err != nil {
			t.Fatalf("append #%d: %v", i, err)
		}
	}
	got := 0
	timeout := time.After(200 * time.Millisecond)
loop:
	for {
		select {
		case <-ch:
			got++
		case <-timeout:
			break loop
		}
	}
	if got != 1 {
		t.Errorf("subscriber received %d events, want 1 (capacity)", got)
	}
}

func TestAppend_RejectsEmptyChatID(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)
	body, _ := json.Marshal(events.TextBody{Text: "x"})
	_, err := l.AppendOutbound(events.OutboundParams{ChatID: "", Kind: events.KindText, Body: body})
	if err == nil || !strings.Contains(err.Error(), "chat id") {
		t.Errorf("err = %v, want empty-chat-id error", err)
	}
}

func TestAppend_RejectsEmptyKind(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)
	_, err := l.AppendOutbound(events.OutboundParams{ChatID: chat.ChatID("alice"), Kind: ""})
	if err == nil || !strings.Contains(err.Error(), "kind") {
		t.Errorf("err = %v, want empty-kind error", err)
	}
}

func TestListBefore_ReturnsNewestFirst(t *testing.T) {
	base := int64(1742643900)
	l, _ := newLog(t, func() time.Time { return time.Unix(base, 0) })

	body, _ := json.Marshal(events.TextBody{Text: "x"})
	for i := range 5 {
		_, err := l.AppendInbound(events.InboundParams{
			ChatID:   chat.ChatID("alice"),
			Kind:     events.KindText,
			SenderTs: base + int64(i)*10,
			Status:   events.DecryptOK,
			Body:     body,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	got, err := l.ListBefore(chat.ChatID("alice"), 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("got %d events, want 5", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].DisplayTs < got[i].DisplayTs {
			t.Errorf("not descending at index %d: %d < %d", i, got[i-1].DisplayTs, got[i].DisplayTs)
		}
	}
}

func TestListBefore_CutsAtBeforeTs(t *testing.T) {
	base := int64(1742644000)
	var clk int64
	l, _ := newLog(t, func() time.Time { return time.Unix(base+clk, 0) })

	body, _ := json.Marshal(events.TextBody{Text: "x"})
	var ts []int64
	for i := range 5 {
		clk = int64(i) * 10
		ev, err := l.AppendInbound(events.InboundParams{
			ChatID:   chat.ChatID("alice"),
			Kind:     events.KindText,
			SenderTs: base + clk,
			Status:   events.DecryptOK,
			Body:     body,
		})
		if err != nil {
			t.Fatal(err)
		}
		ts = append(ts, ev.DisplayTs)
	}

	got, err := l.ListBefore(chat.ChatID("alice"), ts[3], 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range got {
		if ev.DisplayTs >= ts[3] {
			t.Errorf("event with DisplayTs=%d >= cutoff %d was returned", ev.DisplayTs, ts[3])
		}
	}
}

func TestListBefore_EmptyChatID_Errors(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742644000))
	_, err := l.ListBefore("", 0, 10)
	if err == nil {
		t.Fatal("expected error for empty chat id")
	}
}

func TestUpdateDeliveryState_RewritesOutboundRow(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)

	body, _ := json.Marshal(events.TextBody{Text: "hello"})
	ev, err := l.AppendOutbound(events.OutboundParams{
		ChatID:     chat.ChatID("alice"),
		Kind:       events.KindText,
		SenderSeq:  1,
		EnvelopeID: "env-sent-1",
		Body:       body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ev.DeliveryState != "enqueued" {
		t.Errorf("initial DeliveryState = %q, want enqueued", ev.DeliveryState)
	}

	updated, err := l.UpdateDeliveryState("env-sent-1", "sent")
	if err != nil {
		t.Fatal(err)
	}
	if updated.DeliveryState != "sent" {
		t.Errorf("updated DeliveryState = %q, want sent", updated.DeliveryState)
	}
	if updated.RecvSeq != ev.RecvSeq {
		t.Errorf("updated RecvSeq = %d, want %d (same row)", updated.RecvSeq, ev.RecvSeq)
	}

	rows, err := l.List(chat.ChatID("alice"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows after update = %d, want 1", len(rows))
	}
	if rows[0].DeliveryState != "sent" {
		t.Errorf("persisted DeliveryState = %q, want sent", rows[0].DeliveryState)
	}
}

func TestUpdateDeliveryState_DoesNotDowngradeFromRead(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)

	body, _ := json.Marshal(events.TextBody{Text: "hello"})
	ev, err := l.AppendOutbound(events.OutboundParams{
		ChatID:     chat.ChatID("alice"),
		Kind:       events.KindText,
		SenderSeq:  1,
		EnvelopeID: "env-sent-1",
		MsgID:      "msg-1",
		Body:       body,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := l.ApplyReadReceipt("msg-1", ev.DisplayTs+5, chat.ChatID("alice")); err != nil {
		t.Fatalf("ApplyReadReceipt: %v", err)
	}

	updated, err := l.UpdateDeliveryState("env-sent-1", "sent")
	if err != nil {
		t.Fatal(err)
	}
	if updated.DeliveryState != "read" {
		t.Errorf("DeliveryState = %q after late 'sent' update, want still 'read'", updated.DeliveryState)
	}
	rows, _ := l.List(chat.ChatID("alice"), 0, 0)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].DeliveryState != "read" {
		t.Errorf("persisted DeliveryState = %q, want still 'read'", rows[0].DeliveryState)
	}
}

func TestUpdateDeliveryState_UnknownEnvID_ReturnsErrEventNotFound(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)
	_, err := l.UpdateDeliveryState("never-written", "sent")
	if err == nil {
		t.Fatal("expected ErrEventNotFound")
	}
	if !strings.Contains(err.Error(), "event not found") {
		t.Errorf("err = %v, want ErrEventNotFound", err)
	}
}

func TestDeleteByChat_RemovesOnlyTargetRows(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)

	body, _ := json.Marshal(events.TextBody{Text: "m"})

	for i := 0; i < 3; i++ {
		if _, err := l.AppendOutbound(events.OutboundParams{
			ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderSeq: uint64(i), EnvelopeID: "al-out-" + strings.Repeat("x", i+1), MsgID: "al-mid-out-" + strings.Repeat("x", i+1), Body: body,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := l.AppendInbound(events.InboundParams{
			ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderTs: 1742643880 + int64(i), EnvelopeID: "al-in-" + strings.Repeat("x", i+1), MsgID: "al-mid-in-" + strings.Repeat("x", i+1), Status: events.DecryptOK, Body: body,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if _, err := l.AppendOutbound(events.OutboundParams{
			ChatID: chat.ChatID("bob"), Kind: events.KindText, SenderSeq: uint64(i), EnvelopeID: "bob-out-" + strings.Repeat("y", i+1), MsgID: "bob-mid-out-" + strings.Repeat("y", i+1), Body: body,
		}); err != nil {
			t.Fatal(err)
		}
	}

	n, err := l.DeleteByChat(chat.ChatID("alice"))
	if err != nil {
		t.Fatalf("DeleteByChat: %v", err)
	}
	if n != 6 {
		t.Errorf("deleted count = %d, want 6 (3 outbound + 3 inbound)", n)
	}

	rows, err := l.List(chat.ChatID("alice"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("alice rows after delete = %d, want 0", len(rows))
	}

	rows, err = l.List(chat.ChatID("bob"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Errorf("bob rows after alice-delete = %d, want 2", len(rows))
	}

	if _, err := l.UpdateDeliveryState("al-out-x", "delivered"); err == nil {
		t.Errorf("UpdateDeliveryState(deleted envelope) unexpectedly succeeded")
	}
	if _, err := l.UpdateDeliveryState("bob-out-y", "delivered"); err != nil {
		t.Errorf("UpdateDeliveryState(bob envelope) err = %v, want nil", err)
	}
	for _, id := range []string{"al-mid-out-x", "al-mid-in-x"} {
		if _, err := l.GetByMsgID(id); !errors.Is(err, events.ErrEventNotFound) {
			t.Errorf("GetByMsgID(%q) err = %v, want ErrEventNotFound", id, err)
		}
	}
	if _, err := l.GetByMsgID("bob-mid-out-y"); err != nil {
		t.Errorf("GetByMsgID(bob) err = %v, want nil", err)
	}
}

func TestGetByMsgID_RoundTrip(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)

	body, _ := json.Marshal(events.TextBody{Text: "hi"})
	out, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderSeq: 1,
		EnvelopeID: "env-1", MsgID: "mid-out-1", Body: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := l.GetByMsgID("mid-out-1")
	if err != nil {
		t.Fatalf("GetByMsgID outbound: %v", err)
	}
	if got.RecvSeq != out.RecvSeq || got.Direction != events.DirOut {
		t.Errorf("GetByMsgID mismatch: %+v, want RecvSeq=%d direction=out", got, out.RecvSeq)
	}

	in, err := l.AppendInbound(events.InboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderTs: 1742643885,
		EnvelopeID: "env-2", MsgID: "mid-in-1", Status: events.DecryptOK, Body: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err = l.GetByMsgID("mid-in-1")
	if err != nil {
		t.Fatalf("GetByMsgID inbound: %v", err)
	}
	if got.RecvSeq != in.RecvSeq || got.Direction != events.DirIn {
		t.Errorf("GetByMsgID mismatch: %+v, want RecvSeq=%d direction=in", got, in.RecvSeq)
	}
}

func TestGetByMsgID_Unknown(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)
	if _, err := l.GetByMsgID("nonexistent"); !errors.Is(err, events.ErrEventNotFound) {
		t.Errorf("err = %v, want ErrEventNotFound", err)
	}
}

func TestGetByMsgID_EmptyID(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)
	body, _ := json.Marshal(events.TextBody{Text: "hi"})
	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderSeq: 1, EnvelopeID: "env", Body: body,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.GetByMsgID(""); !errors.Is(err, events.ErrEventNotFound) {
		t.Errorf("GetByMsgID(\"\") err = %v, want ErrEventNotFound", err)
	}
}

func TestDeleteByChat_EmptyChat(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)
	n, err := l.DeleteByChat(chat.ChatID("nonexistent"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("count = %d, want 0 for empty chat", n)
	}
}

func TestDeleteByChat_RejectsEmptyID(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)
	if _, err := l.DeleteByChat(""); err == nil {
		t.Error("expected error for empty chat id")
	}
}

func TestClampSenderTs_ExportMatchesInternal(t *testing.T) {

	nowSec := int64(1742643890)
	if got := events.ClampSenderTs(nowSec-999999, nowSec); got != nowSec-events.SkewMaxPastSec {
		t.Errorf("clamp past: got %d, want %d", got, nowSec-events.SkewMaxPastSec)
	}
	if got := events.ClampSenderTs(nowSec+999999, nowSec); got != nowSec+events.SkewMaxFutureSec {
		t.Errorf("clamp future: got %d, want %d", got, nowSec+events.SkewMaxFutureSec)
	}
	if got := events.ClampSenderTs(nowSec-30, nowSec); got != nowSec-30 {
		t.Errorf("in-window preserved: got %d, want %d", got, nowSec-30)
	}
}

func TestAppendLocal_WritesBreadcrumb(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)

	body, _ := json.Marshal(events.TimerChangeBody{From: 0, To: 60, ChangedBy: "peer-bob"})
	ev, err := l.AppendLocal(events.LocalParams{
		ChatID:       chat.ChatID("alice"),
		Kind:         events.KindTimerChange,
		Direction:    events.DirIn,
		DisplayTs:    1742643885,
		SenderPeerID: "peer-bob",
		Body:         body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != events.KindTimerChange {
		t.Errorf("kind = %q, want timer_change", ev.Kind)
	}
	if ev.DisplayTs != 1742643885 {
		t.Errorf("display_ts = %d, want 1742643885 (supplied)", ev.DisplayTs)
	}
	if ev.SenderPeerID != "peer-bob" {
		t.Errorf("sender_peer_id = %q, want peer-bob", ev.SenderPeerID)
	}
	if ev.DeliveryState != "" {
		t.Errorf("delivery_state = %q, want empty (local events don't go through outbox)", ev.DeliveryState)
	}
	var round events.TimerChangeBody
	if err := json.Unmarshal(ev.Body, &round); err != nil {
		t.Fatal(err)
	}
	if round.From != 0 || round.To != 60 || round.ChangedBy != "peer-bob" {
		t.Errorf("body roundtrip drift: %+v", round)
	}
}

func TestMarkRead_StampsInboundDecryptOKOnly(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)
	body, _ := json.Marshal(events.TextBody{Text: "hi"})

	for i := 0; i < 3; i++ {
		if _, err := l.AppendInbound(events.InboundParams{
			ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderTs: 1742643885,
			Status: events.DecryptOK, Body: body,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := l.AppendInbound(events.InboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderTs: 1742643885,
		Status: events.DecryptFailed, RawBlob: []byte{0x01},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderSeq: 1, Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	n, _, err := l.MarkRead(chat.ChatID("alice"))
	if err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if n != 3 {
		t.Errorf("marked = %d, want 3 (only DecryptOK inbound)", n)
	}

	n2, _, err := l.MarkRead(chat.ChatID("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Errorf("second MarkRead = %d, want 0", n2)
	}

	rows, err := l.List(chat.ChatID("alice"), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var inboundOK, outbound, failed int
	for _, r := range rows {
		switch {
		case r.Direction == events.DirIn && r.DecryptStatus == events.DecryptOK:
			inboundOK++
			if r.ReadAt != 1742643890 {
				t.Errorf("inbound-ok ReadAt = %d, want %d", r.ReadAt, 1742643890)
			}
		case r.Direction == events.DirIn:
			failed++
			if r.ReadAt != 0 {
				t.Errorf("failed-decrypt ReadAt = %d, want 0", r.ReadAt)
			}
		case r.Direction == events.DirOut:
			outbound++
			if r.ReadAt != 0 {
				t.Errorf("outbound ReadAt = %d, want 0", r.ReadAt)
			}
		}
	}
	if inboundOK != 3 || outbound != 1 || failed != 1 {
		t.Errorf("row counts: in-ok=%d, out=%d, failed=%d; want 3/1/1", inboundOK, outbound, failed)
	}
}

func TestMarkRead_RejectsEmptyChatID(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	if _, _, err := l.MarkRead(""); err == nil {
		t.Error("expected error for empty chat id")
	}
}

func TestSweepExpired_OutboundFiresFromDisplayTs(t *testing.T) {
	var now int64 = 1742643890
	clock := func() time.Time { return time.Unix(now, 0) }
	l, _ := newLog(t, clock)
	body, _ := json.Marshal(events.TextBody{Text: "bye"})

	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderSeq: 1,
		EnvelopeID: "env-1", MsgID: "m-1", ExpireSeconds: 60, Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderSeq: 2,
		EnvelopeID: "env-2", MsgID: "m-2", ExpireSeconds: 0, Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	n, err := l.SweepExpired(now + 59)
	if err != nil || n != 0 {
		t.Errorf("before-TTL sweep n=%d err=%v, want 0/nil", n, err)
	}

	n, err = l.SweepExpired(now + 60)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("at-TTL sweep n=%d, want 1", n)
	}

	rows, _ := l.List(chat.ChatID("alice"), 0, 100)
	if len(rows) != 1 {
		t.Fatalf("rows after sweep = %d, want 1", len(rows))
	}
	if rows[0].MsgID != "m-2" {
		t.Errorf("survivor MsgID = %q, want m-2", rows[0].MsgID)
	}

	if _, err := l.GetByMsgID("m-1"); !errors.Is(err, events.ErrEventNotFound) {
		t.Errorf("expected ErrEventNotFound for swept msg_id, got %v", err)
	}
	if _, err := l.UpdateDeliveryState("env-1", "delivered"); !errors.Is(err, events.ErrEventNotFound) {
		t.Errorf("envelope index still alive after sweep: %v", err)
	}
}

func TestSweepExpired_InboundFiresFromReadAt(t *testing.T) {
	var now int64 = 1742643890
	clock := func() time.Time { return time.Unix(now, 0) }
	l, _ := newLog(t, clock)
	body, _ := json.Marshal(events.TextBody{Text: "hi"})

	if _, err := l.AppendInbound(events.InboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderTs: now - 10,
		MsgID: "m-a", ExpireSeconds: 60, Status: events.DecryptOK, Body: body,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.AppendInbound(events.InboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderTs: now - 10,
		MsgID: "m-b", ExpireSeconds: 60, Status: events.DecryptOK, Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	if _, _, err := l.MarkRead(chat.ChatID("alice")); err != nil {
		t.Fatal(err)
	}

	n, err := l.SweepExpired(now + 59)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("pre-TTL sweep n=%d, want 0", n)
	}

	n, err = l.SweepExpired(now + 60)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("at-TTL sweep n=%d, want 2", n)
	}
	rows, _ := l.List(chat.ChatID("alice"), 0, 100)
	if len(rows) != 0 {
		t.Errorf("rows after sweep = %d, want 0", len(rows))
	}
}

func TestSweepExpired_UnreadInboundSurvives(t *testing.T) {
	var now int64 = 1742643890
	clock := func() time.Time { return time.Unix(now, 0) }
	l, _ := newLog(t, clock)
	body, _ := json.Marshal(events.TextBody{Text: "hi"})

	if _, err := l.AppendInbound(events.InboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderTs: now - 100,
		MsgID: "m-unread", ExpireSeconds: 10, Status: events.DecryptOK, Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	n, err := l.SweepExpired(now + 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("unread sweep n=%d, want 0 (timer starts on read)", n)
	}
}

func TestSweepExpired_PublishesDeletionOnBus(t *testing.T) {
	var now int64 = 1742643890
	clock := func() time.Time { return time.Unix(now, 0) }
	l, bus := newLog(t, clock)
	body, _ := json.Marshal(events.TextBody{Text: "bye"})

	ch, cancel := bus.SubscribeDeletions(4)
	defer cancel()

	ev, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderSeq: 1,
		EnvelopeID: "env-gone", MsgID: "m-gone", ExpireSeconds: 1, Body: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.SweepExpired(now + 10); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-ch:
		if d.ChatID != "alice" {
			t.Errorf("deletion ChatID = %q, want alice", d.ChatID)
		}
		if d.RecvSeq != ev.RecvSeq {
			t.Errorf("deletion RecvSeq = %d, want %d", d.RecvSeq, ev.RecvSeq)
		}
		if d.MsgID != "m-gone" {
			t.Errorf("deletion MsgID = %q, want %q", d.MsgID, "m-gone")
		}
	case <-time.After(time.Second):
		t.Fatal("no deletion published on bus")
	}
}

func TestApplyDelete_AllowsKindFile(t *testing.T) {
	clock := fixedClock(1742643890)
	l, bus := newLog(t, clock)
	body, _ := json.Marshal(map[string]string{"name": "blob.bin"})

	ev, err := l.AppendOutbound(events.OutboundParams{
		ChatID:     chat.ChatID("alice"),
		Kind:       events.KindFile,
		SenderSeq:  1,
		EnvelopeID: "env-file",
		MsgID:      "m-file",
		Body:       body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != events.KindFile {
		t.Fatalf("kind = %q, want file", ev.Kind)
	}

	subCh, cancel := bus.Subscribe(4)
	defer cancel()

	updated, err := l.ApplyDelete("m-file", 1742643999, "")
	if err != nil {
		t.Fatalf("ApplyDelete on KindFile: %v", err)
	}
	if updated.DeletedAt != 1742643999 {
		t.Errorf("DeletedAt = %d, want 1742643999", updated.DeletedAt)
	}
	if len(updated.Body) != 0 {
		t.Errorf("Body should be cleared on tombstone, got %s", updated.Body)
	}

	select {
	case got := <-subCh:
		if got.MsgID != "m-file" || got.DeletedAt == 0 || got.Kind != events.KindFile {
			t.Errorf("bus push drift: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("ApplyDelete on KindFile did not publish to bus")
	}
}

func TestApplyDelete_StillRejectsBreadcrumb(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)
	body, _ := json.Marshal(events.TimerChangeBody{From: 0, To: 60})
	if _, err := l.AppendLocal(events.LocalParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindTimerChange,
		Direction: events.DirOut, Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	body2, _ := json.Marshal(map[string]string{"target": "x"})
	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindReaction, SenderSeq: 1,
		EnvelopeID: "env-r", MsgID: "m-r", Body: body2,
	}); err != nil {
		t.Fatal(err)
	}
	_, err := l.ApplyDelete("m-r", 1742643999, "")
	if !errors.Is(err, events.ErrDeleteUnsupportedKind) {
		t.Fatalf("err = %v, want ErrDeleteUnsupportedKind", err)
	}
}

func TestBus_SubscribeDeletions_Lifecycle(t *testing.T) {
	bus := events.NewBus()
	ch, cancel := bus.SubscribeDeletions(2)
	if bus.DeletionSubscriberCount() != 1 {
		t.Errorf("deletion subscribers = %d, want 1", bus.DeletionSubscriberCount())
	}
	cancel()
	if bus.DeletionSubscriberCount() != 0 {
		t.Errorf("deletion subscribers after cancel = %d, want 0", bus.DeletionSubscriberCount())
	}
	if _, ok := <-ch; ok {
		t.Error("deletion channel still open after cancel")
	}
	cancel()
}

func TestUpdateDeliveryState_InboundNotIndexed(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)
	body, _ := json.Marshal(events.TextBody{Text: "in"})
	_, err := l.AppendInbound(events.InboundParams{
		ChatID:     chat.ChatID("bob"),
		Kind:       events.KindText,
		SenderTs:   1742643885,
		EnvelopeID: "env-in-1",
		Status:     events.DecryptOK,
		Body:       body,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = l.UpdateDeliveryState("env-in-1", "delivered")
	if err == nil {
		t.Fatal("expected ErrEventNotFound for inbound event")
	}
}

func TestApplyEdit_OutboundHappyPath(t *testing.T) {
	clock := fixedClock(1742643890)
	l, bus := newLog(t, clock)

	body, _ := json.Marshal(events.TextBody{Text: "orignal"})
	orig, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderSeq: 1,
		EnvelopeID: "env-1", MsgID: "mid-1", Body: body,
	})
	if err != nil {
		t.Fatal(err)
	}

	ch, cancel := bus.Subscribe(4)
	defer cancel()

	updated, err := l.ApplyEdit("mid-1", "corrected", 1742643999, "")
	if err != nil {
		t.Fatalf("ApplyEdit: %v", err)
	}
	if updated.EditedAt != 1742643999 {
		t.Errorf("EditedAt = %d, want 1742643999", updated.EditedAt)
	}

	if updated.RecvSeq != orig.RecvSeq || updated.DisplayTs != orig.DisplayTs {
		t.Errorf("key drift after edit: recv=%d/%d display=%d/%d", updated.RecvSeq, orig.RecvSeq, updated.DisplayTs, orig.DisplayTs)
	}
	var tb events.TextBody
	if err := json.Unmarshal(updated.Body, &tb); err != nil {
		t.Fatal(err)
	}
	if tb.Text != "corrected" {
		t.Errorf("text = %q, want 'corrected'", tb.Text)
	}

	got, err := l.GetByMsgID("mid-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.EditedAt != 1742643999 {
		t.Errorf("persisted EditedAt = %d, want 1742643999", got.EditedAt)
	}
	_ = json.Unmarshal(got.Body, &tb)
	if tb.Text != "corrected" {
		t.Errorf("persisted text = %q, want 'corrected'", tb.Text)
	}

	select {
	case ev := <-ch:
		if ev.EditedAt != 1742643999 {
			t.Errorf("bus event EditedAt = %d, want 1742643999", ev.EditedAt)
		}
	case <-time.After(time.Second):
		t.Fatal("no edit event on bus")
	}
}

func TestApplyEdit_InboundDirectChat(t *testing.T) {

	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)

	body, _ := json.Marshal(events.TextBody{Text: "hey"})
	if _, err := l.AppendInbound(events.InboundParams{
		ChatID: chat.ChatID("bob"), Kind: events.KindText, SenderTs: 1742643885,
		EnvelopeID: "env-b", MsgID: "mid-b", Status: events.DecryptOK, Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	updated, err := l.ApplyEdit("mid-b", "hey there", 1742643999, "peer-bob")
	if err != nil {
		t.Fatalf("ApplyEdit: %v", err)
	}
	var tb events.TextBody
	_ = json.Unmarshal(updated.Body, &tb)
	if tb.Text != "hey there" {
		t.Errorf("text = %q, want 'hey there'", tb.Text)
	}
}

func TestApplyEdit_InboundGroupMatchesSender(t *testing.T) {

	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)

	body, _ := json.Marshal(events.TextBody{Text: "hi group"})
	if _, err := l.AppendInbound(events.InboundParams{
		ChatID: chat.ChatID("group-xyz"), Kind: events.KindText, SenderTs: 1742643885,
		EnvelopeID: "env-g", MsgID: "mid-g", SenderPeerID: "peer-alice",
		Status: events.DecryptOK, Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := l.ApplyEdit("mid-g", "hi again", 1742643999, "peer-alice"); err != nil {
		t.Fatalf("ApplyEdit with matching sender: %v", err)
	}
}

func TestApplyEdit_InboundGroupRejectsOtherSender(t *testing.T) {

	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)

	body, _ := json.Marshal(events.TextBody{Text: "alice posted this"})
	if _, err := l.AppendInbound(events.InboundParams{
		ChatID: chat.ChatID("group-xyz"), Kind: events.KindText, SenderTs: 1742643885,
		EnvelopeID: "env-g", MsgID: "mid-g", SenderPeerID: "peer-alice",
		Status: events.DecryptOK, Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	_, err := l.ApplyEdit("mid-g", "mallory's rewrite", 1742643999, "peer-mallory")
	if !errors.Is(err, events.ErrEditorNotAuthor) {
		t.Fatalf("err = %v, want ErrEditorNotAuthor", err)
	}
}

func TestApplyEdit_RejectsEditingOwnOutboundFromRemote(t *testing.T) {

	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)

	body, _ := json.Marshal(events.TextBody{Text: "mine"})
	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderSeq: 1,
		EnvelopeID: "env-o", MsgID: "mid-o", Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	_, err := l.ApplyEdit("mid-o", "forged", 1742643999, "peer-alice")
	if !errors.Is(err, events.ErrEditorNotAuthor) {
		t.Fatalf("err = %v, want ErrEditorNotAuthor", err)
	}
}

func TestApplyEdit_RejectsEditingRemoteInboundFromSelf(t *testing.T) {

	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)

	body, _ := json.Marshal(events.TextBody{Text: "theirs"})
	if _, err := l.AppendInbound(events.InboundParams{
		ChatID: chat.ChatID("bob"), Kind: events.KindText, SenderTs: 1742643885,
		EnvelopeID: "env-r", MsgID: "mid-r", Status: events.DecryptOK, Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	_, err := l.ApplyEdit("mid-r", "rewritten locally", 1742643999, "")
	if !errors.Is(err, events.ErrEditorNotAuthor) {
		t.Fatalf("err = %v, want ErrEditorNotAuthor", err)
	}
}

func TestApplyEdit_UnknownTarget(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)
	_, err := l.ApplyEdit("mid-missing", "x", 1, "")
	if !errors.Is(err, events.ErrEventNotFound) {
		t.Fatalf("err = %v, want ErrEventNotFound", err)
	}
}

func TestApplyEdit_EmptyTargetTreatedAsMissing(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)
	_, err := l.ApplyEdit("", "x", 1, "")
	if !errors.Is(err, events.ErrEventNotFound) {
		t.Fatalf("err = %v, want ErrEventNotFound", err)
	}
}

func TestApplyEdit_EmptyTextRejected(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)
	body, _ := json.Marshal(events.TextBody{Text: "hi"})
	_, _ = l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderSeq: 1,
		EnvelopeID: "env", MsgID: "mid-e", Body: body,
	})
	_, err := l.ApplyEdit("mid-e", "", 1, "")
	if err == nil {
		t.Fatal("expected error on empty edit text")
	}
}

func TestApplyEdit_RejectsNonTextKind(t *testing.T) {

	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)

	body, _ := json.Marshal(events.TimerChangeBody{From: 0, To: 60})
	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindTimerChange,
		SenderSeq: 1, EnvelopeID: "env-tc", MsgID: "mid-tc", Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	_, err := l.ApplyEdit("mid-tc", "rewrite", 1742643999, "")
	if !errors.Is(err, events.ErrEditUnsupportedKind) {
		t.Fatalf("err = %v, want ErrEditUnsupportedKind", err)
	}
}

func TestApplyDelete_OutboundHappyPath(t *testing.T) {
	clock := fixedClock(1742643890)
	l, bus := newLog(t, clock)

	body, _ := json.Marshal(events.TextBody{Text: "sorry wrong window"})
	orig, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderSeq: 1,
		EnvelopeID: "env-1", MsgID: "mid-1", Body: body,
	})
	if err != nil {
		t.Fatal(err)
	}

	ch, cancel := bus.Subscribe(4)
	defer cancel()

	updated, err := l.ApplyDelete("mid-1", 1742643999, "")
	if err != nil {
		t.Fatalf("ApplyDelete: %v", err)
	}
	if updated.DeletedAt != 1742643999 {
		t.Errorf("DeletedAt = %d, want 1742643999", updated.DeletedAt)
	}
	if updated.Body != nil {
		t.Errorf("Body = %q, want nil", string(updated.Body))
	}
	if updated.RecvSeq != orig.RecvSeq || updated.DisplayTs != orig.DisplayTs {
		t.Errorf("key drift after delete: recv=%d/%d display=%d/%d", updated.RecvSeq, orig.RecvSeq, updated.DisplayTs, orig.DisplayTs)
	}

	got, err := l.GetByMsgID("mid-1")
	if err != nil {
		t.Fatalf("GetByMsgID after delete: %v", err)
	}
	if got.DeletedAt != 1742643999 {
		t.Errorf("persisted DeletedAt = %d, want 1742643999", got.DeletedAt)
	}
	if got.Body != nil {
		t.Errorf("persisted Body = %q, want nil", string(got.Body))
	}
	select {
	case ev := <-ch:
		if ev.DeletedAt != 1742643999 {
			t.Errorf("bus event DeletedAt = %d, want 1742643999", ev.DeletedAt)
		}
	case <-time.After(time.Second):
		t.Fatal("no delete event on bus")
	}
}

func TestApplyDelete_InboundDirectChat(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)

	body, _ := json.Marshal(events.TextBody{Text: "inappropriate"})
	if _, err := l.AppendInbound(events.InboundParams{
		ChatID: chat.ChatID("bob"), Kind: events.KindText, SenderTs: 1742643885,
		EnvelopeID: "env-b", MsgID: "mid-b", Status: events.DecryptOK, Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	updated, err := l.ApplyDelete("mid-b", 1742643999, "peer-bob")
	if err != nil {
		t.Fatalf("ApplyDelete: %v", err)
	}
	if updated.Body != nil {
		t.Errorf("Body = %q, want nil", string(updated.Body))
	}
}

func TestApplyDelete_InboundGroupRejectsOtherSender(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)

	body, _ := json.Marshal(events.TextBody{Text: "alice posted this"})
	if _, err := l.AppendInbound(events.InboundParams{
		ChatID: chat.ChatID("group-xyz"), Kind: events.KindText, SenderTs: 1742643885,
		EnvelopeID: "env-g", MsgID: "mid-g", SenderPeerID: "peer-alice",
		Status: events.DecryptOK, Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	_, err := l.ApplyDelete("mid-g", 1742643999, "peer-mallory")
	if !errors.Is(err, events.ErrDeleterNotAuthor) {
		t.Fatalf("err = %v, want ErrDeleterNotAuthor", err)
	}
}

func TestApplyDelete_RejectsDeletingOwnOutboundFromRemote(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)

	body, _ := json.Marshal(events.TextBody{Text: "mine"})
	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderSeq: 1,
		EnvelopeID: "env-o", MsgID: "mid-o", Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	_, err := l.ApplyDelete("mid-o", 1742643999, "peer-alice")
	if !errors.Is(err, events.ErrDeleterNotAuthor) {
		t.Fatalf("err = %v, want ErrDeleterNotAuthor", err)
	}
}

func TestApplyDelete_RejectsDeletingRemoteInboundFromSelf(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)

	body, _ := json.Marshal(events.TextBody{Text: "theirs"})
	if _, err := l.AppendInbound(events.InboundParams{
		ChatID: chat.ChatID("bob"), Kind: events.KindText, SenderTs: 1742643885,
		EnvelopeID: "env-r", MsgID: "mid-r", Status: events.DecryptOK, Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	_, err := l.ApplyDelete("mid-r", 1742643999, "")
	if !errors.Is(err, events.ErrDeleterNotAuthor) {
		t.Fatalf("err = %v, want ErrDeleterNotAuthor", err)
	}
}

func TestApplyDelete_UnknownTarget(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)
	_, err := l.ApplyDelete("mid-missing", 1, "")
	if !errors.Is(err, events.ErrEventNotFound) {
		t.Fatalf("err = %v, want ErrEventNotFound", err)
	}
}

func TestApplyDelete_EmptyTargetTreatedAsMissing(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)
	_, err := l.ApplyDelete("", 1, "")
	if !errors.Is(err, events.ErrEventNotFound) {
		t.Fatalf("err = %v, want ErrEventNotFound", err)
	}
}

func TestApplyDelete_RejectsNonTextKind(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)

	body, _ := json.Marshal(events.TimerChangeBody{From: 0, To: 60})
	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindTimerChange,
		SenderSeq: 1, EnvelopeID: "env-tc", MsgID: "mid-tc", Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	_, err := l.ApplyDelete("mid-tc", 1742643999, "")
	if !errors.Is(err, events.ErrDeleteUnsupportedKind) {
		t.Fatalf("err = %v, want ErrDeleteUnsupportedKind", err)
	}
}

func TestApplyDelete_IdempotentReapply(t *testing.T) {

	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)

	body, _ := json.Marshal(events.TextBody{Text: "x"})
	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderSeq: 1,
		EnvelopeID: "env-1", MsgID: "mid-1", Body: body,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.ApplyDelete("mid-1", 100, ""); err != nil {
		t.Fatal(err)
	}
	updated, err := l.ApplyDelete("mid-1", 200, "")
	if err != nil {
		t.Fatalf("second ApplyDelete: %v", err)
	}
	if updated.DeletedAt != 200 {
		t.Errorf("DeletedAt = %d, want 200", updated.DeletedAt)
	}
}

func TestAppendReactionBreadcrumb_OutboundHappyPath(t *testing.T) {
	clock := fixedClock(1742643890)
	l, bus := newLog(t, clock)

	body, _ := json.Marshal(events.TextBody{Text: "target message"})
	target, err := l.AppendInbound(events.InboundParams{
		ChatID: chat.ChatID("bob"), Kind: events.KindText, SenderTs: 1742643885,
		EnvelopeID: "env-t", MsgID: "mid-t", Status: events.DecryptOK, Body: body,
	})
	if err != nil {
		t.Fatal(err)
	}

	ch, cancel := bus.Subscribe(4)
	defer cancel()

	ev, err := l.AppendReactionBreadcrumb("mid-t", "👍", "", 1742643920)
	if err != nil {
		t.Fatalf("AppendReactionBreadcrumb: %v", err)
	}
	if ev.Kind != events.KindReaction {
		t.Errorf("Kind = %q, want reaction", ev.Kind)
	}
	if ev.Direction != events.DirOut {
		t.Errorf("Direction = %q, want out (reactor empty = us)", ev.Direction)
	}
	if ev.SenderPeerID != "" {
		t.Errorf("SenderPeerID = %q, want empty", ev.SenderPeerID)
	}

	if ev.DisplayTs != target.DisplayTs {
		t.Errorf("DisplayTs = %d, want target.DisplayTs %d (sort anchor)", ev.DisplayTs, target.DisplayTs)
	}
	if ev.ChatID != chat.ChatID("bob") {
		t.Errorf("ChatID = %q, want bob (resolved from target)", ev.ChatID)
	}
	var rb events.ReactionBody
	if err := json.Unmarshal(ev.Body, &rb); err != nil {
		t.Fatal(err)
	}
	if rb.TargetMsgID != "mid-t" || rb.Emoji != "👍" {
		t.Errorf("body drift: %+v", rb)
	}

	if rb.At != 1742643920 {
		t.Errorf("Body.At = %d, want 1742643920 (supplied at)", rb.At)
	}
	select {
	case got := <-ch:
		if got.Kind != events.KindReaction {
			t.Errorf("bus event kind = %q, want reaction", got.Kind)
		}
	case <-time.After(time.Second):
		t.Fatal("no reaction event on bus")
	}
}

func TestAppendReactionBreadcrumb_SortsInlineWithTarget(t *testing.T) {

	now := int64(1742643890)
	clock := func() time.Time { return time.Unix(now, 0) }
	l, _ := newLog(t, clock)

	tBody, _ := json.Marshal(events.TextBody{Text: "old message"})
	target, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("bob"), Kind: events.KindText, SenderSeq: 1,
		EnvelopeID: "env-old", MsgID: "mid-old", Body: tBody,
	})
	if err != nil {
		t.Fatal(err)
	}

	now += 300
	for i := 0; i < 2; i++ {
		nBody, _ := json.Marshal(events.TextBody{Text: "newer"})
		if _, err := l.AppendOutbound(events.OutboundParams{
			ChatID: chat.ChatID("bob"), Kind: events.KindText, SenderSeq: uint64(2 + i),
			EnvelopeID: fmt.Sprintf("env-n%d", i), MsgID: fmt.Sprintf("mid-n%d", i), Body: nBody,
		}); err != nil {
			t.Fatal(err)
		}
	}

	now += 300
	if _, err := l.AppendReactionBreadcrumb("mid-old", "👍", "", now); err != nil {
		t.Fatalf("AppendReactionBreadcrumb: %v", err)
	}

	rows, err := l.List(chat.ChatID("bob"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("got %d rows, want 4 (target + 2 newer + reaction)", len(rows))
	}
	if rows[0].MsgID != target.MsgID {
		t.Errorf("rows[0] MsgID = %q, want target %q", rows[0].MsgID, target.MsgID)
	}
	if rows[1].Kind != events.KindReaction {
		t.Errorf("rows[1] Kind = %q, want reaction (must sort right after target)", rows[1].Kind)
	}

	if rows[2].Kind != events.KindText || rows[3].Kind != events.KindText {
		t.Errorf("rows[2..3] kinds = %q,%q, want text+text", rows[2].Kind, rows[3].Kind)
	}
}

func TestAppendReactionBreadcrumb_InboundCarriesReactor(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)

	body, _ := json.Marshal(events.TextBody{Text: "mine"})
	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("bob"), Kind: events.KindText, SenderSeq: 1,
		EnvelopeID: "env-m", MsgID: "mid-m", Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	ev, err := l.AppendReactionBreadcrumb("mid-m", "❤️", "peer-bob", 1742643925)
	if err != nil {
		t.Fatalf("AppendReactionBreadcrumb: %v", err)
	}
	if ev.Direction != events.DirIn {
		t.Errorf("Direction = %q, want in", ev.Direction)
	}
	if ev.SenderPeerID != "peer-bob" {
		t.Errorf("SenderPeerID = %q, want peer-bob", ev.SenderPeerID)
	}
}

func TestAppendReactionBreadcrumb_EmptyEmojiWritesRemovalAudit(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)

	body, _ := json.Marshal(events.TextBody{Text: "x"})
	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("bob"), Kind: events.KindText, SenderSeq: 1,
		EnvelopeID: "env-x", MsgID: "mid-x", Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	ev, err := l.AppendReactionBreadcrumb("mid-x", "", "peer-bob", 1742643930)
	if err != nil {
		t.Fatalf("AppendReactionBreadcrumb empty emoji: %v", err)
	}
	var rb events.ReactionBody
	_ = json.Unmarshal(ev.Body, &rb)
	if rb.Emoji != "" {
		t.Errorf("Emoji = %q, want empty (removal signal)", rb.Emoji)
	}
}

func TestAppendReactionBreadcrumb_UnknownTarget(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)
	_, err := l.AppendReactionBreadcrumb("mid-missing", "👍", "", 1)
	if !errors.Is(err, events.ErrEventNotFound) {
		t.Fatalf("err = %v, want ErrEventNotFound", err)
	}
}

func TestAppendReactionBreadcrumb_EmptyTargetTreatedAsMissing(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)
	_, err := l.AppendReactionBreadcrumb("", "👍", "", 1)
	if !errors.Is(err, events.ErrEventNotFound) {
		t.Fatalf("err = %v, want ErrEventNotFound", err)
	}
}

func TestAppendReactionBreadcrumb_RejectsTombstonedTarget(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)

	body, _ := json.Marshal(events.TextBody{Text: "x"})
	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("bob"), Kind: events.KindText, SenderSeq: 1,
		EnvelopeID: "env-t", MsgID: "mid-t", Body: body,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.ApplyDelete("mid-t", 1742643900, ""); err != nil {
		t.Fatal(err)
	}
	_, err := l.AppendReactionBreadcrumb("mid-t", "👍", "", 1742643950)
	if !errors.Is(err, events.ErrReactionUnsupportedKind) {
		t.Fatalf("err = %v, want ErrReactionUnsupportedKind (tombstoned)", err)
	}
}

func TestAppendReactionBreadcrumb_RejectsBreadcrumbKind(t *testing.T) {

	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)

	body, _ := json.Marshal(events.TimerChangeBody{From: 0, To: 60})
	bc, err := l.AppendLocal(events.LocalParams{
		ChatID: chat.ChatID("bob"), Kind: events.KindTimerChange,
		Direction: events.DirOut, DisplayTs: 1742643890, Body: body,
	})
	if err != nil {
		t.Fatal(err)
	}

	tcBody, _ := json.Marshal(events.TimerChangeBody{From: 0, To: 60})
	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("bob"), Kind: events.KindTimerChange,
		SenderSeq: 1, EnvelopeID: "env-tc", MsgID: "mid-tc", Body: tcBody,
	}); err != nil {
		t.Fatal(err)
	}
	_, err = l.AppendReactionBreadcrumb("mid-tc", "👍", "", 1742643900)
	if !errors.Is(err, events.ErrReactionUnsupportedKind) {
		t.Fatalf("err = %v, want ErrReactionUnsupportedKind (breadcrumb kind)", err)
	}
	_ = bc
}

func TestAppendReactionBreadcrumb_RejectsFailedDecrypt(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)

	if _, err := l.AppendInbound(events.InboundParams{
		ChatID: chat.ChatID("bob"), Kind: events.KindText, SenderTs: 1742643885,
		EnvelopeID: "env-f", MsgID: "mid-f", Status: events.DecryptFailed,
		RawBlob: []byte("opaque"),
	}); err != nil {
		t.Fatal(err)
	}
	_, err := l.AppendReactionBreadcrumb("mid-f", "👍", "peer-bob", 1742643920)
	if !errors.Is(err, events.ErrReactionUnsupportedKind) {
		t.Fatalf("err = %v, want ErrReactionUnsupportedKind (failed decrypt)", err)
	}
}

func TestMarkRead_PendingReceiptCovers_KindTextOnly(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)
	body, _ := json.Marshal(events.TextBody{Text: "hi"})

	for i, mid := range []string{"mid-a", "mid-b"} {
		if _, err := l.AppendInbound(events.InboundParams{
			ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderTs: 1742643885,
			SenderSeq: uint64(i + 1), MsgID: mid, Status: events.DecryptOK, Body: body,
		}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := l.AppendInbound(events.InboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderTs: 1742643885,
		MsgID: "mid-fail", Status: events.DecryptFailed, RawBlob: []byte{0x01},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderSeq: 1,
		EnvelopeID: "env-out", MsgID: "mid-out", Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	stamped, pending, err := l.MarkRead(chat.ChatID("alice"))
	if err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if stamped != 2 {
		t.Errorf("stamped = %d, want 2 (two DecryptOK DirIn)", stamped)
	}
	if len(pending) != 2 {
		t.Fatalf("pending = %v, want 2 entries (only KindText DirIn DecryptOK)", pending)
	}
	got := map[string]bool{pending[0]: true, pending[1]: true}
	if !got["mid-a"] || !got["mid-b"] {
		t.Errorf("pending = %v, want {mid-a, mid-b}", pending)
	}
}

func TestMarkRead_PendingSurvivesAcrossCallsUntilSent(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)
	body, _ := json.Marshal(events.TextBody{Text: "hi"})

	if _, err := l.AppendInbound(events.InboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderTs: 1742643885,
		MsgID: "mid-a", Status: events.DecryptOK, Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	stamped, pending, err := l.MarkRead(chat.ChatID("alice"))
	if err != nil || stamped != 1 || len(pending) != 1 || pending[0] != "mid-a" {
		t.Fatalf("first MarkRead: stamped=%d pending=%v err=%v", stamped, pending, err)
	}

	stamped2, pending2, err := l.MarkRead(chat.ChatID("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if stamped2 != 0 {
		t.Errorf("second stamped = %d, want 0", stamped2)
	}
	if len(pending2) != 1 || pending2[0] != "mid-a" {
		t.Errorf("second pending = %v, want [mid-a] (retry path)", pending2)
	}

	if err := l.MarkReadReceiptSent([]string{"mid-a"}, 1742643999); err != nil {
		t.Fatalf("MarkReadReceiptSent: %v", err)
	}
	_, pending3, err := l.MarkRead(chat.ChatID("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if len(pending3) != 0 {
		t.Errorf("pending after MarkReadReceiptSent = %v, want empty", pending3)
	}
	got, err := l.GetByMsgID("mid-a")
	if err != nil {
		t.Fatal(err)
	}
	if got.ReadReceiptSentAt != 1742643999 {
		t.Errorf("ReadReceiptSentAt = %d, want 1742643999", got.ReadReceiptSentAt)
	}
}

func TestApplyReadReceipt_OutboundHappyPath(t *testing.T) {
	clock := fixedClock(1742643890)
	l, bus := newLog(t, clock)

	ch, cancel := bus.Subscribe(4)
	defer cancel()

	body, _ := json.Marshal(events.TextBody{Text: "hello"})
	orig, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderSeq: 1,
		EnvelopeID: "env-1", MsgID: "mid-1", Body: body,
	})
	if err != nil {
		t.Fatal(err)
	}

	<-ch

	updated, err := l.ApplyReadReceipt("mid-1", 1742643950, chat.ChatID("alice"))
	if err != nil {
		t.Fatalf("ApplyReadReceipt: %v", err)
	}
	if updated.ReadAt != 1742643950 {
		t.Errorf("ReadAt = %d, want 1742643950", updated.ReadAt)
	}
	if updated.DeliveryState != "read" {
		t.Errorf("DeliveryState = %q, want \"read\"", updated.DeliveryState)
	}
	if updated.RecvSeq != orig.RecvSeq {
		t.Errorf("RecvSeq drift: %d != %d", updated.RecvSeq, orig.RecvSeq)
	}
	select {
	case ev := <-ch:
		if ev.DeliveryState != "read" || ev.ReadAt != 1742643950 {
			t.Errorf("bus event drift: state=%q read_at=%d", ev.DeliveryState, ev.ReadAt)
		}
	case <-time.After(time.Second):
		t.Fatal("ApplyReadReceipt did not publish on bus")
	}
}

func TestApplyReadReceipt_RejectsWrongChat(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)

	body, _ := json.Marshal(events.TextBody{Text: "for alice"})
	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderSeq: 1,
		EnvelopeID: "env-1", MsgID: "mid-1", Body: body,
	}); err != nil {
		t.Fatal(err)
	}
	_, err := l.ApplyReadReceipt("mid-1", 1742643950, chat.ChatID("bob"))
	if !errors.Is(err, events.ErrReaderNotPeer) {
		t.Fatalf("err = %v, want ErrReaderNotPeer", err)
	}
}

func TestApplyReadReceipt_RejectsInbound(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)

	body, _ := json.Marshal(events.TextBody{Text: "from bob"})
	if _, err := l.AppendInbound(events.InboundParams{
		ChatID: chat.ChatID("bob"), Kind: events.KindText, SenderTs: 1742643885,
		MsgID: "mid-i", Status: events.DecryptOK, Body: body,
	}); err != nil {
		t.Fatal(err)
	}
	_, err := l.ApplyReadReceipt("mid-i", 1742643950, chat.ChatID("bob"))
	if !errors.Is(err, events.ErrReaderNotPeer) {
		t.Fatalf("err = %v, want ErrReaderNotPeer (inbound target)", err)
	}
}

func TestApplyReadReceipt_FirstReadWins(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)

	body, _ := json.Marshal(events.TextBody{Text: "hi"})
	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderSeq: 1,
		EnvelopeID: "env-1", MsgID: "mid-1", Body: body,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.ApplyReadReceipt("mid-1", 1742643950, chat.ChatID("alice")); err != nil {
		t.Fatal(err)
	}
	updated, err := l.ApplyReadReceipt("mid-1", 1742643999, chat.ChatID("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if updated.ReadAt != 1742643950 {
		t.Errorf("ReadAt = %d, want 1742643950 (first wins)", updated.ReadAt)
	}
}

func TestApplyReadReceipt_UnknownTarget(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)
	_, err := l.ApplyReadReceipt("mid-ghost", 1742643950, chat.ChatID("alice"))
	if !errors.Is(err, events.ErrEventNotFound) {
		t.Fatalf("err = %v, want ErrEventNotFound", err)
	}
}

func TestSuppressPendingReadReceipts_StampsSentinelOnPendingOnly(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)
	body, _ := json.Marshal(events.TextBody{Text: "hi"})

	for _, mid := range []string{"mid-a", "mid-b"} {
		if _, err := l.AppendInbound(events.InboundParams{
			ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderTs: 1742643885,
			MsgID: mid, Status: events.DecryptOK, Body: body,
		}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := l.AppendInbound(events.InboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderTs: 1742643885,
		MsgID: "mid-shipped", Status: events.DecryptOK, Body: body,
	}); err != nil {
		t.Fatal(err)
	}
	if err := l.MarkReadReceiptSent([]string{"mid-shipped"}, 1742643999); err != nil {
		t.Fatal(err)
	}

	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderSeq: 1,
		EnvelopeID: "env-out", MsgID: "mid-out", Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := l.AppendInbound(events.InboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderTs: 1742643885,
		MsgID: "mid-fail", Status: events.DecryptFailed, RawBlob: []byte{0x01},
	}); err != nil {
		t.Fatal(err)
	}

	n, err := l.SuppressPendingReadReceipts(chat.ChatID("alice"))
	if err != nil {
		t.Fatalf("SuppressPendingReadReceipts: %v", err)
	}
	if n != 2 {
		t.Errorf("suppressed = %d, want 2 (mid-a + mid-b)", n)
	}

	checks := map[string]int64{
		"mid-a":       events.ReadReceiptSuppressedSentinel,
		"mid-b":       events.ReadReceiptSuppressedSentinel,
		"mid-shipped": 1742643999,
		"mid-out":     0,
		"mid-fail":    0,
	}
	for mid, want := range checks {
		got, err := l.GetByMsgID(mid)
		if err != nil {
			t.Errorf("GetByMsgID(%s): %v", mid, err)
			continue
		}
		if got.ReadReceiptSentAt != want {
			t.Errorf("%s ReadReceiptSentAt = %d, want %d", mid, got.ReadReceiptSentAt, want)
		}
	}

	_, pending, err := l.MarkRead(chat.ChatID("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Errorf("pending after suppress = %v, want empty", pending)
	}
}

func TestSuppressPendingReadReceipts_RejectsEmptyChatID(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	if _, err := l.SuppressPendingReadReceipts(""); err == nil {
		t.Error("expected error for empty chat id")
	}
}

func TestReadReceiptSuppressedSentinel_Value(t *testing.T) {

	if events.ReadReceiptSuppressedSentinel != 69 {
		t.Errorf("sentinel = %d, want 69", events.ReadReceiptSuppressedSentinel)
	}
}

func TestMarkRead_SentinelStampedRowsStayOutOfPending(t *testing.T) {
	clock := fixedClock(1742643890)
	l, _ := newLog(t, clock)
	body, _ := json.Marshal(events.TextBody{Text: "during disabled period"})

	if _, err := l.AppendInbound(events.InboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderTs: 1742643885,
		MsgID: "mid-suppressed", Status: events.DecryptOK, Body: body,
	}); err != nil {
		t.Fatal(err)
	}
	if err := l.MarkReadReceiptSent([]string{"mid-suppressed"}, events.ReadReceiptSuppressedSentinel); err != nil {
		t.Fatal(err)
	}

	stamped, pending, err := l.MarkRead(chat.ChatID("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if stamped != 1 {
		t.Errorf("stamped = %d, want 1 (ReadAt stamp must run independently of receipt toggle)", stamped)
	}
	if len(pending) != 0 {
		t.Errorf("pending = %v, want empty (sentinel must keep row out of receipt batch)", pending)
	}
	got, err := l.GetByMsgID("mid-suppressed")
	if err != nil {
		t.Fatal(err)
	}
	if got.ReadAt != 1742643890 {
		t.Errorf("ReadAt = %d, want clock value 1742643890", got.ReadAt)
	}
	if got.ReadReceiptSentAt != events.ReadReceiptSuppressedSentinel {
		t.Errorf("ReadReceiptSentAt = %d, want sentinel %d", got.ReadReceiptSentAt, events.ReadReceiptSuppressedSentinel)
	}
}

func TestEventDeletable_HappyPaths(t *testing.T) {
	now := int64(1_700_000_000)
	for _, k := range []events.Kind{events.KindText, events.KindFile} {
		e := events.Event{
			Kind:      k,
			Direction: events.DirOut,
			DisplayTs: now - 60,
		}
		if !e.Deletable(now) {
			t.Errorf("kind=%s: Deletable = false, want true", k)
		}
	}
}

func TestEventDeletable_DirInRefused(t *testing.T) {
	now := int64(1_700_000_000)
	e := events.Event{
		Kind:      events.KindText,
		Direction: events.DirIn,
		DisplayTs: now - 60,
	}
	if e.Deletable(now) {
		t.Errorf("DirIn Deletable = true, want false")
	}
}

func TestEventDeletable_OutsideWindowRefused(t *testing.T) {
	now := int64(1_700_000_000)
	e := events.Event{
		Kind:      events.KindText,
		Direction: events.DirOut,
		DisplayTs: now - int64(events.MutationWindow) - 1,
	}
	if e.Deletable(now) {
		t.Errorf("outside window Deletable = true, want false")
	}
}

func TestEventDeletable_TombstonedRefused(t *testing.T) {
	now := int64(1_700_000_000)
	e := events.Event{
		Kind:      events.KindText,
		Direction: events.DirOut,
		DisplayTs: now - 60,
		DeletedAt: now - 10,
	}
	if e.Deletable(now) {
		t.Errorf("tombstoned Deletable = true, want false")
	}
}

func TestEventDeletable_OtherKindsRefused(t *testing.T) {
	now := int64(1_700_000_000)
	for _, k := range []events.Kind{events.KindTimerChange, events.KindReaction} {
		e := events.Event{
			Kind:      k,
			Direction: events.DirOut,
			DisplayTs: now - 60,
		}
		if e.Deletable(now) {
			t.Errorf("kind=%s Deletable = true, want false", k)
		}
	}
}
