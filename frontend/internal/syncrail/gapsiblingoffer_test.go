package syncrail_test

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"haoma-frontend/internal/syncrail"
)

const gapOfferMsgID = "0123456789abcdef0123456789abcdef"

func sampleGapFileOffer() json.RawMessage {
	o := sampleSiblingOffer()
	return json.RawMessage(fmt.Sprintf(
		`{"token":%q,"url_path":%q,"name":"report.pdf","size":4016,"mime":"application/pdf","sha256_ciphertext":%q}`,
		o.Token, "/files/"+o.Token, o.Sha256Ciphertext))
}

func sampleGapSiblingOfferBody() syncrail.GapSiblingOfferBody {
	return syncrail.GapSiblingOfferBody{
		Fingerprint:  "aa11bb22cc33dd44ee55ff6600778899",
		MsgID:        gapOfferMsgID,
		FileOffer:    sampleGapFileOffer(),
		SiblingOffer: sampleSiblingOffer(),
	}
}

func gapSiblingOfferEnvelope(t *testing.T, body any) syncrail.Envelope {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return syncrail.Envelope{V: syncrail.Version, Kind: syncrail.KindGapSiblingOffer, Ts: 1, Body: raw}
}

func TestGapSiblingOffer_RoundTripThroughTheSeal(t *testing.T) {
	key, err := syncrail.DeriveKey([]byte("identity-material"))
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	want := sampleGapSiblingOfferBody()
	sealed, err := syncrail.SealEnvelope(key, gapSiblingOfferEnvelope(t, want))
	if err != nil {
		t.Fatalf("SealEnvelope: %v", err)
	}
	env, err := syncrail.OpenEnvelope(key, sealed)
	if err != nil {
		t.Fatalf("OpenEnvelope: %v", err)
	}
	if env.Kind != syncrail.KindGapSiblingOffer {
		t.Fatalf("kind = %q, want %q", env.Kind, syncrail.KindGapSiblingOffer)
	}
	got, err := syncrail.ParseGapSiblingOffer(env)
	if err != nil {
		t.Fatalf("ParseGapSiblingOffer: %v", err)
	}
	if got.Fingerprint != want.Fingerprint || got.MsgID != want.MsgID {
		t.Fatal("fingerprint or msg id changed on the way through the rail")
	}
	var fo, wantFO map[string]any
	if err := json.Unmarshal(got.FileOffer, &fo); err != nil {
		t.Fatalf("file offer does not decode: %v", err)
	}
	_ = json.Unmarshal(want.FileOffer, &wantFO)
	if fmt.Sprint(fo) != fmt.Sprint(wantFO) {
		t.Fatal("file offer changed on the way through the rail")
	}
	o, w := got.SiblingOffer, want.SiblingOffer
	if o.StagingID != w.StagingID || o.Token != w.Token || !bytes.Equal(o.Key, w.Key) ||
		!bytes.Equal(o.Nonce, w.Nonce) || o.Sha256Ciphertext != w.Sha256Ciphertext ||
		o.BlobSize != w.BlobSize || o.PlaintextSize != w.PlaintextSize {
		t.Fatal("sibling offer changed on the way through the rail")
	}
}

func TestGapSiblingOffer_KindIsKnown(t *testing.T) {
	key, err := syncrail.DeriveKey([]byte("identity-material"))
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	sealed, err := syncrail.SealEnvelope(key, gapSiblingOfferEnvelope(t, sampleGapSiblingOfferBody()))
	if err != nil {
		t.Fatalf("SealEnvelope: %v", err)
	}
	if _, err := syncrail.OpenEnvelope(key, sealed); errors.Is(err, syncrail.ErrUnknownKind) {
		t.Fatal("the gap-sibling-offer kind opens as unknown")
	}
}

func TestGapSiblingOffer_RefusesAnUnusableBody(t *testing.T) {
	cases := map[string]func(*syncrail.GapSiblingOfferBody){
		"no fingerprint":    func(b *syncrail.GapSiblingOfferBody) { b.Fingerprint = "" },
		"no msg id":         func(b *syncrail.GapSiblingOfferBody) { b.MsgID = "" },
		"no file offer":     func(b *syncrail.GapSiblingOfferBody) { b.FileOffer = nil },
		"null file offer":   func(b *syncrail.GapSiblingOfferBody) { b.FileOffer = json.RawMessage("null") },
		"no staging id":     func(b *syncrail.GapSiblingOfferBody) { b.SiblingOffer.StagingID = "" },
		"no token":          func(b *syncrail.GapSiblingOfferBody) { b.SiblingOffer.Token = "" },
		"short key":         func(b *syncrail.GapSiblingOfferBody) { b.SiblingOffer.Key = b.SiblingOffer.Key[:16] },
		"short nonce":       func(b *syncrail.GapSiblingOfferBody) { b.SiblingOffer.Nonce = b.SiblingOffer.Nonce[:12] },
		"no hash":           func(b *syncrail.GapSiblingOfferBody) { b.SiblingOffer.Sha256Ciphertext = "" },
		"no padding":        func(b *syncrail.GapSiblingOfferBody) { b.SiblingOffer.BlobSize = b.SiblingOffer.PlaintextSize },
		"no sibling offer":  func(b *syncrail.GapSiblingOfferBody) { b.SiblingOffer = syncrail.SiblingOffer{} },
		"no plaintext size": func(b *syncrail.GapSiblingOfferBody) { b.SiblingOffer.PlaintextSize = 0 },
	}
	for name, spoil := range cases {
		t.Run(name, func(t *testing.T) {
			b := sampleGapSiblingOfferBody()
			spoil(&b)
			got, err := syncrail.ParseGapSiblingOffer(gapSiblingOfferEnvelope(t, b))
			if err == nil {
				t.Fatal("parsed a body that cannot be taken")
			}
			if got.MsgID != "" || len(got.SiblingOffer.Key) != 0 || len(got.FileOffer) != 0 {
				t.Fatal("returned a populated body alongside the error")
			}
		})
	}
}

func TestGapSiblingOffer_RefusesAnotherKind(t *testing.T) {
	env := gapSiblingOfferEnvelope(t, sampleGapSiblingOfferBody())
	env.Kind = syncrail.KindMirror
	if _, err := syncrail.ParseGapSiblingOffer(env); !errors.Is(err, syncrail.ErrUnknownKind) {
		t.Fatalf("err = %v, want ErrUnknownKind", err)
	}
}

func TestGapSiblingOffer_ErrorNeverCarriesASecret(t *testing.T) {
	const secret = "not-base64-secret-token-value!!"
	const numericSecret = "8675309867530986753"
	good := sampleGapSiblingOfferBody()
	bodies := map[string]string{
		"key not base64": `{"fingerprint":"f","msg_id":"m","file_offer":{},"sibling_offer":{"token":"t","key":"` + secret + `"}}`,

		"size out of range": `{"fingerprint":"f","msg_id":"m","file_offer":{},"sibling_offer":{"token":"t","blob_size":-` + numericSecret + `}}`,
		"token not string":  `{"fingerprint":"f","msg_id":"m","file_offer":{},"sibling_offer":{"token":["` + secret + `"]}}`,
		"offer not object":  `{"fingerprint":"f","msg_id":"m","file_offer":{},"sibling_offer":"` + secret + `"}`,
		"truncated":         `{"fingerprint":"f","msg_id":"m","file_offer":{"token":"` + secret,
	}
	for name, raw := range bodies {
		t.Run(name, func(t *testing.T) {
			env := syncrail.Envelope{V: syncrail.Version, Kind: syncrail.KindGapSiblingOffer, Ts: 1, Body: json.RawMessage(raw)}
			_, err := syncrail.ParseGapSiblingOffer(env)
			if err == nil {
				t.Fatal("a body that does not decode parsed")
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), secret[:8]) ||
				strings.Contains(err.Error(), numericSecret) {
				t.Fatalf("the error carries a value from the body: %v", err)
			}
		})
	}

	good.SiblingOffer.Nonce = good.SiblingOffer.Nonce[:3]
	_, err := syncrail.ParseGapSiblingOffer(gapSiblingOfferEnvelope(t, good))
	if err == nil {
		t.Fatal("a short nonce parsed")
	}
	for _, v := range []string{good.SiblingOffer.Token, good.MsgID, hex.EncodeToString(good.SiblingOffer.Key)} {
		if strings.Contains(err.Error(), v) {
			t.Fatalf("the error carries %q: %v", v, err)
		}
	}
}

func TestGapSiblingOffer_NeverRendersItsSecrets(t *testing.T) {
	b := sampleGapSiblingOfferBody()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	logger.Debug("offer", slog.Any("body", b))
	logger.Debug("offer ptr", slog.Any("body", &b))
	jbuf := bytes.Buffer{}
	slog.New(slog.NewJSONHandler(&jbuf, &slog.HandlerOptions{Level: slog.LevelDebug})).Debug("offer", slog.Any("body", b))

	rendered := buf.String() + jbuf.String()
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%d", "%x", "%q"} {
		rendered += fmt.Sprintf(verb, b) + fmt.Sprintf(verb, &b)
	}

	o := b.SiblingOffer
	forbidden := []string{
		o.Token,
		hex.EncodeToString(o.Key),
		hex.EncodeToString(o.Nonce),
		base64.StdEncoding.EncodeToString(o.Key),
		base64.StdEncoding.EncodeToString(o.Nonce),

		fmt.Sprint(o.Key),
		fmt.Sprint(o.Nonce),
		fmt.Sprint([]byte(b.FileOffer)),
		string(b.FileOffer),
		b.MsgID,
		b.Fingerprint,
	}
	for _, f := range forbidden {
		if strings.Contains(rendered, f) {
			t.Fatalf("a secret or a full id reached the rendered output: %q in %s", f, rendered)
		}
	}
	for _, want := range []string{"key_bytes=32", "nonce_bytes=24", "token_present=true", "msg_id=01234567…"} {
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("log line lost %q: %s", want, buf.String())
		}
	}
}

func TestGapSiblingOffer_WipeZeroesKeyNonceAndFileOffer(t *testing.T) {
	b := sampleGapSiblingOfferBody()
	b.Wipe()
	if !bytes.Equal(b.SiblingOffer.Key, make([]byte, syncrail.KeySize)) ||
		!bytes.Equal(b.SiblingOffer.Nonce, make([]byte, syncrail.NonceSize)) ||
		!bytes.Equal(b.FileOffer, make([]byte, len(b.FileOffer))) {
		t.Fatal("Wipe left key, nonce or file offer bytes behind")
	}
}

func TestGapSiblingOffer_IsNotAMirror(t *testing.T) {
	env := gapSiblingOfferEnvelope(t, sampleGapSiblingOfferBody())
	if _, err := syncrail.ParseMirrorBody(env); !errors.Is(err, syncrail.ErrUnknownKind) {
		t.Fatalf("ParseMirrorBody on a sibling offer on gap-fill: err = %v, want ErrUnknownKind", err)
	}
}
