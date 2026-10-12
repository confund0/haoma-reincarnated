package health

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestDefaultSchedule(t *testing.T) {
	if defaultSchedule.start != time.Second {
		t.Fatalf("start interval = %s, want 1s", defaultSchedule.start)
	}
	if defaultSchedule.normal != 10*time.Second {
		t.Fatalf("normal interval = %s, want 10s", defaultSchedule.normal)
	}
	if defaultSchedule.window <= 0 || defaultSchedule.window > 10*time.Minute {
		t.Fatalf("start window = %s, want a bound above zero and at most 10m", defaultSchedule.window)
	}
	if p := New("127.0.0.1:1", ""); p.sched != defaultSchedule || p.query == nil {
		t.Fatal("New does not set the shipped schedule and the real query")
	}
}

func TestScheduleNext(t *testing.T) {
	s := schedule{normal: 10 * time.Second, start: time.Second, window: 5 * time.Minute}
	for _, tc := range []struct {
		name    string
		reached bool
		since   time.Duration
		want    time.Duration
	}{
		{"start, below 100%", false, 0, time.Second},
		{"inside the window, below 100%", false, 4 * time.Minute, time.Second},
		{"window over, still below 100%", false, 5 * time.Minute, 10 * time.Second},
		{"at 100% inside the window", true, 3 * time.Second, 10 * time.Second},
		{"at 100% once, below it now", true, time.Minute, 10 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.next(tc.reached, tc.since); got != tc.want {
				t.Fatalf("next(%v, %s) = %s, want %s", tc.reached, tc.since, got, tc.want)
			}
		})
	}
}

type fakeTor struct {
	mu       sync.Mutex
	readings []int
	calls    int
}

func (f *fakeTor) query(context.Context, string, string) (Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.calls
	if i >= len(f.readings) {
		i = len(f.readings) - 1
	}
	f.calls++
	b := f.readings[i]
	return Status{Bootstrap: b, Ready: b == 100, Phase: BootstrapPhase{Progress: b}}, nil
}

func (f *fakeTor) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func runFor(t *testing.T, p *Poller, d time.Duration) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	time.Sleep(d)
	cancel()
	<-done
}

func TestRunPollsFastUntilReady(t *testing.T) {
	f := &fakeTor{readings: []int{10, 50, 75, 100}}
	p := New("unused", "")
	p.query = f.query
	p.sched = schedule{normal: time.Hour, start: 10 * time.Millisecond, window: time.Hour}
	runFor(t, p, 400*time.Millisecond)

	if got := f.count(); got != 4 {
		t.Fatalf("polls = %d, want 4: three below 100%% at the start interval, then none within the hour", got)
	}
	if !p.Ready() {
		t.Fatal("not ready after the fourth reading")
	}
}

func TestRunWithoutAStartPhaseWaitsTheNormalInterval(t *testing.T) {
	f := &fakeTor{readings: []int{10, 50, 75, 100}}
	p := New("unused", "")
	p.query = f.query
	p.sched = schedule{normal: time.Hour, start: time.Hour, window: time.Hour}
	runFor(t, p, 200*time.Millisecond)
	if got := f.count(); got != 1 {
		t.Fatalf("polls = %d, want 1", got)
	}
}

func TestRunStartPhaseIsBounded(t *testing.T) {
	bounded := &fakeTor{readings: []int{5}}
	p := New("unused", "")
	p.query = bounded.query
	p.sched = schedule{normal: time.Hour, start: 10 * time.Millisecond, window: 100 * time.Millisecond}
	runFor(t, p, 500*time.Millisecond)

	unbounded := &fakeTor{readings: []int{5}}
	q := New("unused", "")
	q.query = unbounded.query
	q.sched = schedule{normal: time.Hour, start: 10 * time.Millisecond, window: time.Hour}
	runFor(t, q, 500*time.Millisecond)

	if got := bounded.count(); got < 3 || got > 20 {
		t.Fatalf("polls with a 100ms window = %d, want about 10", got)
	}
	if got := unbounded.count(); got < 2*bounded.count() {
		t.Fatalf("polls without the bound = %d, with it = %d: the window made no difference", got, bounded.count())
	}
}
