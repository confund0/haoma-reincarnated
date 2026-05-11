package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"haoma/internal/peers"
	"haoma/internal/eventbus"
	"haoma/internal/outbox"
	"haoma/internal/xport"
)

var eventsHeartbeat = 20 * time.Second

func (d *daemon) apiHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", d.handleHealth)
	mux.HandleFunc("GET /tor", d.handleTor)
	mux.HandleFunc("POST /tor-password", d.handleTorPassword)
	mux.HandleFunc("POST /onion/mint", d.handleOnionMint)
	mux.HandleFunc("POST /onion/del", d.handleOnionDel)
	mux.HandleFunc("GET /peers", d.handleListPeers)
	mux.HandleFunc("POST /peers", d.handleImportPeer)
	mux.HandleFunc("GET /peers/{id}", d.handleGetPeer)
	mux.HandleFunc("DELETE /peers/{id}", d.handleDeletePeer)
	mux.HandleFunc("POST /peers/{id}/action", d.handlePeerAction)
	mux.HandleFunc("POST /peers/{id}/overlay-address", d.handleOverlayPeerAddress)
	mux.HandleFunc("POST /peers/{id}/collapse-address", d.handleCollapsePeerAddress)
	mux.HandleFunc("POST /peers/{id}/rotate-own-onion", d.handleRotateOwnOnion)
	mux.HandleFunc("GET /stats", d.handleStats)
	mux.HandleFunc("POST /send", d.handleSend)
	mux.HandleFunc("GET /inbox", d.handleInboxList)
	mux.HandleFunc("DELETE /inbox/{id}", d.handleInboxDelete)
	mux.HandleFunc("GET /outbox", d.handleOutboxList)
	mux.HandleFunc("GET /outbox/{id}", d.handleOutboxGet)
	mux.HandleFunc("GET /ids/stats", d.handleIDSStats)
	mux.HandleFunc("GET /events", d.handleEvents)

	mux.HandleFunc("POST /pair/publish", d.handleDHTInvite)
	mux.HandleFunc("DELETE /pair/publish/{guid}", d.handleDHTCancel)
	mux.HandleFunc("POST /pair/fetch", d.handleDHTFetchBootstrap)

	mux.HandleFunc("POST /pair/proxy/fetch", d.handleDHTProxyFetch)
	mux.HandleFunc("POST /pair/proxy/return", d.handleDHTProxyReturn)

	mux.HandleFunc("GET /pair/return/pending", d.handleDHTPending)

	mux.HandleFunc("POST /pair/onion/invite", d.handlePairOnionInvite)
	mux.HandleFunc("POST /pair/onion/wait/{handle_id}", d.handlePairOnionWait)
	mux.HandleFunc("DELETE /pair/onion/invite/{handle_id}", d.handlePairOnionCancel)
	mux.HandleFunc("POST /pair/onion/accept", d.handlePairOnionAccept)

	mux.HandleFunc("POST /files", d.handleStageFile)
	mux.HandleFunc("DELETE /files/{msg_id}", d.handleDropFile)

	mux.HandleFunc("POST /files/fetch", d.handleFetchFile)
	mux.HandleFunc("GET /files/staging/{msg_id}", d.handleStagingGet)
	mux.HandleFunc("DELETE /files/staging/{msg_id}", d.handleStagingDelete)

	mux.HandleFunc("POST /files/retry-failed", d.handleRetryFailed)

	mux.HandleFunc("POST /files/receipts", d.handleReceiveFileReceipt)

	mux.HandleFunc("POST /proxy/serve", d.handleProxyServe)
	mux.HandleFunc("POST /proxy/fetch", d.handleProxyFetch)
	mux.HandleFunc("DELETE /proxy/{token}", d.handleProxyCancel)
	return mux
}

func (d *daemon) handleIDSStats(w http.ResponseWriter, _ *http.Request) {
	if d.ids == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("ids not initialized"))
		return
	}
	writeJSON(w, http.StatusOK, d.ids.Snapshot())
}

func (d *daemon) handleEvents(w http.ResponseWriter, r *http.Request) {
	if d.bus == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("event bus not initialized"))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, errors.New("streaming unsupported on this ResponseWriter"))
		return
	}

	filter := parseTopicFilter(r.URL.Query().Get("topic"))

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	d.attachedHaoma.Add(1)
	defer d.attachedHaoma.Add(-1)

	sub, cancel := d.bus.Subscribe("", 64)
	defer cancel()

	if len(filter) > 0 {
		slog.Debug("/events stream open with topic filter",
			slog.Any("prefixes", filter),
		)
	}

	tick := time.NewTicker(eventsHeartbeat)
	defer tick.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case ev, ok := <-sub:
			if !ok {
				return
			}
			if !topicMatchesFilter(ev.Topic, filter) {
				continue
			}
			data, err := json.Marshal(ev.Payload)
			if err != nil {
				slog.Warn("/events marshal failed",
					slog.String("topic", ev.Topic),
					slog.Any("err", err),
				)
				continue
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Topic, data); err != nil {
				return
			}
			flusher.Flush()
			logEventPush(ev)
		}
	}
}

func parseTopicFilter(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func topicMatchesFilter(topic string, filter []string) bool {
	if len(filter) == 0 {
		return true
	}
	for _, p := range filter {
		if strings.HasPrefix(topic, p) {
			return true
		}
	}
	return false
}

func logEventPush(ev eventbus.Event) {
	switch ev.Topic {
	case eventbus.TopicDeliveryStateChanged:
		if ds, ok := ev.Payload.(outbox.DeliveryStatus); ok {
			slog.Debug("/events delivery.state-changed push",
				slog.String("envelope_id", ds.EnvelopeID),
				slog.String("state", ds.State),
			)
			return
		}
	case eventbus.TopicInboxReceived:
		if e, ok := ev.Payload.(inboxEntry); ok {
			slog.Debug("/events inbox.received push",
				slog.String("envelope_id", e.Envelope.ID),
				slog.String("peer_id", e.PeerID),
			)
			return
		}
	case eventbus.TopicPeerPresenceChanged:
		if obs, ok := ev.Payload.(peerPresenceObservation); ok {
			slog.Debug("/events peer.presence-changed push",
				slog.String("peer_id", obs.PeerID),
				slog.String("source", obs.Source),
			)
			return
		}
	case eventbus.TopicPeerLastSeenChanged:
		if obs, ok := ev.Payload.(peerLastSeenObservation); ok {
			slog.Debug("/events peer.last-seen-changed push",
				slog.String("peer_id", obs.PeerID),
				slog.Int64("last_active_at", obs.LastActiveAt),
				slog.Int64("last_passive_at", obs.LastPassiveAt),
			)
			return
		}
	case eventbus.TopicPairOnionProbe:
		if obs, ok := ev.Payload.(pairProbeObservation); ok {
			slog.Debug("/events pair.onion-probe push",
				slog.String("handle_id", obs.HandleID),
				slog.Int("attempt", obs.Attempt),
				slog.Bool("ready", obs.Ready),
			)
			return
		}
	}
	slog.Debug("/events push", slog.String("topic", ev.Topic))
}

func (d *daemon) handleOnionMint(w http.ResponseWriter, _ *http.Request) {
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
	o, err := conn.AddOnionNew(ports)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("ADD_ONION NEW: %w", err))
		return
	}
	slog.Info("persistent onion minted",
		slog.String("service_id", o.ServiceID),
	)
	writeJSON(w, http.StatusOK, map[string]string{
		"address":     o.ServiceID,
		"private_key": o.PrivateKey,
	})
}

type peerView struct {
	ID                   string         `json:"id"`
	KnownAddresses       []string       `json:"known_addresses"`
	IDSCounters          map[string]int `json:"ids_counters,omitempty"`
	LastActiveAt         int64          `json:"last_active_at,omitempty"`
	LastPassiveAt        int64          `json:"last_passive_at,omitempty"`
	RetiredAt            int64          `json:"retired_at,omitempty"`
	PrevMyOnionExpiresAt int64          `json:"prev_my_onion_expires_at,omitempty"`
}

func viewOf(p peers.Peer) peerView {
	v := peerView{
		ID:             p.ID,
		KnownAddresses: p.KnownAddresses,
		IDSCounters:    p.IDSCounters,
		LastActiveAt:   p.LastActiveAt,
		LastPassiveAt:  p.LastPassiveAt,
		RetiredAt:      p.RetiredAt,
	}
	if p.PrevMyOnion != nil {
		v.PrevMyOnionExpiresAt = p.PrevMyOnion.ExpiresAt
	}
	return v
}

func (d *daemon) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": version})
}

type torSlot struct {
	Slot      int    `json:"slot"`
	ServiceID string `json:"service_id"`
	URL       string `json:"url"`
}

type torHealth struct {
	Bootstrap   int  `json:"bootstrap"`
	Ready       bool `json:"ready"`
	Unreachable bool `json:"unreachable"`
}

func (d *daemon) handleTor(w http.ResponseWriter, _ *http.Request) {
	var h torHealth
	if d.torPoller != nil {
		s := d.torPoller.Status()
		h = torHealth{Bootstrap: s.Bootstrap, Ready: s.Ready, Unreachable: s.Unreachable}
	}
	d.verifiedSlotsMu.RLock()
	slots := d.verifiedSlots
	d.verifiedSlotsMu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]any{"slots": slots, "health": h})
}

func (d *daemon) handleTorPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("decode body: %w", err))
		return
	}
	d.setTorPassword(body.Password)
	slog.Info("tor password updated; watchdog kicked")
	writeJSON(w, http.StatusAccepted, map[string]string{"state": "queued"})
}

func (d *daemon) handleListPeers(w http.ResponseWriter, _ *http.Request) {
	peers, err := d.registry.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	views := make([]peerView, len(peers))
	for i, p := range peers {
		views[i] = viewOf(p)
	}
	writeJSON(w, http.StatusOK, map[string]any{"peers": views})
}

func (d *daemon) handleGetPeer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := d.registry.Get(id)
	if errors.Is(err, peers.ErrPeerNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, viewOf(*p))
}

func (d *daemon) handleImportPeer(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		writeErr(w, http.StatusUnsupportedMediaType, errors.New("content-type must be application/json"))
		return
	}
	var inv peers.BackendInvite
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&inv); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	displacedAddrs := d.collectCollidingMyOnions(inv.Addresses, inv.PeerID)

	retired, err := d.registry.Import(&inv)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len(retired) > 0 {
		slog.Info("/peers import retired prior peers (addr collision)",
			slog.String("new_peer_id", inv.PeerID),
			slog.Any("retired", retired),
		)

		for _, addr := range displacedAddrs {
			d.unpublishPeerOnion(addr)
		}
	}

	if inv.MyOnionPrivateKey != "" {
		newPeer, gerr := d.registry.Get(inv.PeerID)
		if gerr == nil && newPeer != nil {
			if perr := d.publishPeerOnion(*newPeer); perr != nil {

				slog.Debug("publishPeerOnion post-Import",
					slog.String("peer_id", inv.PeerID),
					slog.Any("err", perr),
				)
			}
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"retired": retired})
}

func (d *daemon) collectCollidingMyOnions(newAddrs []string, newPeerID string) []string {
	if len(newAddrs) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	var addrs []string
	for _, a := range newAddrs {
		existing, err := d.registry.ByAddress(a)
		if err != nil || existing == nil {
			continue
		}
		if existing.ID == newPeerID {
			continue
		}
		if existing.MyOnionAddr == "" {
			continue
		}
		if _, dup := seen[existing.MyOnionAddr]; dup {
			continue
		}
		seen[existing.MyOnionAddr] = struct{}{}
		addrs = append(addrs, existing.MyOnionAddr)
	}
	return addrs
}

func (d *daemon) handleDeletePeer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	pre, _ := d.registry.Get(id)
	if err := d.registry.Remove(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	d.unpublishPeerOnionsFromSnapshot(pre)
	w.WriteHeader(http.StatusNoContent)
}

func (d *daemon) handlePeerAction(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		writeErr(w, http.StatusUnsupportedMediaType, errors.New("content-type must be application/json"))
		return
	}
	id := r.PathValue("id")
	var req struct {
		Action string `json:"action"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	switch req.Action {
	case "retire":

		pre, gerr := d.registry.Get(id)
		if errors.Is(gerr, peers.ErrPeerNotFound) {
			writeErr(w, http.StatusNotFound, gerr)
			return
		}
		if gerr != nil {
			writeErr(w, http.StatusInternalServerError, gerr)
			return
		}
		if err := d.registry.Retire(id); err != nil {
			if errors.Is(err, peers.ErrPeerNotFound) {
				writeErr(w, http.StatusNotFound, err)
				return
			}
			writeErr(w, http.StatusInternalServerError, err)
			return
		}

		d.unpublishPeerOnionsFromSnapshot(pre)
		p, err := d.registry.Get(id)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, viewOf(*p))

	case "delete":

		p, err := d.registry.Get(id)
		if errors.Is(err, peers.ErrPeerNotFound) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if err := d.registry.Remove(id); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		d.unpublishPeerOnionsFromSnapshot(p)
		writeJSON(w, http.StatusOK, viewOf(*p))

	default:
		writeErr(w, http.StatusBadRequest, fmt.Errorf("unknown action %q (want retire|delete)", req.Action))
	}
}

func (d *daemon) handleStats(w http.ResponseWriter, _ *http.Request) {
	st, err := d.registry.Stats()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("writeJSON encode failed",
			slog.Int("status", code),
			slog.Any("err", err),
		)
	}
}

func writeErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if encErr := json.NewEncoder(w).Encode(map[string]string{"error": err.Error()}); encErr != nil {
		slog.Warn("writeErr encode failed",
			slog.Int("status", code),
			slog.Any("orig_err", err),
			slog.Any("enc_err", encErr),
		)
	}
}

type sendRequest struct {
	PeerID  string `json:"peer_id"`
	Payload []byte `json:"payload"`

	Kind           string `json:"kind,omitempty"`
	PresenceSource string `json:"presence_source,omitempty"`
}

type sendResponse struct {
	EnvelopeID string `json:"envelope_id"`
}

func (d *daemon) handleSend(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		writeErr(w, http.StatusUnsupportedMediaType, errors.New("content-type must be application/json"))
		return
	}
	var req sendRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.PeerID == "" {
		writeErr(w, http.StatusBadRequest, errors.New("peer_id required"))
		return
	}
	peer, err := d.registry.Get(req.PeerID)
	if errors.Is(err, peers.ErrPeerNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if peer.RetiredAt != 0 {
		slog.Debug("/send rejected: peer retired",
			slog.String("peer_id", req.PeerID),
			slog.Int64("retired_at", peer.RetiredAt),
		)
		writeJSON(w, http.StatusGone, map[string]any{
			"error":      "peer_retired",
			"retired_at": peer.RetiredAt,
		})
		return
	}
	if len(peer.KnownAddresses) == 0 {
		writeErr(w, http.StatusUnprocessableEntity, errors.New("peer has no known addresses"))
		return
	}
	if peer.MyOnionAddr == "" {
		writeErr(w, http.StatusFailedDependency, errors.New("peer has no published per-peer onion (pair pre-ADR-043?)"))
		return
	}
	if d.worker == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("outbox worker not initialized"))
		return
	}

	envID, err := newEnvelopeID()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	env := xport.Envelope{
		ID:             envID,
		Timestamp:      time.Now().Unix(),
		From:           peer.MyOnionAddr,
		Kind:           req.Kind,
		PresenceSource: req.PresenceSource,
		Payload:        req.Payload,
	}
	env = xport.Sign(env, peer.OutboundSecret)

	dest := "http://" + peer.KnownAddresses[0] + ".onion"
	slog.Debug("/send enqueuing envelope",
		slog.String("envelope_id", envID),
		slog.String("peer_id", req.PeerID),
		slog.String("dest", dest),
		slog.Int("payload_bytes", len(req.Payload)),
	)
	if err := d.worker.Enqueue(dest, env); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, sendResponse{EnvelopeID: envID})
}

func (d *daemon) handleOutboxList(w http.ResponseWriter, r *http.Request) {
	if d.worker == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("outbox worker not initialized"))
		return
	}
	state := r.URL.Query().Get("state")
	if state == "" {
		state = outbox.StateEnqueued
	}
	var since int64
	if s := r.URL.Query().Get("since"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			writeErr(w, http.StatusBadRequest, errors.New("since must be an int64 unix nanos"))
			return
		}
		since = v
	}
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		v, err := strconv.Atoi(l)
		if err != nil || v <= 0 {
			writeErr(w, http.StatusBadRequest, errors.New("limit must be a positive int"))
			return
		}
		limit = v
	}
	rows, err := d.worker.ListByState(state, since, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows})
}

func (d *daemon) handleOutboxGet(w http.ResponseWriter, r *http.Request) {
	if d.worker == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("outbox worker not initialized"))
		return
	}
	id := r.PathValue("id")
	row, err := d.worker.Load(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("outbox entry not found: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (d *daemon) handleInboxList(w http.ResponseWriter, r *http.Request) {
	sinceStr := r.URL.Query().Get("since")
	var since int64
	if sinceStr != "" {
		v, err := strconv.ParseInt(sinceStr, 10, 64)
		if err != nil {
			writeErr(w, http.StatusBadRequest, errors.New("since must be an int64 unix nanos"))
			return
		}
		since = v
	}
	limit := 0
	if l := r.URL.Query().Get("limit"); l != "" {
		v, err := strconv.Atoi(l)
		if err != nil || v <= 0 {
			writeErr(w, http.StatusBadRequest, errors.New("limit must be a positive int"))
			return
		}
		limit = v
	}
	entries, err := d.inbox.List(since, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

func (d *daemon) handleInboxDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := d.inbox.Delete(id); err != nil {
		if errors.Is(err, errInboxNotFound) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
