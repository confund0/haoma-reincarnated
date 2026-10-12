package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
)

func tapPushes(t *testing.T, d *daemon) func() ipc.DeliveryStatusPayload {
	t.Helper()
	srv := httptest.NewServer(d.ipcSrv.Handler())
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer t")
	conn, _, err := websocket.Dial(ctx, "ws://"+strings.TrimPrefix(srv.URL, "http://")+"/ws",
		&websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "") })
	writeFrame(t, ctx, conn, mustFrame(ipc.FrameHello, "h", ipc.HelloPayload{ClientName: "test"}))
	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatalf("welcome: %v", err)
	}

	for deadline := time.Now().Add(5 * time.Second); d.ipcSrv.SessionCount() == 0; {
		if time.Now().After(deadline) {
			t.Fatal("the test client was never registered")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return func() ipc.DeliveryStatusPayload {
		t.Helper()
		f := readUntil(t, ctx, conn, ipc.FrameDeliveryStatus)
		var p ipc.DeliveryStatusPayload
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
}

func deliveryStream(t *testing.T, reports []backendapi.DeliveryStatus) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /inbox", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(backendapi.InboxResponse{})
	})
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, ds := range reports {
			raw, _ := json.Marshal(ds)
			_, _ = w.Write([]byte("event: delivery.state-changed\ndata: " + string(raw) + "\n\n"))
		}
		flusher.Flush()
		<-r.Context().Done()
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func appendFannedText(t *testing.T, d *daemon, msgID string, envIDs ...string) {
	t.Helper()
	body, _ := json.Marshal(events.TextBody{Text: "to " + msgID})
	if _, err := d.events.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText, SenderSeq: 1,
		EnvelopeIDs: envIDs, MsgID: msgID, Body: body,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestEventsLoop_OnDelivery_PushesUnderTheRowsFirstEnvelope(t *testing.T) {
	reports := []backendapi.DeliveryStatus{

		{EnvelopeID: "env-rail-sibling", State: events.DeliverySent, At: 1},

		{EnvelopeID: "env-alice-phone", State: events.DeliverySent, At: 2},

		{EnvelopeID: "env-alice-desktop", State: events.DeliverySent, At: 3},

		{EnvelopeID: "env-read-phone", State: events.DeliverySent, At: 4},

		{EnvelopeID: "env-end", State: events.DeliverySent, At: 5},
	}
	stream := deliveryStream(t, reports)
	d := minimalDaemon(t, stream.URL)

	appendFannedText(t, d, "m-fold", "env-alice-desktop", "env-alice-phone")
	appendFannedText(t, d, "m-read", "env-read-desktop", "env-read-phone")
	if _, _, err := d.events.ApplyReadReceipt("m-read", 1700000000, chat.ChatID("alice")); err != nil {
		t.Fatal(err)
	}

	next := tapPushes(t, d)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eventsLoop(ctx, d)

	type pushed struct{ env, state string }
	want := []pushed{
		{"env-rail-sibling", events.DeliverySent},
		{"env-alice-desktop", events.DeliverySent},
		{"env-end", events.DeliverySent},
	}
	for i, w := range want {
		p := next()
		if got := (pushed{p.EnvelopeID, p.State}); got != w {
			t.Fatalf("push %d = %+v, want %+v", i, got, w)
		}
	}

	row, err := d.events.GetByMsgID("m-fold")
	if err != nil {
		t.Fatal(err)
	}
	if row.DeliveryState != events.DeliverySent {
		t.Errorf("folded row state = %q, want sent", row.DeliveryState)
	}
	read, err := d.events.GetByMsgID("m-read")
	if err != nil {
		t.Fatal(err)
	}
	if read.DeliveryState != events.DeliveryRead {
		t.Errorf("read row state = %q, want read", read.DeliveryState)
	}
}

func TestDeliveryPush_RailEnvelopeLeavesTheRow(t *testing.T) {
	d := minimalDaemon(t, "http://127.0.0.1:1")
	appendFannedText(t, d, "m-rail", "env-alice-desktop", "env-alice-phone")

	p, ok := deliveryPush(d, backendapi.DeliveryStatus{EnvelopeID: "env-rail-sibling", State: events.DeliverySent})
	if !ok || p.EnvelopeID != "env-rail-sibling" {
		t.Fatalf("push = %+v ok=%v, want it under its own id", p, ok)
	}
	row, err := d.events.GetByMsgID("m-rail")
	if err != nil {
		t.Fatal(err)
	}
	if row.DeliveryState != events.DeliveryEnqueued {
		t.Errorf("row state after a rail ack = %q, want enqueued", row.DeliveryState)
	}
}

func TestSweepDeliveryReconcile_PushesUnderTheRowsFirstEnvelope(t *testing.T) {
	stub := startOutboxStub(t)
	d := minimalDaemon(t, stub.URL())
	appendFannedText(t, d, "m-reconcile", "env-alice-desktop", "env-alice-phone")
	stub.byState["sent"] = []backendapi.OutboxRow{
		{EnvelopeID: "env-alice-phone", State: "sent", StateChangedAt: 9000},
	}
	next := tapPushes(t, d)

	sweepDeliveryReconcile(context.Background(), d)

	p := next()
	if p.EnvelopeID != "env-alice-desktop" || p.State != events.DeliverySent {
		t.Fatalf("push = (%q, %q), want (env-alice-desktop, sent)", p.EnvelopeID, p.State)
	}
}

func TestDeliveryPush_FailedOnlyWhenEveryEnvelopeFailed(t *testing.T) {
	d := minimalDaemon(t, "http://127.0.0.1:1")
	appendFannedText(t, d, "m-fail", "env-alice-desktop", "env-alice-phone")

	if p, ok := deliveryPush(d, backendapi.DeliveryStatus{EnvelopeID: "env-alice-phone", State: events.DeliveryFailed}); ok {
		t.Fatalf("one failed envelope of two pushed %+v, want no push", p)
	}
	p, ok := deliveryPush(d, backendapi.DeliveryStatus{EnvelopeID: "env-alice-desktop", State: events.DeliveryFailed, LastError: "gave up"})
	if !ok || p.EnvelopeID != "env-alice-desktop" || p.State != events.DeliveryFailed {
		t.Fatalf("last envelope failed: push = %+v ok=%v, want (env-alice-desktop, failed)", p, ok)
	}

	appendFannedText(t, d, "m-fail-2", "env-b-desktop", "env-b-phone")
	if _, ok := deliveryPush(d, backendapi.DeliveryStatus{EnvelopeID: "env-b-desktop", State: events.DeliveryFailed}); ok {
		t.Fatal("one failed envelope of two pushed, want no push")
	}
	p, ok = deliveryPush(d, backendapi.DeliveryStatus{EnvelopeID: "env-b-phone", State: events.DeliveryFailed})
	if !ok || p.EnvelopeID != "env-b-desktop" || p.State != events.DeliveryFailed {
		t.Fatalf("second envelope failed last: push = %+v ok=%v, want (env-b-desktop, failed)", p, ok)
	}
}
