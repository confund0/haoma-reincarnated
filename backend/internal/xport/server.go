package xport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"haoma/internal/logid"
)

const MaxEnvelopeBytes = 1 << 20

type Receiver interface {
	Receive(ctx context.Context, env Envelope) error
}

type ReceiverFunc func(ctx context.Context, env Envelope) error

func (f ReceiverFunc) Receive(ctx context.Context, env Envelope) error { return f(ctx, env) }

type Verifier interface {
	Verify(ctx context.Context, env Envelope) error
}

type VerifierFunc func(ctx context.Context, env Envelope) error

func (f VerifierFunc) Verify(ctx context.Context, env Envelope) error { return f(ctx, env) }

type StatusResponder interface {
	Respond(ctx context.Context, req Envelope) (Envelope, error)
}

type StatusResponderFunc func(ctx context.Context, req Envelope) (Envelope, error)

func (f StatusResponderFunc) Respond(ctx context.Context, req Envelope) (Envelope, error) {
	return f(ctx, req)
}

type SentAckResponder interface {
	Acknowledge(ctx context.Context, req Envelope) (Envelope, error)
}

type SentAckResponderFunc func(ctx context.Context, req Envelope) (Envelope, error)

func (f SentAckResponderFunc) Acknowledge(ctx context.Context, req Envelope) (Envelope, error) {
	return f(ctx, req)
}

type slotCtxKey struct{}

func ContextWithSlot(ctx context.Context, slotIdx int) context.Context {
	return context.WithValue(ctx, slotCtxKey{}, slotIdx)
}

func SlotFromContext(ctx context.Context) (int, bool) {
	v, ok := ctx.Value(slotCtxKey{}).(int)
	if !ok {
		return -1, false
	}
	return v, true
}

type peerIDStamp struct{ peerID string }
type peerIDStampKey struct{}

func ContextWithPeerIDStamp(ctx context.Context) context.Context {
	return context.WithValue(ctx, peerIDStampKey{}, &peerIDStamp{})
}

func StampPeerID(ctx context.Context, peerID string) {
	if s, ok := ctx.Value(peerIDStampKey{}).(*peerIDStamp); ok && s != nil {
		s.peerID = peerID
	}
}

func PeerIDFromContext(ctx context.Context) string {
	if s, ok := ctx.Value(peerIDStampKey{}).(*peerIDStamp); ok && s != nil {
		return s.peerID
	}
	return ""
}

type hostCtxKey struct{}

func ContextWithHost(ctx context.Context, host string) context.Context {
	return context.WithValue(ctx, hostCtxKey{}, host)
}

func HostFromContext(ctx context.Context) string {
	h, _ := ctx.Value(hostCtxKey{}).(string)
	return h
}

func NewServer(slotIdx int, r Receiver, v Verifier, sr StatusResponder, sars ...SentAckResponder) http.Handler {
	var sar SentAckResponder
	if len(sars) > 0 {
		sar = sars[0]
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /message", handleMessage(slotIdx, r, v, sr, sar))
	mux.HandleFunc("GET /health", handleHealth)
	return mux
}

func handleMessage(slotIdx int, r Receiver, v Verifier, sr StatusResponder, sar SentAckResponder) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if ct := req.Header.Get("Content-Type"); ct != "application/json" {
			http.Error(w, "content-type must be application/json", http.StatusUnsupportedMediaType)
			return
		}
		body := http.MaxBytesReader(w, req.Body, MaxEnvelopeBytes)
		defer body.Close()

		var env Envelope
		dec := json.NewDecoder(body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&env); err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				slog.Debug("xport: inbound envelope too large", slog.Int("slot", slotIdx))
				http.Error(w, "envelope too large", http.StatusRequestEntityTooLarge)
				return
			}
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				slog.Debug("xport: inbound empty body", slog.Int("slot", slotIdx))
				http.Error(w, "empty body", http.StatusBadRequest)
				return
			}
			slog.Debug("xport: inbound malformed envelope", slog.Int("slot", slotIdx), slog.Any("err", err))
			http.Error(w, "malformed envelope: "+err.Error(), http.StatusBadRequest)
			return
		}
		if env.ID == "" {
			slog.Debug("xport: inbound envelope missing id", slog.Int("slot", slotIdx))
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}
		slog.Debug("xport: inbound envelope",
			slog.Int("slot", slotIdx),
			slog.String("envelope_id", env.ID),
			slog.String("from", logid.Short(env.From)),
			slog.String("kind", string(env.EffectiveKind())),
		)

		ctx := ContextWithSlot(req.Context(), slotIdx)
		ctx = ContextWithHost(ctx, req.Host)
		ctx = ContextWithPeerIDStamp(ctx)
		if v != nil {
			if err := v.Verify(ctx, env); err != nil {
				slog.Warn("xport: inbound verify failed",
					slog.Int("slot", slotIdx),
					slog.String("envelope_id", env.ID),
					slog.String("from", logid.Short(env.From)),
					slog.Any("err", err),
				)
				http.Error(w, "unauthorized: "+err.Error(), http.StatusUnauthorized)
				return
			}
		}

		if env.EffectiveKind() == KindSentAck {
			slog.Warn("xport: inbound sent_ack from peer is a protocol violation; dropping",
				slog.String("envelope_id", env.ID),
				slog.String("from", logid.Short(env.From)),
			)
			w.WriteHeader(http.StatusOK)
			return
		}

		if env.EffectiveKind() == KindDestroyVerifyAck || env.EffectiveKind() == KindDestroyReceipt {
			slog.Warn("xport: inbound control answer from peer is a protocol violation; dropping",
				slog.String("kind", env.EffectiveKind()),
				slog.String("envelope_id", env.ID),
				slog.String("from", logid.Short(env.From)),
			)
			w.WriteHeader(http.StatusOK)
			return
		}

		if sr != nil && (env.EffectiveKind() == KindStatus ||
			env.EffectiveKind() == KindDestroyVerify ||
			env.EffectiveKind() == KindDestroy) {
			resp, err := sr.Respond(ctx, env)
			if err != nil {
				slog.Warn("xport: control responder error",
					slog.String("envelope_id", env.ID),
					slog.String("kind", env.EffectiveKind()),
					slog.Any("err", err),
				)
				http.Error(w, "control responder: "+err.Error(), http.StatusInternalServerError)
				return
			}

			if resp.ID == "" {
				slog.Debug("xport: control handled, no response body",
					slog.String("envelope_id", env.ID),
					slog.String("kind", env.EffectiveKind()),
				)
				w.WriteHeader(http.StatusOK)
				return
			}
			slog.Debug("xport: control response sent",
				slog.String("envelope_id", env.ID),
				slog.String("kind", env.EffectiveKind()),
				slog.String("resp_id", resp.ID),
			)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			if err := json.NewEncoder(w).Encode(resp); err != nil {
				slog.Warn("xport: encode status response failed", slog.Any("err", err))
				return
			}
			return
		}

		if err := r.Receive(ctx, env); err != nil {
			slog.Warn("xport: receiver error",
				slog.String("envelope_id", env.ID),
				slog.String("from", logid.Short(env.From)),
				slog.Any("err", err),
			)
			http.Error(w, "receive: "+err.Error(), http.StatusInternalServerError)
			return
		}
		slog.Debug("xport: envelope received",
			slog.String("envelope_id", env.ID),
			slog.String("from", logid.Short(env.From)),
			slog.String("kind", string(env.EffectiveKind())),
		)

		if sar != nil {
			ack, err := sar.Acknowledge(ctx, env)
			if err != nil {
				slog.Warn("xport: sent_ack responder error; sending bare 200",
					slog.String("envelope_id", env.ID),
					slog.Any("err", err),
				)
			} else if ack.ID != "" {
				slog.Debug("xport: sent_ack written", slog.String("envelope_id", env.ID), slog.String("ack_id", ack.ID))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(ack)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	}
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}
