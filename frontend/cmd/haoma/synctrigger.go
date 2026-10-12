package main

import (
	"context"
	"log/slog"
	"time"
)

type syncTriggerTiming struct {
	debounce time.Duration

	ceiling time.Duration

	minInterval time.Duration
}

var defaultSyncTriggerTiming = syncTriggerTiming{
	debounce:    5 * time.Second,
	ceiling:     30 * time.Second,
	minInterval: 60 * time.Second,
}

type syncTrigger struct {
	signal chan struct{}

	timing syncTriggerTiming

	sweep func(context.Context) sweepOutcome

	now func() time.Time
}

func newSyncTrigger(sweep func(context.Context) sweepOutcome) *syncTrigger {
	return &syncTrigger{
		signal: make(chan struct{}, 1),
		timing: defaultSyncTriggerTiming,
		sweep:  sweep,
		now:    time.Now,
	}
}

func (t *syncTrigger) note() {
	if t == nil {
		return
	}
	select {
	case t.signal <- struct{}{}:
	default:

	}
}

func sweepDelay(now, firstGrowth, lastSweep time.Time, timing syncTriggerTiming) (time.Duration, string) {

	due := now.Add(timing.debounce)
	reason := "debounce"

	if !firstGrowth.IsZero() {
		if capped := firstGrowth.Add(timing.ceiling); capped.Before(due) {
			due = capped
			reason = "ceiling"
		}
	}

	if !lastSweep.IsZero() {
		if floor := lastSweep.Add(timing.minInterval); floor.After(due) {
			due = floor
			reason = "minimum_interval"
		}
	}

	wait := due.Sub(now)
	if wait < 0 {
		wait = 0
	}
	return wait, reason
}

func (t *syncTrigger) run(ctx context.Context) {

	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()

	var firstGrowth, lastSweep time.Time

	for {
		select {
		case <-ctx.Done():
			return

		case <-t.signal:
			now := t.now()
			if firstGrowth.IsZero() {
				firstGrowth = now
			}
			wait, reason := sweepDelay(now, firstGrowth, lastSweep, t.timing)
			resetTimer(timer, wait)
			slog.Debug("synctrigger: the journal grew; a sweep is armed",
				slog.Duration("wait", wait),
				slog.String("held_by", reason),
			)

		case <-timer.C:
			waited := t.now().Sub(firstGrowth)
			firstGrowth = time.Time{}

			lastSweep = t.now()

			switch outcome := t.sweep(ctx); outcome {
			case sweepStarted:
				slog.Debug("synctrigger: journal growth started a sweep",
					slog.Duration("waited", waited),
				)
			case sweepNoSiblings:
				slog.Debug("synctrigger: journal growth started no sweep — this device has no sibling to ask")
			case sweepAlreadyRunning:
				slog.Debug("synctrigger: journal growth started no sweep — one was already running; this growth rides the next one",
					slog.Duration("waited", waited),
				)
			default:
				slog.Warn("synctrigger: the sweep reported an outcome this build does not know",
					slog.String("outcome", string(outcome)),
				)
			}
		}
	}
}

func resetTimer(timer *time.Timer, d time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(d)
}
