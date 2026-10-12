package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"haoma/internal/tor/control"
)

const networkChangeWindow = 60 * time.Second

const (
	networkChangeSourceTor = "tor"
	networkChangeSourceOS  = "os"
)

const (
	networkChangeKickedLine  = "network change: kicked"
	networkChangeDroppedLine = "network change: dropped inside the window"
	livenessDownLine         = "tor network liveness: down"
	livenessUpLine           = "tor network liveness: up after down"
)

type networkChangeGate struct {
	mu     sync.Mutex
	last   time.Time
	source string
}

type networkChangeStamp struct {
	at, prevAt time.Time
	prevSource string
}

func (g *networkChangeGate) take(now time.Time, source string) (ok bool, since time.Duration, stamp networkChangeStamp) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.last.IsZero() && now.Sub(g.last) < networkChangeWindow &&
		!(source == networkChangeSourceTor && g.source == networkChangeSourceOS) {
		return false, now.Sub(g.last), networkChangeStamp{}
	}
	stamp = networkChangeStamp{at: now, prevAt: g.last, prevSource: g.source}
	g.last, g.source = now, source
	return true, 0, stamp
}

func (g *networkChangeGate) giveBack(stamp networkChangeStamp) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.last.Equal(stamp.at) {
		g.last, g.source = stamp.prevAt, stamp.prevSource
	}
}

type networkChangeResult struct {
	Dropped      bool `json:"dropped"`
	Rows         int  `json:"rows"`
	Destinations int  `json:"destinations"`
	Fetches      int  `json:"fetches"`

	Failed bool `json:"failed,omitempty"`

	ResetFailed bool `json:"reset_failed,omitempty"`
}

func (d *daemon) networkChanged(ctx context.Context, source string) networkChangeResult {
	return d.networkChangedAt(ctx, source, time.Now())
}

func (d *daemon) networkChangedAt(ctx context.Context, source string, now time.Time) networkChangeResult {
	ok, since, stamp := d.netChange.take(now, source)
	if !ok {
		slog.Info(networkChangeDroppedLine,
			slog.String("source", source),
			slog.Duration("since_last", since.Round(time.Second)),
			slog.Duration("window", networkChangeWindow),
		)
		return networkChangeResult{Dropped: true}
	}
	var res networkChangeResult
	resetRan := false
	if source == networkChangeSourceOS {
		resetRan, res.ResetFailed = d.torNetworkReset()
	}
	if d.worker != nil {
		rows, dests, err := d.worker.KickAll(d.liveDests())
		if err != nil {
			res.Failed = true
			slog.Warn("network change: outbox kick failed",
				slog.String("source", source),
				slog.Any("err", err),
			)
		}
		res.Rows, res.Destinations = rows, dests
	}
	if fw := d.fetchWorker.Load(); fw != nil {
		n, err := fw.RetryAllFailedKicked(ctx, d.peerRetired)
		if err != nil {
			res.Failed = true
			slog.Warn("network change: fetch retry failed",
				slog.String("source", source),
				slog.Any("err", err),
			)
		}
		res.Fetches = n
	} else {

		slog.Debug("network change: no fetch worker yet", slog.String("source", source))
	}
	if resetRan {

		d.scheduleResetRetry()
	}
	if res.Failed {
		d.netChange.giveBack(stamp)
	}
	slog.Info(networkChangeKickedLine,
		slog.String("source", source),
		slog.Int("rows", res.Rows),
		slog.Int("destinations", res.Destinations),
		slog.Int("fetches", res.Fetches),
		slog.Bool("failed", res.Failed),
		slog.Bool("reset_failed", res.ResetFailed),
	)
	return res
}

func (d *daemon) peerRetired(peerID string) bool {
	p, err := d.registry.Get(peerID)
	if err != nil {
		return false
	}
	return p.RetiredAt != 0
}

func (d *daemon) liveDests() func(dest string) bool {
	if d.registry == nil {
		return nil
	}
	list, err := d.registry.List()
	if err != nil {
		slog.Warn("network change: peer list unreadable; kicking every destination", slog.Any("err", err))
		return nil
	}
	live := make(map[string]struct{})
	for i := range list {
		if list[i].RetiredAt != 0 {
			continue
		}
		for _, dest := range peerDests(&list[i]) {
			live[dest] = struct{}{}
		}
	}
	return func(dest string) bool {
		_, ok := live[dest]
		return ok
	}
}

func (d *daemon) handleNetworkChanged(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, 4<<10))
	res := d.networkChanged(r.Context(), networkChangeSourceOS)
	code := http.StatusOK
	if res.Failed {
		code = http.StatusInternalServerError
	}
	writeJSON(w, code, res)
}

type networkLiveness struct {
	mu   sync.Mutex
	down bool

	ownReset   bool
	quietUntil time.Time

	seen int
}

func (l *networkLiveness) ownResetStart() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ownReset = true
}

func (l *networkLiveness) ownResetEnd(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ownReset = false
	l.quietUntil = now.Add(livenessOwnResetQuiet)
}

func (l *networkLiveness) observe(status string) bool {
	fire, _ := l.observeAt(status, time.Now())
	return fire
}

func (l *networkLiveness) observeAt(status string, now time.Time) (fire, own bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen++
	if l.ownReset || now.Before(l.quietUntil) {
		return false, true
	}
	switch status {
	case "DOWN":
		l.down = true
	case "UP":
		if l.down {
			l.down = false
			return true, false
		}
	}
	return false, false
}

func livenessStatus(ev control.Event) (status string, ok bool) {
	if ev.Type != "NETWORK_LIVENESS" || len(ev.Lines) == 0 {
		return "", false
	}
	fields := strings.Fields(ev.Lines[0])
	if len(fields) < 2 {
		return "", false
	}
	switch s := strings.ToUpper(fields[1]); s {
	case "UP", "DOWN":
		return s, true
	}
	return "", false
}

func (d *daemon) watchNetworkLiveness(ctx context.Context, conn *control.Conn) {
	if conn == nil {
		return
	}

	if err := conn.SetEvents("NETWORK_LIVENESS"); err != nil {
		slog.Warn("tor network liveness: subscribe failed", slog.Any("err", err))
		return
	}
	slog.Debug("tor network liveness: subscribed")
	go d.readNetworkLiveness(ctx, conn)
}

func (d *daemon) readNetworkLiveness(ctx context.Context, conn *control.Conn) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-conn.Done():
			return
		case ev := <-conn.Events():
			d.onLivenessEvent(ctx, ev)
		}
	}
}

func (d *daemon) onLivenessEvent(ctx context.Context, ev control.Event) {
	status, ok := livenessStatus(ev)
	if !ok {
		return
	}
	fire, own := d.liveness.observeAt(status, time.Now())
	if own {
		slog.Debug("tor network liveness: ignored; this daemon's own network reset",
			slog.String("status", status))
		return
	}
	if !fire {
		if status == "DOWN" {
			slog.Info(livenessDownLine)
		} else {
			slog.Debug("tor network liveness: up, with no down before it")
		}
		return
	}
	slog.Info(livenessUpLine)
	d.networkChanged(ctx, networkChangeSourceTor)
}
