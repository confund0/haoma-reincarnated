package syncrail

import (
	"encoding/json"
	"strings"
	"testing"

	"haoma-frontend/internal/logid"
)

const (
	testInvitationSecret = "correct-horse-battery-staple-secret"

	testFingerprint = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	testOfferingDevice = "fedcba9876543210fedcba9876543210"
	testMintedAt       = 1234567800
)

func invitationEnvelope(t *testing.T, body InvitationBody) Envelope {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return Envelope{V: Version, Kind: KindInvitation, Ts: 100, Body: raw}
}

func TestParseInvitationBody_RoundTrip(t *testing.T) {
	want := InvitationBody{
		ID:             "abc123",
		Fingerprint:    testFingerprint,
		OfferingDevice: testOfferingDevice,
		Secret:         testInvitationSecret,
		MintedAt:       testMintedAt,
		ExpiresAt:      1234567890,
	}
	got, err := ParseInvitationBody(invitationEnvelope(t, want))
	if err != nil {
		t.Fatalf("ParseInvitationBody: %v", err)
	}
	if got != want {
		t.Fatalf("round trip changed the body: got %+v", got)
	}
}

func TestParseInvitationBody_RefusesIncomplete(t *testing.T) {
	cases := map[string]InvitationBody{
		"no id":              {Fingerprint: testFingerprint, OfferingDevice: testOfferingDevice, Secret: testInvitationSecret, MintedAt: testMintedAt},
		"no fingerprint":     {ID: "abc123", OfferingDevice: testOfferingDevice, Secret: testInvitationSecret, MintedAt: testMintedAt},
		"no offering device": {ID: "abc123", Fingerprint: testFingerprint, Secret: testInvitationSecret, MintedAt: testMintedAt},
		"no secret":          {ID: "abc123", Fingerprint: testFingerprint, OfferingDevice: testOfferingDevice, MintedAt: testMintedAt},
		"no mint time":       {ID: "abc123", Fingerprint: testFingerprint, OfferingDevice: testOfferingDevice, Secret: testInvitationSecret},
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseInvitationBody(invitationEnvelope(t, body)); err == nil {
				t.Fatal("an incomplete invitation was accepted")
			}
		})
	}
}

func TestParseInvitationBody_RefusesOtherKinds(t *testing.T) {
	env := invitationEnvelope(t, InvitationBody{ID: "a", Fingerprint: "b", OfferingDevice: "d", Secret: "c"})
	env.Kind = KindMirror
	if _, err := ParseInvitationBody(env); err == nil {
		t.Fatal("a mirror envelope was parsed as an invitation")
	}
}

func TestInvitationBody_NeverLogsTheSecret(t *testing.T) {
	body := InvitationBody{
		ID:             "abc123",
		Fingerprint:    testFingerprint,
		OfferingDevice: testOfferingDevice,
		Secret:         testInvitationSecret,
		MintedAt:       testMintedAt,
		ExpiresAt:      99,
	}
	rendered := body.LogValue().String()
	if strings.Contains(rendered, testInvitationSecret) {
		t.Fatalf("the log value carries the secret: %s", rendered)
	}
	for i := 4; i <= len(testInvitationSecret); i++ {
		if strings.Contains(rendered, testInvitationSecret[:i]) {
			t.Fatalf("the log value carries a %d-character prefix of the secret: %s", i, rendered)
		}
	}
	if !strings.Contains(rendered, "abc123") || !strings.Contains(rendered, logid.Short(testFingerprint)) {
		t.Fatalf("the log value dropped the fields a log line needs: %s", rendered)
	}
}

func TestInvitationBody_LogValueCarriesNoFullLengthIDs(t *testing.T) {
	body := InvitationBody{
		ID:             "0123456789abcdef0123456789abcdef",
		Fingerprint:    testFingerprint,
		OfferingDevice: testOfferingDevice,
		Secret:         testInvitationSecret,
		MintedAt:       testMintedAt,
		ExpiresAt:      99,
	}
	rendered := body.LogValue().String()
	for _, id := range []string{body.ID, body.Fingerprint, body.OfferingDevice} {
		if strings.Contains(rendered, id) {
			t.Fatalf("the log value carries a full-length id: %s", rendered)
		}
	}
}

func TestParseInvitationBody_ErrorsQuoteNoSecret(t *testing.T) {
	env := Envelope{
		V:    Version,
		Kind: KindInvitation,
		Ts:   100,

		Body: []byte(`{"id":"abc123","secret":"` + testInvitationSecret + `","expires_at":`),
	}
	_, err := ParseInvitationBody(env)
	if err == nil {
		t.Fatal("a truncated body was accepted")
	}
	for i := 4; i <= len(testInvitationSecret); i++ {
		if strings.Contains(err.Error(), testInvitationSecret[:i]) {
			t.Fatalf("the refusal quotes a %d-character prefix of the secret: %v", i, err)
		}
	}
}

func TestInvitationEnvelopeOpens(t *testing.T) {
	key, err := DeriveKey([]byte("identity-private-key-material-32"))
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	body := InvitationBody{ID: "abc123", Fingerprint: testFingerprint, OfferingDevice: testOfferingDevice, Secret: testInvitationSecret, MintedAt: testMintedAt, ExpiresAt: 42}
	sealed, err := SealEnvelope(key, invitationEnvelope(t, body))
	if err != nil {
		t.Fatalf("SealEnvelope: %v", err)
	}
	env, err := OpenEnvelope(key, sealed)
	if err != nil {
		t.Fatalf("OpenEnvelope: %v", err)
	}
	if env.Kind != KindInvitation {
		t.Fatalf("kind survived as %q", env.Kind)
	}
	got, err := ParseInvitationBody(env)
	if err != nil {
		t.Fatalf("ParseInvitationBody: %v", err)
	}
	if got.Secret != testInvitationSecret {
		t.Fatal("the secret did not survive the seal")
	}
}
