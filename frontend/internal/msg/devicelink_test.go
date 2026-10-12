package msg

import (
	"encoding/json"
	"testing"
)

func testIdentityKeys() (public, private []byte) {
	public = make([]byte, IdentityPublicLen)
	private = make([]byte, IdentityPrivateLen)
	for i := range public {
		public[i] = byte(i + 1)
	}
	for i := range private {
		private[i] = byte(i + 40)
	}
	return public, private
}

func TestDeviceLinkIdentity_RoundTripsTheSelfNick(t *testing.T) {
	public, private := testIdentityKeys()
	w, err := BuildDeviceLinkIdentity(1, 1756200000, "6ad3910e75c2483bb0f9e6172c8d5401", public, private, "mara", 1756200123)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	body, err := w.DeviceLinkIdentity()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.SelfNick != "mara" {
		t.Fatalf("self_nick round-tripped as %q, want %q", body.SelfNick, "mara")
	}

	if body.SelfNickAt != 1756200123 {
		t.Fatalf("self_nick_at round-tripped as %d, want %d", body.SelfNickAt, 1756200123)
	}
}

func TestDeviceLinkIdentity_OmitsAnEmptySelfNick(t *testing.T) {
	public, private := testIdentityKeys()
	w, err := BuildDeviceLinkIdentity(1, 1756200000, "6ad3910e75c2483bb0f9e6172c8d5401", public, private, "", 0)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body, &raw); err != nil {
		t.Fatalf("decode raw body: %v", err)
	}
	if _, present := raw["self_nick"]; present {
		t.Fatalf("the body carries a self_nick key with no nick to send; raw body = %s", w.Body)
	}
	body, err := w.DeviceLinkIdentity()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.SelfNick != "" {
		t.Fatalf("self_nick decoded as %q, want empty", body.SelfNick)
	}
}

func TestDeviceLinkIdentity_OldShapeBodyStillDecodes(t *testing.T) {
	public, private := testIdentityKeys()

	old, err := json.Marshal(map[string]any{
		"identity_public":  public,
		"identity_private": private,
	})
	if err != nil {
		t.Fatal(err)
	}
	w := &Wrapper{
		V:     Version,
		Seq:   1,
		Ts:    1756200000,
		MsgID: "6ad3910e75c2483bb0f9e6172c8d5401",
		Kind:  KindDeviceLinkIdentity,
		Body:  old,
	}
	body, err := w.DeviceLinkIdentity()
	if err != nil {
		t.Fatalf("an old-shape body failed to decode: %v", err)
	}
	if body.SelfNick != "" {
		t.Fatalf("an old-shape body decoded self_nick as %q, want empty", body.SelfNick)
	}
	if len(body.Public) != IdentityPublicLen || len(body.Private) != IdentityPrivateLen {
		t.Fatalf("an old-shape body lost its key material: public %d, private %d", len(body.Public), len(body.Private))
	}
}

func TestDeviceLinkIdentityBody_ZeroWipesTheKeyMaterial(t *testing.T) {
	public, private := testIdentityKeys()
	body := &DeviceLinkIdentityBody{Public: public, Private: private, SelfNick: "mara"}
	body.Zero()

	for i, b := range body.Private {
		if b != 0 {
			t.Fatalf("Zero left private byte %d as %d", i, b)
		}
	}
	for i, b := range body.Public {
		if b != 0 {
			t.Fatalf("Zero left public byte %d as %d", i, b)
		}
	}
}
