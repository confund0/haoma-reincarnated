package main

import (
	"log/slog"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/ipc"
)

func clockSkewFromTor(h backendapi.TorHealth) ipc.ClockSkew {
	return ipc.ClockSkew{
		Seconds:      h.ClockSkew,
		Known:        h.ClockSkewKnown,
		OverLimit:    h.ClockSkewOverLim,
		LimitSeconds: h.ClockSkewLimit,
	}
}

func logClockSkew(d *daemon, cs ipc.ClockSkew) {
	if d == nil {
		return
	}
	prev := d.lastClockSkew.Swap(&cs)
	if !cs.Known {
		return
	}
	if prev != nil && prev.Known && prev.Seconds == cs.Seconds && prev.OverLimit == cs.OverLimit {
		return
	}
	if cs.OverLimit {
		slog.Warn("clock drift: this machine's clock is wrong",
			slog.Int("skew_seconds", cs.Seconds),
			slog.Int("limit_seconds", cs.LimitSeconds),
			slog.String("source", "tor"),
		)
		return
	}
	if prev != nil && prev.OverLimit {
		slog.Info("clock drift is over: this machine's clock is back within the limit",
			slog.Int("skew_seconds", cs.Seconds),
			slog.Int("limit_seconds", cs.LimitSeconds),
		)
		return
	}
	slog.Debug("clock reading from tor",
		slog.Int("skew_seconds", cs.Seconds),
		slog.Int("limit_seconds", cs.LimitSeconds),
	)
}
