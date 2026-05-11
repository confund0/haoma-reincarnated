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

const pollInterval = 10 * time.Second

type Status struct {
	Bootstrap   int
	Ready       bool
	Unreachable bool
}

type Poller struct {
	addr     string
	password string

	mu     sync.RWMutex
	status Status
}

func New(addr, password string) *Poller {
	return &Poller{addr: addr, password: password}
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

	p.poll(ctx)
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			p.poll(ctx)
		}
	}
}

func (p *Poller) poll(ctx context.Context) {
	p.mu.RLock()
	pw := p.password
	p.mu.RUnlock()
	s, err := query(ctx, p.addr, pw)
	if err != nil {
		if ctx.Err() == nil {
			slog.Debug("tor health poll failed", slog.Any("err", err))
		}
		p.set(Status{Unreachable: true})
		return
	}
	if s.Ready && !p.status.Ready {
		slog.Info("tor ready", slog.Int("bootstrap", s.Bootstrap))
	} else if !s.Ready {
		slog.Debug("tor bootstrapping", slog.Int("bootstrap", s.Bootstrap))
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

	pct := parseBootstrap(raw)
	return Status{
		Bootstrap: pct,
		Ready:     pct == 100,
	}, nil
}

func parseBootstrap(s string) int {
	for _, field := range strings.Fields(s) {
		if strings.HasPrefix(field, "PROGRESS=") {
			n, err := strconv.Atoi(field[9:])
			if err == nil {
				return n
			}
		}
	}
	return 0
}
