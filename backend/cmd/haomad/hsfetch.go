package main

import (
	"log/slog"
	"strings"
	"time"
)

func onionFromDest(dest string) string {
	s := dest
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "https://")
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexByte(s, ':'); i >= 0 {
		s = s[:i]
	}
	if !strings.HasSuffix(s, ".onion") {
		return ""
	}
	return strings.TrimSuffix(s, ".onion")
}

const hsfetchCooldown = 60 * time.Second

func (d *daemon) HsfetchPeer(onion string) {
	if onion == "" {
		return
	}
	d.hsfetchMu.Lock()
	if d.hsfetchLastAt == nil {
		d.hsfetchLastAt = make(map[string]time.Time)
	}
	now := time.Now()
	if last, ok := d.hsfetchLastAt[onion]; ok && now.Sub(last) < hsfetchCooldown {
		d.hsfetchMu.Unlock()
		slog.Debug("hsfetch cooldown — skip",
			slog.String("onion", onion),
			slog.Duration("since_last", now.Sub(last)),
		)
		return
	}
	d.hsfetchLastAt[onion] = now
	d.hsfetchMu.Unlock()

	d.ctrlMu.Lock()
	conn := d.ctrlConn
	d.ctrlMu.Unlock()
	if conn == nil {
		slog.Debug("hsfetch skipped — control conn not up",
			slog.String("onion", onion),
		)
		return
	}
	if err := conn.HsFetch(onion); err != nil {
		slog.Warn("hsfetch failed",
			slog.String("onion", onion),
			slog.Any("err", err),
		)
		return
	}
	slog.Info("hsfetch fired — refreshing descriptor",
		slog.String("onion", onion),
	)
}
