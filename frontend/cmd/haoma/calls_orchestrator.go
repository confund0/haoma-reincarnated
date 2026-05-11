package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/calls"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/streamers"
)

const streamerReadyTimeout = 5 * time.Second

const spawnReadyBudget = 8 * time.Second

func streamIDForModality(modality string) string {
	switch modality {
	case msg.ModalityAudio:
		return "mic"
	case msg.ModalityVideo:
		return "cam"
	case msg.ModalityScreen:
		return "screen"
	default:
		return ""
	}
}

func mintCallSecrets(modalities []string) (outboundKey []byte, tokens map[string]string, err error) {
	outboundKey = make([]byte, msg.CallOutboundKeyBytes)
	if _, err := io.ReadFull(rand.Reader, outboundKey); err != nil {
		return nil, nil, fmt.Errorf("calls: mint outbound key: %w", err)
	}
	tokens = make(map[string]string, len(modalities))
	for _, m := range modalities {
		var b [32]byte
		if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
			return nil, nil, fmt.Errorf("calls: mint token (%s): %w", m, err)
		}
		tokens[m] = base64.RawURLEncoding.EncodeToString(b[:])
	}
	return outboundKey, tokens, nil
}

func pickLocalPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("calls: pick local port: %w", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func peerOnionURL(ctx context.Context, d *daemon, peerID, modality, token string) (string, error) {
	if d.backendClient == nil {
		return "", errors.New("calls: backend client not configured")
	}
	peer, err := d.backendClient.Peer(ctx, peerID)
	if err != nil {
		return "", fmt.Errorf("calls: resolve peer %s: %w", peerID, err)
	}
	if len(peer.KnownAddresses) == 0 {
		return "", fmt.Errorf("calls: peer %s has no known onion addresses", peerID)
	}
	addr := strings.TrimSuffix(peer.KnownAddresses[0], ".onion")
	return fmt.Sprintf("http://%s.onion/%s/%s", addr, modality, token), nil
}

func spawnSenderLeg(ctx context.Context, d *daemon, callID, modality, token string, outboundKey []byte) (int, error) {
	if d.streamers == nil {
		return 0, errors.New("calls: streamers manager unavailable")
	}
	if d.backendClient == nil {
		return 0, errors.New("calls: backend client not configured")
	}
	streamID := streamIDForModality(modality)
	if streamID == "" {
		return 0, fmt.Errorf("calls: unsupported modality %q", modality)
	}
	subkey, err := streamers.DeriveStreamKey(outboundKey, streamID)
	if err != nil {
		return 0, fmt.Errorf("calls: derive sender subkey: %w", err)
	}
	port, err := pickLocalPort()
	if err != nil {
		return 0, err
	}
	spawnCtx, spawnCancel := context.WithTimeout(ctx, spawnReadyBudget)
	defer spawnCancel()
	stream, err := d.streamers.SpawnMic(spawnCtx, callID, port, subkey, streamID)
	if err != nil {
		return 0, fmt.Errorf("calls: spawn mic: %w", err)
	}
	readyCtx, readyCancel := context.WithTimeout(ctx, streamerReadyTimeout)
	defer readyCancel()
	if rerr := stream.WaitReady(readyCtx); rerr != nil {
		_ = d.streamers.Teardown(callID)
		return 0, fmt.Errorf("calls: mic ready: %w", rerr)
	}
	if perr := d.backendClient.ProxyServe(ctx, backendapi.ProxyServeRequest{
		Token:     token,
		Modality:  modality,
		LocalPort: port,
	}); perr != nil {
		_ = d.streamers.Teardown(callID)
		return 0, fmt.Errorf("calls: proxy serve: %w", perr)
	}
	pumpStreamEvents(d, callID, streamers.SideMic, stream)
	slog.Info("call sender leg up",
		slog.String("call_id", callID),
		slog.String("modality", modality),
		slog.Int("port", port),
	)
	return port, nil
}

func spawnReceiverLeg(ctx context.Context, d *daemon, callID, peerID, modality, peerToken string, peerOutboundKey []byte) (int, error) {
	if d.streamers == nil {
		return 0, errors.New("calls: streamers manager unavailable")
	}
	if d.backendClient == nil {
		return 0, errors.New("calls: backend client not configured")
	}
	streamID := streamIDForModality(modality)
	if streamID == "" {
		return 0, fmt.Errorf("calls: unsupported modality %q", modality)
	}
	if len(peerOutboundKey) != msg.CallOutboundKeyBytes {
		return 0, fmt.Errorf("calls: peer outbound key wrong length: got %d want %d", len(peerOutboundKey), msg.CallOutboundKeyBytes)
	}
	if peerToken == "" {
		return 0, errors.New("calls: peer token empty")
	}
	subkey, err := streamers.DeriveStreamKey(peerOutboundKey, streamID)
	if err != nil {
		return 0, fmt.Errorf("calls: derive receiver subkey: %w", err)
	}
	peerURL, err := peerOnionURL(ctx, d, peerID, modality, peerToken)
	if err != nil {
		return 0, err
	}
	port, err := pickLocalPort()
	if err != nil {
		return 0, err
	}
	spawnCtx, spawnCancel := context.WithTimeout(ctx, spawnReadyBudget)
	defer spawnCancel()
	stream, err := d.streamers.SpawnSpk(spawnCtx, callID, port, subkey, streamID)
	if err != nil {
		return 0, fmt.Errorf("calls: spawn spk: %w", err)
	}
	readyCtx, readyCancel := context.WithTimeout(ctx, streamerReadyTimeout)
	defer readyCancel()
	if rerr := stream.WaitReady(readyCtx); rerr != nil {
		_ = d.streamers.Teardown(callID)
		return 0, fmt.Errorf("calls: spk ready: %w", rerr)
	}
	if perr := d.backendClient.ProxyFetch(ctx, backendapi.ProxyFetchRequest{
		Token:     peerToken,
		Modality:  modality,
		PeerURL:   peerURL,
		LocalPort: port,
	}); perr != nil {
		_ = d.streamers.Teardown(callID)
		return 0, fmt.Errorf("calls: proxy fetch: %w", perr)
	}
	pumpStreamEvents(d, callID, streamers.SideSpk, stream)
	slog.Info("call receiver leg up",
		slog.String("call_id", callID),
		slog.String("modality", modality),
		slog.String("peer_url", peerURL),
		slog.Int("port", port),
	)
	return port, nil
}

func teardownCall(d *daemon, state calls.State) {
	if d.streamers != nil {
		if err := d.streamers.Teardown(state.CallID); err != nil {
			slog.Warn("teardown streamers failed",
				slog.String("call_id", state.CallID),
				slog.Any("err", err),
			)
		}
	}
	if d.backendClient == nil {
		return
	}
	cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for modality, tok := range state.LocalTokens {
		if tok == "" {
			continue
		}
		if err := d.backendClient.ProxyCancel(cancelCtx, tok); err != nil {
			slog.Debug("teardown ProxyCancel(local) failed",
				slog.String("call_id", state.CallID),
				slog.String("modality", modality),
				slog.Any("err", err),
			)
		}
	}
	for modality, tok := range state.RemoteTokens {
		if tok == "" {
			continue
		}
		if err := d.backendClient.ProxyCancel(cancelCtx, tok); err != nil {
			slog.Debug("teardown ProxyCancel(remote) failed",
				slog.String("call_id", state.CallID),
				slog.String("modality", modality),
				slog.Any("err", err),
			)
		}
	}
}

func rememberLocalTokens(d *daemon, callID string, tokens map[string]string) {
	if d.calls == nil || len(tokens) == 0 {
		return
	}
	state, err := d.calls.GetState(callID)
	if err != nil {
		slog.Warn("rememberLocalTokens: state lookup failed",
			slog.String("call_id", callID),
			slog.Any("err", err),
		)
		return
	}
	if state.LocalTokens == nil {
		state.LocalTokens = map[string]string{}
	}
	for k, v := range tokens {
		state.LocalTokens[k] = v
	}
	if err := d.calls.PutState(state); err != nil {
		slog.Warn("rememberLocalTokens: persist failed",
			slog.String("call_id", callID),
			slog.Any("err", err),
		)
	}
}

func pumpStreamEvents(d *daemon, callID string, side streamers.Side, stream *streamers.Stream) {
	if d == nil || d.ipcSrv == nil || stream == nil {
		return
	}
	go func() {
		for ev := range stream.Events() {
			if ev.Type == "" || ev.Type == "ready" {
				continue
			}
			push(d.ipcSrv, ipc.FrameCallStreamEvent, "", ipc.CallStreamEventPayload{
				CallID:        callID,
				Side:          string(side),
				Type:          ev.Type,
				BytesIn:       ev.BytesIn,
				BytesOut:      ev.BytesOut,
				FramesIn:      ev.FramesIn,
				FramesOut:     ev.FramesOut,
				FramesDropped: ev.FramesDropped,
				JitterMs:      ev.JitterMs,
				CpuPct:        ev.CpuPct,
				Counter:       ev.Counter,
				Bytes:         ev.Bytes,
				Muted:         ev.Muted,
				Reason:        ev.Reason,
			})
		}
	}()
}
