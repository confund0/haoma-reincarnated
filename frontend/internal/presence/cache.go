package presence

import (
	"log/slog"
	"sort"
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
	Sending   bool
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

	obsMu sync.RWMutex
	obs   map[chan string]struct{}

	dirty map[*dirtySub]struct{}
}

type peerState struct {
	accepting bool
	sending   bool
	chatty    string

	last   Snapshot
	timerA Timer
	timerB Timer
	timerC Timer

	genA uint64
	genB uint64
	genC uint64
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
		obs:       map[chan string]struct{}{},
		dirty:     map[*dirtySub]struct{}{},
		staleness: staleness,
		afterFunc: af,
	}
}

func (c *Cache) Staleness() time.Duration {
	return c.staleness
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
		slog.Debug("presence cache: snapshot changed",
			slog.String("peer_id", peerID),
			slog.String("trigger", "technical"),
			slog.String("effective", emit.Snapshot.Effective),
		)
	} else {
		slog.Debug("presence cache: technical signal (no change)",
			slog.String("peer_id", peerID),
		)
	}
	c.publish(emit)
	c.publishObserved(peerID)
}

func (c *Cache) ObserveSending(peerID string) {
	if peerID == "" {
		return
	}
	emit := c.observe(peerID, func(p *peerState) {
		p.sending = true
		c.armTimerC(peerID, p)
	})
	if emit.PeerID != "" {
		slog.Debug("presence cache: snapshot changed",
			slog.String("peer_id", peerID),
			slog.String("trigger", "sending"),
			slog.String("effective", emit.Snapshot.Effective),
		)
	} else {
		slog.Debug("presence cache: sending signal (no change)",
			slog.String("peer_id", peerID),
		)
	}
	c.publish(emit)
	c.publishObserved(peerID)
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
		slog.Debug("presence cache: snapshot changed",
			slog.String("peer_id", peerID),
			slog.String("trigger", "human"),
			slog.String("state", state),
			slog.String("effective", emit.Snapshot.Effective),
		)
	} else {
		slog.Debug("presence cache: human signal (no change)",
			slog.String("peer_id", peerID),
			slog.String("state", state),
		)
	}
	c.publish(emit)
	c.publishObserved(peerID)
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

func (c *Cache) SubscribeObservations(buffer int) (<-chan string, func()) {
	if buffer <= 0 {
		buffer = 16
	}
	ch := make(chan string, buffer)
	c.obsMu.Lock()
	c.obs[ch] = struct{}{}
	c.obsMu.Unlock()
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			c.obsMu.Lock()
			delete(c.obs, ch)
			c.obsMu.Unlock()
			close(ch)
		})
	}
	return ch, cancel
}

func (c *Cache) publishObserved(peerID string) {
	c.obsMu.RLock()
	defer c.obsMu.RUnlock()
	for ch := range c.obs {
		select {
		case ch <- peerID:
		default:
		}
	}
	for s := range c.dirty {
		s.mark(peerID)
	}
}

type dirtySub struct {
	mu    sync.Mutex
	peers map[string]struct{}

	wake chan struct{}
}

func (s *dirtySub) mark(peerID string) {
	s.mu.Lock()
	s.peers[peerID] = struct{}{}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *dirtySub) drain() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.peers))
	for id := range s.peers {
		out = append(out, id)
	}
	clear(s.peers)
	sort.Strings(out)
	return out
}

func (c *Cache) SubscribeDirty() (wake <-chan struct{}, drain func() []string, cancel func()) {
	s := &dirtySub{peers: map[string]struct{}{}, wake: make(chan struct{}, 1)}
	c.obsMu.Lock()
	c.dirty[s] = struct{}{}
	c.obsMu.Unlock()
	var once sync.Once
	cancel = func() {
		once.Do(func() {
			c.obsMu.Lock()
			delete(c.dirty, s)
			c.obsMu.Unlock()
			close(s.wake)
		})
	}
	return s.wake, s.drain, cancel
}

func (c *Cache) DirtySubscriberCount() int {
	c.obsMu.RLock()
	defer c.obsMu.RUnlock()
	return len(c.dirty)
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
		p = &peerState{last: Snapshot{Effective: EffectiveUnknown}}
		c.peers[peerID] = p
	}
	mutate(p)
	return changeLocked(peerID, p)
}

func changeLocked(peerID string, p *peerState) Change {
	snap := snapshotLocked(p)
	if snap == p.last {
		return Change{}
	}
	p.last = snap
	return Change{PeerID: peerID, Snapshot: snap}
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

func (c *Cache) armTimerC(peerID string, p *peerState) {
	if p.timerC != nil {
		p.timerC.Stop()
	}
	p.genC++
	gen := p.genC
	p.timerC = c.afterFunc(c.staleness, func() { c.expireC(peerID, gen) })
}

func (c *Cache) expireA(peerID string, gen uint64) {
	c.expire(peerID, "technical", func(p *peerState) bool {
		if p.genA != gen {
			return false
		}
		p.accepting = false
		p.timerA = nil
		return true
	})
}

func (c *Cache) expireB(peerID string, gen uint64) {
	c.expire(peerID, "human", func(p *peerState) bool {
		if p.genB != gen {
			return false
		}
		p.chatty = ""
		p.timerB = nil
		return true
	})
}

func (c *Cache) expireC(peerID string, gen uint64) {
	c.expire(peerID, "sending", func(p *peerState) bool {
		if p.genC != gen {
			return false
		}
		p.sending = false
		p.timerC = nil
		return true
	})
}

func (c *Cache) expire(peerID, slot string, clear func(*peerState) bool) {
	c.mu.Lock()
	p := c.peers[peerID]
	if p == nil || !clear(p) {
		c.mu.Unlock()
		return
	}
	emit := changeLocked(peerID, p)
	c.mu.Unlock()
	if emit.PeerID != "" {
		slog.Debug("presence cache: timer expired (snapshot changed)",
			slog.String("peer_id", peerID),
			slog.String("slot", slot),
			slog.String("effective", emit.Snapshot.Effective),
		)
	}
	c.publish(emit)
	c.publishObserved(peerID)
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
		Sending:   p.sending,
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
