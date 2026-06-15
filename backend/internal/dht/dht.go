package dht

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"
)

var bootstrapNodes = []string{
	"router.bittorrent.com:6881",
	"dht.libtorrent.org:25401",
	"router.utorrent.com:6881",
	"dht.transmissionbt.com:6881",
	"dht.aelitis.com:6881",
	"router.bittorrent.cloud:42069",
	"dht.anacrolix.link:42069",
}

const minBootstrapNodes = 64

type Server struct {
	t      *transport
	table  *routingTable
	logger *slog.Logger
	selfID ID
}

func NewServer(logger *slog.Logger) (*Server, error) {
	if logger == nil {
		logger = slog.Default()
	}
	id, err := RandomID()
	if err != nil {
		return nil, err
	}
	t, err := newTransport(id, logger)
	if err != nil {
		return nil, err
	}
	s := &Server{
		t:      t,
		table:  newRoutingTable(id),
		logger: logger,
		selfID: id,
	}

	t.onIncomingQuery = s.handleQuery
	return s, nil
}

func (s *Server) Close() error { return s.t.Close() }

func (s *Server) LocalAddr() net.Addr { return s.t.conn.LocalAddr() }

func (s *Server) NodeCount() int { return s.table.count() }

func (s *Server) SelfID() ID { return s.selfID }

func (s *Server) Bootstrap(ctx context.Context) error {

	var seeds []netip.AddrPort
	for _, hp := range bootstrapNodes {
		host, port, err := net.SplitHostPort(hp)
		if err != nil {
			s.logger.Debug("dht: bootstrap split", slog.String("hp", hp), slog.String("err", err.Error()))
			continue
		}
		portNum, err := strconv.Atoi(port)
		if err != nil {
			continue
		}
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
		if err != nil {
			s.logger.Debug("dht: bootstrap resolve",
				slog.String("host", host),
				slog.String("err", err.Error()),
			)
			continue
		}
		for _, ip := range ips {
			addr, ok := netip.AddrFromSlice(ip.To4())
			if !ok {
				continue
			}
			seeds = append(seeds, netip.AddrPortFrom(addr, uint16(portNum)))
		}
	}
	if len(seeds) == 0 {
		return errors.New("dht: bootstrap: no seed addresses resolved")
	}
	s.logger.Debug("dht: bootstrap seeds resolved", slog.Int("count", len(seeds)))

	bootstrapCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	for _, addr := range seeds {
		wg.Add(1)
		go func(addr netip.AddrPort) {
			defer wg.Done()
			reply, err := s.t.query(bootstrapCtx, addr, qFindNode, &krpcArgs{Target: s.selfID})
			if err != nil {
				s.logger.Debug("dht: seed find_node failed",
					slog.String("addr", addr.String()),
					slog.String("err", err.Error()),
				)
				return
			}

			s.table.addNode(reply.ID, addr)
			if len(reply.Nodes) > 0 {
				ns, perr := parseCompactNodes(reply.Nodes)
				if perr != nil {
					return
				}
				for _, n := range ns {
					s.table.addNode(n.ID, n.Addr)
				}
			}
		}(addr)
	}
	wg.Wait()

	s.logger.Debug("dht: post-seed table", slog.Int("nodes", s.table.count()))
	if s.table.count() == 0 {
		return errors.New("dht: bootstrap: all seeds failed to respond")
	}

	if err := s.fillRoutingTable(bootstrapCtx); err != nil {
		s.logger.Debug("dht: routing-fill warning", slog.String("err", err.Error()))

	}
	s.logger.Info("dht: bootstrap complete",
		slog.Int("nodes", s.table.count()),
		slog.String("local", s.t.conn.LocalAddr().String()),
	)
	return nil
}

func (s *Server) fillRoutingTable(ctx context.Context) error {
	cand := newCandidateSet(s.selfID)
	for _, n := range s.table.closestNodes(s.selfID, kBucketSize) {
		cand.add(n.id, n.addr)
	}

	for round := 0; round < maxRounds; round++ {
		if s.table.count() >= minBootstrapNodes {
			return nil
		}
		picks := cand.pickUnqueried(alpha)
		if len(picks) == 0 {
			return nil
		}
		var wg sync.WaitGroup
		anyProgress := false
		var mu sync.Mutex
		for _, p := range picks {
			wg.Add(1)
			go func(c *candidate) {
				defer wg.Done()
				reply, err := s.t.query(ctx, c.addr, qFindNode, &krpcArgs{Target: s.selfID})
				if err != nil {
					return
				}
				if len(reply.Nodes) == 0 {
					return
				}
				ns, perr := parseCompactNodes(reply.Nodes)
				if perr != nil {
					return
				}
				mu.Lock()
				defer mu.Unlock()
				for _, n := range ns {
					if cand.add(n.ID, n.Addr) {
						s.table.addNode(n.ID, n.Addr)
						anyProgress = true
					}
				}
			}(p)
		}
		wg.Wait()
		if !anyProgress {
			return nil
		}
	}
	return nil
}

func (s *Server) Get(ctx context.Context, target ID, expectPubKey *[32]byte, expectSalt []byte) (*GetResult, error) {
	if s.table.count() == 0 {
		return nil, errors.New("dht: get called before bootstrap")
	}
	return iterativeGet(ctx, s.t, s.table, target, expectPubKey, expectSalt)
}

func (s *Server) Put(ctx context.Context, item *MutableItem, tokens map[netip.AddrPort]string) (int, error) {
	return iterativePut(ctx, s.t, item, tokens)
}

func (s *Server) handleQuery(_ netip.AddrPort, m *krpcMsg) *krpcMsg {
	if m.Q != qPing {
		return nil
	}
	return &krpcMsg{R: &krpcReturn{ID: s.selfID}}
}
