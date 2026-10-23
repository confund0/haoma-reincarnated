package main

import (
	"sync"
	"time"
)

type stallState int

const (
	stateForward stallState = iota

	stateStalled

	stateDraining
)

type stallController struct {
	mu sync.Mutex

	stallEvery time.Duration
	stallFor   time.Duration
	capBps     int64
	bufCap     int

	log func(format string, args ...interface{})

	state          stallState
	backlog        []byte
	stallEndAt     time.Time
	nextPeriodicAt time.Time

	lastDrainAt time.Time
	drainBudget float64
}

func newStallController(stallEvery, stallFor time.Duration, capBps int64, bufCap int, start time.Time, log func(format string, args ...interface{})) *stallController {
	c := &stallController{
		stallEvery: stallEvery,
		stallFor:   stallFor,
		capBps:     capBps,
		bufCap:     bufCap,
		log:        log,
		state:      stateForward,
	}
	if stallEvery > 0 {
		c.nextPeriodicAt = start.Add(stallEvery)
	}
	return c
}

func (c *stallController) logf(format string, args ...interface{}) {
	if c.log != nil {
		c.log(format, args...)
	}
}

func (c *stallController) Trigger(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == stateStalled {
		c.logf("stall: manual trigger ignored, a stall is already active")
		return
	}
	c.beginStall(now, "manual")
}

func (c *stallController) beginStall(now time.Time, reason string) {
	c.state = stateStalled
	c.stallEndAt = now.Add(c.stallFor)
	c.logf("stall: starting (%s), buffering for %s", reason, c.stallFor)
}

func (c *stallController) checkPeriodic(now time.Time) {
	if c.stallEvery <= 0 || c.nextPeriodicAt.IsZero() {
		return
	}
	if now.Before(c.nextPeriodicAt) {
		return
	}
	if c.state == stateStalled {
		c.nextPeriodicAt = now.Add(c.stallEvery)
		return
	}
	c.beginStall(now, "periodic")
	c.nextPeriodicAt = now.Add(c.stallEvery)
}

func (c *stallController) push(data []byte) {
	if len(data) == 0 {
		return
	}
	c.backlog = append(c.backlog, data...)
	if over := len(c.backlog) - c.bufCap; over > 0 {
		c.backlog = c.backlog[over:]
	}
}

func (c *stallController) Submit(now time.Time, data []byte) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.checkPeriodic(now)
	c.push(data)

	switch c.state {
	case stateStalled:
		if now.Before(c.stallEndAt) {
			return nil
		}
		c.logf("stall: ended, draining backlog of %d bytes capped at %d bytes/sec", len(c.backlog), c.capBps)
		c.state = stateDraining
		c.lastDrainAt = now
		c.drainBudget = 0
		return c.drain(now)
	case stateDraining:
		return c.drain(now)
	default:
		if len(c.backlog) == 0 {
			return nil
		}
		out := c.backlog
		c.backlog = nil
		return out
	}
}

func (c *stallController) drain(now time.Time) []byte {
	elapsed := now.Sub(c.lastDrainAt).Seconds()
	c.lastDrainAt = now
	if elapsed > 0 {
		c.drainBudget += elapsed * float64(c.capBps)
	}
	allowed := int(c.drainBudget)
	if allowed > len(c.backlog) {
		allowed = len(c.backlog)
	}
	if allowed <= 0 {
		return nil
	}
	out := make([]byte, allowed)
	copy(out, c.backlog[:allowed])
	c.backlog = c.backlog[allowed:]
	c.drainBudget -= float64(allowed)

	if len(c.backlog) == 0 {
		c.state = stateForward
		c.drainBudget = 0
		c.logf("stall: backlog drained, back to full speed")
	}
	return out
}
