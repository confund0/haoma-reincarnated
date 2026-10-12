package main

import (
	"testing"
	"time"

	"haoma-frontend/internal/ipc"
)

func TestJoinWaitsOutlastTheDial(t *testing.T) {
	for _, c := range []struct {
		name string
		got  time.Duration
		min  time.Duration
	}{
		{"acceptTimeout", acceptTimeout, ipc.JoinerDialTimeout},
		{"joinCheckTimeout", joinCheckTimeout, ipc.JoinerDialTimeout},

		{"linkAcceptTimeout", linkAcceptTimeout, ipc.JoinerDialTimeout + 30*time.Second + 3*time.Minute},
	} {
		if c.got <= c.min {
			t.Errorf("%s is %v; it must be longer than %v", c.name, c.got, c.min)
		}
	}
}
