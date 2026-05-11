package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"haoma/internal/pair"
)

const pairMACHeader = "X-Haoma-Pair-Mac"

const pairGUIDHeader = "X-Haoma-Pair-GUID"

var bounceCodes = [...]int{
	http.StatusBadRequest,
	http.StatusForbidden,
	http.StatusNotFound,
	http.StatusInternalServerError,
	http.StatusBadGateway,
}

func bounceStatus(w http.ResponseWriter) {
	var b [1]byte
	_, _ = randRead(b[:])
	w.WriteHeader(bounceCodes[int(b[0])%len(bounceCodes)])
}

func (d *daemon) ensurePairDHT(ctx context.Context) (*pair.DHT, error) {
	d.pairDHTMu.Lock()
	defer d.pairDHTMu.Unlock()
	if d.pairDHT != nil {
		return d.pairDHT, nil
	}
	slog.Info("pair: starting DHT client on demand")
	c, err := pair.StartDHT(ctx)
	if err != nil {
		return nil, err
	}
	d.pairDHT = c
	return c, nil
}

func (d *daemon) closePairDHT() {
	d.pairDHTMu.Lock()
	defer d.pairDHTMu.Unlock()
	if d.pairDHT != nil {
		d.pairDHT.Close()
		d.pairDHT = nil
	}
}

func (d *daemon) handlePairFetch(w http.ResponseWriter, r *http.Request) {
	if !d.allowPair() {
		slog.Warn("pair: /pair GET rate-limited", slog.String("remote", r.RemoteAddr))
		bounceStatus(w)
		return
	}
	guid := strings.TrimSpace(r.Header.Get(pairGUIDHeader))
	if guid == "" {
		bounceStatus(w)
		return
	}
	entry, err := d.dhtPairCache.Get(guid)
	if errors.Is(err, errDHTPairNotFound) {
		writeErr(w, http.StatusNotFound, errors.New("pair: unknown guid"))
		return
	}
	if errors.Is(err, errDHTPairExpired) {
		writeErr(w, http.StatusGone, errors.New("pair: invite expired"))
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	slog.Info("pair: served invite",
		slog.String("guid", guid),
		slog.String("remote", r.RemoteAddr),
	)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(entry.InviteJSON)
}

func (d *daemon) handlePairReturn(w http.ResponseWriter, r *http.Request) {
	if !d.allowPair() {
		slog.Warn("pair: /pair/return POST rate-limited", slog.String("remote", r.RemoteAddr))
		bounceStatus(w)
		return
	}
	guid := strings.TrimSpace(r.Header.Get(pairGUIDHeader))
	macHex := strings.TrimSpace(r.Header.Get(pairMACHeader))
	if guid == "" || macHex == "" {
		bounceStatus(w)
		return
	}
	mac, err := hex.DecodeString(macHex)
	if err != nil || len(mac) != sha256.Size {
		bounceStatus(w)
		return
	}
	entry, err := d.dhtPairCache.Get(guid)
	if errors.Is(err, errDHTPairNotFound) {
		writeErr(w, http.StatusNotFound, errors.New("pair: unknown guid"))
		return
	}
	if errors.Is(err, errDHTPairExpired) {
		writeErr(w, http.StatusGone, errors.New("pair: invite expired"))
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("read body: %w", err))
		return
	}
	secret, err := hex.DecodeString(entry.SecretHex)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("pair: cached secret invalid: %w", err))
		return
	}

	h := hmac.New(sha256.New, secret)
	h.Write([]byte(guid))
	h.Write(body)
	want := h.Sum(nil)
	if !hmac.Equal(want, mac) {
		slog.Warn("pair: /pair/return MAC mismatch",
			slog.String("guid", guid),
			slog.String("remote", r.RemoteAddr),
		)
		writeErr(w, http.StatusUnauthorized, errors.New("pair: mac mismatch"))
		return
	}

	entry.ReturnInvite = body
	entry.ReturnAt = time.Now().Unix()
	if err := d.dhtPairCache.Put(entry); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	slog.Info("pair: return invite received",
		slog.String("guid", guid),
		slog.Int("bytes", len(body)),
	)
	w.WriteHeader(http.StatusAccepted)
}

type dhtInviteRequest struct {
	InviteJSON []byte `json:"invite_json"`
	SecretHex  string `json:"secret_hex"`
}

type dhtInviteResponse struct {
	GUID            string   `json:"guid"`
	IDWords         []string `json:"id_words"`
	PassphraseWords []string `json:"passphrase_words"`
	ExpiresAt       int64    `json:"expires_at"`
}

func (d *daemon) handleDHTInvite(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		writeErr(w, http.StatusUnsupportedMediaType, errors.New("content-type must be application/json"))
		return
	}
	var req dhtInviteRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len(req.InviteJSON) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("invite_json required"))
		return
	}
	if req.SecretHex == "" {
		writeErr(w, http.StatusBadRequest, errors.New("secret_hex required"))
		return
	}
	if _, err := hex.DecodeString(req.SecretHex); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("secret_hex not valid hex: %w", err))
		return
	}

	d.ctrlMu.Lock()
	conn := d.ctrlConn
	d.ctrlMu.Unlock()
	if conn == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("tor control not yet up"))
		return
	}
	ports := d.xportPorts()
	if len(ports) == 0 {
		writeErr(w, http.StatusServiceUnavailable, errors.New("xport listener not yet bound"))
		return
	}
	dhtOnion, err := conn.AddOnionNew(ports)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("ADD_ONION (dht-pair): %w", err))
		return
	}
	onionURL := "http://" + dhtOnion.ServiceID + ".onion"

	dhtCtx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	dhtClient, err := d.ensurePairDHT(dhtCtx)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, fmt.Errorf("pair: dht start: %w", err))
		return
	}
	mats, err := pair.Publish(dhtCtx, dhtClient, onionURL, time.Now())
	if err != nil {
		writeErr(w, http.StatusBadGateway, fmt.Errorf("pair: dht publish: %w", err))
		return
	}
	entry := dhtPairCacheEntry{
		GUID:       mats.GUID,
		InviteJSON: req.InviteJSON,
		SecretHex:  req.SecretHex,
		Materials:  *mats,
		CreatedAt:  time.Now().Unix(),
		ExpiresAt:  mats.ExpiresAt.Unix(),
	}
	if err := d.dhtPairCache.Put(entry); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, dhtInviteResponse{
		GUID:            mats.GUID,
		IDWords:         mats.IDWords,
		PassphraseWords: mats.PassphraseWords,
		ExpiresAt:       mats.ExpiresAt.Unix(),
	})
}

func (d *daemon) handleDHTCancel(w http.ResponseWriter, r *http.Request) {
	guid := r.PathValue("guid")
	entry, err := d.dhtPairCache.Get(guid)
	if errors.Is(err, errDHTPairNotFound) || errors.Is(err, errDHTPairExpired) {
		writeErr(w, http.StatusNotFound, errors.New("pair: unknown guid"))
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	dhtCtx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	dhtClient, err := d.ensurePairDHT(dhtCtx)
	if err == nil {
		if rerr := pair.Revoke(dhtCtx, dhtClient, entry.Materials.IDEntropy); rerr != nil {
			slog.Warn("pair: dht tombstone failed (cache row dropped anyway)",
				slog.String("guid", guid),
				slog.Any("err", rerr),
			)
		}
	} else {
		slog.Warn("pair: dht start failed during revoke (cache row dropped anyway)",
			slog.String("guid", guid),
			slog.Any("err", err),
		)
	}
	if err := d.dhtPairCache.Delete(guid); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type dhtFetchBootstrapRequest struct {
	IDWords         []string `json:"id_words"`
	PassphraseWords []string `json:"passphrase_words"`
}

type dhtProxyFetchRequest struct {
	OnionURL string `json:"onion_url"`
	GUID     string `json:"guid"`
}

func (d *daemon) handleDHTProxyFetch(w http.ResponseWriter, r *http.Request) {
	var req dhtProxyFetchRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.OnionURL == "" || req.GUID == "" {
		writeErr(w, http.StatusBadRequest, errors.New("onion_url and guid required"))
		return
	}
	if d.torHTTP == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("tor HTTP client not ready"))
		return
	}

	up, err := http.NewRequestWithContext(r.Context(), http.MethodGet, strings.TrimRight(req.OnionURL, "/")+"/pair", nil)
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("build upstream: %w", err))
		return
	}
	up.Header.Set(pairGUIDHeader, req.GUID)

	resp, err := d.torHTTP.Do(up)
	if err != nil {
		writeErr(w, http.StatusBadGateway, fmt.Errorf("tor fetch: %w", err))
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(http.MaxBytesReader(w, resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		writeErr(w, resp.StatusCode, fmt.Errorf("upstream /pair: %s (%q)", resp.Status, strings.TrimSpace(string(body))))
		return
	}
	slog.Info("pair: proxy fetch ok",
		slog.String("guid", req.GUID),
		slog.String("onion", req.OnionURL),
		slog.Int("bytes", len(body)),
	)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

type dhtProxyReturnRequest struct {
	OnionURL   string `json:"onion_url"`
	GUID       string `json:"guid"`
	SecretHex  string `json:"secret_hex"`
	ReturnBody []byte `json:"return_body"`
}

func (d *daemon) handleDHTProxyReturn(w http.ResponseWriter, r *http.Request) {
	var req dhtProxyReturnRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.OnionURL == "" || req.GUID == "" || req.SecretHex == "" || len(req.ReturnBody) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("onion_url, guid, secret_hex, return_body all required"))
		return
	}
	if d.torHTTP == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("tor HTTP client not ready"))
		return
	}
	secret, err := hex.DecodeString(req.SecretHex)
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("secret_hex: %w", err))
		return
	}

	h := hmac.New(sha256.New, secret)
	h.Write([]byte(req.GUID))
	h.Write(req.ReturnBody)
	mac := hex.EncodeToString(h.Sum(nil))

	up, err := http.NewRequestWithContext(
		r.Context(), http.MethodPost,
		strings.TrimRight(req.OnionURL, "/")+"/pair/return",
		strings.NewReader(string(req.ReturnBody)),
	)
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("build upstream: %w", err))
		return
	}
	up.Header.Set(pairGUIDHeader, req.GUID)
	up.Header.Set(pairMACHeader, mac)
	up.Header.Set("Content-Type", "application/json")

	resp, err := d.torHTTP.Do(up)
	if err != nil {
		writeErr(w, http.StatusBadGateway, fmt.Errorf("tor send return: %w", err))
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(http.MaxBytesReader(w, resp.Body, 4<<10))
	if resp.StatusCode >= 300 {
		writeErr(w, resp.StatusCode, fmt.Errorf("upstream /pair/return: %s (%q)", resp.Status, strings.TrimSpace(string(body))))
		return
	}
	slog.Info("pair: proxy return-send ok",
		slog.String("guid", req.GUID),
		slog.String("onion", req.OnionURL),
	)
	w.WriteHeader(http.StatusAccepted)
}

type dhtPendingEntry struct {
	GUID         string `json:"guid"`
	ReturnInvite []byte `json:"return_invite"`
	ReturnAt     int64  `json:"return_at"`
}

func (d *daemon) handleDHTPending(w http.ResponseWriter, _ *http.Request) {
	entries, err := d.dhtPairCache.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]dhtPendingEntry, 0, len(entries))
	for _, e := range entries {
		if len(e.ReturnInvite) == 0 {
			continue
		}
		out = append(out, dhtPendingEntry{
			GUID:         e.GUID,
			ReturnInvite: e.ReturnInvite,
			ReturnAt:     e.ReturnAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"pending": out})
}

func (d *daemon) handleDHTFetchBootstrap(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		writeErr(w, http.StatusUnsupportedMediaType, errors.New("content-type must be application/json"))
		return
	}
	var req dhtFetchBootstrapRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len(req.IDWords) == 0 || len(req.PassphraseWords) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("id_words and passphrase_words required"))
		return
	}
	dhtCtx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	dhtClient, err := d.ensurePairDHT(dhtCtx)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, fmt.Errorf("pair: dht start: %w", err))
		return
	}
	payload, err := pair.Fetch(dhtCtx, dhtClient, req.IDWords, req.PassphraseWords)
	if err != nil {

		switch {
		case errors.Is(err, pair.ErrItemNotFound):
			writeErr(w, http.StatusNotFound, err)
		case errors.Is(err, pair.ErrDecrypt):
			writeErr(w, http.StatusForbidden, err)
		case errors.Is(err, pair.ErrExpired):
			writeErr(w, http.StatusGone, err)
		default:
			if _, isWordErr := err.(pair.ErrInvalidWord); isWordErr {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
			writeErr(w, http.StatusBadGateway, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, payload)
}
