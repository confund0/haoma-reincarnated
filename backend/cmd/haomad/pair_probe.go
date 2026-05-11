package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"haoma/internal/eventbus"
	"haoma/internal/pair"
)

type pairProbeObservation struct {
	HandleID string `json:"handle_id"`
	Attempt  int    `json:"attempt"`
	Ready    bool   `json:"ready"`
	Error    string `json:"error,omitempty"`
	At       int64  `json:"at"`
}

const probeOnionInviteInterval = 10 * time.Second

const probeOnionInviteMaxAttempts = 30

func probeOnionInvite(ctx context.Context, d *daemon, handleID string, words []string, pi pair.PendingInvite) {

	mat, err := pair.OnionDerive(words)
	if err != nil {
		slog.Warn("pair: probe — derive failed",
			slog.String("handle_id", handleID),
			slog.Any("err", err),
		)
		return
	}
	url := fmt.Sprintf("http://%s.onion:%d/", mat.OnionAddress, mat.Port)

	rendezvousDone := make(chan struct{})
	go func() {
		_, _ = pi.Wait(ctx)
		close(rendezvousDone)
	}()

	tick := time.NewTimer(probeOnionInviteInterval)
	defer tick.Stop()

	for attempt := 1; attempt <= probeOnionInviteMaxAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return
		case <-rendezvousDone:
			return
		case <-tick.C:
		}
		probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		err := probeOnionGET(probeCtx, d.torHTTP, url)
		cancel()
		obs := pairProbeObservation{
			HandleID: handleID,
			Attempt:  attempt,
			Ready:    err == nil,
			At:       time.Now().Unix(),
		}
		if err != nil {
			obs.Error = err.Error()
		}
		if d.bus != nil {
			d.bus.Publish(eventbus.TopicPairOnionProbe, obs)
		}
		slog.Info("pair: self-probe attempt",
			slog.String("handle_id", handleID),
			slog.Int("attempt", attempt),
			slog.Bool("ready", obs.Ready),
			slog.Any("err", err),
		)
		if obs.Ready {
			return
		}
		tick.Reset(probeOnionInviteInterval)
	}

	if d.bus != nil {
		d.bus.Publish(eventbus.TopicPairOnionProbe, pairProbeObservation{
			HandleID: handleID,
			Attempt:  probeOnionInviteMaxAttempts,
			Ready:    true,
			Error:    "probe-timeout",
			At:       time.Now().Unix(),
		})
	}
	slog.Warn("pair: self-probe gave up; showing words regardless",
		slog.String("handle_id", handleID),
		slog.Int("attempts", probeOnionInviteMaxAttempts),
	)
}

func probeOnionGET(ctx context.Context, client *http.Client, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}
