package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

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
	if err := conn.DelOnion(req.Address); err != nil {
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
		slog.String("address", req.Address),
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
		slog.String("retain", req.Retain),
	)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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
		slog.String("new_address", req.Address),
		slog.String("old_address", oldAddr),
	)
	writeJSON(w, http.StatusOK, map[string]string{
		"status":      "ok",
		"old_address": oldAddr,
	})
}
