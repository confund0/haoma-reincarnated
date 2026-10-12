package syncrail_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"haoma-frontend/internal/syncrail"
)

func contactsEnvelope(t *testing.T, body syncrail.ContactsBody) syncrail.Envelope {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return syncrail.Envelope{V: syncrail.Version, Kind: syncrail.KindContacts, Ts: 1, Body: raw}
}

func TestParseContacts_DropsAnEntryWithNoFingerprintAndKeepsTheRest(t *testing.T) {
	env := contactsEnvelope(t, syncrail.ContactsBody{Contacts: []syncrail.ContactSnapshot{
		{Fingerprint: "", Alias: "nobody"},
		{Fingerprint: "fp-alice", Alias: "Boss", AliasAt: 1000},
		{Fingerprint: "fp-bob", Nick: "bob", NickAt: 900},
	}})
	body, err := syncrail.ParseContacts(env)
	if err != nil {
		t.Fatalf("ParseContacts: %v", err)
	}
	if len(body.Contacts) != 2 {
		t.Fatalf("got %d contacts, want 2 — the unnamed entry should go and the rest should stay", len(body.Contacts))
	}
	if body.Contacts[0].Fingerprint != "fp-alice" || body.Contacts[1].Fingerprint != "fp-bob" {
		t.Errorf("got %+v, want alice then bob in order", body.Contacts)
	}
}

func TestParseContacts_RefusesAnEnvelopeThatNamesNobody(t *testing.T) {
	env := contactsEnvelope(t, syncrail.ContactsBody{Contacts: []syncrail.ContactSnapshot{
		{Alias: "nobody"},
		{Nick: "also nobody"},
	}})
	if _, err := syncrail.ParseContacts(env); err == nil {
		t.Fatal("ParseContacts accepted an envelope naming nobody")
	}
}

func TestParseContacts_RefusesAnEmptyBody(t *testing.T) {
	if _, err := syncrail.ParseContacts(contactsEnvelope(t, syncrail.ContactsBody{})); err == nil {
		t.Fatal("ParseContacts accepted an empty body")
	}
}

func TestParseContacts_RefusesAnotherKind(t *testing.T) {
	env := contactsEnvelope(t, syncrail.ContactsBody{Contacts: []syncrail.ContactSnapshot{{Fingerprint: "fp-alice"}}})
	env.Kind = syncrail.KindChatSettings
	if _, err := syncrail.ParseContacts(env); err == nil {
		t.Fatal("ParseContacts accepted a chat-settings envelope")
	}
}

func TestParseContacts_CarriesEveryFieldAndItsStamp(t *testing.T) {
	want := syncrail.ContactSnapshot{
		Fingerprint: "fp-alice",
		Nick:        "alice",
		NickAt:      900,
		Alias:       "Boss",
		AliasAt:     1000,
		Verified:    true,
		VerifiedAt:  1100,
		Blocked:     true,
		BlockedAt:   1200,

		PairedDevices:     []uint32{17, 4000000000},
		PairedDevicesAtMs: 1300000,
	}
	body, err := syncrail.ParseContacts(contactsEnvelope(t, syncrail.ContactsBody{
		Contacts: []syncrail.ContactSnapshot{want},
	}))
	if err != nil {
		t.Fatalf("ParseContacts: %v", err)
	}
	if !reflect.DeepEqual(body.Contacts[0], want) {
		t.Errorf("got %+v, want %+v", body.Contacts[0], want)
	}
}

func TestParseContacts_CarriesTheConversationsWhole(t *testing.T) {
	want := syncrail.ChatIdentity{ChatID: "chat-1", Fingerprint: "fp-alice"}
	body, err := syncrail.ParseContacts(contactsEnvelope(t, syncrail.ContactsBody{
		Contacts: []syncrail.ContactSnapshot{{Fingerprint: "fp-alice", Nick: "alice"}},
		Chats:    []syncrail.ChatIdentity{want},
	}))
	if err != nil {
		t.Fatalf("ParseContacts: %v", err)
	}
	if len(body.Chats) != 1 {
		t.Fatalf("got %d conversations, want 1 — the receiving device cannot draw a person with none", len(body.Chats))
	}
	if body.Chats[0] != want {
		t.Errorf("got %+v, want %+v", body.Chats[0], want)
	}
}

func TestParseContacts_AnOldShapeBodyStillApplies(t *testing.T) {

	raw := []byte(`{"contacts":[{"fingerprint":"fp-alice","nick":"alice","nick_at":900}]}`)
	env := syncrail.Envelope{V: syncrail.Version, Kind: syncrail.KindContacts, Ts: 1, Body: raw}
	body, err := syncrail.ParseContacts(env)
	if err != nil {
		t.Fatalf("ParseContacts refused a body with no conversations: %v", err)
	}
	if len(body.Contacts) != 1 || body.Contacts[0].Nick != "alice" {
		t.Fatalf("got %+v, want the one contact", body.Contacts)
	}
	if len(body.Chats) != 0 {
		t.Errorf("got %d conversations from a body that carries none", len(body.Chats))
	}
}

func TestParseContacts_DropsAConversationThatNamesNothing(t *testing.T) {
	body, err := syncrail.ParseContacts(contactsEnvelope(t, syncrail.ContactsBody{
		Contacts: []syncrail.ContactSnapshot{{Fingerprint: "fp-alice"}},
		Chats: []syncrail.ChatIdentity{
			{ChatID: "chat-nobody", Fingerprint: ""},
			{ChatID: "", Fingerprint: "fp-bob"},
			{ChatID: "chat-1", Fingerprint: "fp-alice"},
		},
	}))
	if err != nil {
		t.Fatalf("ParseContacts: %v", err)
	}
	if len(body.Contacts) != 1 {
		t.Errorf("got %d contacts, want 1 — a dropped conversation must not cost a person", len(body.Contacts))
	}
	if len(body.Chats) != 1 || body.Chats[0].ChatID != "chat-1" {
		t.Errorf("got %+v, want only chat-1", body.Chats)
	}
}
