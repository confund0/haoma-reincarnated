package main

import (
	"log/slog"
	"os"
	"time"

	"haoma/internal/outbox"
)

const giveUpEnv = "HAOMA_OUTBOX_GIVEUP"

func applyGiveUpOverride(w *outbox.Worker) {
	raw := os.Getenv(giveUpEnv)
	if raw == "" {
		return
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		slog.Warn("outbox give-up override ignored (not a positive duration)",
			slog.String("env", giveUpEnv),
			slog.Any("err", err),
		)
		return
	}
	w.DeadLetterAge = d
	slog.Info("outbox give-up window overridden",
		slog.Duration("give_up_age", d),
		slog.Duration("default", outbox.DefaultGiveUpAge),
	)
}
