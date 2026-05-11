package presence

import (
	"log/slog"
	"sync"
	"time"
)

const (
	EffectiveAvailable = "available"
	EffectiveAway      = "away"
	EffectiveBusy      = "busy"
	EffectiveAccepting = "accepting"
	EffectiveUnknown   = "unknown"
)

const DefaultStaleness = 90 * time.Second

type Snapshot struct {
	Accepting bool
	Chatty    string
	Effective string
}

type Change struct {
	PeerID   string
	Snapshot Snapshot
}

type Timer interface {
	Stop() bool
	Reset(d time.Duration) bool
}

type AfterFunc func(d time.Duration, f func()) Timer

type Options struct {
	Staleness time.Duration
	AfterFunc AfterFunc
}

type Cache struct {
	mu        sync.Mutex
	peers     map[string]*peerState
	staleness time.Duration
	afterFunc AfterFunc

	subsMu sync.RWMutex
	subs   map[chan Change]struct{}
}

type peerState struct {
	accepting     bool
	chatty        string
	lastEffective string
	timerA        Timer
	timerB        Timer

	genA uint64
	genB uint64
}

func New() *Cache {
	return NewWithOptions(Options{})
}

func NewWithOptions(opts Options) *Cache {
	staleness := opts.Staleness
	if staleness <= 0 {
		staleness = DefaultStaleness
	}
	af := opts.AfterFunc
	if af == nil {
		af = realAfterFunc
	}
	return &Cache{
		peers:     map[string]*peerState{},
		subs:      map[chan Change]struct{}{},
		staleness: staleness,
		afterFunc: af,
	}
}

func realAfterFunc(d time.Duration, f func()) Timer {
	return time.AfterFunc(d, f)
}

func (c *Cache) ObserveTechnical(peerID string) {
	if peerID == "" {
		return
	}
	emit := c.observe(peerID, func(p *peerState) {
		p.accepting = true
		c.armTimerA(peerID, p)
	})
	if emit.PeerID != "" {
		slog.Debug("presence cache: effective transition",
			slog.String("peer_id", peerID),
			slog.String("trigger", "technical"),
			slog.String("effective", emit.Snapshot.Effective),
		)
	} else {
		slog.Debug("presence cache: technical signal (no transition)",
			slog.String("peer_id", peerID),
		)
	}
	c.publish(emit)
}

func (c *Cache) ObserveHuman(peerID, state string) {
	if peerID == "" || state == "" {
		return
	}
	emit := c.observe(peerID, func(p *peerState) {
		p.chatty = state
		c.armTimerB(peerID, p)
	})
	if emit.PeerID != "" {
		slog.Debug("presence cache: effective transition",
			slog.String("peer_id", peerID),
			slog.String("trigger", "human"),
			slog.String("state", state),
			slog.String("effective", emit.Snapshot.Effective),
		)
	} else {
		slog.Debug("presence cache: human signal (no transition)",
			slog.String("peer_id", peerID),
			slog.String("state", state),
		)
	}
	c.publish(emit)
}

func (c *Cache) Snapshot(peerID string) Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.peers[peerID]
	if p == nil {
		return Snapshot{Effective: EffectiveUnknown}
	}
	return snapshotLocked(p)
}

func (c *Cache) All() map[string]Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]Snapshot, len(c.peers))
	for peerID, p := range c.peers {
		out[peerID] = snapshotLocked(p)
	}
	return out
}

func (c *Cache) Subscribe(buffer int) (<-chan Change, func()) {
	if buffer <= 0 {
		buffer = 16
	}
	ch := make(chan Change, buffer)
	c.subsMu.Lock()
	c.subs[ch] = struct{}{}
	c.subsMu.Unlock()
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			c.subsMu.Lock()
			delete(c.subs, ch)
			c.subsMu.Unlock()
			close(ch)
		})
	}
	return ch, cancel
}

func (c *Cache) SubscriberCount() int {
	c.subsMu.RLock()
	defer c.subsMu.RUnlock()
	return len(c.subs)
}

func (c *Cache) observe(peerID string, mutate func(*peerState)) Change {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.peers[peerID]
	if p == nil {
		p = &peerState{lastEffective: EffectiveUnknown}
		c.peers[peerID] = p
	}
	mutate(p)
	eff := resolveLocked(p)
	if eff == p.lastEffective {
		return Change{}
	}
	p.lastEffective = eff
	return Change{PeerID: peerID, Snapshot: snapshotLocked(p)}
}

func (c *Cache) armTimerA(peerID string, p *peerState) {
	if p.timerA != nil {
		p.timerA.Stop()
	}
	p.genA++
	gen := p.genA
	p.timerA = c.afterFunc(c.staleness, func() { c.expireA(peerID, gen) })
}

func (c *Cache) armTimerB(peerID string, p *peerState) {
	if p.timerB != nil {
		p.timerB.Stop()
	}
	p.genB++
	gen := p.genB
	p.timerB = c.afterFunc(c.staleness, func() { c.expireB(peerID, gen) })
}

func (c *Cache) expireA(peerID string, gen uint64) {
	c.mu.Lock()
	p := c.peers[peerID]
	if p == nil || p.genA != gen {
		c.mu.Unlock()
		return
	}
	p.accepting = false
	p.timerA = nil
	eff := resolveLocked(p)
	var emit Change
	if eff != p.lastEffective {
		p.lastEffective = eff
		emit = Change{PeerID: peerID, Snapshot: snapshotLocked(p)}
	}
	c.mu.Unlock()
	if emit.PeerID != "" {
		slog.Debug("presence cache: technical timer expired (transition)",
			slog.String("peer_id", peerID),
			slog.String("effective", emit.Snapshot.Effective),
		)
	}
	c.publish(emit)
}

func (c *Cache) expireB(peerID string, gen uint64) {
	c.mu.Lock()
	p := c.peers[peerID]
	if p == nil || p.genB != gen {
		c.mu.Unlock()
		return
	}
	p.chatty = ""
	p.timerB = nil
	eff := resolveLocked(p)
	var emit Change
	if eff != p.lastEffective {
		p.lastEffective = eff
		emit = Change{PeerID: peerID, Snapshot: snapshotLocked(p)}
	}
	c.mu.Unlock()
	if emit.PeerID != "" {
		slog.Debug("presence cache: human timer expired (transition)",
			slog.String("peer_id", peerID),
			slog.String("effective", emit.Snapshot.Effective),
		)
	}
	c.publish(emit)
}

func (c *Cache) publish(ev Change) {
	if ev.PeerID == "" {
		return
	}
	c.subsMu.RLock()
	defer c.subsMu.RUnlock()
	for ch := range c.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func snapshotLocked(p *peerState) Snapshot {
	return Snapshot{
		Accepting: p.accepting,
		Chatty:    p.chatty,
		Effective: resolveLocked(p),
	}
}

func resolveLocked(p *peerState) string {
	if p.chatty != "" {
		return p.chatty
	}
	if p.accepting {
		return EffectiveAccepting
	}
	return EffectiveUnknown
}
