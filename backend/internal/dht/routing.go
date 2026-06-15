package dht

import (
	"crypto/rand"
	"fmt"
	"net/netip"
	"sort"
	"sync"
	"time"
)

const kBucketSize = 8

type ID [20]byte

func RandomID() (ID, error) {
	var id ID
	if _, err := rand.Read(id[:]); err != nil {
		return id, fmt.Errorf("dht: random id: %w", err)
	}
	return id, nil
}

func (a ID) Xor(b ID) ID {
	var out ID
	for i := range a {
		out[i] = a[i] ^ b[i]
	}
	return out
}

func (a ID) BitLen() int {
	for i := 0; i < len(a); i++ {
		if a[i] == 0 {
			continue
		}

		b := a[i]
		bit := 8
		for b > 0 {
			b >>= 1
			bit--
		}
		return (len(a)-i)*8 - bit
	}
	return 0
}

func (a ID) String() string {
	const hex = "0123456789abcdef"
	out := make([]byte, 40)
	for i, b := range a {
		out[i*2] = hex[b>>4]
		out[i*2+1] = hex[b&0x0f]
	}
	return string(out)
}

type node struct {
	id       ID
	addr     netip.AddrPort
	lastSeen time.Time
}

type bucket struct {
	nodes []*node
}

type routingTable struct {
	mu      sync.Mutex
	self    ID
	buckets [160]*bucket
	addrIdx map[netip.AddrPort]*node
}

func newRoutingTable(self ID) *routingTable {
	t := &routingTable{
		self:    self,
		addrIdx: make(map[netip.AddrPort]*node),
	}
	for i := range t.buckets {
		t.buckets[i] = &bucket{}
	}
	return t
}

func (t *routingTable) bucketIndex(id ID) int {
	dist := id.Xor(t.self)
	bl := dist.BitLen()
	if bl == 0 {
		return -1
	}

	return bl - 1
}

func (t *routingTable) addNode(id ID, addr netip.AddrPort) bool {
	idx := t.bucketIndex(id)
	if idx < 0 {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	if existing, ok := t.addrIdx[addr]; ok {

		existing.lastSeen = now
		existing.id = id
		return true
	}

	b := t.buckets[idx]
	if len(b.nodes) < kBucketSize {
		n := &node{id: id, addr: addr, lastSeen: now}
		b.nodes = append(b.nodes, n)
		t.addrIdx[addr] = n
		return true
	}

	return false
}

func (t *routingTable) closestNodes(target ID, count int) []*node {
	t.mu.Lock()
	defer t.mu.Unlock()

	var all []*node
	for _, b := range t.buckets {
		all = append(all, b.nodes...)
	}
	sort.Slice(all, func(i, j int) bool {
		di := all[i].id.Xor(target)
		dj := all[j].id.Xor(target)
		return less(di, dj)
	})
	if len(all) > count {
		all = all[:count]
	}

	out := make([]*node, len(all))
	for i, n := range all {
		nc := *n
		out[i] = &nc
	}
	return out
}

func (t *routingTable) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.addrIdx)
}

func less(a, b ID) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
