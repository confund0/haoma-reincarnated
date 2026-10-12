package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/calls"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/logid"
	"haoma-frontend/internal/mirror"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/session"
)

var callOfferMaxAge = 2 * time.Minute

var calleeRingTimeout = 60 * time.Second

var (
	spawnCallReceiver = spawnReceiverLeg
	spawnCallSender   = spawnSenderLeg
)

var (
	transitionCall = func(m *calls.Manager, callID string, next calls.Status, reason string, nowUnix int64) (calls.State, error) {
		return m.Transition(callID, next, reason, nowUnix)
	}
	claimCallAnswer = func(m *calls.Manager, callID, deviceID string) (calls.State, bool, error) {
		return m.ClaimAnswer(callID, deviceID)
	}
	failCallUnanswered = func(m *calls.Manager, callID, reason string, nowUnix int64) (calls.State, error) {
		return m.FailUnanswered(callID, reason, nowUnix)
	}
)

var callTeardown = teardownCall

var callOfferTimers sync.Map

type offerTimer struct {
	mu sync.Mutex
	t  *time.Timer
}

func stopCallOfferTimer(callID string) {
	if v, ok := callOfferTimers.LoadAndDelete(callID); ok {
		e := v.(*offerTimer)
		e.mu.Lock()
		if e.t != nil {
			e.t.Stop()
		}
		e.mu.Unlock()
	}
}

var callsAnswering sync.Map

var (
	callTimerRearmDelay = 2 * time.Second
	callTimerRearms     = 5
)

type callWire struct {
	callID      string
	kind        msg.Kind
	modalities  []string
	reason      string
	tokens      map[string]string
	outboundKey []byte
	senderNick  string
}

func (w callWire) build(seq uint64, now int64, msgID string) (*msg.Wrapper, error) {
	switch w.kind {
	case msg.KindCallOffer:
		return msg.BuildCallOffer(seq, now, msgID, w.callID, w.modalities, w.tokens, w.outboundKey, w.senderNick, 0)
	case msg.KindCallAccept:
		return msg.BuildCallAccept(seq, now, msgID, w.callID, w.modalities, w.tokens, w.outboundKey, w.senderNick, 0)
	case msg.KindCallReject:
		return msg.BuildCallReject(seq, now, msgID, w.callID, w.reason, 0)
	case msg.KindCallEnd:
		return msg.BuildCallEnd(seq, now, msgID, w.callID, w.reason, 0)
	default:
		return nil, fmt.Errorf("unsupported call kind %q", w.kind)
	}
}

func (w callWire) prepare(d *daemon) mirror.PrepareFunc {
	msgID, idErr := msg.NewID()
	now := time.Now().Unix()
	return func(ctx context.Context, deviceID string) (backendapi.SendRequest, []byte, error) {
		if idErr != nil {
			return backendapi.SendRequest{}, nil, fmt.Errorf("msg id: %w", idErr)
		}
		seq, err := d.peerSeq.NextSendSeq(deviceID)
		if err != nil {
			return backendapi.SendRequest{}, nil, fmt.Errorf("seq: %w", err)
		}
		wrapper, err := w.build(seq, now, msgID)
		if err != nil {
			return backendapi.SendRequest{}, nil, fmt.Errorf("build wrapper: %w", err)
		}
		plaintext, err := msg.Marshal(wrapper)
		if err != nil {
			return backendapi.SendRequest{}, nil, fmt.Errorf("marshal wrapper: %w", err)
		}
		blob, err := d.cipher.Encrypt(ctx, deviceID, plaintext)
		if err != nil {
			if errors.Is(err, session.ErrNoSession) {
				slog.Debug("call send skipped a device with no session yet",
					slog.String("call_id", w.callID),
					slog.String("kind", string(w.kind)),
					slog.String("peer_id", deviceID),
				)
			}
			return backendapi.SendRequest{}, nil, fmt.Errorf("encrypt: %w", err)
		}
		return backendapi.SendRequest{
			PeerID:         deviceID,
			Payload:        blob,
			PresenceSource: backendapi.PresenceSourceHaoma,
		}, plaintext, nil
	}
}

func shipCallEnvelope(ctx context.Context, d *daemon, deviceID string, w callWire) error {
	if _, err := mirror.Dispatch(ctx, d.backendClient, mirror.NewRail(d.railKey, d.siblings, d.peerMeta),
		mirror.SingleTarget(deviceID), w.prepare(d), mirror.NewOp(w.kind, "")); err != nil {
		return fmt.Errorf("backend send: %w", err)
	}
	return nil
}

func shipCallToDevices(ctx context.Context, d *daemon, devices []string, w callWire) (int, error) {
	sent := 0
	var lastErr error
	for _, dev := range devices {
		if err := shipCallEnvelope(ctx, d, dev, w); err != nil {

			slog.Warn("call send to a device failed",
				slog.String("call_id", w.callID),
				slog.String("kind", string(w.kind)),
				slog.String("peer_id", logid.Short(dev)),
				slog.String("reason", w.reason),
				slog.String("stage", callSendStage(err)),
			)
			lastErr = err
			continue
		}
		sent++
	}
	return sent, lastErr
}

func callSendStage(err error) string {
	if errors.Is(err, session.ErrNoSession) {
		return "no_session"
	}
	var de *mirror.DispatchError
	if errors.As(err, &de) {
		return de.Stage
	}
	return "other"
}

func callRecipients(s calls.State) []string {
	if s.Direction != calls.DirOut {
		return s.CallTargets()
	}
	if s.AnsweredBy != "" {
		return []string{s.AnsweredBy}
	}
	return s.Ringing()
}

func endCallOnDevices(ctx context.Context, d *daemon, callID string, devices []string, reason, why string) {
	if len(devices) == 0 {
		return
	}
	sent, err := shipCallToDevices(ctx, d, devices, callWire{callID: callID, kind: msg.KindCallEnd, reason: reason})
	slog.Info("call end sent to devices",
		slog.String("call_id", callID),
		slog.String("why", why),
		slog.String("reason", reason),
		slog.Int("devices", len(devices)),
		slog.Int("sent", sent),
		slog.Any("devices_short", shortIDs(devices)),
		slog.Bool("all_sent", err == nil),
	)
}

func startCallOffer(ctx context.Context, d *daemon, dc *chat.DirectChat, state calls.State, outboundKey []byte, senderNick string) (calls.State, string, error) {
	targets := dc.FanoutPeerIDs()
	if len(targets) == 0 {
		teardownCall(d, state)
		return state, "internal", errors.New("chat has no devices to call")
	}
	state.Targets = append([]string(nil), targets...)
	if err := d.calls.PutState(state); err != nil {
		teardownCall(d, state)
		return state, "persist_failed", err
	}

	broadcastCallStateChanged(d, state)

	w := callWire{
		callID:      state.CallID,
		kind:        msg.KindCallOffer,
		modalities:  state.Modalities,
		tokens:      state.LocalTokens,
		outboundKey: outboundKey,
		senderNick:  senderNick,
	}
	result, err := mirror.Dispatch(ctx, d.backendClient, mirror.NewRail(d.railKey, d.siblings, d.peerMeta),
		mirror.FanoutTargets(targets), w.prepare(d), mirror.NewOp(msg.KindCallOffer, ""))
	if err != nil {
		failed, terr := d.calls.Transition(state.CallID, calls.StatusFailed, calls.FailReasonSendFail+": "+clientDetail(calls.FailReasonSendFail, err.Error()), time.Now().Unix())
		if terr == nil {
			teardownCall(d, failed)
			broadcastCallStateChanged(d, failed)
			return failed, "backend_send", err
		}
		teardownCall(d, state)
		return state, "backend_send", err
	}
	if len(result.Skipped) > 0 {
		skipped := make([]string, 0, len(result.Skipped))
		for _, sk := range result.Skipped {
			skipped = append(skipped, sk.DeviceID)
		}
		if updated, merr := d.calls.MarkStopped(state.CallID, skipped...); merr != nil {
			slog.Warn("call offer: recording the devices it did not reach failed",
				slog.String("call_id", state.CallID),
				slog.Any("err", merr),
			)
		} else {
			state = updated
		}
	}
	scheduleCallOfferTimeout(d, state.CallID)
	slog.Info("call offered to every device of the contact",
		slog.String("call_id", state.CallID),
		slog.Int("targets", len(targets)),
		slog.Int("offered", len(result.EnvelopeIDs)),
		slog.Int("skipped", len(result.Skipped)),
	)
	return state, "", nil
}

func callOfferTooOld(offerTs int64, now time.Time) bool {
	if offerTs <= 0 {
		return false
	}
	age := now.Sub(time.Unix(offerTs, 0))
	return age > callOfferMaxAge
}

func scheduleCalleeRingTimeout(d *daemon, callID string) {
	armCalleeRingTimer(d, callID, calleeRingTimeout, 0)
}

func armCalleeRingTimer(d *daemon, callID string, after time.Duration, rearms int) {
	time.AfterFunc(after, func() {
		state, err := d.calls.GetState(callID)
		if err != nil || state.Status != calls.StatusRinging {
			return
		}
		if _, answering := callsAnswering.Load(callID); answering {

			slog.Info("call ring timeout: an answer is starting here; waiting for it",
				slog.String("call_id", callID),
			)
			armCalleeRingTimer(d, callID, callTimerRearmDelay, rearms)
			return
		}
		updated, err := transitionCall(d.calls, callID, calls.StatusEnded, "", time.Now().Unix())
		if err != nil {
			if errors.Is(err, calls.ErrIllegalTransition) {
				return
			}
			if rearms < callTimerRearms {
				slog.Warn("call ring timeout: transition failed; trying again",
					slog.String("call_id", callID),
					slog.Int("attempt", rearms+1),
					slog.Any("err", err),
				)
				armCalleeRingTimer(d, callID, callTimerRearmDelay, rearms+1)
				return
			}
			slog.Error("call ring timeout: transition failed on every try; the call rings until the caller's end",
				slog.String("call_id", callID),
				slog.Any("err", err),
			)
			return
		}
		slog.Info("call stopped ringing here (ring timeout); nothing sent",
			slog.String("call_id", callID),
			slog.Duration("after", calleeRingTimeout),
		)
		broadcastCallStateChanged(d, updated)
		teardownCall(d, updated)
	})
}
