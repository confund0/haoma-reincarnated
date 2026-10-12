package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"haoma/internal/logid"
	"haoma/internal/peers"
)

func (d *daemon) handleOnionDel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Address string `json:"address"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("decode body: %w", err))
		return
	}
	if req.Address == "" {
		writeErr(w, http.StatusBadRequest, errors.New("address required"))
		return
	}
	d.ctrlMu.Lock()
	conn := d.ctrlConn
	d.ctrlMu.Unlock()
	if conn == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("tor control not yet up"))
		return
	}
	err := conn.DelOnion(req.Address)
	d.published.forget(conn, req.Address, err)
	if err != nil {
		slog.Warn("DelOnion via /onion/del failed",
			slog.String("service_id", req.Address),
			slog.Any("err", err),
		)
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("DEL_ONION: %w", err))
		return
	}
	slog.Info("onion deleted via /onion/del",
		slog.String("service_id", req.Address),
	)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (d *daemon) handleOverlayPeerAddress(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, errors.New("peer id required"))
		return
	}
	var req struct {
		Address string `json:"address"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("decode body: %w", err))
		return
	}
	if err := d.registry.OverlayPeerAddress(id, req.Address); err != nil {
		if errors.Is(err, peers.ErrPeerNotFound) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	slog.Info("peer address overlaid",
		slog.String("peer_id", id),
		slog.String("address", logid.Short(req.Address)),
	)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (d *daemon) handleCollapsePeerAddress(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, errors.New("peer id required"))
		return
	}
	var req struct {
		Retain string `json:"retain"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("decode body: %w", err))
		return
	}
	if err := d.registry.CollapsePeerAddress(id, req.Retain); err != nil {
		if errors.Is(err, peers.ErrPeerNotFound) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	slog.Info("peer address collapsed",
		slog.String("peer_id", id),
		slog.String("retain", logid.Short(req.Retain)),
	)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

var newCircuitControlTimeout = 15 * time.Second

func (d *daemon) handleNewCircuitForPeer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, errors.New("peer id required"))
		return
	}
	peer, err := d.registry.Get(id)
	if err != nil {
		if errors.Is(err, peers.ErrPeerNotFound) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if len(peer.KnownAddresses) == 0 {
		writeJSON(w, http.StatusOK, map[string]int{"closed": 0})
		return
	}
	d.ctrlMu.Lock()
	conn := d.ctrlConn
	d.ctrlMu.Unlock()
	if conn == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("tor control not yet up"))
		return
	}

	if !d.routes.startManual(id) {
		writeErr(w, http.StatusConflict, errors.New("a new circuit for this peer is still waiting on tor"))
		return
	}
	type result struct {
		closed int
		err    error
	}
	done := make(chan result, 1)
	go func() {
		defer d.routes.endManual(id)
		n, err := closePeerRendCircuits(conn, id, peer.KnownAddresses)
		done <- result{n, err}
	}()
	var closed int
	select {
	case res := <-done:
		if res.err != nil {
			writeErr(w, http.StatusInternalServerError, res.err)
			return
		}
		closed = res.closed
	case <-time.After(newCircuitControlTimeout):
		writeErr(w, http.StatusGatewayTimeout, errors.New("tor control did not answer in time"))
		return
	case <-r.Context().Done():
		return
	}

	d.routes.arm(id, time.Now())
	slog.Info("new tor circuit requested for peer",
		slog.String("peer_id", id),
		slog.Int("closed", closed),
	)
	writeJSON(w, http.StatusOK, map[string]int{"closed": closed})
}

func (d *daemon) handleRotateOwnOnion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, errors.New("peer id required"))
		return
	}
	var req struct {
		Address    string `json:"address"`
		PrivateKey string `json:"private_key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("decode body: %w", err))
		return
	}
	oldAddr, err := d.registry.RotateOwnOnion(id, req.Address, req.PrivateKey)
	if err != nil {
		if errors.Is(err, peers.ErrPeerNotFound) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	slog.Info("peer own-onion rotated",
		slog.String("peer_id", id),
		slog.String("new_address", logid.Short(req.Address)),
		slog.String("old_address", logid.Short(oldAddr)),
	)
	writeJSON(w, http.StatusOK, map[string]string{
		"status":      "ok",
		"old_address": oldAddr,
	})
}
