package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
)

func (d *daemon) handleTorRecover(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		writeErr(w, http.StatusUnsupportedMediaType, errors.New("content-type must be application/json"))
		return
	}
	var body struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("decode body: %w", err))
		return
	}
	if body.Mode != "newnym" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("unknown mode %q (want newnym)", body.Mode))
		return
	}

	d.ctrlMu.Lock()
	conn := d.ctrlConn
	d.ctrlMu.Unlock()
	if conn == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("tor control not yet up"))
		return
	}

	slog.Info("tor-recover: SIGNAL NEWNYM")
	if err := conn.Signal("NEWNYM"); err != nil {
		slog.Warn("tor-recover: SIGNAL NEWNYM failed", slog.Any("err", err))
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("SIGNAL NEWNYM: %w", err))
		return
	}
	slog.Info("tor-recover: SIGNAL NEWNYM ok")

	writeJSON(w, http.StatusOK, map[string]string{"mode": body.Mode})
}
