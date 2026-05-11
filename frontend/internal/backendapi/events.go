package backendapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Notification struct {
	Event   Event             `json:"event"`
	Actions []json.RawMessage `json:"actions"`
}

type Event struct {
	Kind       string `json:"kind"`
	At         string `json:"at"`
	PeerID     string `json:"peer_id,omitempty"`
	SourceAddr string `json:"source_addr,omitempty"`
	SlotIdx    int    `json:"slot_idx"`
	Detail     string `json:"detail,omitempty"`
}

type DeliveryStatus struct {
	EnvelopeID string `json:"envelope_id"`
	State      string `json:"state"`
	At         int64  `json:"at"`
	Attempts   int    `json:"attempts"`
	LastError  string `json:"last_error,omitempty"`
}

type PresenceObservation struct {
	PeerID string `json:"peer_id"`
	Source string `json:"source"`
	At     int64  `json:"at"`
}

type LastSeenObservation struct {
	PeerID        string `json:"peer_id"`
	LastActiveAt  int64  `json:"last_active_at"`
	LastPassiveAt int64  `json:"last_passive_at"`
}

type PairOnionProbe struct {
	HandleID string `json:"handle_id"`
	Attempt  int    `json:"attempt"`
	Ready    bool   `json:"ready"`
	Error    string `json:"error,omitempty"`
	At       int64  `json:"at"`
}

type FileFetchEvent struct {
	MsgID         string `json:"msg_id"`
	Token         string `json:"token"`
	State         string `json:"state"`
	BytesReceived int64  `json:"bytes_received,omitempty"`
	TotalBytes    int64  `json:"total_bytes,omitempty"`
	LastError     string `json:"last_error,omitempty"`
	At            int64  `json:"at"`
}

type EventsOpts struct {
	OnEvent             func(Notification)
	OnDelivery          func(DeliveryStatus)
	OnInbox             func(InboxEntry)
	OnPresence          func(PresenceObservation)
	OnLastSeen          func(LastSeenObservation)
	OnPairOnionProbe    func(PairOnionProbe)
	OnFileFetchState    func(FileFetchEvent)
	OnFileFetchProgress func(FileFetchEvent)

	OnReady func()
}

func (c *Client) Events(ctx context.Context, opts EventsOpts) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/events", nil)
	if err != nil {
		return fmt.Errorf("backendapi: build events: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	c.authHeader(req)

	streamClient := *c.http
	streamClient.Timeout = 0

	resp, err := streamClient.Do(req)
	if err != nil {
		return fmt.Errorf("backendapi: events request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return httpError(resp)
	}
	if opts.OnReady != nil {
		opts.OnReady()
	}
	return parseSSE(ctx, resp.Body, opts)
}

func parseSSE(ctx context.Context, r io.Reader, opts EventsOpts) error {
	sc := bufio.NewScanner(r)

	sc.Buffer(make([]byte, 0, 4096), 1<<20)

	var (
		evType string
		data   strings.Builder
	)
	flush := func() {
		if data.Len() == 0 {
			return
		}
		defer func() {
			evType = ""
			data.Reset()
		}()
		switch evType {
		case "system.ids-event":
			if opts.OnEvent == nil {
				return
			}
			var n Notification
			if err := json.Unmarshal([]byte(data.String()), &n); err != nil {
				return
			}
			opts.OnEvent(n)
		case "delivery.state-changed":
			if opts.OnDelivery == nil {
				return
			}
			var ds DeliveryStatus
			if err := json.Unmarshal([]byte(data.String()), &ds); err != nil {
				return
			}
			opts.OnDelivery(ds)
		case "inbox.received":
			if opts.OnInbox == nil {
				return
			}
			var e InboxEntry
			if err := json.Unmarshal([]byte(data.String()), &e); err != nil {
				return
			}
			opts.OnInbox(e)
		case "peer.presence-changed":
			if opts.OnPresence == nil {
				return
			}
			var p PresenceObservation
			if err := json.Unmarshal([]byte(data.String()), &p); err != nil {
				return
			}
			opts.OnPresence(p)
		case "peer.last-seen-changed":
			if opts.OnLastSeen == nil {
				return
			}
			var ls LastSeenObservation
			if err := json.Unmarshal([]byte(data.String()), &ls); err != nil {
				return
			}
			opts.OnLastSeen(ls)
		case "pair.onion-probe":
			if opts.OnPairOnionProbe == nil {
				return
			}
			var p PairOnionProbe
			if err := json.Unmarshal([]byte(data.String()), &p); err != nil {
				return
			}
			opts.OnPairOnionProbe(p)
		case "file.fetch-state-changed":
			if opts.OnFileFetchState == nil {
				return
			}
			var ev FileFetchEvent
			if err := json.Unmarshal([]byte(data.String()), &ev); err != nil {
				return
			}
			opts.OnFileFetchState(ev)
		case "file.fetch-progress":
			if opts.OnFileFetchProgress == nil {
				return
			}
			var ev FileFetchEvent
			if err := json.Unmarshal([]byte(data.String()), &ev); err != nil {
				return
			}
			opts.OnFileFetchProgress(ev)
		}
	}

	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := sc.Text()

		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, ":"):

		case strings.HasPrefix(line, "event:"):
			evType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		default:

		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("backendapi: events scan: %w", err)
	}
	return nil
}

func DefaultReconnectBackoff(attempt int) time.Duration {
	d := time.Second
	for i := 0; i < attempt; i++ {
		d *= 2
		if d > 5*time.Second {
			return 5 * time.Second
		}
	}
	return d
}
