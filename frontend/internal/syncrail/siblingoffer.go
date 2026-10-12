package syncrail

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"haoma-frontend/internal/logid"
)

type SiblingOffer struct {
	StagingID string `json:"staging_id"`

	Token string `json:"token"`

	Key   []byte `json:"key"`
	Nonce []byte `json:"nonce"`

	BlobSize      uint64 `json:"blob_size"`
	PlaintextSize uint64 `json:"plaintext_size"`

	Sha256Ciphertext string `json:"sha256_ciphertext"`
}

func (o SiblingOffer) Check() error {
	switch {
	case o.StagingID == "":
		return errors.New("syncrail: sibling offer has no staging id")
	case o.Token == "":
		return errors.New("syncrail: sibling offer has no token")
	case len(o.Key) != KeySize:
		return fmt.Errorf("syncrail: sibling offer key is %d bytes, want %d", len(o.Key), KeySize)
	case len(o.Nonce) != NonceSize:
		return fmt.Errorf("syncrail: sibling offer nonce is %d bytes, want %d", len(o.Nonce), NonceSize)
	case o.Sha256Ciphertext == "":
		return errors.New("syncrail: sibling offer has no ciphertext hash")
	case o.PlaintextSize == 0 || o.BlobSize <= o.PlaintextSize:
		return fmt.Errorf("syncrail: sibling offer sizes do not fit a padded blob (blob %d, plaintext %d)", o.BlobSize, o.PlaintextSize)
	}
	return nil
}

func (o *SiblingOffer) Wipe() {
	for i := range o.Key {
		o.Key[i] = 0
	}
	for i := range o.Nonce {
		o.Nonce[i] = 0
	}
}

func (o SiblingOffer) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Bool("token_present", o.Token != ""),
		slog.Int("key_bytes", len(o.Key)),
		slog.Int("nonce_bytes", len(o.Nonce)),
	)
}

func (o SiblingOffer) String() string {
	return fmt.Sprintf("SiblingOffer{token_present=%t key_bytes=%d nonce_bytes=%d}",
		o.Token != "", len(o.Key), len(o.Nonce))
}

func (o SiblingOffer) GoString() string { return o.String() }

func (o SiblingOffer) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, o.String()) }

type GapSiblingOfferBody struct {
	Fingerprint string `json:"fingerprint"`

	MsgID string `json:"msg_id"`

	FileOffer json.RawMessage `json:"file_offer"`

	SiblingOffer SiblingOffer `json:"sibling_offer"`
}

func (b GapSiblingOfferBody) Check() error {
	switch {
	case b.Fingerprint == "":
		return errors.New("syncrail: sibling offer on gap-fill has no fingerprint")
	case b.MsgID == "":
		return errors.New("syncrail: sibling offer on gap-fill has no msg id")
	case len(b.FileOffer) == 0 || string(b.FileOffer) == "null":
		return errors.New("syncrail: sibling offer on gap-fill has no file offer")
	}
	return b.SiblingOffer.Check()
}

func ParseGapSiblingOffer(env Envelope) (GapSiblingOfferBody, error) {
	if env.Kind != KindGapSiblingOffer {
		return GapSiblingOfferBody{}, fmt.Errorf("%w: %q", ErrUnknownKind, env.Kind)
	}
	var body GapSiblingOfferBody
	if err := json.Unmarshal(env.Body, &body); err != nil {
		return GapSiblingOfferBody{}, errors.New("syncrail: sibling offer on gap-fill will not decode")
	}
	if err := body.Check(); err != nil {
		body.Wipe()
		return GapSiblingOfferBody{}, err
	}
	return body, nil
}

func (b *GapSiblingOfferBody) Wipe() {
	b.SiblingOffer.Wipe()
	for i := range b.FileOffer {
		b.FileOffer[i] = 0
	}
}

func (b GapSiblingOfferBody) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("msg_id", logid.Short(b.MsgID)),
		slog.String("fingerprint", logid.Short(b.Fingerprint)),
		slog.Int("file_offer_bytes", len(b.FileOffer)),
		slog.Any("sibling_offer", b.SiblingOffer),
	)
}

func (b GapSiblingOfferBody) String() string {
	return fmt.Sprintf("GapSiblingOfferBody{msg_id=%s file_offer_bytes=%d sibling_offer=%s}",
		logid.Short(b.MsgID), len(b.FileOffer), b.SiblingOffer.String())
}

func (b GapSiblingOfferBody) GoString() string { return b.String() }

func (b GapSiblingOfferBody) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, b.String()) }
