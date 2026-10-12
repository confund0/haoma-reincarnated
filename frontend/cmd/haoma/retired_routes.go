package main

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/ipc"
)

const (
	routesOutcomeNone           = "noop_no_retired_route"
	routesOutcomeDropped        = "retired_routes_dropped"
	routesOutcomeMoved          = "primary_moved_to_live_pairing"
	routesOutcomeNoLivePairing  = "kept_no_live_pairing"
	routesOutcomeNoFingerprint  = "kept_no_fingerprint"
	routesOutcomeFingerprintBad = "kept_fingerprint_mismatch"
)

const routesDroppedMsg = "chat routing set: retired pairings left it"

type retiredRoutesPlan struct {
	primary string
	drop    []string
	outcome string
}

func planRetiredRoutes(dc *chat.DirectChat, peers map[string]backendapi.Peer, fingerprintOf func(string) (string, error), prefer string) retiredRoutesPlan {
	routes := dc.FanoutPeerIDs()
	retired := func(id string) bool {
		p, ok := peers[id]
		return ok && p.RetiredAt != 0
	}
	live := func(id string) bool {
		p, ok := peers[id]
		return ok && p.RetiredAt == 0
	}
	var drop []string
	for _, id := range routes {
		if retired(id) {
			drop = append(drop, id)
		}
	}
	if len(drop) == 0 {
		return retiredRoutesPlan{outcome: routesOutcomeNone}
	}
	primary := dc.PeerID
	if primary == "" {
		primary = routes[0]
	}
	if !retired(primary) {
		return retiredRoutesPlan{primary: primary, drop: drop, outcome: routesOutcomeDropped}
	}

	person, err := fingerprintOf(primary)
	if err != nil {
		person = ""
	}
	if person == "" {
		person = dc.Fingerprint
	} else if dc.Fingerprint != "" && dc.Fingerprint != person {
		return retiredRoutesPlan{outcome: routesOutcomeFingerprintBad}
	}
	if person == "" {
		return retiredRoutesPlan{outcome: routesOutcomeNoFingerprint}
	}
	qualifies := func(id string) bool {
		if !live(id) {
			return false
		}
		fp, fErr := fingerprintOf(id)
		return fErr == nil && fp == person
	}
	if prefer != "" && slices.Contains(routes, prefer) && qualifies(prefer) {
		return retiredRoutesPlan{primary: prefer, drop: drop, outcome: routesOutcomeMoved}
	}
	for _, id := range routes {
		if qualifies(id) {
			return retiredRoutesPlan{primary: id, drop: drop, outcome: routesOutcomeMoved}
		}
	}
	return retiredRoutesPlan{outcome: routesOutcomeNoLivePairing}
}

var peerListRetryPause = 2 * time.Second

func peerRegistry(ctx context.Context, d *daemon) (map[string]backendapi.Peer, error) {
	resp, err := d.backendClient.Peers(ctx)
	if err != nil {
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(peerListRetryPause):
		}
		resp, err = d.backendClient.Peers(ctx)
		if err != nil {
			return nil, err
		}
	}
	out := make(map[string]backendapi.Peer, len(resp.Peers))
	for _, p := range resp.Peers {
		out[p.ID] = p
	}
	return out, nil
}

func (d *daemon) pairingFingerprint(peerID string) (string, error) {
	rec, err := d.peerMeta.Get(peerID)
	if err != nil {
		return "", err
	}
	return rec.Fingerprint, nil
}

var keptLogged sync.Map

func moveChatOffRetired(d *daemon, dc *chat.DirectChat, peers map[string]backendapi.Peer, prefer, trigger string) (*chat.DirectChat, bool) {
	plan := planRetiredRoutes(dc, peers, d.pairingFingerprint, prefer)
	attrs := []any{
		slog.String("chat_id", string(dc.ID)),
		slog.String("peer_id", dc.PeerID),
		slog.String("trigger", trigger),
		slog.String("outcome", plan.outcome),
	}
	switch plan.outcome {
	case routesOutcomeNone:
		return dc, false
	case routesOutcomeNoLivePairing, routesOutcomeNoFingerprint, routesOutcomeFingerprintBad:
		if _, said := keptLogged.LoadOrStore(string(dc.ID)+"/"+plan.outcome, struct{}{}); said {
			return dc, false
		}
		if plan.outcome == routesOutcomeNoLivePairing {
			slog.Debug("chat routing set: the primary pairing is retired and the person has no live pairing in it; the chat stays retired", attrs...)
		} else {
			slog.Warn("chat routing set: the primary pairing is retired and the person it belongs to cannot be established; nothing is moved", attrs...)
		}
		return dc, false
	}
	after, changed, err := d.dropRoutes(dc.ID, plan.primary, plan.drop)
	if err != nil {
		if errors.Is(err, chat.ErrRoutesChanged) {

			slog.Debug("chat routing set: the chat changed while it was planned; nothing is written", append(attrs, slog.Any("err", err))...)
			return dc, false
		}
		slog.Warn("chat routing set: the retired pairings could not be taken out", append(attrs, slog.Any("err", err))...)
		return dc, false
	}
	if !changed {
		return after, false
	}
	slog.Info(routesDroppedMsg,
		append(attrs,
			slog.String("primary_peer_id", after.PeerID),
			slog.Int("routes_dropped", len(plan.drop)),
			slog.Int("routes_left", len(after.PeerIDs)),
		)...,
	)

	noteContactChangedForPeer(d, after.PeerID)
	return after, true
}

func pushChatUpdated(d *daemon, dc *chat.DirectChat) {
	if d.ipcSrv == nil {
		return
	}
	entry := chatToEntry(dc)
	applyChatLabel(d, &entry)
	applyChatPresenceSnapshot(d, &entry, dc)
	push(d.ipcSrv, ipc.FrameChatUpdated, "", ipc.ChatUpdatedPayload{Chat: entry})
	slog.Debug("chat.updated pushed",
		slog.String("chat_id", entry.ChatID),
		slog.String("peer_id", entry.PeerID),
	)
}

func routesWired(d *daemon) bool {
	return d != nil && d.chats != nil && d.peerMeta != nil && d.backendClient != nil
}

func dropRetiredRoutes(ctx context.Context, d *daemon, peerID string) {
	if !routesWired(d) {
		return
	}
	dc, err := d.chats.GetByDirectPeer(peerID)
	if err != nil {
		if !errors.Is(err, chat.ErrNotFound) {
			slog.Warn("chat routing set: the chat of a pairing could not be read",
				slog.String("peer_id", peerID),
				slog.Any("err", err),
			)
		}
		return
	}
	peers, err := peerRegistry(ctx, d)
	if err != nil {
		slog.Warn("chat routing set: the peer list could not be read; the chat is looked at again on the next backend-ready transition",
			slog.String("peer_id", peerID),
			slog.String("chat_id", string(dc.ID)),
			slog.Any("err", err),
		)
		return
	}
	if after, changed := moveChatOffRetired(d, dc, peers, peerID, "pairing"); changed {
		pushChatUpdated(d, after)
	}
}

func sweepRetiredRoutes(ctx context.Context, d *daemon) {
	if !routesWired(d) {
		return
	}
	all, err := d.chats.List()
	if err != nil {
		slog.Warn("chat routing set: the chat list could not be read; nothing is swept", slog.Any("err", err))
		return
	}
	peers, err := peerRegistry(ctx, d)
	if err != nil {
		slog.Warn("chat routing set: the peer list could not be read; nothing is swept", slog.Any("err", err))
		return
	}
	for _, c := range all {
		dc, ok := c.(*chat.DirectChat)
		if !ok {
			continue
		}
		if after, changed := moveChatOffRetired(d, dc, peers, "", "backend_ready"); changed {
			pushChatUpdated(d, after)
		}
	}
}

func afterPairingsRetired(ctx context.Context, d *daemon, ids []string, prefer string) {
	if len(ids) == 0 {
		return
	}
	if routesWired(d) {
		peers, err := peerRegistry(ctx, d)
		if err != nil {
			slog.Warn("chat routing set: the peer list could not be read; the retired pairings stay in their chats until the next backend-ready transition",
				slog.Int("pairings", len(ids)),
				slog.Any("err", err),
			)
		}
		seen := map[chat.ChatID]struct{}{}
		for _, id := range ids {
			dc, gErr := d.chats.GetByDirectPeer(id)
			if gErr != nil {
				continue
			}
			if _, done := seen[dc.ID]; done {
				continue
			}
			seen[dc.ID] = struct{}{}
			after := dc
			if peers != nil {
				after, _ = moveChatOffRetired(d, dc, peers, prefer, "retire")
			}

			pushChatUpdated(d, after)
		}
	}
	for _, id := range ids {
		pushPeerRowUpdated(ctx, d, id)
	}
}

func chatOutlivesPairing(ctx context.Context, d *daemon, dc *chat.DirectChat, peerID string) (bool, error) {
	if dc == nil || dc.PeerID == peerID {
		return false, nil
	}
	var others []string
	for _, id := range dc.FanoutPeerIDs() {
		if id != peerID {
			others = append(others, id)
		}
	}
	if len(others) == 0 {
		return false, nil
	}
	peers, err := peerRegistry(ctx, d)
	if err != nil {
		return false, err
	}
	for _, id := range others {
		if p, ok := peers[id]; ok && p.RetiredAt == 0 {
			return true, nil
		}
	}
	return false, nil
}
