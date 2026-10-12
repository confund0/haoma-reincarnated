package syncrail_test

import (
	"encoding/json"
	"errors"
	"testing"

	"haoma-frontend/internal/syncrail"
)

func TestContactDelete_WireIsPinned(t *testing.T) {
	key := mustDeriveKey(t, identityA)
	const raw = `{"v":1,"kind":"contact-delete","ts":1,"body":{"deleted":[{"fingerprint":"aa11","deleted_at":1790869613}]}}`
	env, err := syncrail.OpenEnvelope(key, sealRawEnvelope(t, key, raw))
	if err != nil {
		t.Fatalf("OpenEnvelope on a hand-written contact delete: %v", err)
	}
	body, err := syncrail.ParseContactDelete(env)
	if err != nil {
		t.Fatalf("ParseContactDelete: %v", err)
	}
	if len(body.Deleted) != 1 || body.Deleted[0].Fingerprint != "aa11" || body.Deleted[0].DeletedAt != 1790869613 {
		t.Fatalf("parsed %+v; a tag changed", body.Deleted)
	}
	out, err := json.Marshal(syncrail.BackfillResponseBody{
		Section:         syncrail.SectionContacts,
		Contacts:        []syncrail.ContactSnapshot{{Fingerprint: "aa11", PairedAt: 7}},
		DeletedContacts: []syncrail.DeletedContact{{Fingerprint: "bb22", DeletedAt: 9}},
	})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"section":"contacts","contacts":[{"fingerprint":"aa11","paired_at":7}],"deleted_contacts":[{"fingerprint":"bb22","deleted_at":9}]}`
	if string(out) != want {
		t.Fatalf("encoded\n%s\nwant\n%s", out, want)
	}
}

func TestContactDelete_ParseDropsUnusableEntries(t *testing.T) {
	env := func(body string) syncrail.Envelope {
		return syncrail.Envelope{V: syncrail.Version, Kind: syncrail.KindContactDelete, Ts: 1, Body: json.RawMessage(body)}
	}
	body, err := syncrail.ParseContactDelete(env(`{"deleted":[{"fingerprint":"","deleted_at":5},{"fingerprint":"aa","deleted_at":0},{"fingerprint":"bb","deleted_at":5}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(body.Deleted) != 1 || body.Deleted[0].Fingerprint != "bb" {
		t.Fatalf("kept %+v, want only bb", body.Deleted)
	}
	if _, err := syncrail.ParseContactDelete(env(`{"deleted":[{"fingerprint":"","deleted_at":5}]}`)); err == nil {
		t.Fatal("an envelope naming nobody was taken")
	}
	if _, err := syncrail.ParseContactDelete(syncrail.Envelope{V: 1, Kind: syncrail.KindContacts, Body: json.RawMessage(`{}`)}); !errors.Is(err, syncrail.ErrUnknownKind) {
		t.Fatalf("a contacts envelope parsed as a delete: %v", err)
	}
}

func TestContactDelete_KindIsKnown(t *testing.T) {
	key := mustDeriveKey(t, identityA)
	raw, _ := json.Marshal(syncrail.ContactDeleteBody{Deleted: []syncrail.DeletedContact{{Fingerprint: "aa", DeletedAt: 1}}})
	sealed, err := syncrail.SealEnvelope(key, syncrail.Envelope{Kind: syncrail.KindContactDelete, Ts: 1, Body: raw})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := syncrail.OpenEnvelope(key, sealed); err != nil {
		t.Fatalf("the contact-delete kind does not open: %v", err)
	}
}

func TestContactDelete_AnOlderBuildReadsTheSectionWithoutTheNewFields(t *testing.T) {
	type oldContact struct {
		Fingerprint string `json:"fingerprint"`
		Nick        string `json:"nick,omitempty"`
	}
	type oldResponse struct {
		Section  string       `json:"section"`
		Contacts []oldContact `json:"contacts,omitempty"`
	}
	raw, err := json.Marshal(syncrail.BackfillResponseBody{
		Section:         syncrail.SectionContacts,
		Contacts:        []syncrail.ContactSnapshot{{Fingerprint: "aa11", Nick: "dave", PairedAt: 7}},
		DeletedContacts: []syncrail.DeletedContact{{Fingerprint: "bb22", DeletedAt: 9}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var old oldResponse
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatalf("an older build fails on the new section: %v", err)
	}
	if len(old.Contacts) != 1 || old.Contacts[0].Fingerprint != "aa11" {
		t.Fatalf("an older build read %+v", old.Contacts)
	}
}
