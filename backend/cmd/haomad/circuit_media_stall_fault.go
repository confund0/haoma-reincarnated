//go:build faults

package main

import (
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"haoma/internal/proxy"
)

const callMediaStallEnv = "HAOMA_FAULT_CALL_MEDIA_STALL"

const defaultMediaStallValue = "clean=5s,stall=8s,cap=5000,capfor=30s,every=60s"

const defaultCleanPeriod = 5 * time.Second

const callMediaStallOnLine = "seeded fault on: call media writes stall"

func callMediaStalledLine(stall time.Duration) string {
	return fmt.Sprintf("seeded fault: call media stalled stall_ms=%d", stall.Milliseconds())
}

type mediaStallFault struct {
	clean, stall, every, capFor time.Duration
	capBytesPerSec              int64

	now   func() time.Time
	sleep func(time.Duration)
}

func parseMediaStallFaultValue(v string) (stall, every, capFor, clean time.Duration, capBps int64, ok bool) {
	fields := map[string]string{}
	for _, part := range strings.Split(v, ",") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 || kv[0] == "" || kv[1] == "" {
			return 0, 0, 0, 0, 0, false
		}
		fields[kv[0]] = kv[1]
	}
	var err error
	if stall, err = time.ParseDuration(fields["stall"]); err != nil || stall <= 0 {
		return 0, 0, 0, 0, 0, false
	}
	if every, err = time.ParseDuration(fields["every"]); err != nil || every <= 0 {
		return 0, 0, 0, 0, 0, false
	}
	if capFor, err = time.ParseDuration(fields["capfor"]); err != nil || capFor <= 0 {
		return 0, 0, 0, 0, 0, false
	}
	capBps, err = strconv.ParseInt(fields["cap"], 10, 64)
	if err != nil || capBps <= 0 {
		return 0, 0, 0, 0, 0, false
	}
	clean = defaultCleanPeriod
	if s, present := fields["clean"]; present {
		if clean, err = time.ParseDuration(s); err != nil || clean < 0 {
			return 0, 0, 0, 0, 0, false
		}
	}
	if clean+stall+capFor > every {
		return 0, 0, 0, 0, 0, false
	}
	return stall, every, capFor, clean, capBps, true
}

func readMediaStallFault(getenv func(string) string) *mediaStallFault {
	v := strings.TrimSpace(getenv(callMediaStallEnv))
	if v == "" {
		return nil
	}
	if v == "1" || v == "on" {
		v = defaultMediaStallValue
	}
	stall, every, capFor, clean, capBps, ok := parseMediaStallFaultValue(v)
	if !ok {
		slog.Warn("seeded fault: call media stall value could not be parsed, fault stays off",
			slog.String("value", v))
		return nil
	}
	slog.Warn(callMediaStallOnLine)
	return &mediaStallFault{
		stall: stall, every: every, capFor: capFor, clean: clean, capBytesPerSec: capBps,
		now: time.Now, sleep: time.Sleep,
	}
}

func (f *mediaStallFault) wrapServe(modality proxy.Modality, w io.Writer) io.Writer {
	if f == nil || modality != proxy.ModalityAudio {
		return w
	}
	return &mediaStallWriter{f: f, w: w, loggedStallCycle: -1, done: make(chan struct{})}
}

type mediaStallWriter struct {
	f    *mediaStallFault
	w    io.Writer
	done chan struct{}

	closeOnce        sync.Once
	mu               sync.Mutex
	start            time.Time
	loggedStallCycle int64
}

func (sw *mediaStallWriter) Close() error {
	sw.closeOnce.Do(func() { close(sw.done) })
	return nil
}

func (sw *mediaStallWriter) sleep(d time.Duration) {
	const step = 50 * time.Millisecond
	for d > 0 {
		s := step
		if d < s {
			s = d
		}
		select {
		case <-sw.done:
			return
		default:
		}
		sw.f.sleep(s)
		d -= s
	}
}

func (sw *mediaStallWriter) Write(p []byte) (int, error) {
	f := sw.f
	sw.mu.Lock()
	if sw.start.IsZero() {
		sw.start = f.now()
	}
	elapsed := f.now().Sub(sw.start)
	cycleIdx := int64(elapsed / f.every)
	cyc := elapsed % f.every
	stallEnd := f.clean + f.stall
	capEnd := stallEnd + f.capFor

	switch {
	case cyc < f.clean:

		sw.mu.Unlock()
		return sw.w.Write(p)

	case cyc < stallEnd:
		logIt := sw.loggedStallCycle != cycleIdx
		if logIt {
			sw.loggedStallCycle = cycleIdx
		}
		wait := stallEnd - cyc
		sw.mu.Unlock()
		if logIt {
			slog.Debug(callMediaStalledLine(f.stall))
		}
		sw.sleep(wait)
		return sw.w.Write(p)

	case cyc < capEnd:
		sw.mu.Unlock()
		n, err := sw.w.Write(p)
		if err == nil && n > 0 {
			want := time.Duration(float64(n) / float64(f.capBytesPerSec) * float64(time.Second))
			sw.sleep(want)
		}
		return n, err

	default:
		sw.mu.Unlock()
		return sw.w.Write(p)
	}
}
