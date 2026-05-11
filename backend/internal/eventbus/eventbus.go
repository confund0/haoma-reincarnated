package eventbus

import (
	"log/slog"
	"strings"
	"sync"
)

const (
	TopicSystemIDSEvent       = "system.ids-event"
	TopicInboxReceived        = "inbox.received"
	TopicDeliveryStateChanged = "delivery.state-changed"
	TopicPeerPresenceChanged  = "peer.presence-changed"
	TopicPeerLastSeenChanged  = "peer.last-seen-changed"
	TopicPairOnionProbe       = "pair.onion-probe"

	TopicFileFetchStateChanged = "file.fetch-state-changed"
	TopicFileFetchProgress     = "file.fetch-progress"
)

type Event struct {
	Topic   string
	Payload any
}

type Bus struct {
	mu   sync.Mutex
	subs []*sub
}

type sub struct {
	prefix string
	ch     chan Event
}

func (b *Bus) Subscribe(prefix string, bufSize int) (<-chan Event, func()) {
	if bufSize < 1 {
		bufSize = 1
	}
	s := &sub{prefix: prefix, ch: make(chan Event, bufSize)}
	b.mu.Lock()
	b.subs = append(b.subs, s)
	b.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			for i, x := range b.subs {
				if x == s {
					b.subs = append(b.subs[:i], b.subs[i+1:]...)
					close(s.ch)
					return
				}
			}
		})
	}
	return s.ch, cancel
}

func (b *Bus) Publish(topic string, payload any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	matched := 0
	for _, s := range b.subs {
		if s.prefix != "" && !strings.HasPrefix(topic, s.prefix) {
			continue
		}
		matched++
		select {
		case s.ch <- Event{Topic: topic, Payload: payload}:
		default:
			slog.Warn("eventbus: subscriber dropped (buffer full)",
				slog.String("topic", topic),
				slog.String("prefix", s.prefix),
			)
		}
	}
	slog.Debug("eventbus publish",
		slog.String("topic", topic),
		slog.Int("subscribers", matched),
	)
}
