package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"haoma/internal/tor/control"
)

const (
	torNetworkResetLine       = "network change: tor network reset"
	torNetworkResetFailedLine = "network change: tor network reset failed"
)

const livenessOwnResetQuiet = 10 * time.Second

var errNoControlConn = errors.New("no tor control connection")

func (d *daemon) socksAddr() string {
	if p := d.socks.Load(); p != nil {
		return *p
	}
	return d.cfg.torSocks
}

func (d *daemon) setSocksAddr(addr string) {
	d.socks.Store(&addr)
}

func (d *daemon) torNetworkReset() (ran, failed bool) {
	if d.cfg.manageTor == "" {
		slog.Debug("network change: no tor network reset; this tor is not haomad's own")
		return false, false
	}
	start := time.Now()
	moved, err := d.resetTorNetwork()
	took := time.Since(start)
	if err != nil {
		slog.Warn(torNetworkResetFailedLine,
			slog.Int64("took_ms", took.Milliseconds()),
			slog.Any("err", err),
		)
		return false, true
	}
	slog.Info(torNetworkResetLine,
		slog.Int64("took_ms", took.Milliseconds()),
		slog.Bool("socks_moved", moved),
	)
	return true, false
}

var resetRetryDelay = 30 * time.Second

func (d *daemon) scheduleResetRetry() {
	parent := d.bgCtx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	d.resetRetryMu.Lock()
	if d.resetRetryCancel != nil {
		d.resetRetryCancel()
	}
	d.resetRetryCancel = cancel
	d.resetRetryMu.Unlock()
	delay := resetRetryDelay
	go func() {
		defer cancel()
		t := time.NewTimer(delay)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		fw := d.fetchWorker.Load()
		if fw == nil {
			return
		}
		n, err := fw.RetryAllFailedKicked(ctx, d.peerRetired)
		if err != nil {
			slog.Warn("network change: fetch retry after the tor network reset failed", slog.Any("err", err))
			return
		}
		slog.Info("network change: fetches retried after the tor network reset", slog.Int("fetches", n))
	}()
}

func (d *daemon) resetTorNetwork() (moved bool, err error) {
	d.ctrlMu.Lock()
	defer d.ctrlMu.Unlock()
	conn := d.ctrlConn
	if conn == nil {
		return false, errNoControlConn
	}
	orig, pinned, err := conn.PinSocksPort()
	if err != nil {
		return false, fmt.Errorf("pin the SOCKS port: %w", err)
	}
	if pinned != "" && d.socksPinnedFrom == "" {

		d.socksPinnedFrom = orig
	}
	d.liveness.ownResetStart()
	off, err := conn.ToggleNetwork()
	d.liveness.ownResetEnd(time.Now())
	d.torNetworkOff = off
	if off {
		slog.Error("network change: tor's network may be OFF after a failed reset; trying again with the SOCKS port un-pinned, then at each onion watchdog tick until tor takes it",
			slog.Any("err", err))
		d.restoreTorNetworkLocked()
	}
	if err != nil {
		return false, fmt.Errorf("toggle DisableNetwork: %w", err)
	}
	return d.followSocksListener(conn)
}

func (d *daemon) followSocksListener(conn *control.Conn) (moved bool, err error) {
	listeners, err := conn.SocksListeners()
	if err != nil {
		return false, fmt.Errorf("read the SOCKS listener: %w", err)
	}
	cur := d.socksAddr()
	switch {
	case slices.Contains(listeners, cur):
		return false, nil
	case len(listeners) == 1:
		d.setSocksAddr(listeners[0])
		slog.Warn("network change: tor's SOCKS listener moved; haomad dials the new address",
			slog.String("from", cur),
			slog.String("to", listeners[0]),
		)
		return true, nil
	default:
		return false, fmt.Errorf("SOCKS listeners %v, none at %s", listeners, cur)
	}
}

func (d *daemon) restoreTorNetwork() {
	d.ctrlMu.Lock()
	defer d.ctrlMu.Unlock()
	d.restoreTorNetworkLocked()
}

func (d *daemon) restoreTorNetworkLocked() {
	conn := d.ctrlConn
	if !d.torNetworkOff || conn == nil {
		return
	}
	if d.socksPinnedFrom == "" {
		if err := conn.SetConf("DisableNetwork", "0"); err != nil {
			slog.Warn("network change: tor's network is still off after a failed reset; trying again at the next watchdog tick",
				slog.Any("err", err))
			return
		}
		d.torNetworkOff = false
		slog.Info("network change: tor's network put back on after a failed reset")
		return
	}
	if err := conn.EnableNetworkUnpinned(d.socksPinnedFrom); err != nil {
		slog.Warn("network change: tor's network is still off after a failed reset, with the SOCKS port un-pinned too; trying again at the next watchdog tick",
			slog.Any("err", err))
		return
	}
	d.torNetworkOff = false
	d.socksPinnedFrom = ""
	moved, err := d.followSocksListener(conn)
	slog.Warn("network change: tor's network put back on with the SOCKS port un-pinned",
		slog.Bool("socks_moved", moved),
		slog.Any("err", err),
	)
}
