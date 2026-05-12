package extprobe

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"sync"
	"time"
)

const Cooldown = 60 * time.Second

const burstTimeout = 45 * time.Second

const (
	pairedDelayMin = 2 * time.Second
	pairedDelayMax = 5 * time.Second
)

type Result struct {
	Ok             bool
	LastTargetName string
	At             time.Time
}

type burstShape int

const (
	shapeSingle burstShape = iota
	shapeDoubledSame
	shapePairedDifferent
)

var shapeWeights = []struct {
	shape  burstShape
	weight int
}{
	{shapeSingle, 60},
	{shapeDoubledSame, 25},
	{shapePairedDifferent, 15},
}

type Prober struct {
	HTTP *http.Client

	mu        sync.Mutex
	lastFired map[string]time.Time
	burstMu   sync.Mutex
}

func New(http *http.Client) *Prober {
	return &Prober{HTTP: http, lastFired: make(map[string]time.Time)}
}

func (p *Prober) Burst(ctx context.Context) Result {
	p.burstMu.Lock()
	defer p.burstMu.Unlock()

	shape := pickShape()
	eligible := p.eligibleTargets()
	if len(eligible) == 0 {
		slog.Debug("extprobe: burst skipped — all targets on cooldown")
		return Result{}
	}

	switch shape {
	case shapeSingle:
		t := eligible[randIntN(len(eligible))]
		ok, name := p.fire(ctx, t)
		return Result{Ok: ok, LastTargetName: name, At: time.Now()}
	case shapeDoubledSame:
		t := eligible[randIntN(len(eligible))]
		ok1, name := p.fire(ctx, t)

		ok2, _ := p.fireWithoutStamp(ctx, t)
		return Result{Ok: ok1 || ok2, LastTargetName: name, At: time.Now()}
	case shapePairedDifferent:

		t1 := eligible[randIntN(len(eligible))]
		ok1, name1 := p.fire(ctx, t1)
		if len(eligible) < 2 {
			return Result{Ok: ok1, LastTargetName: name1, At: time.Now()}
		}

		var t2 Target
		for {
			t2 = eligible[randIntN(len(eligible))]
			if t2.Name != t1.Name {
				break
			}
		}

		select {
		case <-ctx.Done():
			return Result{Ok: ok1, LastTargetName: name1, At: time.Now()}
		case <-time.After(jitter(pairedDelayMin, pairedDelayMax)):
		}
		ok2, name2 := p.fire(ctx, t2)
		name := name2
		if !ok2 && ok1 {
			name = name1
		}
		return Result{Ok: ok1 || ok2, LastTargetName: name, At: time.Now()}
	}
	return Result{}
}

func (p *Prober) eligibleTargets() []Target {
	all := Targets()
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	out := make([]Target, 0, len(all))
	for _, t := range all {
		last, ok := p.lastFired[t.Name]
		if !ok || now.Sub(last) >= Cooldown {
			out = append(out, t)
		}
	}
	return out
}

func (p *Prober) fire(ctx context.Context, t Target) (bool, string) {
	p.stampFired(t.Name)
	return p.fireWithoutStamp(ctx, t)
}

func (p *Prober) stampFired(name string) {
	p.mu.Lock()
	if p.lastFired == nil {
		p.lastFired = make(map[string]time.Time)
	}
	p.lastFired[name] = time.Now()
	p.mu.Unlock()
}

func (p *Prober) fireWithoutStamp(ctx context.Context, t Target) (bool, string) {
	url := fmt.Sprintf("http://%s.onion%s", t.Onion, t.Path)
	reqCtx, cancel := context.WithTimeout(ctx, burstTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		slog.Debug("extprobe: request build failed",
			slog.String("target", t.Name),
			slog.Any("err", err),
		)
		return false, t.Name
	}
	applyBrowserHeaders(req)
	resp, err := p.HTTP.Do(req)
	if err != nil {
		slog.Debug("extprobe: request failed",
			slog.String("target", t.Name),
			slog.Any("err", err),
		)
		return false, t.Name
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<14))
	slog.Debug("extprobe: response",
		slog.String("target", t.Name),
		slog.Int("status", resp.StatusCode),
	)

	return true, t.Name
}

func applyBrowserHeaders(req *http.Request) {
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; rv:115.0) Gecko/20100101 Firefox/115.0")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")
	req.Header.Set("Accept-Encoding", "gzip, deflate")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	req.Header.Set("DNT", "1")
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")
}

func pickShape() burstShape {
	total := 0
	for _, s := range shapeWeights {
		total += s.weight
	}
	pick := randIntN(total)
	cur := 0
	for _, s := range shapeWeights {
		cur += s.weight
		if pick < cur {
			return s.shape
		}
	}
	return shapeSingle
}

func randIntN(n int) int {
	if n <= 0 {
		return 0
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0
	}
	return int(v.Int64())
}

func jitter(lo, hi time.Duration) time.Duration {
	if hi <= lo {
		return lo
	}
	delta := hi - lo
	v, err := rand.Int(rand.Reader, big.NewInt(int64(delta)))
	if err != nil {
		return lo
	}
	return lo + time.Duration(v.Int64())
}
