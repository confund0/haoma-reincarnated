package syncrail

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"haoma-frontend/internal/logid"
)

const Version = 1

type Kind string

const (
	KindMirror Kind = "mirror"

	KindGapRequest Kind = "gap-request"

	KindGapResponse Kind = "gap-response"

	KindBackfillRequest Kind = "backfill-request"

	KindBackfillResponse Kind = "backfill-response"

	KindChatSettings Kind = "chat-settings"

	KindContacts Kind = "contacts"

	KindSelfNick Kind = "self-nick"

	KindDestroySalt Kind = "destroy-salt"

	KindInvitation Kind = "invitation"

	KindFileRequest Kind = "file-request"

	KindGapSiblingOffer Kind = "gap-sibling-offer"

	KindContactDelete Kind = "contact-delete"

	KindCallSummary Kind = "call-summary"
)

var knownKinds = map[Kind]bool{
	KindMirror:           true,
	KindGapRequest:       true,
	KindGapResponse:      true,
	KindBackfillRequest:  true,
	KindBackfillResponse: true,
	KindChatSettings:     true,
	KindContacts:         true,
	KindSelfNick:         true,
	KindDestroySalt:      true,
	KindInvitation:       true,
	KindFileRequest:      true,
	KindGapSiblingOffer:  true,
	KindContactDelete:    true,
	KindCallSummary:      true,
}

var ErrUnknownKind = errors.New("syncrail: unknown rail kind")

var ErrVersionMismatch = errors.New("syncrail: unsupported rail version")

type Envelope struct {
	V    int             `json:"v"`
	Kind Kind            `json:"kind"`
	Ts   int64           `json:"ts"`
	Body json.RawMessage `json:"body"`
}

type MirrorBody struct {
	Fingerprint string `json:"fingerprint"`

	Wrapper []byte `json:"wrapper"`

	WireKind  string `json:"wire_kind"`
	MsgID     string `json:"msg_id"`
	DisplayTs int64  `json:"display_ts"`

	OriginChatID string `json:"origin_chat_id,omitempty"`
	OriginPeerID string `json:"origin_peer_id,omitempty"`

	SiblingOffer *SiblingOffer `json:"sibling_offer,omitempty"`
}

func (b MirrorBody) LogValue() slog.Value {
	attrs := []slog.Attr{
		slog.String("wire_kind", b.WireKind),
		slog.String("msg_id", logid.Short(b.MsgID)),
		slog.String("fingerprint", logid.Short(b.Fingerprint)),
		slog.Int64("display_ts", b.DisplayTs),
		slog.String("origin_chat_id", logid.Short(b.OriginChatID)),
		slog.String("origin_peer_id", logid.Short(b.OriginPeerID)),
		slog.Int("wrapper_bytes", len(b.Wrapper)),
		slog.Bool("sibling_offer_present", b.SiblingOffer != nil),
	}
	if b.SiblingOffer != nil {
		attrs = append(attrs, slog.Any("sibling_offer", *b.SiblingOffer))
	}
	return slog.GroupValue(attrs...)
}

func (b MirrorBody) String() string {
	offer := "none"
	if b.SiblingOffer != nil {
		offer = b.SiblingOffer.String()
	}
	return fmt.Sprintf("MirrorBody{wire_kind=%s msg_id=%s wrapper_bytes=%d sibling_offer=%s}",
		b.WireKind, logid.Short(b.MsgID), len(b.Wrapper), offer)
}

func (b MirrorBody) GoString() string { return b.String() }

func (b MirrorBody) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, b.String()) }

func SealEnvelope(key []byte, env Envelope) ([]byte, error) {
	if env.V == 0 {
		env.V = Version
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("syncrail: marshal envelope: %w", err)
	}
	return Seal(key, raw)
}

func OpenEnvelope(key, ciphertext []byte) (Envelope, error) {
	return openEnvelope(key, ciphertext, knownKinds)
}

func openEnvelope(key, ciphertext []byte, known map[Kind]bool) (Envelope, error) {
	plain, err := Open(key, ciphertext)
	if err != nil {
		return Envelope{}, err
	}
	var env Envelope
	if err := json.Unmarshal(plain, &env); err != nil {
		return Envelope{}, fmt.Errorf("syncrail: parse envelope: %w", err)
	}
	if env.V != Version {
		return Envelope{}, fmt.Errorf("%w: %d", ErrVersionMismatch, env.V)
	}
	if !known[env.Kind] {
		return Envelope{}, fmt.Errorf("%w: %q", ErrUnknownKind, env.Kind)
	}
	return env, nil
}

func ParseMirrorBody(env Envelope) (MirrorBody, error) {
	if env.Kind != KindMirror {
		return MirrorBody{}, fmt.Errorf("%w: %q", ErrUnknownKind, env.Kind)
	}
	var body MirrorBody
	if err := json.Unmarshal(env.Body, &body); err != nil {
		return MirrorBody{}, fmt.Errorf("syncrail: parse mirror body: %w", err)
	}
	switch {
	case body.Fingerprint == "":
		return MirrorBody{}, errors.New("syncrail: mirror body has no fingerprint")
	case body.MsgID == "":
		return MirrorBody{}, errors.New("syncrail: mirror body has no msg id")
	case len(body.Wrapper) == 0:
		return MirrorBody{}, errors.New("syncrail: mirror body has no wrapper")
	}
	return body, nil
}
