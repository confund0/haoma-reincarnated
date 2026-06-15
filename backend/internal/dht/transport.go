package dht

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"haoma/internal/bencode"
)

const queryTimeout = 5 * time.Second

const maxDatagramBytes = 4096

type transport struct {
	conn   *net.UDPConn
	selfID ID
	logger *slog.Logger

	mu       sync.Mutex
	inflight map[string]*pendingTx
	txCtr    uint32

	onIncomingQuery func(addr netip.AddrPort, m *krpcMsg) *krpcMsg

	closed chan struct{}
	wg     sync.WaitGroup
}

type pendingTx struct {
	addr  netip.AddrPort
	reply chan *krpcMsg
}

func newTransport(selfID ID, logger *slog.Logger) (*transport, error) {
	if logger == nil {
		logger = slog.Default()
	}
	addr, err := net.ResolveUDPAddr("udp4", ":0")
	if err != nil {
		return nil, fmt.Errorf("dht: resolve udp: %w", err)
	}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return nil, fmt.Errorf("dht: listen udp: %w", err)
	}
	t := &transport{
		conn:     conn,
		selfID:   selfID,
		logger:   logger,
		inflight: make(map[string]*pendingTx),
		closed:   make(chan struct{}),
	}
	t.wg.Add(1)
	go t.readLoop()
	logger.Debug("dht: transport up",
		slog.String("local", conn.LocalAddr().String()),
		slog.String("self", selfID.String()),
	)
	return t, nil
}

func (t *transport) Close() error {
	select {
	case <-t.closed:
		return nil
	default:
		close(t.closed)
	}
	err := t.conn.Close()
	t.wg.Wait()

	t.mu.Lock()
	for tx, p := range t.inflight {
		close(p.reply)
		delete(t.inflight, tx)
	}
	t.mu.Unlock()
	return err
}

func (t *transport) query(ctx context.Context, addr netip.AddrPort, method string, args *krpcArgs) (*krpcReturn, error) {
	args.ID = t.selfID
	txID := t.nextTxID()
	msg := &krpcMsg{
		T: txID,
		Y: yQuery,
		Q: method,
		A: args,
	}
	body, err := bencode.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("dht: marshal %s: %w", method, err)
	}

	p := &pendingTx{
		addr:  addr,
		reply: make(chan *krpcMsg, 1),
	}
	t.mu.Lock()
	t.inflight[txID] = p
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		delete(t.inflight, txID)
		t.mu.Unlock()
	}()

	udpAddr := net.UDPAddrFromAddrPort(addr)
	if _, err := t.conn.WriteToUDP(body, udpAddr); err != nil {
		return nil, fmt.Errorf("dht: send %s to %s: %w", method, addr, err)
	}

	deadline := time.NewTimer(queryTimeout)
	defer deadline.Stop()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-deadline.C:
		return nil, fmt.Errorf("dht: %s to %s: timeout after %s", method, addr, queryTimeout)
	case <-t.closed:
		return nil, errors.New("dht: transport closed")
	case reply, ok := <-p.reply:
		if !ok {
			return nil, errors.New("dht: transport closed")
		}
		if reply.Y == yError {
			code, errMsg := errorFromBencode(reply.E)
			return nil, fmt.Errorf("dht: %s to %s: KRPC error %d: %s", method, addr, code, errMsg)
		}
		if reply.R == nil {
			return nil, fmt.Errorf("dht: %s to %s: response missing 'r' field", method, addr)
		}
		return reply.R, nil
	}
}

func (t *transport) nextTxID() string {
	n := atomic.AddUint32(&t.txCtr, 1)
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], uint16(n))
	return string(b[:])
}

func (t *transport) readLoop() {
	defer t.wg.Done()
	buf := make([]byte, maxDatagramBytes)
	for {
		n, fromAddr, err := t.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-t.closed:
				return
			default:
			}

			t.logger.Debug("dht: read error", slog.String("err", err.Error()))
			continue
		}
		payload := make([]byte, n)
		copy(payload, buf[:n])
		t.handleDatagram(fromAddr, payload)
	}
}

func (t *transport) handleDatagram(from *net.UDPAddr, payload []byte) {
	var msg krpcMsg
	if err := bencode.Unmarshal(payload, &msg); err != nil {
		t.logger.Debug("dht: decode failed",
			slog.String("from", from.String()),
			slog.String("err", err.Error()),
			slog.Int("len", len(payload)),
		)
		return
	}
	if msg.T == "" {

		return
	}

	switch msg.Y {
	case yResponse, yError:
		t.mu.Lock()
		p, ok := t.inflight[msg.T]
		t.mu.Unlock()
		if !ok {
			t.logger.Debug("dht: orphan response",
				slog.String("from", from.String()),
				slog.String("y", msg.Y),
			)
			return
		}
		select {
		case p.reply <- &msg:
		default:

		}

	case yQuery:
		if t.onIncomingQuery == nil {
			return
		}
		fromAP, ok := netip.AddrFromSlice(from.IP)
		if !ok {
			return
		}
		reply := t.onIncomingQuery(netip.AddrPortFrom(fromAP, uint16(from.Port)), &msg)
		if reply == nil {
			return
		}
		reply.T = msg.T
		reply.Y = yResponse
		body, err := bencode.Marshal(reply)
		if err != nil {
			t.logger.Debug("dht: marshal reply failed", slog.String("err", err.Error()))
			return
		}
		if _, err := t.conn.WriteToUDP(body, from); err != nil {
			t.logger.Debug("dht: reply send failed",
				slog.String("to", from.String()),
				slog.String("err", err.Error()),
			)
		}
	}
}
