package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
)

type outboxStub struct {
	mu      sync.Mutex
	entries map[string]backendapi.OutboxRow
	byState map[string][]backendapi.OutboxRow
	status  int
	url     string

	lastLimit string

	entryCalls int
}

func startOutboxStub(t *testing.T) *outboxStub {
	t.Helper()
	s := &outboxStub{
		entries: map[string]backendapi.OutboxRow{},
		byState: map[string][]backendapi.OutboxRow{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /outbox/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.entryCalls++
		if s.status != 0 {
			http.Error(w, "stub failure", s.status)
			return
		}
		row, ok := s.entries[r.PathValue("id")]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(row)
	})
	mux.HandleFunc("GET /outbox", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.lastLimit = r.URL.Query().Get("limit")
		rows := s.byState[r.URL.Query().Get("state")]

		cap := 50
		if s.lastLimit != "" {
			if v, err := strconv.Atoi(s.lastLimit); err == nil && v > 0 {
				cap = v
			}
		}
		if len(rows) > cap {
			rows = rows[:cap]
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(backendapi.OutboxResponse{Rows: rows})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	s.url = srv.URL
	return s
}

func (s *outboxStub) URL() string { return s.url }

func seedShippedReceipt(t *testing.T, d *daemon, chatID chat.ChatID, envID string, msgIDs ...string) {
	t.Helper()
	body, _ := json.Marshal(events.TextBody{Text: "hi"})
	for _, id := range msgIDs {
		if _, err := d.events.AppendInbound(events.InboundParams{
			ChatID: chatID, Kind: events.KindText, SenderTs: 1742643885,
			MsgID: id, Status: events.DecryptOK, Body: body,
		}); err != nil {
			t.Fatal(err)
		}
	}
	_, pending, err := d.events.MarkRead(chatID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != len(msgIDs) {
		t.Fatalf("seed: pending = %v, want %d rows", pending, len(msgIDs))
	}
	ids := events.ReceiptMsgIDs(pending)
	if err := d.events.MarkReadReceiptSent(ids, 1742643999); err != nil {
		t.Fatal(err)
	}
	if err := d.events.RecordReceiptEnvelope(envID, chatID, ids, 1742643999); err != nil {
		t.Fatal(err)
	}
}

func pendingCount(t *testing.T, d *daemon, chatID chat.ChatID) int {
	t.Helper()
	_, pending, err := d.events.MarkRead(chatID)
	if err != nil {
		t.Fatal(err)
	}
	return len(pending)
}

func TestApplyReceiptOutcome_FailedClearsStamps(t *testing.T) {
	d := minimalDaemon(t, "http://127.0.0.1:1")
	const chatID = chat.ChatID("alice")
	seedShippedReceipt(t, d, chatID, "env-rcpt", "mid-a")

	if got := pendingCount(t, d, chatID); got != 0 {
		t.Fatalf("pending before failure = %d, want 0", got)
	}
	if handled, _ := applyReceiptOutcome(d, "env-rcpt", "failed"); !handled {
		t.Fatal("failed report was not recognised as a receipt envelope")
	}
	if got := pendingCount(t, d, chatID); got != 1 {
		t.Errorf("pending after failure = %d, want 1 (row handed back)", got)
	}
}

func TestApplyReceiptOutcome_SentDropsMapping(t *testing.T) {
	d := minimalDaemon(t, "http://127.0.0.1:1")
	const chatID = chat.ChatID("alice")
	seedShippedReceipt(t, d, chatID, "env-rcpt", "mid-a")

	if handled, _ := applyReceiptOutcome(d, "env-rcpt", "sent"); !handled {
		t.Fatal("sent report was not recognised as a receipt envelope")
	}
	list, err := d.events.ListReceiptEnvelopes()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("mappings after success = %+v, want empty", list)
	}
	if got := pendingCount(t, d, chatID); got != 0 {
		t.Errorf("pending after success = %d, want 0 — the receipt landed", got)
	}
}

func TestApplyReceiptOutcome_TextEnvelopeFallsThrough(t *testing.T) {
	d := minimalDaemon(t, "http://127.0.0.1:1")
	if handled, _ := applyReceiptOutcome(d, "env-text", "failed"); handled {
		t.Error("a text envelope was treated as a receipt")
	}
	if handled, _ := applyReceiptOutcome(d, "env-text", "sent"); handled {
		t.Error("a text envelope was treated as a receipt")
	}
}

func TestSweepDeliveryReconcile_DetachedGiveUp(t *testing.T) {
	stub := startOutboxStub(t)
	d := minimalDaemon(t, stub.URL())
	const chatID = chat.ChatID("alice")
	seedShippedReceipt(t, d, chatID, "env-rcpt", "mid-a", "mid-b")

	stub.entries["env-rcpt"] = backendapi.OutboxRow{
		EnvelopeID: "env-rcpt", State: "failed", Attempts: 72, StateChangedAt: 4242,
	}

	sweepDeliveryReconcile(context.Background(), d)

	if got := pendingCount(t, d, chatID); got != 2 {
		t.Errorf("pending after reconcile = %d, want 2 (both rows handed back)", got)
	}
	list, _ := d.events.ListReceiptEnvelopes()
	if len(list) != 0 {
		t.Errorf("mappings after reconcile = %+v, want empty", list)
	}
}

func TestSweepDeliveryReconcile_InFlightUntouched(t *testing.T) {
	stub := startOutboxStub(t)
	d := minimalDaemon(t, stub.URL())
	const chatID = chat.ChatID("alice")
	seedShippedReceipt(t, d, chatID, "env-rcpt", "mid-a")

	stub.entries["env-rcpt"] = backendapi.OutboxRow{
		EnvelopeID: "env-rcpt", State: "enqueued", Attempts: 3,
	}

	sweepDeliveryReconcile(context.Background(), d)

	if got := pendingCount(t, d, chatID); got != 0 {
		t.Errorf("pending after reconcile = %d, want 0 — receipt is still in flight", got)
	}
	list, _ := d.events.ListReceiptEnvelopes()
	if len(list) != 1 {
		t.Errorf("mappings after reconcile = %+v, want the mapping kept", list)
	}
}

func TestSweepDeliveryReconcile_BackendErrorKeepsMapping(t *testing.T) {
	stub := startOutboxStub(t)
	d := minimalDaemon(t, stub.URL())
	const chatID = chat.ChatID("alice")
	seedShippedReceipt(t, d, chatID, "env-rcpt", "mid-a")
	stub.status = http.StatusInternalServerError

	sweepDeliveryReconcile(context.Background(), d)

	if got := pendingCount(t, d, chatID); got != 0 {
		t.Errorf("pending after a 500 = %d, want 0 — the receipt may still be in flight", got)
	}
	list, _ := d.events.ListReceiptEnvelopes()
	if len(list) != 1 {
		t.Errorf("mappings after a 500 = %+v, want the mapping kept for the next edge", list)
	}
}

func TestSweepDeliveryReconcile_UnknownEnvelopeClears(t *testing.T) {
	stub := startOutboxStub(t)
	d := minimalDaemon(t, stub.URL())
	const chatID = chat.ChatID("alice")
	seedShippedReceipt(t, d, chatID, "env-gone", "mid-a")

	sweepDeliveryReconcile(context.Background(), d)

	if got := pendingCount(t, d, chatID); got != 1 {
		t.Errorf("pending after unknown envelope = %d, want 1", got)
	}
	list, _ := d.events.ListReceiptEnvelopes()
	if len(list) != 0 {
		t.Errorf("mappings after unknown envelope = %+v, want empty", list)
	}
}

func TestSweepDeliveryReconcile_RepairsStaleMarker(t *testing.T) {
	stub := startOutboxStub(t)
	d := minimalDaemon(t, stub.URL())
	body, _ := json.Marshal(events.TextBody{Text: "out"})
	if _, err := d.events.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderSeq: 1,
		EnvelopeID: "env-text", MsgID: "mid-out", Body: body,
	}); err != nil {
		t.Fatal(err)
	}
	stub.byState["sent"] = []backendapi.OutboxRow{
		{EnvelopeID: "env-text", State: "sent", StateChangedAt: 9000},
	}

	sweepDeliveryReconcile(context.Background(), d)

	got, err := d.events.GetByEnvelopeID("env-text")
	if err != nil {
		t.Fatal(err)
	}
	if got.DeliveryState != "sent" {
		t.Errorf("delivery state = %q, want %q (stale marker repaired)", got.DeliveryState, "sent")
	}
	at, err := d.events.OutboxReconcileWatermark()
	if err != nil {
		t.Fatal(err)
	}
	if at != 9000 {
		t.Errorf("watermark = %d, want 9000 so the next pass skips this ground", at)
	}
}

func TestSweepDeliveryReconcile_SingleFlight(t *testing.T) {
	stub := startOutboxStub(t)
	d := minimalDaemon(t, stub.URL())
	seedShippedReceipt(t, d, chat.ChatID("alice"), "env-rcpt", "mid-a")
	stub.entries["env-rcpt"] = backendapi.OutboxRow{EnvelopeID: "env-rcpt", State: "enqueued"}

	d.deliveryReconcileRunning.Store(true)
	sweepDeliveryReconcile(context.Background(), d)

	stub.mu.Lock()
	calls := stub.entryCalls
	stub.mu.Unlock()
	if calls != 0 {
		t.Errorf("outbox lookups during a concurrent pass = %d, want 0", calls)
	}
}

func TestSweepDeliveryReconcile_DoesNotStrandRowsBeyondThePage(t *testing.T) {
	stub := startOutboxStub(t)
	d := minimalDaemon(t, stub.URL())
	body, _ := json.Marshal(events.TextBody{Text: "out"})

	defer func(orig int) { outboxScanLimit = orig }(outboxScanLimit)
	outboxScanLimit = 10

	const rows = 12
	for i := 0; i < rows; i++ {
		envID := fmt.Sprintf("env-%03d", i)
		if _, err := d.events.AppendOutbound(events.OutboundParams{
			ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderSeq: uint64(i + 1),
			EnvelopeID: envID, MsgID: fmt.Sprintf("mid-%03d", i), Body: body,
		}); err != nil {
			t.Fatal(err)
		}
		at := int64(1000 + i)
		if i == 0 {
			at = 99_000
		}
		stub.byState["sent"] = append(stub.byState["sent"], backendapi.OutboxRow{
			EnvelopeID: envID, State: "sent", StateChangedAt: at,
		})
	}

	sweepDeliveryReconcile(context.Background(), d)

	if stub.lastLimit == "" {
		t.Error("reconcile sent no limit — the backend then applies a small default and the scan is silently partial")
	}
	at, err := d.events.OutboxReconcileWatermark()
	if err != nil {
		t.Fatal(err)
	}
	if at != 0 {
		t.Errorf("watermark = %d after a capped pass, want 0 — rows beyond the page would never be revisited", at)
	}
}
