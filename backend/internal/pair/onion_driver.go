package pair

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"haoma/internal/tor/control"
)

const OnionDriverDefaultTimeout = 60 * time.Minute

const OnionDriverMaxTimeout = 60 * time.Minute

const OnionMaxHandshakeBytes = 64 << 10

type OnionPublisher interface {
	AddOnion(privateKey string, ports []control.OnionPort, flags ...string) (*control.Onion, error)
	DelOnion(serviceID string) error
}

type OnionDialer interface {
	Do(req *http.Request) (*http.Response, error)
}

type OnionDriver struct {
	pub    OnionPublisher
	dialer OnionDialer

	Timeout time.Duration

	nowFn func() time.Time

	skipServiceIDCheck bool
}

func NewOnionDriver(pub OnionPublisher, dialer OnionDialer) *OnionDriver {
	return &OnionDriver{
		pub:    pub,
		dialer: dialer,
		nowFn:  time.Now,
	}
}

func (d *OnionDriver) SkipServiceIDCheckForTest() { d.skipServiceIDCheck = true }

func (d *OnionDriver) Tag() string { return "onion" }

func (d *OnionDriver) CreateInvite(ctx context.Context, req CreateRequest) (PendingInvite, error) {
	if d.pub == nil {
		return nil, errors.New("pair: OnionDriver has no publisher")
	}
	if len(req.Payload) == 0 {
		return nil, errors.New("pair: CreateInvite payload empty")
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = OnionDriverDefaultTimeout
	}
	if timeout > OnionDriverMaxTimeout {
		timeout = OnionDriverMaxTimeout
	}

	words, err := GenerateOnionWords()
	if err != nil {
		return nil, fmt.Errorf("pair: generate words: %w", err)
	}
	mat, err := OnionDerive(words)
	if err != nil {
		return nil, fmt.Errorf("pair: derive material: %w", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("pair: bind local listener: %w", err)
	}

	o, err := d.pub.AddOnion(mat.TorExpandedKeyB64, []control.OnionPort{{VirtPort: mat.Port, Target: ln.Addr().String()}})
	if err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("pair: ADD_ONION: %w", err)
	}
	if !d.skipServiceIDCheck && o.ServiceID != mat.OnionAddress {

		_ = d.pub.DelOnion(o.ServiceID)
		_ = ln.Close()
		return nil, fmt.Errorf("pair: ADD_ONION returned %q, derivation predicted %q", o.ServiceID, mat.OnionAddress)
	}

	now := d.nowFn()
	expires := now.Add(timeout)

	pi := &onionPendingInvite{
		mat:           mat,
		ln:            ln,
		pub:           d.pub,
		serviceID:     o.ServiceID,
		expiresAtUnix: expires.Unix(),
		inviterBytes:  req.Payload,
		done:          make(chan struct{}),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST "+PairHandoffPath, pi.servePair)
	pi.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 30 * time.Second,
		ReadTimeout:       30 * time.Second,
	}

	pi.serveDone = make(chan struct{})
	go func() {
		defer close(pi.serveDone)
		if err := pi.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			slog.Warn("pair: ephemeral onion serve exited",
				slog.String("service_id", o.ServiceID),
				slog.Any("err", err),
			)
		}
	}()

	go func() {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-timer.C:
			pi.completeWith(handshakeOutcome{err: ErrTimedOut})
		case <-ctx.Done():
			pi.completeWith(handshakeOutcome{err: ctx.Err()})
		case <-pi.done:

		}
	}()

	slog.Info("pair: onion invite live",
		slog.String("service_id", o.ServiceID),
		slog.Int("port", mat.Port),
		slog.String("local_target", ln.Addr().String()),
		slog.Time("expires_at", expires),
	)
	return pi, nil
}

func (d *OnionDriver) AcceptInvite(ctx context.Context, req AcceptRequest) (AcceptResult, error) {
	if d.dialer == nil {
		return AcceptResult{}, errors.New("pair: OnionDriver has no dialer")
	}
	if len(req.Payload) == 0 {
		return AcceptResult{}, errors.New("pair: AcceptInvite payload empty")
	}
	mat, err := OnionDerive(req.Blob.Words)
	if err != nil {
		return AcceptResult{}, fmt.Errorf("pair: derive: %w", err)
	}

	url := fmt.Sprintf("http://%s.onion:%d%s", mat.OnionAddress, mat.Port, PairHandoffPath)

	reqCtx, reqCancel := context.WithTimeout(ctx, 30*time.Second)
	defer reqCancel()
	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(req.Payload))
	if err != nil {
		return AcceptResult{}, fmt.Errorf("pair: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", PairContentType)
	httpReq.Header.Set(PairMACHeader, JoinerHandshakeMAC(mat.HandoffHMAC, req.Payload))

	slog.Debug("pair: onion accept dialing",
		slog.String("service_id", mat.OnionAddress),
		slog.Int("port", mat.Port),
		slog.Int("payload_bytes", len(req.Payload)),
	)
	resp, err := d.dialer.Do(httpReq)
	if err != nil {
		return AcceptResult{}, fmt.Errorf("pair: dial onion: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, OnionMaxHandshakeBytes+1))
	if err != nil {
		return AcceptResult{}, fmt.Errorf("pair: read response: %w", err)
	}
	if len(body) > OnionMaxHandshakeBytes {
		return AcceptResult{}, fmt.Errorf("pair: response exceeds %d bytes", OnionMaxHandshakeBytes)
	}
	if resp.StatusCode != http.StatusOK {
		return AcceptResult{}, fmt.Errorf("pair: onion responded %d (%q)", resp.StatusCode, string(body))
	}
	macHex := resp.Header.Get(PairMACHeader)
	if macHex == "" {
		return AcceptResult{}, errors.New("pair: response missing MAC header")
	}
	if !VerifyInviterHandshakeMAC(mat.HandoffHMAC, body, macHex) {
		return AcceptResult{}, ErrMACMismatch
	}
	slog.Info("pair: onion handshake ok (joiner side)",
		slog.String("service_id", mat.OnionAddress),
		slog.Int("inviter_bytes", len(body)),
	)
	return AcceptResult{InviterPayload: body}, nil
}

type onionPendingInvite struct {
	mat           *OnionMaterial
	ln            net.Listener
	srv           *http.Server
	pub           OnionPublisher
	serviceID     string
	expiresAtUnix int64
	inviterBytes  []byte

	serveDone chan struct{}

	done chan struct{}

	finalMu sync.Mutex
	final   *handshakeOutcome

	served bool
}

type handshakeOutcome struct {
	joinerPayload []byte
	err           error
}

func (pi *onionPendingInvite) servePair(w http.ResponseWriter, r *http.Request) {
	slog.Info("pair: onion handshake — joiner connected",
		slog.String("service_id", pi.serviceID),
		slog.String("remote_addr", r.RemoteAddr),
	)
	body, err := io.ReadAll(io.LimitReader(r.Body, OnionMaxHandshakeBytes+1))
	if err != nil {
		slog.Warn("pair: onion handshake — read body failed", slog.String("service_id", pi.serviceID), slog.Any("err", err))
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	if len(body) == 0 {
		slog.Warn("pair: onion handshake — empty body", slog.String("service_id", pi.serviceID))
		http.Error(w, "empty body", http.StatusBadRequest)
		return
	}
	if len(body) > OnionMaxHandshakeBytes {
		slog.Warn("pair: onion handshake — body too large", slog.String("service_id", pi.serviceID), slog.Int("bytes", len(body)))
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	mac := r.Header.Get(PairMACHeader)
	if mac == "" {
		slog.Warn("pair: onion handshake — missing MAC header", slog.String("service_id", pi.serviceID))
		http.Error(w, "missing mac", http.StatusUnauthorized)
		return
	}
	if !VerifyJoinerHandshakeMAC(pi.mat.HandoffHMAC, body, mac) {
		slog.Warn("pair: onion handshake — joiner MAC verify failed (wrong words?)", slog.String("service_id", pi.serviceID))
		http.Error(w, "mac mismatch", http.StatusUnauthorized)
		return
	}
	slog.Debug("pair: onion handshake — joiner MAC verified", slog.String("service_id", pi.serviceID), slog.Int("joiner_bytes", len(body)))

	pi.finalMu.Lock()
	if pi.served {
		pi.finalMu.Unlock()
		slog.Warn("pair: onion handshake — second valid POST on consumed rendezvous, returning 410",
			slog.String("service_id", pi.serviceID),
			slog.String("remote_addr", r.RemoteAddr),
		)
		http.Error(w, "rendezvous already consumed", http.StatusGone)
		return
	}
	pi.served = true
	pi.finalMu.Unlock()

	w.Header().Set("Content-Type", PairContentType)
	w.Header().Set(PairMACHeader, InviterHandshakeMAC(pi.mat.HandoffHMAC, pi.inviterBytes))
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(pi.inviterBytes); err != nil {
		slog.Warn("pair: onion handshake response write failed",
			slog.String("service_id", pi.serviceID),
			slog.Any("err", err),
		)
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	slog.Info("pair: onion handshake ok (inviter side)",
		slog.String("service_id", pi.serviceID),
		slog.Int("joiner_bytes", len(body)),
	)

	go pi.completeWith(handshakeOutcome{joinerPayload: body})
}

func (pi *onionPendingInvite) OOB() OOB {
	return OOB{
		Words: append([]string(nil), pi.mat.Words...),
		Tag:   "onion",
	}
}

func (pi *onionPendingInvite) ExpiresAt() int64 { return pi.expiresAtUnix }

func (pi *onionPendingInvite) Wait(ctx context.Context) (WaitResult, error) {

	pi.finalMu.Lock()
	if pi.final != nil {
		out := *pi.final
		pi.finalMu.Unlock()
		return resultFromOutcome(out)
	}
	pi.finalMu.Unlock()

	select {
	case <-pi.done:

		pi.finalMu.Lock()
		out := *pi.final
		pi.finalMu.Unlock()
		return resultFromOutcome(out)
	case <-ctx.Done():
		return WaitResult{}, ctx.Err()
	}
}

func (pi *onionPendingInvite) Cancel() {
	pi.completeWith(handshakeOutcome{err: ErrCancelled})
}

func (pi *onionPendingInvite) completeWith(out handshakeOutcome) {
	pi.finalMu.Lock()
	if pi.final != nil {
		pi.finalMu.Unlock()
		return
	}
	pi.final = &out
	pi.finalMu.Unlock()

	close(pi.done)

	if pi.srv != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = pi.srv.Shutdown(shutdownCtx)
		cancel()
	} else if pi.ln != nil {
		_ = pi.ln.Close()
	}
	if pi.pub != nil && pi.serviceID != "" {
		if err := pi.pub.DelOnion(pi.serviceID); err != nil {
			slog.Warn("pair: DEL_ONION failed (rendezvous already torn down?)",
				slog.String("service_id", pi.serviceID),
				slog.Any("err", err),
			)
		}
	}
	slog.Info("pair: onion rendezvous closed",
		slog.String("service_id", pi.serviceID),
		slog.Bool("had_payload", len(out.joinerPayload) > 0),
		slog.Any("err", out.err),
	)

	pi.mat.Wipe()
}

func resultFromOutcome(out handshakeOutcome) (WaitResult, error) {
	if out.err != nil {
		return WaitResult{}, out.err
	}
	return WaitResult{JoinerPayload: out.joinerPayload}, nil
}
