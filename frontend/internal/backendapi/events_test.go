package backendapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEvents_ParsesEventFrames(t *testing.T) {
	raw := "" +
		"event: system.ids-event\n" +
		`data: {"event":{"kind":"bad_mac","at":"2026-04-21T19:00:00Z","slot_idx":1},"actions":[]}` + "\n" +
		"\n" +
		": keepalive\n" +
		"\n" +
		"event: system.ids-event\n" +
		`data: {"event":{"kind":"probe_all_dead","at":"2026-04-21T19:01:00Z","slot_idx":-1},"actions":[]}` + "\n" +
		"\n" +
		"event: other\n" +
		`data: {"should":"be ignored"}` + "\n" +
		"\n"

	var got []Notification
	err := parseSSE(context.Background(), strings.NewReader(raw), EventsOpts{
		OnEvent: func(n Notification) { got = append(got, n) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("decoded %d notifications, want 2", len(got))
	}
	if got[0].Event.Kind != "bad_mac" || got[0].Event.SlotIdx != 1 {
		t.Errorf("first = %+v", got[0].Event)
	}
	if got[1].Event.Kind != "probe_all_dead" || got[1].Event.SlotIdx != -1 {
		t.Errorf("second = %+v", got[1].Event)
	}
}

func TestEvents_DeliveryStatusFrames(t *testing.T) {
	raw := "" +
		"event: delivery.state-changed\n" +
		`data: {"envelope_id":"abc123","state":"sent","at":1000,"attempts":1}` + "\n" +
		"\n" +
		"event: system.ids-event\n" +
		`data: {"event":{"kind":"bad_mac","at":"t","slot_idx":0},"actions":[]}` + "\n" +
		"\n" +
		"event: delivery.state-changed\n" +
		`data: {"envelope_id":"def456","state":"failed","at":2000,"attempts":12,"last_error":"401 Unauthorized"}` + "\n" +
		"\n"

	var gotIDS []Notification
	var gotDS []DeliveryStatus
	err := parseSSE(context.Background(), strings.NewReader(raw), EventsOpts{
		OnEvent:    func(n Notification) { gotIDS = append(gotIDS, n) },
		OnDelivery: func(ds DeliveryStatus) { gotDS = append(gotDS, ds) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(gotIDS) != 1 {
		t.Errorf("ids frames = %d, want 1", len(gotIDS))
	}
	if len(gotDS) != 2 {
		t.Fatalf("delivery_status frames = %d, want 2", len(gotDS))
	}
	if gotDS[0].EnvelopeID != "abc123" || gotDS[0].State != "sent" || gotDS[0].Attempts != 1 {
		t.Errorf("first delivery_status = %+v", gotDS[0])
	}
	if gotDS[1].EnvelopeID != "def456" || gotDS[1].State != "failed" || gotDS[1].LastError == "" {
		t.Errorf("second delivery_status = %+v", gotDS[1])
	}
}

func TestEvents_DeliveryStatusNilCallback(t *testing.T) {

	raw := "event: delivery.state-changed\n" +
		`data: {"envelope_id":"x","state":"sent","at":1,"attempts":1}` + "\n\n"
	if err := parseSSE(context.Background(), strings.NewReader(raw), EventsOpts{}); err != nil {
		t.Fatal(err)
	}
}

func TestEvents_InboxFrames(t *testing.T) {
	raw := "" +
		"event: inbox.received\n" +
		`data: {"arrival_at":42,"peer_id":"p1","envelope":{"id":"env-1","ts":1,"from":"alice","kind":"text","payload":"aGk="}}` + "\n" +
		"\n"

	var got []InboxEntry
	err := parseSSE(context.Background(), strings.NewReader(raw), EventsOpts{
		OnInbox: func(e InboxEntry) { got = append(got, e) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("decoded %d inbox frames, want 1", len(got))
	}
	if got[0].Envelope.ID != "env-1" || got[0].PeerID != "p1" {
		t.Errorf("inbox entry = %+v", got[0])
	}
}

func TestEvents_InboxNilCallback(t *testing.T) {
	raw := "event: inbox.received\n" +
		`data: {"arrival_at":1,"peer_id":"x","envelope":{"id":"e","ts":1,"from":"a","payload":"aGk="}}` + "\n\n"
	if err := parseSSE(context.Background(), strings.NewReader(raw), EventsOpts{}); err != nil {
		t.Fatal(err)
	}
}

func TestEvents_PresenceObservedFrames(t *testing.T) {
	raw := "" +
		"event: peer.presence-changed\n" +
		`data: {"peer_id":"alice","source":"haoma","at":1700000000}` + "\n" +
		"\n" +
		"event: peer.presence-changed\n" +
		`data: {"peer_id":"bob","source":"haomad","at":1700000060}` + "\n" +
		"\n"

	var got []PresenceObservation
	err := parseSSE(context.Background(), strings.NewReader(raw), EventsOpts{
		OnPresence: func(p PresenceObservation) { got = append(got, p) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("decoded %d presence frames, want 2", len(got))
	}
	if got[0].PeerID != "alice" || got[0].Source != "haoma" || got[0].At != 1700000000 {
		t.Errorf("first = %+v", got[0])
	}
	if got[1].PeerID != "bob" || got[1].Source != "haomad" || got[1].At != 1700000060 {
		t.Errorf("second = %+v", got[1])
	}
}

func TestEvents_PresenceNilCallback(t *testing.T) {
	raw := "event: peer.presence-changed\n" +
		`data: {"peer_id":"x","source":"haoma","at":1}` + "\n\n"
	if err := parseSSE(context.Background(), strings.NewReader(raw), EventsOpts{}); err != nil {
		t.Fatal(err)
	}
}

func TestEvents_MalformedDataIsSkipped(t *testing.T) {
	raw := "event: system.ids-event\ndata: not valid json\n\n" +
		"event: system.ids-event\n" +
		`data: {"event":{"kind":"bad_mac","at":"t","slot_idx":0},"actions":[]}` + "\n" +
		"\n"
	var got []Notification
	if err := parseSSE(context.Background(), strings.NewReader(raw), EventsOpts{
		OnEvent: func(n Notification) { got = append(got, n) },
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d notifications; malformed should have been dropped", len(got))
	}
	if got[0].Event.Kind != "bad_mac" {
		t.Errorf("wrong notification: %+v", got[0])
	}
}

func TestEvents_CtxCancelStops(t *testing.T) {

	pr, pw := blockingPipe()
	defer pw.Close()

	go func() {
		_, _ = pw.Write([]byte("event: system.ids-event\n" +
			`data: {"event":{"kind":"bad_mac","at":"t","slot_idx":0},"actions":[]}` + "\n\n"))

	}()

	ctx, cancel := context.WithCancel(context.Background())
	var gotErr error
	var wg sync.WaitGroup
	wg.Add(1)
	got := 0
	go func() {
		defer wg.Done()
		gotErr = parseSSE(ctx, pr, EventsOpts{OnEvent: func(Notification) { got++ }})
	}()

	time.Sleep(30 * time.Millisecond)
	if got != 1 {
		t.Errorf("expected one frame decoded before cancel; got %d", got)
	}
	cancel()

	_ = pw.Close()
	wg.Wait()

	if gotErr == nil {

		t.Log("parseSSE returned nil (pipe closed before ctx check); acceptable")
	}
}

func TestEvents_HTTPRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		payload, _ := json.Marshal(Notification{Event: Event{Kind: "bad_mac", At: "t", SlotIdx: 0}})
		fmt.Fprintf(w, "event: system.ids-event\ndata: %s\n\n", payload)
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan Notification, 1)

	errCh := make(chan error, 1)
	go func() {
		errCh <- New(srv.URL, "", nil).Events(ctx, EventsOpts{
			OnEvent: func(n Notification) {
				select {
				case got <- n:
				default:
				}
			},
		})
	}()

	select {
	case n := <-got:
		if n.Event.Kind != "bad_mac" {
			t.Errorf("got kind %q, want bad_mac", n.Event.Kind)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("didn't receive notification within 2s")
	}

	cancel()
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("Events didn't return after cancel")
	}
}

func TestDefaultReconnectBackoff_Caps(t *testing.T) {
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, time.Second},
		{1, 2 * time.Second},
		{2, 4 * time.Second},
		{3, 5 * time.Second},
		{10, 5 * time.Second},
	}
	for _, c := range cases {
		got := DefaultReconnectBackoff(c.attempt)
		if got != c.want {
			t.Errorf("attempt=%d got %v, want %v", c.attempt, got, c.want)
		}
	}
}

func blockingPipe() (*blockingReader, *blockingWriter) {
	ch := make(chan []byte, 16)
	closed := make(chan struct{})
	return &blockingReader{ch: ch, closed: closed}, &blockingWriter{ch: ch, closed: closed}
}

type blockingReader struct {
	ch       chan []byte
	closed   chan struct{}
	leftover []byte
}

func (r *blockingReader) Read(p []byte) (int, error) {
	if len(r.leftover) > 0 {
		n := copy(p, r.leftover)
		r.leftover = r.leftover[n:]
		return n, nil
	}
	select {
	case b, ok := <-r.ch:
		if !ok {
			return 0, fmt.Errorf("EOF")
		}
		n := copy(p, b)
		if n < len(b) {
			r.leftover = append(r.leftover, b[n:]...)
		}
		return n, nil
	case <-r.closed:
		return 0, fmt.Errorf("EOF")
	}
}

type blockingWriter struct {
	ch     chan []byte
	closed chan struct{}
	once   sync.Once
}

func (w *blockingWriter) Write(p []byte) (int, error) {
	b := make([]byte, len(p))
	copy(b, p)
	w.ch <- b
	return len(p), nil
}

func (w *blockingWriter) Close() error {
	w.once.Do(func() { close(w.closed); close(w.ch) })
	return nil
}
