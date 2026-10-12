package main

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"haoma-frontend/internal/backuparchive"
	"haoma-frontend/internal/pair"
)

const emptyRetry = 10 * time.Second

type emptyMarker struct {
	root string

	mu      sync.Mutex
	grown   bool
	cleared bool
}

func newEmptyMarker(root, dataDir string) (*emptyMarker, error) {
	m := &emptyMarker{root: root}
	dir := root
	if dir == "" {
		if dataDir == "" {
			return m, nil
		}
		dir = filepath.Dir(dataDir)
	}
	if err := backuparchive.ClearDeviceEmpty(dir); err != nil {
		return nil, fmt.Errorf("device-empty: the file of an earlier run could not be removed: %w", err)
	}
	return m, nil
}

func (m *emptyMarker) grow(what string) error {
	if m == nil || m.root == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.grown = true
	if m.cleared {
		return nil
	}
	if err := backuparchive.ClearDeviceEmpty(m.root); err != nil {
		slog.Error("device-empty: the file could not be removed; the write is refused",
			slog.String("adds", what), slog.Any("err", err))
		return fmt.Errorf("device-empty: %w", err)
	}
	m.cleared = true
	slog.Debug("device-empty: removed before a write", slog.String("adds", what))
	return nil
}

func (d *daemon) wireEmptyMarker() (unwire func()) {
	d.chats.SetBeforeGrow(func() error { return d.emptyMarker.grow("a chat") })
	d.linkedDevices.SetBeforeGrow(func() error { return d.emptyMarker.grow("a linked device") })
	pair.SetBeforeRegister(func() error { return d.emptyMarker.grow("a contact") })
	return func() { pair.SetBeforeRegister(nil) }
}

func (d *daemon) startEmptyCheck(ctx context.Context, launch func(func())) {
	launch(func() { d.emptyMarker.watch(ctx, d.deviceLinkJoinerPrecheck) })
}

type emptyCheck func(ctx context.Context) (reason, detail string, ok bool)

func (m *emptyMarker) markIfEmpty(ctx context.Context, check emptyCheck) bool {
	reason, detail, ok := check(ctx)
	if !ok {
		if reason == reasonPeerCheckFailed {
			slog.Debug("device-empty: the rule could not be read; trying again", slog.String("detail", detail))
			return false
		}
		slog.Debug("device-empty: not empty", slog.String("reason", reason))
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.grown {
		return true
	}
	m.cleared = false
	if err := backuparchive.MarkDeviceEmpty(m.root); err != nil {
		slog.Warn("device-empty: the file could not be written; a restore is refused until it is",
			slog.Any("err", err))
		return true
	}
	slog.Info("device-empty: this device holds no contact, no chat and no linked device; a restore may run onto it")
	return true
}

func (m *emptyMarker) watch(ctx context.Context, check emptyCheck) {
	if m == nil || m.root == "" {
		return
	}
	for !m.markIfEmpty(ctx, check) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(emptyRetry):
		}
	}
}
