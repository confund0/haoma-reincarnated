package main

import (
	"encoding/json"
	"fmt"
	"time"

	"haoma-frontend/internal/ipc"
)

const seededSkewSeconds = 300

const clockStatusTimeout = 90 * time.Second

func assertClockWarning(dev *rig) map[string]error {
	out := map[string]error{}

	quiet, err := awaitClockSkew(dev, func(cs ipc.ClockSkew) bool {
		return cs.LimitSeconds > 0 && !cs.OverLimit
	})
	switch {
	case err != nil:
		out["clock reading reaches a client"] = fmt.Errorf(
			"%s never carried a clock reading with a limit on its status payload — the field is not wired: %w",
			dev.name, err)
	case !quiet.Known && quiet.Seconds != 0:

		out["clock reading reaches a client"] = fmt.Errorf(
			"%s reported %ds while marking the reading unknown — an unmeasured clock must carry no number",
			dev.name, quiet.Seconds)
	default:
		logf("[%s] clock reading present: %ds, known=%v, limit=%ds",
			dev.name, quiet.Seconds, quiet.Known, quiet.LimitSeconds)
		out["clock reading reaches a client"] = nil
	}

	logf("[%s] restarting with its clock reading shifted %ds …", dev.name, seededSkewSeconds)
	dev.clockOffset = fmt.Sprintf("%d", seededSkewSeconds)
	restored := false
	defer func() {
		if restored {
			return
		}

		dev.clockOffset = ""
		if err := dev.restart(); err != nil {
			logf("[%s] could not restore the unshifted reading: %v", dev.name, err)
		}
	}()
	if err := dev.restart(); err != nil {
		out["seeded clock drift warns"] = fmt.Errorf(
			"could not restart %s with a shifted reading: %w", dev.name, err)
		return out
	}

	loud, err := awaitClockSkew(dev, func(cs ipc.ClockSkew) bool { return cs.OverLimit })
	switch {
	case err != nil:
		out["seeded clock drift warns"] = fmt.Errorf(
			"%s never reported its clock over the limit after the reading moved %ds: %w",
			dev.name, seededSkewSeconds, err)
	case !loud.Known:
		out["seeded clock drift warns"] = fmt.Errorf(
			"%s reported a reading over the limit but marked it unknown — an unmeasured clock must not read as a wrong one",
			dev.name)
	case !nearSkew(int64(loud.Seconds), seededSkewSeconds):
		out["seeded clock drift warns"] = fmt.Errorf(
			"%s reports its clock %ds out, want about %d — the seeded shift is not what reached the payload",
			dev.name, loud.Seconds, seededSkewSeconds)
	default:
		out["seeded clock drift warns"] = nil
	}

	dev.clockOffset = ""
	if err := dev.restart(); err != nil {
		logf("[%s] could not restore the unshifted reading: %v", dev.name, err)
	} else {
		restored = true
	}
	return out
}

func nearSkew(got, want int64) bool {
	d := got - want
	return d >= -2 && d <= 2
}

func awaitClockSkew(r *rig, want func(ipc.ClockSkew) bool) (ipc.ClockSkew, error) {
	sub, cancel := r.subscribe()
	defer cancel()

	var last ipc.ClockSkew
	var sawAny bool
	f, err := awaitFrame(sub, clockStatusTimeout, func(f ipc.Frame) bool {
		if f.Type != ipc.FrameBackendStatus {
			return false
		}
		var p ipc.BackendStatusPayload
		if json.Unmarshal(f.Payload, &p) != nil {
			return false
		}

		sawAny = true
		last = p.ClockSkew
		return want(p.ClockSkew)
	})
	if err != nil {
		if sawAny {
			return last, fmt.Errorf("readings arrived but none matched; last was %ds known=%v over_limit=%v limit=%ds: %w",
				last.Seconds, last.Known, last.OverLimit, last.LimitSeconds, err)
		}
		return ipc.ClockSkew{}, fmt.Errorf("no status push carried a clock reading at all: %w", err)
	}
	var p ipc.BackendStatusPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		return ipc.ClockSkew{}, fmt.Errorf("decode status: %w", err)
	}
	return p.ClockSkew, nil
}
