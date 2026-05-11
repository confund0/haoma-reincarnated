package ids

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

type EventKind int

const (
	EventUnknownSource EventKind = iota
	EventBadMAC
	EventProtocolViolation
	EventConnectionAccepted
	EventProbeAllDead
)

func (k EventKind) String() string {
	switch k {
	case EventUnknownSource:
		return "unknown_source"
	case EventBadMAC:
		return "bad_mac"
	case EventProtocolViolation:
		return "protocol_violation"
	case EventConnectionAccepted:
		return "connection_accepted"
	case EventProbeAllDead:
		return "probe_all_dead"
	default:
		return "unknown"
	}
}

func (k EventKind) MarshalJSON() ([]byte, error) { return json.Marshal(k.String()) }

func (k *EventKind) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	switch s {
	case "unknown_source":
		*k = EventUnknownSource
	case "bad_mac":
		*k = EventBadMAC
	case "protocol_violation":
		*k = EventProtocolViolation
	case "connection_accepted":
		*k = EventConnectionAccepted
	case "probe_all_dead":
		*k = EventProbeAllDead
	default:
		return fmt.Errorf("ids: unknown EventKind %q", s)
	}
	return nil
}

type Event struct {
	Kind       EventKind `json:"kind"`
	At         time.Time `json:"at"`
	PeerID     string    `json:"peer_id,omitempty"`
	SourceAddr string    `json:"source_addr,omitempty"`
	SlotIdx    int       `json:"slot_idx"`
	Detail     string    `json:"detail,omitempty"`
}

const SlotUnknown = -1

type Action interface {
	Type() string
}

type CounterBump struct {
	PeerID     string `json:"peer_id,omitempty"`
	SourceAddr string `json:"source_addr,omitempty"`
	Count      int    `json:"count"`
}

func (CounterBump) Type() string { return "counter_bump" }

func (a CounterBump) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type       string `json:"type"`
		PeerID     string `json:"peer_id,omitempty"`
		SourceAddr string `json:"source_addr,omitempty"`
		Count      int    `json:"count"`
	}{a.Type(), a.PeerID, a.SourceAddr, a.Count})
}

type AlertEmit struct {
	Level       AlertLevel `json:"level"`
	Message     string     `json:"message"`
	RelatedPeer string     `json:"related_peer,omitempty"`
}

func (AlertEmit) Type() string { return "alert" }

func (a AlertEmit) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type        string     `json:"type"`
		Level       AlertLevel `json:"level"`
		Message     string     `json:"message"`
		RelatedPeer string     `json:"related_peer,omitempty"`
	}{a.Type(), a.Level, a.Message, a.RelatedPeer})
}

type PanicRotate struct {
	Reason      string `json:"reason"`
	RelatedPeer string `json:"related_peer,omitempty"`
	SlotIdx     int    `json:"slot_idx"`
}

func (PanicRotate) Type() string { return "panic_rotate" }

func (a PanicRotate) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type        string `json:"type"`
		Reason      string `json:"reason"`
		RelatedPeer string `json:"related_peer,omitempty"`
		SlotIdx     int    `json:"slot_idx"`
	}{a.Type(), a.Reason, a.RelatedPeer, a.SlotIdx})
}

type AlertLevel int

const (
	AlertInfo AlertLevel = iota
	AlertWarn
	AlertCritical
)

func (l AlertLevel) String() string {
	switch l {
	case AlertInfo:
		return "info"
	case AlertWarn:
		return "warn"
	case AlertCritical:
		return "critical"
	default:
		return "unknown"
	}
}

func (l AlertLevel) MarshalJSON() ([]byte, error) { return json.Marshal(l.String()) }

func (l *AlertLevel) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	switch s {
	case "info":
		*l = AlertInfo
	case "warn":
		*l = AlertWarn
	case "critical":
		*l = AlertCritical
	default:
		return fmt.Errorf("ids: unknown AlertLevel %q", s)
	}
	return nil
}

type Rule interface {
	Name() string
	Evaluate(ctx context.Context, ev Event) []Action
}

type Stats struct {
	EventCounts  map[string]int64 `json:"event_counts"`
	ActionCounts map[string]int64 `json:"action_counts"`
	LastEventAt  time.Time        `json:"last_event_at"`
}

type Notification struct {
	Event   Event    `json:"event"`
	Actions []Action `json:"actions"`
}

type IDS struct {
	mu          sync.RWMutex
	rules       []Rule
	stats       Stats
	subscribers []chan Notification
	subscribeMu sync.RWMutex
	now         func() time.Time
}

func New() *IDS {
	return &IDS{
		stats: Stats{
			EventCounts:  map[string]int64{},
			ActionCounts: map[string]int64{},
		},
		now: time.Now,
	}
}

func (i *IDS) AddRule(r Rule) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.rules = append(i.rules, r)
}

func (i *IDS) Observe(ctx context.Context, ev Event) []Action {
	if ev.At.IsZero() {
		ev.At = i.now()
	}
	i.mu.RLock()
	rules := make([]Rule, len(i.rules))
	copy(rules, i.rules)
	i.mu.RUnlock()

	var actions []Action
	for _, r := range rules {
		actions = append(actions, r.Evaluate(ctx, ev)...)
	}

	i.mu.Lock()
	i.stats.EventCounts[ev.Kind.String()]++
	i.stats.LastEventAt = ev.At
	for _, a := range actions {
		i.stats.ActionCounts[a.Type()]++
	}
	i.mu.Unlock()

	i.fanOut(Notification{Event: ev, Actions: actions})
	return actions
}

func (i *IDS) Snapshot() Stats {
	i.mu.RLock()
	defer i.mu.RUnlock()
	out := Stats{
		EventCounts:  make(map[string]int64, len(i.stats.EventCounts)),
		ActionCounts: make(map[string]int64, len(i.stats.ActionCounts)),
		LastEventAt:  i.stats.LastEventAt,
	}
	for k, v := range i.stats.EventCounts {
		out.EventCounts[k] = v
	}
	for k, v := range i.stats.ActionCounts {
		out.ActionCounts[k] = v
	}
	return out
}

func (i *IDS) Subscribe(buffer int) (<-chan Notification, func()) {
	if buffer < 1 {
		buffer = 1
	}
	ch := make(chan Notification, buffer)
	i.subscribeMu.Lock()
	i.subscribers = append(i.subscribers, ch)
	i.subscribeMu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			i.subscribeMu.Lock()
			defer i.subscribeMu.Unlock()
			for j, c := range i.subscribers {
				if c == ch {
					i.subscribers = append(i.subscribers[:j], i.subscribers[j+1:]...)
					close(ch)
					return
				}
			}
		})
	}
	return ch, cancel
}

func (i *IDS) fanOut(n Notification) {
	i.subscribeMu.RLock()
	subs := make([]chan Notification, len(i.subscribers))
	copy(subs, i.subscribers)
	i.subscribeMu.RUnlock()
	for _, s := range subs {
		select {
		case s <- n:
		default:

		}
	}
}
