package health

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"haoma/internal/tor/control"
)

const (
	pollInterval = 10 * time.Second

	startPollInterval = time.Second

	startPollWindow = 5 * time.Minute
)

type schedule struct {
	normal time.Duration
	start  time.Duration
	window time.Duration
}

var defaultSchedule = schedule{normal: pollInterval, start: startPollInterval, window: startPollWindow}

func (s schedule) next(reached bool, since time.Duration) time.Duration {
	if !reached && since < s.window {
		return s.start
	}
	return s.normal
}

type Status struct {
	Bootstrap   int
	Ready       bool
	Unreachable bool

	Phase BootstrapPhase

	ClockSkew      int
	ClockSkewKnown bool
}

const ClockSkewLimit = 30

func (s Status) SkewOverLimit() bool {
	if !s.ClockSkewKnown {
		return false
	}
	return s.ClockSkew > ClockSkewLimit || s.ClockSkew < -ClockSkewLimit
}

type Poller struct {
	addr     string
	password string

	sched schedule
	query func(ctx context.Context, addr, password string) (Status, error)

	mu     sync.RWMutex
	status Status

	lastTag  string
	lastWarn string
}

func New(addr, password string) *Poller {
	return &Poller{addr: addr, password: password, sched: defaultSchedule, query: query}
}

func (p *Poller) Status() Status {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.status
}

func (p *Poller) SetPassword(pw string) {
	p.mu.Lock()
	p.password = pw
	p.mu.Unlock()
}

func (p *Poller) Ready() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.status.Ready
}

func (p *Poller) Run(ctx context.Context) {
	started := time.Now()
	reached := false

	p.poll(ctx)
	reached = p.Ready()
	wait := p.sched.next(reached, time.Since(started))
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			p.poll(ctx)
			reached = reached || p.Ready()
			next := p.sched.next(reached, time.Since(started))
			if !reached && next != wait {
				slog.Info("tor not at 100% at the end of the start window; checking it at the normal interval",
					slog.Duration("window", p.sched.window),
					slog.Duration("interval", next))
			}
			wait = next
			timer.Reset(wait)
		}
	}
}

func (p *Poller) poll(ctx context.Context) {
	p.mu.RLock()
	pw := p.password
	p.mu.RUnlock()
	s, err := p.query(ctx, p.addr, pw)
	if err != nil {
		if ctx.Err() == nil {
			slog.Debug("tor health poll failed", slog.Any("err", err))
		}
		p.set(Status{Unreachable: true})
		p.lastTag, p.lastWarn = "", ""
		return
	}
	ph := s.Phase
	if ph.Tag != p.lastTag {
		slog.Info("tor bootstrap phase",
			slog.Int("bootstrap", ph.Progress),
			slog.String("tag", ph.Tag),
			slog.String("summary", ph.Summary))
		p.lastTag = ph.Tag
	}
	if k := ph.warnKey(); k != p.lastWarn {
		if k != "" {
			slog.Warn("tor bootstrap warning",
				slog.Int("bootstrap", ph.Progress),
				slog.String("tag", ph.Tag),
				slog.String("warning", ph.Warning),
				slog.String("reason", ph.Reason),
				slog.Int("count", ph.Count),
				slog.String("recommendation", ph.Recommendation))
		}
		p.lastWarn = k
	}
	if s.Ready && !p.status.Ready {
		slog.Info("tor ready", slog.Int("bootstrap", s.Bootstrap))
	} else if !s.Ready {
		slog.Debug("tor bootstrapping",
			slog.Int("bootstrap", s.Bootstrap),
			slog.String("tag", ph.Tag),
			slog.String("severity", ph.Severity),
			slog.String("warning", ph.Warning),
			slog.String("reason", ph.Reason),
			slog.Int("count", ph.Count))
	}
	p.set(s)
}

func (p *Poller) set(s Status) {
	p.mu.Lock()
	p.status = s
	p.mu.Unlock()
}

func query(ctx context.Context, addr, password string) (Status, error) {
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := control.Dial(dialCtx, addr)
	if err != nil {
		return Status{}, err
	}
	defer conn.Close()

	if _, err := conn.Authenticate(password); err != nil {
		return Status{}, err
	}

	raw, err := conn.GetInfo("status/bootstrap-phase")
	if err != nil {
		return Status{}, err
	}

	ph := parseBootstrapPhase(raw)
	s := Status{
		Bootstrap: ph.Progress,
		Ready:     ph.Progress == 100,
		Phase:     ph,
	}

	if skewRaw, sErr := conn.GetInfo("status/clock-skew"); sErr == nil {
		s.ClockSkew, s.ClockSkewKnown = parseClockSkew(skewRaw)
	}

	if off := skewOffset(); off != 0 {
		s.ClockSkew += off
		s.ClockSkewKnown = true
	}
	return s, nil
}

func parseClockSkew(s string) (int, bool) {
	for _, field := range strings.Fields(s) {
		key, value, found := strings.Cut(field, "=")
		if !found || !strings.EqualFold(key, "SKEW") {
			continue
		}

		if dot := strings.IndexByte(value, '.'); dot >= 0 {
			value = value[:dot]
		}
		n, err := strconv.Atoi(value)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

type BootstrapPhase struct {
	Severity       string
	Progress       int
	Tag            string
	Summary        string
	Warning        string
	Reason         string
	Count          int
	Recommendation string
}

func (b BootstrapPhase) warnKey() string {
	if b.Severity != "WARN" && b.Severity != "ERR" {
		return ""
	}
	return b.Severity + "|" + b.Tag + "|" + b.Warning + "|" + b.Reason
}

func parseBootstrapPhase(s string) BootstrapPhase {
	var b BootstrapPhase
	first := true
	for len(s) > 0 {
		s = strings.TrimLeft(s, " ")
		if s == "" {
			break
		}
		var tok string
		tok, s = nextToken(s)
		key, value, found := strings.Cut(tok, "=")
		if !found {
			if first {
				b.Severity = tok
			}
			first = false
			continue
		}
		first = false
		switch key {
		case "PROGRESS":
			if n, err := strconv.Atoi(value); err == nil {
				b.Progress = n
			}
		case "TAG":
			b.Tag = value
		case "SUMMARY":
			b.Summary = value
		case "WARNING":
			b.Warning = value
		case "REASON":
			b.Reason = value
		case "COUNT":
			if n, err := strconv.Atoi(value); err == nil {
				b.Count = n
			}
		case "RECOMMENDATION":
			b.Recommendation = value
		}
	}
	return b
}

func nextToken(s string) (string, string) {
	eq := strings.IndexAny(s, "= ")
	if eq < 0 || s[eq] == ' ' || eq+1 >= len(s) || s[eq+1] != '"' {
		if sp := strings.IndexByte(s, ' '); sp >= 0 {
			return s[:sp], s[sp:]
		}
		return s, ""
	}
	var v strings.Builder
	i := eq + 2
	for ; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			i++
			v.WriteByte(s[i])
			continue
		}
		if c == '"' {
			i++
			break
		}
		v.WriteByte(c)
	}
	return s[:eq+1] + v.String(), s[i:]
}
