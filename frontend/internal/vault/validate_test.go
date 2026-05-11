package vault_test

import (
	"strings"
	"testing"

	"haoma-frontend/internal/secrets"
	"haoma-frontend/internal/vault"
)

func freshPayload() vault.Payload {

	return vault.Payload{
		Secrets: secrets.Secrets{
			HaomadStorePassphrase:   "x",
			FrontendStorePassphrase: "y",
			HaomadToken:             "z",
		},
	}
}

func TestPayloadValidateAcceptsZeroValues(t *testing.T) {
	p := freshPayload()
	if err := p.Validate(); err != nil {
		t.Fatalf("zero-value payload should validate: %v", err)
	}
}

func TestPayloadValidateRejectsBadEnums(t *testing.T) {
	cases := []struct {
		name  string
		mut   func(*vault.Payload)
		field string
	}{
		{"idle_action", func(p *vault.Payload) { p.IdleAction = "panic-lock" }, "idle_action"},
		{"panic_action", func(p *vault.Payload) { p.PanicAction = "burn-everything" }, "panic_action"},
		{"threat_profile", func(p *vault.Payload) { p.ThreatProfile = "civilian" }, "threat_profile"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := freshPayload()
			c.mut(&p)
			err := p.Validate()
			if err == nil || !strings.Contains(err.Error(), c.field) {
				t.Errorf("expected error mentioning %q, got %v", c.field, err)
			}
		})
	}
}

func TestPayloadValidateAcceptsKnownEnums(t *testing.T) {
	idleVals := []string{"", "soft-lock", "safe-lock", "hard-lock"}
	panicVals := []string{"", "safe-lock", "hard-lock", "self-destruct"}
	profileVals := []string{"", vault.PresetDomestic, vault.PresetPrivacy, vault.PresetActivist}
	for _, ia := range idleVals {
		for _, pa := range panicVals {
			for _, pr := range profileVals {
				p := freshPayload()
				p.IdleAction = ia
				p.PanicAction = pa
				p.ThreatProfile = pr
				if err := p.Validate(); err != nil {
					t.Errorf("idle=%q panic=%q profile=%q: %v", ia, pa, pr, err)
				}
			}
		}
	}
}

func TestPayloadValidateRejectsNegativeNumerics(t *testing.T) {
	p := freshPayload()
	p.PinValiditySec = -1
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "pin_validity_sec") {
		t.Errorf("negative PinValiditySec: %v", err)
	}
	p = freshPayload()
	p.RotationIntervalSec = -1
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "rotation_interval_sec") {
		t.Errorf("negative RotationIntervalSec: %v", err)
	}
}

func TestPayloadValidateRejectsEmptySecrets(t *testing.T) {
	p := freshPayload()
	p.HaomadToken = ""
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "haomad_token") {
		t.Errorf("empty HaomadToken: %v", err)
	}
}
