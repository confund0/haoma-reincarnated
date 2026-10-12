package main

import (
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"sync"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/syncrail"
)

type saltOrderTiming struct {
	wait time.Duration

	poll time.Duration
}

var saltOrder = saltOrderTiming{wait: deviceLinkBundleTimeout, poll: time.Second}

const saltSendTimeout = 30 * time.Second

const outboxStateEnqueued = "enqueued"

const (
	bundleOutSent      = "sent"
	bundleOutFailed    = "failed"
	bundleOutGone      = "gone"
	bundleOutTimedOut  = "timed_out"
	bundleOutCancelled = "cancelled"
)

type saltPush struct {
	bundleEnvelope string

	waiting bool

	lastEnvelope string
}

var saltPushes = struct {
	mu sync.Mutex
	m  map[deviceLinkWaitKey]*saltPush
}{m: make(map[deviceLinkWaitKey]*saltPush)}

func withSaltPush(d *daemon, peerID string, f func(p *saltPush)) {
	k := deviceLinkWaitKey{d: d, peerID: peerID}
	saltPushes.mu.Lock()
	defer saltPushes.mu.Unlock()
	p := saltPushes.m[k]
	if p == nil {
		p = &saltPush{}
		saltPushes.m[k] = p
	}
	f(p)
}

func noteIdentityBundleQueued(d *daemon, peerID, envelopeID string) {
	if d == nil || peerID == "" || envelopeID == "" {
		return
	}
	withSaltPush(d, peerID, func(p *saltPush) { p.bundleEnvelope = envelopeID })
	slog.Debug("destroy pin: a salt push to this device waits for the identity bundle",
		slog.String("sibling_peer_id", peerID),
		slog.String("envelope_id", envelopeID),
	)
}

func identityBundleInFlight(d *daemon, peerID string) (envelopeID string) {
	withSaltPush(d, peerID, func(p *saltPush) { envelopeID = p.bundleEnvelope })
	return envelopeID
}

type outboxStateFunc func(ctx context.Context, envelopeID string) (string, error)

func outboxStateOf(d *daemon) outboxStateFunc {
	return func(ctx context.Context, envelopeID string) (string, error) {
		row, err := d.backendClient.OutboxEntry(ctx, envelopeID)
		if err != nil {
			return "", err
		}
		return row.State, nil
	}
}

func awaitEnvelopeLeft(ctx context.Context, state outboxStateFunc, envelopeID string, timing saltOrderTiming) string {
	deadline := time.Now().Add(timing.wait)
	for {
		st, err := state(ctx, envelopeID)
		switch {
		case errors.Is(err, backendapi.ErrOutboxEntryNotFound):
			return bundleOutGone
		case err != nil:
			slog.Debug("destroy pin: the identity bundle's outbox row could not be read; asking again",
				slog.String("envelope_id", envelopeID),
				slog.Any("err", err),
			)
		case st == deliveryStateFailed:
			return bundleOutFailed
		case st == deliveryStateSent, st == deliveryStateDelivered, st == deliveryStateRead:
			return bundleOutSent
		}
		if !time.Now().Before(deadline) {
			return bundleOutTimedOut
		}
		select {
		case <-ctx.Done():
			return bundleOutCancelled
		case <-time.After(timing.poll):
		}
	}
}

func holdSaltBehindBundle(d *daemon, sibling string, salt []byte, setAt int64) bool {
	bundle := identityBundleInFlight(d, sibling)
	if bundle == "" {
		return false
	}
	withSaltPush(d, sibling, func(p *saltPush) { p.waiting = true })
	timing := saltOrder
	held := append([]byte(nil), salt...)
	slog.Info("destroy pin: the salt waits until the identity bundle has left the outbox",
		slog.String("sibling_peer_id", sibling),
		slog.String("envelope_id", bundle),
		slog.Duration("bound", timing.wait),
	)
	go func() {
		ctx, cancel := context.WithTimeout(d.relayContext(), timing.wait+saltSendTimeout)
		defer cancel()
		pushSaltAfterBundle(ctx, d, sibling, bundle, held, setAt, outboxStateOf(d), timing)
	}()
	return true
}

func pushSaltAfterBundle(ctx context.Context, d *daemon, sibling, bundle string, salt []byte, setAt int64, state outboxStateFunc, timing saltOrderTiming) {
	logger := slog.With(
		slog.String("sibling_peer_id", sibling),
		slog.String("envelope_id", bundle),
	)
	outcome := awaitEnvelopeLeft(ctx, state, bundle, timing)

	if outcome == bundleOutCancelled {
		endSaltWait(d, sibling, bundle, "")
		logger.Info("destroy pin: salt not sent; this process stopped while it waited for the identity bundle. The next sweep sends it",
			slog.String("outcome", outcome),
		)
		return
	}

	if !d.isSibling(sibling) {
		endSaltWait(d, sibling, bundle, "")
		logger.Info("destroy pin: salt not sent; that device was unlinked while the salt waited for the identity bundle",
			slog.String("outcome", outcome),
		)
		return
	}
	logger.Info("destroy pin: the wait for the identity bundle ended; sending the salt",
		slog.String("outcome", outcome),
	)
	envID, ok := sendSalt(ctx, d, sibling, salt, setAt)
	endSaltWait(d, sibling, bundle, envID)
	if ok {
		slog.Info("destroy pin: salt push finished",
			slog.Int("siblings", 1),
			slog.Int("pushed", 1),
		)
	}
}

func endSaltWait(d *daemon, sibling, bundle, envID string) {
	k := deviceLinkWaitKey{d: d, peerID: sibling}
	saltPushes.mu.Lock()
	defer saltPushes.mu.Unlock()
	p := saltPushes.m[k]
	if p == nil {
		return
	}
	p.waiting = false
	if p.bundleEnvelope == bundle {
		p.bundleEnvelope = ""
	}
	if envID != "" {
		p.lastEnvelope = envID
	}
}

func forgetSaltPushes(d *daemon, peerID string) {
	saltPushes.mu.Lock()
	delete(saltPushes.m, deviceLinkWaitKey{d: d, peerID: peerID})
	saltPushes.mu.Unlock()
}

func sendSaltTo(ctx context.Context, d *daemon, sibling string, salt []byte, setAt int64) bool {
	envID, ok := sendSalt(ctx, d, sibling, salt, setAt)
	if ok {
		withSaltPush(d, sibling, func(p *saltPush) { p.lastEnvelope = envID })
	}
	return ok
}

func sendSalt(ctx context.Context, d *daemon, sibling string, salt []byte, setAt int64) (string, bool) {
	envID, err := sendGapEnvelopeID(ctx, d, sibling, syncrail.KindDestroySalt, syncrail.NewDestroySaltBody(salt, setAt))
	if err != nil {
		slog.Warn("destroy pin: salt push failed; that device cannot command this one until it lands",
			slog.String("sibling_peer_id", sibling),
			slog.Any("err", err),
		)
		return "", false
	}
	slog.Debug("destroy pin: salt pushed to a sibling",
		slog.String("sibling_peer_id", sibling),
		slog.String("envelope_id", envID),
		slog.Int("salt_bytes", len(salt)),
	)
	return envID, true
}

func saltPushOnItsWay(ctx context.Context, d *daemon, sibling string, state outboxStateFunc) string {
	var waiting bool
	var last string
	withSaltPush(d, sibling, func(p *saltPush) {
		waiting = p.waiting
		last = p.lastEnvelope
	})
	if waiting {
		return "waiting_for_identity_bundle"
	}
	if last == "" {
		return ""
	}
	st, err := state(ctx, last)
	if err == nil && st == outboxStateEnqueued {
		return "still_in_outbox"
	}
	return ""
}

func destroySaltReportFor(d *daemon, sibling string) *syncrail.DestroySaltReport {
	if d == nil || d.linkedDevices == nil {
		return nil
	}
	rec, held, err := d.linkedDevices.SiblingSalt(sibling)
	if err != nil {
		slog.Warn("destroy pin: the other device's salt could not be read; this sweep does not report it",
			slog.String("sibling_peer_id", sibling),
			slog.Any("err", err),
		)
		return nil
	}
	if !held || len(rec.Salt) == 0 {
		return &syncrail.DestroySaltReport{Held: false}
	}
	return &syncrail.DestroySaltReport{Held: true, SHA256: syncrail.DestroySaltDigest(rec.Salt)}
}

func healDestroySalt(ctx context.Context, d *daemon, sibling string, report syncrail.DestroySaltReport, state outboxStateFunc) {
	logger := slog.With(slog.String("sibling_peer_id", sibling))
	if d == nil || d.backendClient == nil {
		return
	}
	guard, err := d.backendClient.DestroyPIN(ctx)
	if err != nil {
		logger.Warn("destroy pin: this device's guard could not be read; the other device's salt is not checked on this sweep",
			slog.Any("err", err),
		)
		return
	}
	if !guard.Set {
		logger.Debug("destroy pin: no destroy PIN on this device; the other device needs no salt from it")
		return
	}

	if guard.SiblingPeerID != sibling {
		logger.Info("destroy pin: salt not sent again; this device's guard names another device",
			slog.Bool("guard_names_a_device", guard.SiblingPeerID != ""),
		)
		return
	}
	salt, err := hex.DecodeString(guard.Salt)
	if err != nil || len(salt) == 0 {
		logger.Warn("destroy pin: haomad holds a guard with no usable salt; nothing to send",
			slog.Int("salt_hex_bytes", len(guard.Salt)),
		)
		return
	}
	if report.Held && report.SHA256 == syncrail.DestroySaltDigest(salt) {
		logger.Debug("destroy pin: the other device holds this device's salt")
		return
	}
	if why := saltPushOnItsWay(ctx, d, sibling, state); why != "" {
		logger.Info("destroy pin: the other device does not hold this device's salt; a push is already on its way",
			slog.Bool("other_holds_a_salt", report.Held),
			slog.String("reason", why),
		)
		return
	}
	logger.Info("destroy pin: the other device does not hold this device's salt; sending it again",
		slog.Bool("other_holds_a_salt", report.Held),
	)
	sendSaltTo(ctx, d, sibling, salt, guard.SetAt)
}
