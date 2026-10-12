package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/ipc"
)

const stateSessionTowardB = "session-toward-b"

var warmUpProvides = map[string]bool{stateSessionTowardB: true}

const (
	warmUpSyncTimeout = 5 * time.Minute

	warmUpQuietWindow = 60 * time.Second

	warmUpQuietTimeout = 5 * time.Minute

	warmUpBoundWait = 90*time.Second + warmUpQuietWindow

	warmUpSettleTimeout = 15 * time.Minute
)

const (
	triggerArmedLine = "synctrigger: the journal grew; a sweep is armed"
	triggerFiredLine = "synctrigger: journal growth started"
)

func siblingWarmUp(f *siblingFleet) error {
	devA, devB, peer := f.devA, f.devB, f.peer

	logf("[warm-up] one text each way on each pairing, dev-b first")
	if err := warmUpMyText(devB, devA, peer, f.bSeesPeer); err != nil {
		return err
	}
	if err := warmUpMyText(devA, devB, peer, f.aSeesPeer); err != nil {
		return err
	}
	if err := warmUpPeerText(peer, devA, devB, f.peerSeesA); err != nil {
		return err
	}
	if err := warmUpPeerText(peer, devB, devA, f.peerSeesB); err != nil {
		return err
	}

	for _, r := range []*rig{devA, devB} {
		logf("[warm-up] a sync on %s, waited for until it finishes", r.name)
		if err := syncAndAwaitFinished(r, warmUpSyncTimeout); err != nil {
			return fmt.Errorf("the sync on %s did not finish: %w", r.name, err)
		}
	}

	rigs := []*rig{devA, devB, peer}
	deadline := time.Now().Add(warmUpSettleTimeout)
	for pass := 1; ; pass++ {
		if err := awaitOutboxesEmpty(rigs, outboxSettleTimeout(peer)); err != nil {
			return err
		}
		if err := warmUpQuiet(devA, devB); err != nil {
			return err
		}
		queued, err := outboxesQueued(rigs)
		if err != nil {
			return err
		}
		if len(queued) == 0 {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the outboxes and the sync sweeps never settled in the same pass: after %d pass(es) the quiet wait still left %s",
				pass, strings.Join(queued, "; "))
		}
		logf("[warm-up] pass %d: the quiet wait left %s — waiting for the outboxes and the quiet again", pass, strings.Join(queued, "; "))
	}

	mark, err := gapFillMarkOf(devB)
	if err != nil {
		return fmt.Errorf("could not read %s's gap-fill batches after the warm-up: %w", devB.name, err)
	}
	f.gapFillSince = mark
	logf("[warm-up] done; %s's gap-fill stands at %d batch(es), %d row(s), and gapfill counts from there", devB.name, mark.batches, mark.applied)
	return nil
}

func warmUpQuiet(devA, devB *rig) error {
	if err := devicesAtDebugLevel(devA, devB); err != nil {
		logf("[warm-up] the trigger's lines are not readable (%v) — waiting the trigger's documented bound, %s, instead of watching it", err, warmUpBoundWait)
		rigSleep(warmUpBoundWait, "warm-up: the trigger's documented bound")
		return nil
	}
	return awaitSweepsQuiet([]*rig{devA, devB}, warmUpQuietWindow, warmUpQuietTimeout)
}

func warmUpMyText(origin, sibling, peer *rig, originSeesPeer string) error {
	_, peerErr, mirrorErr, _ := railText(origin, sibling, peer, originSeesPeer, "warm-up "+nextCorrID())
	if peerErr != nil {
		return fmt.Errorf("%s -> %s did not answer: %w", origin.name, peer.name, peerErr)
	}
	if mirrorErr != nil {
		return fmt.Errorf("the rail %s -> %s did not answer: %w", origin.name, sibling.name, mirrorErr)
	}
	return nil
}

func warmUpPeerText(peer, to, other *rig, peerSeesTo string) error {
	otherSub, cancel := other.subscribe()
	defer cancel()
	d, err := deliverText(peer, to, peerSeesTo, "warm-up "+nextCorrID())
	if err != nil {
		return fmt.Errorf("%s -> %s did not answer: %w", peer.name, to.name, err)
	}
	if _, err := awaitInboundText(otherSub, d.msgID, mirrorTimeout); err != nil {
		return fmt.Errorf("%s's fan-out to %s did not answer (the text to %s arrived): %w", peer.name, other.name, to.name, err)
	}
	return nil
}

func syncAndAwaitFinished(r *rig, timeout time.Duration) error {
	return syncAndAwaitFinishedWith(r, timeout, false)
}

func syncAndAwaitFinishedWith(r *rig, timeout time.Duration, full bool) error {
	sub, cancel := r.subscribe()
	defer cancel()
	id, err := r.sendReq(ipc.FrameDeviceSync, ipc.DeviceSyncRequest{Full: full})
	if err != nil {
		return fmt.Errorf("devicesync send: %w", err)
	}
	f, err := awaitFrame(sub, 30*time.Second, func(f ipc.Frame) bool {
		return f.ID == id && (f.Type == ipc.FrameDeviceSyncSent || f.Type == ipc.FrameError)
	})
	if err != nil {
		return fmt.Errorf("await devicesync_sent: %w", err)
	}
	if f.Type == ipc.FrameError {
		return frameErr("devicesync", f)
	}
	var sent ipc.DeviceSyncSentResponse
	if err := json.Unmarshal(f.Payload, &sent); err != nil {
		return fmt.Errorf("decode devicesync_sent: %w", err)
	}
	if sent.Siblings == 0 {
		return fmt.Errorf("%s reports no linked devices to sync with", r.name)
	}
	f, err = awaitFrame(sub, timeout, func(f ipc.Frame) bool { return f.Type == ipc.FrameDeviceSyncFinished })
	if err != nil {
		return fmt.Errorf("await devicesync.finished: %w", err)
	}
	var done ipc.DeviceSyncFinishedPush
	if err := json.Unmarshal(f.Payload, &done); err != nil {
		return fmt.Errorf("decode devicesync.finished: %w", err)
	}
	return syncFinishedErr(r.name, done)
}

func syncFinishedErr(name string, p ipc.DeviceSyncFinishedPush) error {
	switch {
	case p.TimedOut:
		return fmt.Errorf("%s stopped waiting: %d of %d linked device(s) answered", name, p.Answered, p.Siblings)
	case p.Unreachable > 0:
		return fmt.Errorf("%s could not reach %d of %d linked device(s)", name, p.Unreachable, p.Siblings)
	case p.Answered < p.Siblings:
		return fmt.Errorf("%s heard from %d of %d linked device(s)", name, p.Answered, p.Siblings)
	}
	logf("[%s] sync finished: %d linked device(s) answered, %d row(s) applied", name, p.Answered, p.RowsApplied)
	return nil
}

func outboxSettleTimeout(r *rig) time.Duration {
	if d, err := time.ParseDuration(r.giveUp); err == nil && r.giveUp != "" {
		return d + outboxGiveUpBaseSlack
	}
	return warmUpSyncTimeout
}

func awaitOutboxesEmpty(rigs []*rig, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		queued, err := outboxesQueued(rigs)
		if err != nil {
			return err
		}
		if len(queued) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("after %s the outboxes still hold %s — those routes did not answer", timeout, strings.Join(queued, "; "))
		}
		time.Sleep(syncPollInterval)
	}
}

func outboxesQueued(rigs []*rig) ([]string, error) {
	var out []string
	for _, r := range rigs {
		rows, err := outboxQueued(r)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			continue
		}
		ids := make([]string, len(rows))
		for i, row := range rows {
			ids[i] = fmt.Sprintf("%s (%d attempt(s))", short(row.EnvelopeID), row.Attempts)
		}
		out = append(out, fmt.Sprintf("%s: %d envelope(s) %s", r.name, len(rows), strings.Join(ids, ",")))
	}
	return out, nil
}

func outboxQueued(r *rig) ([]backendapi.OutboxRow, error) {
	c, err := backendClientFor(r)
	if err != nil {
		return nil, fmt.Errorf("could not reach %s's haomad: %w", r.name, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := c.Outbox(ctx, outboxStateEnqueued, 0, outboxListLimit)
	if err != nil {
		return nil, fmt.Errorf("could not list %s's outbox: %w", r.name, err)
	}
	return stillQueued(resp.Rows), nil
}

const outboxStateEnqueued = "enqueued"

const outboxListLimit = 1000

func stillQueued(rows []backendapi.OutboxRow) []backendapi.OutboxRow {
	var out []backendapi.OutboxRow
	for _, row := range rows {
		if row.State == outboxStateEnqueued {
			out = append(out, row)
		}
	}
	return out
}

func awaitSweepsQuiet(rigs []*rig, window, timeout time.Duration) error {
	logf("[warm-up] waiting until no sync sweep is armed on %d device(s) and nothing moved for %s", len(rigs), window)
	deadline := time.Now().Add(timeout)
	lastCount := map[string]int{}
	quietSince := time.Now()
	for {
		moved := false
		var armedOn []string
		for _, r := range rigs {
			lines, err := rigLogLines(r)
			if err != nil {
				return fmt.Errorf("could not read %s's log: %w", r.name, err)
			}
			armed, count := sweepState(lines)
			if armed {
				armedOn = append(armedOn, r.name)
			}
			if count != lastCount[r.name] {
				moved = true
				lastCount[r.name] = count
			}
		}
		if moved || len(armedOn) > 0 {
			quietSince = time.Now()
		}
		if time.Since(quietSince) >= window {
			logf("[warm-up] quiet: no sweep armed and nothing moved for %s", window)
			return nil
		}
		if time.Now().After(deadline) {
			if len(armedOn) > 0 {
				return fmt.Errorf("a sync sweep was still armed on %s after %s", strings.Join(armedOn, ","), timeout)
			}
			return fmt.Errorf("the trigger and gap-fill lines kept moving on my devices for %s — they never went quiet", timeout)
		}
		time.Sleep(syncPollInterval)
	}
}

func sweepState(lines []string) (armed bool, count int) {
	lastArmed, lastFired := -1, -1
	for i, line := range lines {
		switch {
		case hasLogMsg(line, triggerArmedLine):
			lastArmed = i
		case strings.Contains(line, `msg="`+triggerFiredLine):
			lastFired = i
		}
		if strings.Contains(line, `msg="synctrigger:`) || strings.Contains(line, `msg="gapfill:`) {
			count++
		}
	}
	return lastArmed > lastFired, count
}
