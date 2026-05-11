package pair

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

type BootstrapPayload struct {
	OnionURL  string `json:"onion_url"`
	GUID      string `json:"guid"`
	ExpiresAt int64  `json:"expires_at"`
}

type Materials struct {
	IDWords           []string
	PassphraseWords   []string
	GUID              string
	PubKey            []byte
	ExpiresAt         time.Time
	IDEntropy         []byte
	PassphraseEntropy []byte
}

const (
	DefaultTTL     = 24 * time.Hour
	IDBits         = 33
	PassphraseBits = 44
)

func Publish(ctx context.Context, d *DHT, onionURL string, now time.Time) (*Materials, error) {
	if d == nil {
		return nil, errors.New("pair: nil DHT client")
	}
	if onionURL == "" {
		return nil, errors.New("pair: empty onion URL")
	}
	idWords, err := RandomWords(IDBits)
	if err != nil {
		return nil, err
	}
	passWords, err := RandomWords(PassphraseBits)
	if err != nil {
		return nil, err
	}
	idEnt, err := DecodeWords(idWords, IDBits)
	if err != nil {
		return nil, err
	}
	passEnt, err := DecodeWords(passWords, PassphraseBits)
	if err != nil {
		return nil, err
	}

	guid, err := newGUID()
	if err != nil {
		return nil, err
	}

	expires := now.Add(DefaultTTL)
	payload := BootstrapPayload{
		OnionURL:  onionURL,
		GUID:      guid,
		ExpiresAt: expires.Unix(),
	}
	plain, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("pair: marshal payload: %w", err)
	}
	cipher, err := EncryptBootstrap(plain, passEnt, idEnt)
	if err != nil {
		return nil, err
	}
	slog.Debug("pair: publishing bootstrap",
		slog.String("guid", guid),
		slog.Int("cipher_bytes", len(cipher)),
		slog.Int("id_bits", IDBits),
		slog.Int("pass_bits", PassphraseBits),
	)
	pub, err := d.Publish(ctx, idEnt, cipher)
	if err != nil {
		return nil, err
	}
	slog.Info("pair: published",
		slog.String("guid", guid),
		slog.String("onion", onionURL),
		slog.Time("expires_at", expires),
	)
	return &Materials{
		IDWords:           idWords,
		PassphraseWords:   passWords,
		GUID:              guid,
		PubKey:            pub,
		ExpiresAt:         expires,
		IDEntropy:         idEnt,
		PassphraseEntropy: passEnt,
	}, nil
}

func Fetch(ctx context.Context, d *DHT, idWords, passphraseWords []string) (*BootstrapPayload, error) {
	if d == nil {
		return nil, errors.New("pair: nil DHT client")
	}
	idEnt, err := DecodeWords(idWords, IDBits)
	if err != nil {
		return nil, err
	}
	passEnt, err := DecodeWords(passphraseWords, PassphraseBits)
	if err != nil {
		return nil, err
	}
	slog.Debug("pair: fetching bootstrap",
		slog.Int("id_words", len(idWords)),
		slog.Int("pass_words", len(passphraseWords)),
	)
	cipher, err := d.Fetch(ctx, idEnt)
	if err != nil {
		return nil, err
	}
	if len(cipher) == 0 {

		return nil, ErrItemNotFound
	}
	slog.Debug("pair: bootstrap cipher retrieved",
		slog.Int("cipher_bytes", len(cipher)),
	)
	plain, err := DecryptBootstrap(cipher, passEnt, idEnt)
	if err != nil {
		return nil, err
	}
	var payload BootstrapPayload
	if err := json.Unmarshal(plain, &payload); err != nil {
		return nil, fmt.Errorf("pair: parse bootstrap payload: %w", err)
	}
	if payload.ExpiresAt > 0 && time.Now().Unix() > payload.ExpiresAt {
		return nil, ErrExpired
	}
	slog.Info("pair: bootstrap decrypted",
		slog.String("guid", payload.GUID),
		slog.String("onion", payload.OnionURL),
		slog.Int64("expires_at", payload.ExpiresAt),
	)
	return &payload, nil
}

func Revoke(ctx context.Context, d *DHT, idEntropy []byte) error {
	return d.Revoke(ctx, idEntropy)
}

var ErrExpired = errors.New("pair: bootstrap expired")

func newGUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("pair: random guid: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
