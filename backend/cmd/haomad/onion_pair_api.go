package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"haoma/internal/pair"
	"haoma/internal/tor/control"
)

type onionInviteEntry struct {
	HandleID  string
	Words     []string
	ExpiresAt int64
	Pending   pair.PendingInvite
	CreatedAt time.Time
}

type onionInviteRegistry struct {
	mu      sync.Mutex
	entries map[string]*onionInviteEntry
}

func newOnionInviteRegistry() *onionInviteRegistry {
	return &onionInviteRegistry{entries: make(map[string]*onionInviteEntry)}
}

func (r *onionInviteRegistry) put(e *onionInviteEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[e.HandleID] = e
}

func (r *onionInviteRegistry) get(handle string) (*onionInviteEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[handle]
	return e, ok
}

func (r *onionInviteRegistry) drop(handle string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, handle)
}

type lockedOnionPublisher struct {
	d *daemon
}

func (p *lockedOnionPublisher) AddOnion(privateKey string, ports []control.OnionPort, flags ...string) (*control.Onion, error) {
	p.d.ctrlMu.Lock()
	defer p.d.ctrlMu.Unlock()
	if p.d.ctrlConn == nil {
		return nil, errors.New("pair: tor control not connected")
	}
	return p.d.ctrlConn.AddOnion(privateKey, ports, flags...)
}

func (p *lockedOnionPublisher) DelOnion(serviceID string) error {
	p.d.ctrlMu.Lock()
	defer p.d.ctrlMu.Unlock()
	if p.d.ctrlConn == nil {
		return errors.New("pair: tor control not connected")
	}
	return p.d.ctrlConn.DelOnion(serviceID)
}

func (d *daemon) ensureOnionDriver() (*pair.OnionDriver, error) {
	if d.torHTTP == nil {
		return nil, errors.New("pair: tor HTTP client not ready")
	}
	d.onionDriverOnce.Do(func() {
		d.onionDriver = pair.NewOnionDriver(&lockedOnionPublisher{d: d}, d.torHTTP)
	})
	return d.onionDriver, nil
}

type pairOnionInviteRequest struct {
	Payload        []byte `json:"payload"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

type pairOnionInviteResponse struct {
	HandleID  string   `json:"handle_id"`
	Words     []string `json:"words"`
	ExpiresAt int64    `json:"expires_at"`
}

func (d *daemon) handlePairOnionInvite(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		writeErr(w, http.StatusUnsupportedMediaType, errors.New("content-type must be application/json"))
		return
	}
	var req pairOnionInviteRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len(req.Payload) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("payload required"))
		return
	}
	driver, err := d.ensureOnionDriver()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}

	cReq := pair.CreateRequest{Payload: req.Payload}
	if req.TimeoutSeconds > 0 {
		cReq.Timeout = time.Duration(req.TimeoutSeconds) * time.Second
	}

	pi, err := driver.CreateInvite(d.bgCtx, cReq)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	handle, herr := newPairHandleID()
	if herr != nil {
		pi.Cancel()
		writeErr(w, http.StatusInternalServerError, herr)
		return
	}
	entry := &onionInviteEntry{
		HandleID:  handle,
		Words:     pi.OOB().Words,
		ExpiresAt: pi.ExpiresAt(),
		Pending:   pi,
		CreatedAt: time.Now(),
	}
	d.onionInvites.put(entry)

	slog.Info("pair: onion invite created",
		slog.String("handle_id", handle),
		slog.Int64("expires_at", entry.ExpiresAt),
	)

	go probeOnionInvite(d.bgCtx, d, handle, entry.Words, pi)

	writeJSON(w, http.StatusCreated, pairOnionInviteResponse{
		HandleID:  handle,
		Words:     entry.Words,
		ExpiresAt: entry.ExpiresAt,
	})
}

type pairOnionWaitResponse struct {
	JoinerPayload []byte `json:"joiner_payload"`
}

func (d *daemon) handlePairOnionWait(w http.ResponseWriter, r *http.Request) {
	handle := r.PathValue("handle_id")
	if handle == "" {
		writeErr(w, http.StatusBadRequest, errors.New("handle_id required"))
		return
	}
	slog.Debug("pair: onion-wait handler entered", slog.String("handle_id", handle))
	entry, ok := d.onionInvites.get(handle)
	if !ok {
		slog.Warn("pair: onion-wait unknown handle", slog.String("handle_id", handle))
		writeErr(w, http.StatusNotFound, errors.New("unknown handle"))
		return
	}
	slog.Debug("pair: onion-wait blocking on rendezvous",
		slog.String("handle_id", handle),
		slog.Int64("expires_at", entry.ExpiresAt),
	)
	wr, err := entry.Pending.Wait(r.Context())
	if err != nil {
		slog.Info("pair: onion-wait returned error",
			slog.String("handle_id", handle),
			slog.Any("err", err),
		)

		switch {
		case errors.Is(err, pair.ErrCancelled):
			d.onionInvites.drop(handle)
			writeErr(w, http.StatusGone, err)
		case errors.Is(err, pair.ErrTimedOut):
			d.onionInvites.drop(handle)
			writeErr(w, http.StatusGatewayTimeout, err)
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			writeErr(w, http.StatusRequestTimeout, err)
		default:
			d.onionInvites.drop(handle)
			writeErr(w, http.StatusInternalServerError, err)
		}
		return
	}
	slog.Info("pair: onion-wait returning joiner payload",
		slog.String("handle_id", handle),
		slog.Int("joiner_payload_bytes", len(wr.JoinerPayload)),
	)
	writeJSON(w, http.StatusOK, pairOnionWaitResponse{JoinerPayload: wr.JoinerPayload})
	d.onionInvites.drop(handle)
}

func (d *daemon) handlePairOnionCancel(w http.ResponseWriter, r *http.Request) {
	handle := r.PathValue("handle_id")
	if handle == "" {
		writeErr(w, http.StatusBadRequest, errors.New("handle_id required"))
		return
	}
	entry, ok := d.onionInvites.get(handle)
	if ok {
		entry.Pending.Cancel()
		d.onionInvites.drop(handle)
	}
	w.WriteHeader(http.StatusNoContent)
}

type pairOnionAcceptRequest struct {
	Words   []string `json:"words"`
	Payload []byte   `json:"payload"`
}

type pairOnionAcceptResponse struct {
	InviterPayload []byte `json:"inviter_payload"`
}

func (d *daemon) handlePairOnionAccept(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		writeErr(w, http.StatusUnsupportedMediaType, errors.New("content-type must be application/json"))
		return
	}
	var req pairOnionAcceptRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len(req.Words) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("words required"))
		return
	}
	if len(req.Payload) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("payload required"))
		return
	}
	driver, err := d.ensureOnionDriver()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	res, err := driver.AcceptInvite(r.Context(), pair.AcceptRequest{
		Blob:    pair.InviteBlob{Words: req.Words},
		Payload: req.Payload,
	})
	if err != nil {
		switch {
		case errors.Is(err, pair.ErrMACMismatch):
			writeErr(w, http.StatusUnauthorized, err)
		default:
			if _, isWordErr := err.(pair.ErrInvalidEFFShortWord); isWordErr {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
			writeErr(w, http.StatusBadGateway, err)
		}
		return
	}
	slog.Info("pair: onion accept ok",
		slog.Int("inviter_bytes", len(res.InviterPayload)),
	)
	writeJSON(w, http.StatusOK, pairOnionAcceptResponse{InviterPayload: res.InviterPayload})
}

func newPairHandleID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("pair: handle id: %w", err)
	}
	return strings.ToLower(hex.EncodeToString(b[:])), nil
}
