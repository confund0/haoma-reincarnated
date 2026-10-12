package syncrail_test

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"haoma-frontend/internal/syncrail"
)

func destroySaltEnvelope(t *testing.T, body any) syncrail.Envelope {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return syncrail.Envelope{V: syncrail.Version, Kind: syncrail.KindDestroySalt, Ts: 1, Body: raw}
}

func TestDestroySalt_RoundTripThroughTheSeal(t *testing.T) {
	key, err := syncrail.DeriveKey([]byte("identity-material"))
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	salt := []byte("0123456789abcdef")
	sealed, err := syncrail.SealEnvelope(key, destroySaltEnvelope(t, syncrail.NewDestroySaltBody(salt, 1700000000)))
	if err != nil {
		t.Fatalf("SealEnvelope: %v", err)
	}
	env, err := syncrail.OpenEnvelope(key, sealed)
	if err != nil {
		t.Fatalf("OpenEnvelope: %v", err)
	}
	if env.Kind != syncrail.KindDestroySalt {
		t.Fatalf("kind = %q, want %q", env.Kind, syncrail.KindDestroySalt)
	}
	body, got, err := syncrail.ParseDestroySalt(env)
	if err != nil {
		t.Fatalf("ParseDestroySalt: %v", err)
	}
	if string(got) != string(salt) {
		t.Fatalf("salt = %q, want %q", got, salt)
	}
	if body.SetAt != 1700000000 {
		t.Fatalf("set_at = %d, want 1700000000", body.SetAt)
	}
}

func TestDestroySalt_KindIsKnown(t *testing.T) {
	key, err := syncrail.DeriveKey([]byte("identity-material"))
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	sealed, err := syncrail.SealEnvelope(key, destroySaltEnvelope(t, syncrail.NewDestroySaltBody([]byte("abc"), 0)))
	if err != nil {
		t.Fatalf("SealEnvelope: %v", err)
	}
	if _, err := syncrail.OpenEnvelope(key, sealed); errors.Is(err, syncrail.ErrUnknownKind) {
		t.Fatal("the destroy-salt kind opens as unknown")
	}
}

func TestDestroySalt_RefusesAnUnusableBody(t *testing.T) {
	cases := map[string]any{
		"no salt":    map[string]string{},
		"empty salt": map[string]string{"salt": ""},
		"not hex":    map[string]string{"salt": "zzzz"},
		"over the limit": map[string]string{
			"salt": hex.EncodeToString(make([]byte, 65)),
		},
	}
	for name, body := range cases {
		if _, _, err := syncrail.ParseDestroySalt(destroySaltEnvelope(t, body)); err == nil {
			t.Errorf("%s: parsed without error", name)
		}
	}
}

func TestDestroySalt_RefusesAnotherKind(t *testing.T) {
	env := destroySaltEnvelope(t, syncrail.NewDestroySaltBody([]byte("abc"), 0))
	env.Kind = syncrail.KindContacts
	if _, _, err := syncrail.ParseDestroySalt(env); !errors.Is(err, syncrail.ErrUnknownKind) {
		t.Fatalf("err = %v, want ErrUnknownKind", err)
	}
}

func TestDestroySalt_ErrorNeverCarriesTheSalt(t *testing.T) {
	const bad = "zzzznotthisvaluezzzz"
	_, _, err := syncrail.ParseDestroySalt(destroySaltEnvelope(t, map[string]string{"salt": bad}))
	if err == nil {
		t.Fatal("a non-hex salt parsed")
	}
	if strings.Contains(err.Error(), bad) {
		t.Fatalf("the error carries the salt: %v", err)
	}
}

func TestDestroySaltReport_WireIsPinned(t *testing.T) {
	const raw = `{"section":"contacts","destroy_salt":{"held":true,"sha256":"ab12"}}`
	env := syncrail.Envelope{V: syncrail.Version, Kind: syncrail.KindBackfillRequest, Ts: 1, Body: json.RawMessage(raw)}
	req, err := syncrail.ParseBackfillRequest(env)
	if err != nil {
		t.Fatalf("ParseBackfillRequest: %v", err)
	}
	if req.DestroySalt == nil || !req.DestroySalt.Held || req.DestroySalt.SHA256 != "ab12" {
		t.Fatalf("report = %+v; a destroy_salt tag changed", req.DestroySalt)
	}

	out, err := json.Marshal(syncrail.BackfillRequestBody{
		Section:     syncrail.SectionContacts,
		DestroySalt: &syncrail.DestroySaltReport{Held: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"section":"contacts","destroy_salt":{"held":false}}`; string(out) != want {
		t.Fatalf("encoded %s, want %s", out, want)
	}

	out, err = json.Marshal(syncrail.BackfillRequestBody{Section: syncrail.SectionContacts})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "destroy_salt") {
		t.Fatalf("a request with no report encoded %s", out)
	}
}

func TestDestroySaltReport_DigestIsSHA256OfTheSalt(t *testing.T) {
	got := syncrail.DestroySaltDigest([]byte("abc"))
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got != want {
		t.Fatalf("digest = %s, want %s", got, want)
	}
	if _, err := hex.DecodeString(got); err != nil {
		t.Fatalf("digest is not hex: %v", err)
	}
}
