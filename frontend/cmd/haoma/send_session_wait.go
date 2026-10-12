package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"go.mau.fi/libsignal/protocol"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/pair"
)

var sendSessionTimeout = 60 * time.Second

const noSessionYetDetail = "No secure session with this contact yet. Try again in a moment."

var errNoSessionYet error = noSessionYetError{}

type noSessionYetError struct{}

func (noSessionYetError) Error() string { return noSessionYetDetail }

type sendQueue struct {
	mu   sync.Mutex
	tail map[string]chan struct{}
}

type sendPlace struct {
	q    *sendQueue
	key  string
	prev <-chan struct{}
	mine chan struct{}
	once sync.Once
}

func (q *sendQueue) take(key string) *sendPlace {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.tail == nil {
		q.tail = make(map[string]chan struct{})
	}
	p := &sendPlace{q: q, key: key, mine: make(chan struct{})}
	if t, ok := q.tail[key]; ok {
		p.prev = t
	}
	q.tail[key] = p.mine
	return p
}

func (p *sendPlace) release() {
	p.once.Do(func() {
		leave := func() {
			p.q.mu.Lock()
			if p.q.tail[p.key] == p.mine {
				delete(p.q.tail, p.key)
			}
			p.q.mu.Unlock()
			close(p.mine)
		}
		if p.prev == nil {
			leave()
			return
		}
		select {
		case <-p.prev:
			leave()
		default:
			go func() {
				<-p.prev
				leave()
			}()
		}
	})
}

type sendPlaceKey struct{}

func takeSendPlace(ctx context.Context, d *daemon, f ipc.Frame) (context.Context, *sendPlace) {
	switch f.Type {
	case ipc.FrameSendText, ipc.FrameSendFile, ipc.FrameSendEdit, ipc.FrameSendDelete, ipc.FrameSendReaction:
	default:
		return ctx, nil
	}
	var req struct {
		PeerID string `json:"peer_id"`
	}
	if err := json.Unmarshal(f.Payload, &req); err != nil || req.PeerID == "" {

		return ctx, nil
	}
	p := d.sendQueue.take(req.PeerID)
	return context.WithValue(ctx, sendPlaceKey{}, p), p
}

func awaitSendSession(ctx context.Context, d *daemon, peerID string, chatID chat.ChatID) (dc *chat.DirectChat, release func(), err error) {
	arrived := time.Now()
	place, _ := ctx.Value(sendPlaceKey{}).(*sendPlace)
	if place == nil {
		place = d.sendQueue.take(peerID)
	}
	release = place.release

	if place.prev != nil {
		select {
		case <-place.prev:
		case <-ctx.Done():
			release()
			return nil, nil, ctx.Err()
		}
	}

	deadline := time.NewTimer(time.Until(arrived.Add(sendSessionTimeout)))
	defer deadline.Stop()
	waited := false
	for {
		wake := d.sessionsChanged.wait()
		dc, err := directChat(d, chatID)
		if err != nil {
			release()
			return nil, nil, err
		}
		if contactHoldsSession(ctx, d, dc) {
			if waited {
				slog.Info("send: a session with the contact is up; the send goes out now",
					slog.String("chat_id", string(chatID)),
					slog.Duration("waited", time.Since(arrived).Round(time.Millisecond)),
				)
			}
			return dc, release, nil
		}
		if !waited {
			slog.Info("send: no device of the contact holds a session yet; the send waits for one",
				slog.String("chat_id", string(chatID)),
				slog.String("reason", "no_session_yet"),
				slog.Duration("timeout", sendSessionTimeout),
			)
			waited = true
		}
		select {
		case <-wake:
		case <-deadline.C:
			release()
			slog.Warn("send: no device of the contact held a session before the wait ran out",
				slog.String("chat_id", string(chatID)),
				slog.String("reason", "no_session"),
				slog.Duration("waited", time.Since(arrived).Round(time.Millisecond)),
			)
			return nil, nil, errNoSessionYet
		case <-ctx.Done():
			release()
			return nil, nil, ctx.Err()
		}
	}
}

func sendWaitErrorCode(err error) string {
	if errors.Is(err, errNoSessionYet) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "encrypt_failed"
	}
	return "internal"
}

func directChat(d *daemon, chatID chat.ChatID) (*chat.DirectChat, error) {
	c, err := d.chats.Get(chatID)
	if err != nil {
		return nil, fmt.Errorf("read chat %s: %w", chatID, err)
	}
	dc, ok := c.(*chat.DirectChat)
	if !ok {
		return nil, fmt.Errorf("chat %s is not a direct chat", chatID)
	}
	return dc, nil
}

func contactHoldsSession(ctx context.Context, d *daemon, dc *chat.DirectChat) bool {
	targets := dc.FanoutPeerIDs()
	if len(targets) == 0 || d.stores == nil {
		return true
	}
	for _, peerID := range targets {
		has, err := d.stores.ContainsSession(ctx, protocol.NewSignalAddress(peerID, pair.DeviceID))
		if err != nil {
			slog.Debug("send: session check failed; the send goes on without the wait",
				slog.String("peer_id", peerID),
				slog.Any("err", err),
			)
			return true
		}
		if has {
			return true
		}
	}
	return false
}
