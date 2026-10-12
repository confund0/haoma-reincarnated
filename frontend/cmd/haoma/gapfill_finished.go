package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"haoma-frontend/internal/ipc"
)

const (
	manualSyncIdleBound = 2 * time.Minute

	manualSyncCap = 10 * time.Minute

	manualSyncGateBound = 30 * time.Second

	gatePoll = 50 * time.Millisecond
)

type manualSyncTracker struct {
	idle, cap, gate time.Duration

	mu  sync.Mutex
	cur *manualSweep
}

type sweepPhase int

const (
	phaseSnapshot sweepPhase = iota

	phaseJournal

	phaseAnswered

	phaseUnreachable
)

func (p sweepPhase) waitedFor() bool { return p == phaseSnapshot || p == phaseJournal }

type manualSweep struct {
	full        bool
	phases      map[string]sweepPhase
	answered    int
	unreachable int
	rows        int
	snapshot    int
	held        int
	started     time.Time
	lastHeard   time.Time

	done chan struct{}
}

func (m *manualSyncTracker) idleBound() time.Duration {
	if m.idle > 0 {
		return m.idle
	}
	return manualSyncIdleBound
}

func (m *manualSyncTracker) capBound() time.Duration {
	if m.cap > 0 {
		return m.cap
	}
	return manualSyncCap
}

func (m *manualSyncTracker) gateBound() time.Duration {
	if m.gate > 0 {
		return m.gate
	}
	return manualSyncGateBound
}

func (m *manualSyncTracker) waiting() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cur != nil
}

func enterGateWithin(ctx context.Context, g *gapfillGate, bound time.Duration) bool {
	if g.enter() {
		return true
	}
	deadline := time.NewTimer(bound)
	defer deadline.Stop()
	tick := time.NewTicker(gatePoll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return g.enter()
		case <-tick.C:
			if g.enter() {
				return true
			}
		}
	}
}

func (m *manualSyncTracker) begin(sibs []string, full bool) *manualSweep {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur != nil {
		return nil
	}
	now := time.Now()
	sw := &manualSweep{
		full:      full,
		phases:    make(map[string]sweepPhase, len(sibs)),
		started:   now,
		lastHeard: now,
		done:      make(chan struct{}),
	}
	for _, s := range sibs {
		sw.phases[s] = phaseSnapshot
	}
	m.cur = sw
	return sw
}

func (m *manualSyncTracker) tracked(sib string, full bool) *manualSweep {
	sw := m.cur
	if sw == nil {
		return nil
	}
	if _, ok := sw.phases[sib]; !ok {
		return nil
	}
	if sw.full && !full {
		return nil
	}
	return sw
}

func (m *manualSyncTracker) snapshot(sib string, full bool, n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sw := m.tracked(sib, full)
	if sw == nil || !sw.phases[sib].waitedFor() {
		return
	}
	sw.lastHeard = time.Now()
	if sw.phases[sib] == phaseSnapshot {
		sw.snapshot += n
	}
}

func (m *manualSyncTracker) journalAsked(sib string, full bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sw := m.tracked(sib, full)
	if sw == nil || sw.phases[sib] != phaseSnapshot {
		return
	}
	sw.lastHeard = time.Now()
	sw.phases[sib] = phaseJournal
}

func (m *manualSyncTracker) batch(d *daemon, sib string, full, more bool, applied, held int) {
	m.mu.Lock()
	sw := m.tracked(sib, full)
	if sw == nil || !sw.phases[sib].waitedFor() {

		m.mu.Unlock()
		return
	}
	sw.lastHeard = time.Now()
	if sw.phases[sib] != phaseJournal {

		m.mu.Unlock()
		return
	}
	sw.rows += applied
	sw.held += held
	if more {
		m.mu.Unlock()
		return
	}
	sw.phases[sib] = phaseAnswered
	sw.answered++
	m.endIfDoneLocked(d, sw)
}

func (m *manualSyncTracker) sendFailed(d *daemon, sib string, full bool) {
	m.mu.Lock()
	sw := m.tracked(sib, full)
	if sw == nil || !sw.phases[sib].waitedFor() {
		m.mu.Unlock()
		return
	}
	sw.phases[sib] = phaseUnreachable
	sw.unreachable++
	slog.Debug("gapfill: manual sweep stops waiting for a device its request did not reach",
		slog.String("sibling_peer_id", sib),
	)
	m.endIfDoneLocked(d, sw)
}

func (m *manualSyncTracker) endIfDoneLocked(d *daemon, sw *manualSweep) {
	if sw.answered+sw.unreachable < len(sw.phases) {
		m.mu.Unlock()
		return
	}
	out := m.endLocked(sw, false)
	m.mu.Unlock()
	pushSyncFinished(d, out)
}

func (m *manualSyncTracker) endLocked(sw *manualSweep, timedOut bool) ipc.DeviceSyncFinishedPush {
	m.cur = nil
	close(sw.done)
	return ipc.DeviceSyncFinishedPush{
		Full:            sw.full,
		Siblings:        len(sw.phases),
		Answered:        sw.answered,
		Unreachable:     sw.unreachable,
		RowsApplied:     sw.rows,
		SnapshotApplied: sw.snapshot,
		Held:            sw.held,
		TimedOut:        timedOut,
	}
}

func (m *manualSyncTracker) wait(ctx context.Context, d *daemon, sw *manualSweep) {
	for {
		m.mu.Lock()
		if m.cur != sw {
			m.mu.Unlock()
			return
		}
		deadline := sw.lastHeard.Add(m.idleBound())
		if hard := sw.started.Add(m.capBound()); hard.Before(deadline) {
			deadline = hard
		}
		m.mu.Unlock()

		timer := time.NewTimer(time.Until(deadline))
		select {
		case <-sw.done:
			timer.Stop()
			return
		case <-ctx.Done():
			timer.Stop()
			m.mu.Lock()
			if m.cur == sw {
				m.cur = nil
				close(sw.done)
			}
			m.mu.Unlock()
			slog.Debug("gapfill: manual sweep no longer waited for — the daemon is stopping")
			return
		case <-timer.C:
		}

		m.mu.Lock()
		if m.cur != sw {
			m.mu.Unlock()
			return
		}
		now := time.Now()
		if now.Before(sw.lastHeard.Add(m.idleBound())) && now.Before(sw.started.Add(m.capBound())) {

			m.mu.Unlock()
			continue
		}
		out := m.endLocked(sw, true)
		m.mu.Unlock()
		pushSyncFinished(d, out)
		return
	}
}

func pushSyncFinished(d *daemon, out ipc.DeviceSyncFinishedPush) {
	slog.Info("gapfill: manual sweep finished",
		slog.Bool("full", out.Full),
		slog.Int("siblings", out.Siblings),
		slog.Int("answered", out.Answered),
		slog.Int("unreachable", out.Unreachable),
		slog.Int("rows_applied", out.RowsApplied),
		slog.Int("snapshot_applied", out.SnapshotApplied),
		slog.Int("held", out.Held),
		slog.Bool("timed_out", out.TimedOut),
	)
	if d == nil || d.ipcSrv == nil {
		return
	}
	push(d.ipcSrv, ipc.FrameDeviceSyncFinished, "", out)
}
