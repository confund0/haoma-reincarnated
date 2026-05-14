package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"haoma/internal/proxy"
	"haoma/internal/xport"
)

type proxyServeRequest struct {
	Token     string `json:"token"`
	Modality  string `json:"modality"`
	LocalPort int    `json:"local_port"`
}

func (d *daemon) handleProxyServe(w http.ResponseWriter, r *http.Request) {
	if d.proxy == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("proxy manager not initialised"))
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		writeErr(w, http.StatusUnsupportedMediaType, errors.New("content-type must be application/json"))
		return
	}
	var req proxyServeRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Token == "" {
		writeErr(w, http.StatusBadRequest, errors.New("token required"))
		return
	}
	mod, err := proxy.ParseModality(req.Modality)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	switch err := d.proxy.RegisterServe(req.Token, mod, req.LocalPort); {
	case errors.Is(err, proxy.ErrTokenInUse):
		writeErr(w, http.StatusConflict, err)
		return
	case errors.Is(err, proxy.ErrInvalidLocalPort):
		writeErr(w, http.StatusBadRequest, err)
		return
	case err != nil:
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	slog.Debug("/proxy/serve registered",
		slog.String("modality", req.Modality),
		slog.Int("local_port", req.LocalPort),
	)
	w.WriteHeader(http.StatusCreated)
}

type proxyFetchRequest struct {
	Token     string `json:"token"`
	Modality  string `json:"modality"`
	PeerURL   string `json:"peer_url"`
	LocalPort int    `json:"local_port"`
}

func (d *daemon) handleProxyFetch(w http.ResponseWriter, r *http.Request) {
	if d.proxy == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("proxy manager not initialised"))
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		writeErr(w, http.StatusUnsupportedMediaType, errors.New("content-type must be application/json"))
		return
	}
	var req proxyFetchRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Token == "" {
		writeErr(w, http.StatusBadRequest, errors.New("token required"))
		return
	}
	if req.PeerURL == "" {
		writeErr(w, http.StatusBadRequest, errors.New("peer_url required"))
		return
	}
	mod, err := proxy.ParseModality(req.Modality)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	var hc *http.Client
	if peerID, ok := d.peerIDByDest(req.PeerURL); ok {
		hc, err = d.httpClientForPeer(peerID)
	} else {

		hc, err = xport.NewTorHTTPClient(d.cfg.torSocks, "haomad-proxy:"+req.Token)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	hc.Timeout = 0

	parent := d.bgCtx
	if parent == nil {
		parent = r.Context()
	}
	switch err := d.proxy.StartFetch(parent, req.Token, mod, req.PeerURL, req.LocalPort, hc); {
	case errors.Is(err, proxy.ErrTokenInUse):
		writeErr(w, http.StatusConflict, err)
		return
	case errors.Is(err, proxy.ErrInvalidLocalPort):
		writeErr(w, http.StatusBadRequest, err)
		return
	case err != nil:
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	slog.Debug("/proxy/fetch started",
		slog.String("modality", req.Modality),
		slog.Int("local_port", req.LocalPort),
		slog.String("peer_url", req.PeerURL),
	)
	w.WriteHeader(http.StatusCreated)
}

func (d *daemon) handleProxyCancel(w http.ResponseWriter, r *http.Request) {
	if d.proxy == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("proxy manager not initialised"))
		return
	}
	token := r.PathValue("token")
	if token == "" {
		writeErr(w, http.StatusBadRequest, errors.New("token required"))
		return
	}
	d.proxy.Cancel(token)
	w.WriteHeader(http.StatusNoContent)
}

func (d *daemon) handleProxyStreamGet(w http.ResponseWriter, r *http.Request) {
	if d.proxy == nil {
		http.Error(w, "proxy manager not initialised", http.StatusServiceUnavailable)
		return
	}
	token := r.PathValue("token")
	if token == "" {
		http.NotFound(w, r)
		return
	}
	if !d.proxy.HandleServe(w, r, token) {
		http.NotFound(w, r)
	}
}
