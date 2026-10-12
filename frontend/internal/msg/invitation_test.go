package msg_test

import (
	"strings"
	"testing"

	"haoma-frontend/internal/msg"
)

const (
	invitationSecret = "correct-horse-battery-staple-secret"

	invitationDevice = "fedcba9876543210fedcba9876543210"
)

func TestBuildPairingInvitation_RoundTrip(t *testing.T) {
	w, err := msg.BuildPairingInvitation(1, 1000, "inv-1", invitationSecret, invitationDevice, 900, 2000, 3600)
	if err != nil {
		t.Fatalf("BuildPairingInvitation: %v", err)
	}
	raw, err := msg.Marshal(w)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := msg.Unmarshal(raw)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Kind != msg.KindPairingInvitation {
		t.Fatalf("kind survived as %q", got.Kind)
	}
	body, err := got.PairingInvitation()
	if err != nil {
		t.Fatalf("PairingInvitation: %v", err)
	}
	if body.Secret != invitationSecret || body.ExpiresAt != 2000 || body.MintedAt != 900 || body.OfferingDevice != invitationDevice {
		t.Fatalf("body did not survive the round trip: %+v", body)
	}
}

func TestBuildPairingInvitation_RefusesIncomplete(t *testing.T) {
	cases := map[string]struct {
		secret    string
		device    string
		mintedAt  int64
		expiresAt int64
	}{
		"no secret":          {"", invitationDevice, 900, 2000},
		"no offering device": {invitationSecret, "", 900, 2000},
		"no mint time":       {invitationSecret, invitationDevice, 0, 2000},
		"no deadline":        {invitationSecret, invitationDevice, 900, 0},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := msg.BuildPairingInvitation(1, 1000, "inv-1", c.secret, c.device, c.mintedAt, c.expiresAt, 0); err == nil {
				t.Fatal("an incomplete invitation was built")
			}
		})
	}
}

func TestPairingInvitation_RefusesABodyWithNoMintTime(t *testing.T) {
	w, err := msg.BuildPairingInvitation(1, 1000, "inv-1", invitationSecret, invitationDevice, 900, 2000, 0)
	if err != nil {
		t.Fatalf("BuildPairingInvitation: %v", err)
	}
	w.Body = []byte(`{"secret":"` + invitationSecret + `","offering_device":"` + invitationDevice + `","expires_at":2000}`)
	if _, err := w.PairingInvitation(); err == nil {
		t.Fatal("a body with no mint time was accepted")
	}
}

func TestPairingInvitation_ErrorQuotesNoSecret(t *testing.T) {
	w, err := msg.BuildPairingInvitation(1, 1000, "inv-1", invitationSecret, invitationDevice, 900, 2000, 0)
	if err != nil {
		t.Fatalf("BuildPairingInvitation: %v", err)
	}
	w.Body = []byte(`{"secret":"` + invitationSecret + `","expires_at":`)
	_, err = w.PairingInvitation()
	if err == nil {
		t.Fatal("a truncated body was accepted")
	}
	if strings.Contains(err.Error(), invitationSecret) {
		t.Fatalf("the decode error quotes the secret: %v", err)
	}
}

func TestPairingInvitation_RefusesOtherKinds(t *testing.T) {
	w, err := msg.BuildText(1, 1000, "m-1", "hello", 0, "", "", nil, 0)
	if err != nil {
		t.Fatalf("BuildText: %v", err)
	}
	if _, err := w.PairingInvitation(); err == nil {
		t.Fatal("a text wrapper was read as an invitation")
	}
}
