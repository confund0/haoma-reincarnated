package main

import (
	"testing"
	"time"

	"haoma/internal/outbox"
)

func TestApplyGiveUpOverride(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want time.Duration
	}{
		{"unset keeps the default", "", outbox.DefaultGiveUpAge},
		{"positive duration applies", "90s", 90 * time.Second},
		{"malformed keeps the default", "two days", outbox.DefaultGiveUpAge},
		{"zero keeps the default", "0s", outbox.DefaultGiveUpAge},
		{"negative keeps the default", "-1h", outbox.DefaultGiveUpAge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(giveUpEnv, tc.env)
			w := outbox.NewWorker(nil, nil, nil, nil)
			applyGiveUpOverride(w)
			if w.DeadLetterAge != tc.want {
				t.Errorf("DeadLetterAge = %s, want %s", w.DeadLetterAge, tc.want)
			}
		})
	}
}
