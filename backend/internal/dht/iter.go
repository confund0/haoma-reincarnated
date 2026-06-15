package dht

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"log/slog"
	"net/netip"
	"sort"
	"sync"

	"haoma/internal/bencode"
)

const alpha = 15

const maxRounds = 20

type GetResult struct {
	Value  []byte
	Seq    int64
	PubKey [32]byte
	Sig    [64]byte
	Tokens map[netip.AddrPort]string
}

func iterativeGet(ctx context.Context, t *transport, table *routingTable, target ID, expectPubKey *[32]byte, expectSalt []byte) (*GetResult, error) {
	res := &GetResult{
		Tokens: make(map[netip.AddrPort]string),
	}

	cand := newCandidateSet(target)

	for _, n := range table.closestNodes(target, 3*kBucketSize) {
		cand.add(n.id, n.addr)
	}
	if cand.size() == 0 {
		return nil, fmt.Errorf("dht: get: routing table empty (bootstrap first)")
	}

	for round := 0; round < maxRounds; round++ {
		picks := cand.pickUnqueried(alpha)
		if len(picks) == 0 {
			break
		}

		closestBefore := cand.closestDist()
		hadAny := cand.hadAny()

		t.logger.Debug("dht: get round",
			slog.Int("round", round),
			slog.Int("queries", len(picks)),
			slog.Int("tokens_so_far", len(res.Tokens)),
			slog.String("target", target.String()),
		)

		var wg sync.WaitGroup
		var mu sync.Mutex
		for _, p := range picks {
			wg.Add(1)
			go func(c *candidate) {
				defer wg.Done()
				reply, err := t.query(ctx, c.addr, qGet, &krpcArgs{Target: target})
				cand.markQueried(c.id)
				if err != nil {
					t.logger.Debug("dht: get query failed",
						slog.String("addr", c.addr.String()),
						slog.String("err", err.Error()),
					)
					return
				}

				mu.Lock()
				defer mu.Unlock()

				if reply.Token != "" {
					res.Tokens[c.addr] = reply.Token
				}

				if len(reply.Nodes) > 0 {
					ns, perr := parseCompactNodes(reply.Nodes)
					if perr == nil {
						for _, n := range ns {
							if cand.add(n.ID, n.Addr) {
								table.addNode(n.ID, n.Addr)
							}
						}
					}
				}

				if len(reply.V) > 0 && len(reply.K) == 32 && len(reply.Sig) == 64 && reply.Seq != nil {

					var payload []byte
					if err := bencode.Unmarshal(reply.V, &payload); err != nil {
						t.logger.Debug("dht: get value decode failed",
							slog.String("addr", c.addr.String()),
							slog.String("err", err.Error()),
						)
						return
					}
					var pk [32]byte
					copy(pk[:], reply.K)
					if expectPubKey != nil && pk != *expectPubKey {

						t.logger.Debug("dht: get pubkey mismatch",
							slog.String("addr", c.addr.String()),
						)
						return
					}
					item := &MutableItem{
						PubKey: pk,
						Salt:   expectSalt,
						Value:  payload,
						Seq:    *reply.Seq,
					}
					copy(item.Sig[:], reply.Sig)
					if err := item.Verify(); err != nil {
						t.logger.Debug("dht: get signature invalid",
							slog.String("addr", c.addr.String()),
							slog.String("err", err.Error()),
						)
						return
					}
					if item.Seq > res.Seq || res.Value == nil {
						res.Value = payload
						res.Seq = item.Seq
						res.PubKey = pk
						res.Sig = item.Sig
					}
				}
			}(p)
		}
		wg.Wait()

		newCloser := false
		if !hadAny {
			newCloser = cand.hadAny()
		} else if less(cand.closestDist(), closestBefore) {
			newCloser = true
		}

		t.logger.Debug("dht: get round done",
			slog.Int("round", round),
			slog.Int("tokens", len(res.Tokens)),
			slog.Bool("new_closer", newCloser),
		)

		if !newCloser && len(res.Tokens) >= kBucketSize {
			t.logger.Debug("dht: get done",
				slog.Int("round", round),
				slog.Int("tokens", len(res.Tokens)),
			)
			break
		}
		if !newCloser && len(res.Tokens) >= 1 && len(picks) < alpha/2 {

			t.logger.Debug("dht: get exhausted",
				slog.Int("round", round),
				slog.Int("tokens", len(res.Tokens)),
			)
			break
		}
	}

	return res, nil
}

func iterativePut(ctx context.Context, t *transport, item *MutableItem, tokens map[netip.AddrPort]string) (int, error) {
	if len(tokens) == 0 {
		return 0, fmt.Errorf("dht: put: no tokens (run get first)")
	}
	if err := item.Verify(); err != nil {
		return 0, fmt.Errorf("dht: put: item signature invalid: %w", err)
	}

	seq := item.Seq
	salt := item.Salt
	args := &krpcArgs{
		V:    item.Value,
		K:    item.PubKey[:],
		Sig:  item.Sig[:],
		Seq:  &seq,
		Salt: salt,
	}

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		success int
	)
	for addr, token := range tokens {
		wg.Add(1)
		go func(addr netip.AddrPort, token string) {
			defer wg.Done()
			a := *args
			a.Token = token
			if _, err := t.query(ctx, addr, qPut, &a); err != nil {
				t.logger.Debug("dht: put failed",
					slog.String("addr", addr.String()),
					slog.String("err", err.Error()),
				)
				return
			}
			mu.Lock()
			success++
			mu.Unlock()
		}(addr, token)
	}
	wg.Wait()

	t.logger.Debug("dht: put complete",
		slog.Int("tokens", len(tokens)),
		slog.Int("success", success),
		slog.Int64("seq", seq),
	)
	if success == 0 {
		return 0, fmt.Errorf("dht: put: all %d targets rejected", len(tokens))
	}
	return success, nil
}

type candidate struct {
	id      ID
	addr    netip.AddrPort
	queried bool
}

type candidateSet struct {
	mu      sync.Mutex
	target  ID
	byID    map[ID]*candidate
	sorted  []*candidate
	closest ID
	hasAny  bool
}

func newCandidateSet(target ID) *candidateSet {
	return &candidateSet{
		target: target,
		byID:   make(map[ID]*candidate),
	}
}

func (s *candidateSet) add(id ID, addr netip.AddrPort) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[id]; ok {
		return false
	}
	c := &candidate{id: id, addr: addr}
	s.byID[id] = c

	dist := id.Xor(s.target)
	idx := sort.Search(len(s.sorted), func(i int) bool {
		return !less(s.sorted[i].id.Xor(s.target), dist)
	})
	s.sorted = append(s.sorted, nil)
	copy(s.sorted[idx+1:], s.sorted[idx:])
	s.sorted[idx] = c
	if !s.hasAny || less(dist, s.closest) {
		s.closest = dist
		s.hasAny = true
	}
	return true
}

func (s *candidateSet) size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sorted)
}

func (s *candidateSet) closestDist() ID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closest
}

func (s *candidateSet) hadAny() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hasAny
}

func (s *candidateSet) pickUnqueried(n int) []*candidate {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*candidate, 0, n)
	for _, c := range s.sorted {
		if c.queried {
			continue
		}
		c.queried = true
		out = append(out, c)
		if len(out) >= n {
			break
		}
	}
	return out
}

func (s *candidateSet) markQueried(id ID) {}

var _ = ed25519.SignatureSize
