package syncrail_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"haoma-frontend/internal/syncrail"
)

func sampleSiblingOffer() syncrail.SiblingOffer {
	key := bytes.Repeat([]byte{0xa7}, syncrail.KeySize)
	nonce := bytes.Repeat([]byte{0x5c}, syncrail.NonceSize)
	return syncrail.SiblingOffer{
		StagingID:        "5a1b2c3d4e5f60718293a4b5c6d7e8f9",
		Token:            "sibling-token-4f1e9d",
		Key:              key,
		Nonce:            nonce,
		Sha256Ciphertext: "c0ffee",
		BlobSize:         5000,
		PlaintextSize:    4000,
	}
}

func TestSiblingOffer_RoundTripsThroughTheRail(t *testing.T) {
	key, err := syncrail.DeriveKey([]byte("identity"))
	if err != nil {
		t.Fatal(err)
	}
	body := sampleMirrorBody()
	offer := sampleSiblingOffer()
	body.SiblingOffer = &offer

	sealed, err := syncrail.SealEnvelope(key, mirrorEnvelope(t, body))
	if err != nil {
		t.Fatalf("SealEnvelope: %v", err)
	}
	env, err := syncrail.OpenEnvelope(key, sealed)
	if err != nil {
		t.Fatalf("OpenEnvelope: %v", err)
	}
	got, err := syncrail.ParseMirrorBody(env)
	if err != nil {
		t.Fatalf("ParseMirrorBody: %v", err)
	}
	if got.SiblingOffer == nil {
		t.Fatal("sibling offer lost on the way through the rail")
	}
	if got.SiblingOffer.StagingID != offer.StagingID || got.SiblingOffer.Token != offer.Token ||
		!bytes.Equal(got.SiblingOffer.Key, offer.Key) ||
		!bytes.Equal(got.SiblingOffer.Nonce, offer.Nonce) ||
		got.SiblingOffer.Sha256Ciphertext != offer.Sha256Ciphertext {
		t.Fatalf("sibling offer changed on the way through the rail")
	}
	if err := got.SiblingOffer.Check(); err != nil {
		t.Fatalf("Check on a good offer: %v", err)
	}
}

func TestSiblingOffer_AbsentOnAPlainMirror(t *testing.T) {
	raw := mustMarshal(t, sampleMirrorBody())
	if bytes.Contains(raw, []byte("sibling_offer")) {
		t.Fatalf("plain mirror body names a sibling offer: %s", raw)
	}
	env := mirrorEnvelope(t, sampleMirrorBody())
	got, err := syncrail.ParseMirrorBody(env)
	if err != nil {
		t.Fatal(err)
	}
	if got.SiblingOffer != nil {
		t.Fatal("plain mirror parsed with a sibling offer")
	}
}

func TestSiblingOffer_OlderDecoderIgnoresIt(t *testing.T) {
	body := sampleMirrorBody()
	offer := sampleSiblingOffer()
	body.SiblingOffer = &offer
	raw := mustMarshal(t, body)

	var older struct {
		Fingerprint string `json:"fingerprint"`
		Wrapper     []byte `json:"wrapper"`
		MsgID       string `json:"msg_id"`
	}
	if err := json.Unmarshal(raw, &older); err != nil {
		t.Fatalf("older decode refused a sibling offer: %v", err)
	}
	if !bytes.Equal(older.Wrapper, body.Wrapper) || older.MsgID != body.MsgID {
		t.Fatal("older decode lost the wrapper or the msg id")
	}
}

func TestSiblingOffer_NeverRendersItsSecrets(t *testing.T) {
	offer := sampleSiblingOffer()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	logger.Debug("offer", slog.Any("offer", offer))
	logger.Debug("offer ptr", slog.Any("offer", &offer))
	rendered := buf.String() +
		fmt.Sprintf("%v %+v %#v %s", offer, offer, offer, offer) +
		fmt.Sprintf("%v %+v", &offer, &offer)

	forbidden := []string{
		offer.Token,
		hex.EncodeToString(offer.Key),
		hex.EncodeToString(offer.Nonce),

		fmt.Sprint(offer.Key),
		fmt.Sprint(offer.Nonce),
	}
	for _, f := range forbidden {
		if strings.Contains(rendered, f) {
			t.Fatalf("a secret reached the rendered output: %q in %s", f, rendered)
		}
	}
	if !strings.Contains(buf.String(), "key_bytes=32") {
		t.Fatalf("log line lost the key size: %s", buf.String())
	}
}

func TestSiblingOffer_CheckRefusesAnOfferThatCannotOpen(t *testing.T) {
	cases := map[string]func(*syncrail.SiblingOffer){
		"no staging id": func(o *syncrail.SiblingOffer) { o.StagingID = "" },
		"no padding":    func(o *syncrail.SiblingOffer) { o.BlobSize = o.PlaintextSize },
		"no plaintext":  func(o *syncrail.SiblingOffer) { o.PlaintextSize = 0 },
		"no token":      func(o *syncrail.SiblingOffer) { o.Token = "" },
		"short key":     func(o *syncrail.SiblingOffer) { o.Key = o.Key[:16] },
		"short nonce":   func(o *syncrail.SiblingOffer) { o.Nonce = o.Nonce[:12] },
		"no hash":       func(o *syncrail.SiblingOffer) { o.Sha256Ciphertext = "" },
		"no key at all": func(o *syncrail.SiblingOffer) { o.Key = nil },
	}
	for name, spoil := range cases {
		t.Run(name, func(t *testing.T) {
			o := sampleSiblingOffer()
			spoil(&o)
			if err := o.Check(); err == nil {
				t.Fatal("Check accepted an offer that cannot open a blob")
			}
		})
	}
}

func TestSiblingOffer_WipeZeroesKeyAndNonce(t *testing.T) {
	o := sampleSiblingOffer()
	o.Wipe()
	if !bytes.Equal(o.Key, make([]byte, syncrail.KeySize)) || !bytes.Equal(o.Nonce, make([]byte, syncrail.NonceSize)) {
		t.Fatal("Wipe left key or nonce bytes behind")
	}
}
