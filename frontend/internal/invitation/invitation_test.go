package invitation_test

import (
	"errors"
	"strings"
	"testing"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/invitation"
	"haoma-frontend/internal/logid"
	"haoma-frontend/internal/store"
)

func init() {
	store.DefaultKDFParams = store.KDFParams{
		Time: 1, Memory: 8 * 1024, Threads: 2, KeyLen: 32, SaltLen: 16,
	}
}

const (
	testChatID = "aabbccddeeff00112233445566778899"
	testSecret = "correct-horse-battery-staple-secret"

	testFP = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	testDevice = "fedcba9876543210fedcba9876543210"
	testMinted = 1234567800
)

func newStore(t *testing.T) (*invitation.Store, *store.Store) {
	t.Helper()
	st, err := store.Unlock(t.TempDir(), "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Lock() })
	return invitation.New(st), st
}

func record(msgID string) invitation.Record {
	return invitation.Record{
		MsgID:          msgID,
		ChatID:         testChatID,
		Fingerprint:    testFP,
		OfferingDevice: testDevice,
		Secret:         testSecret,
		MintedAt:       testMinted,
		ExpiresAt:      1234567890,
		State:          events.InvitationLive,
	}
}

func TestPutGetRoundTrip(t *testing.T) {
	s, _ := newStore(t)
	want := record("inv-1")
	if err := s.Put(want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := s.Get("inv-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != want {

		t.Fatalf("round trip changed the record: %v", got.LogValue())
	}
}

func TestGetUnknownIsNotFound(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Get("nobody"); !errors.Is(err, invitation.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestPutRefusesAnUnusableRecord(t *testing.T) {
	cases := map[string]func(*invitation.Record){
		"no secret":          func(r *invitation.Record) { r.Secret = "" },
		"no fingerprint":     func(r *invitation.Record) { r.Fingerprint = "" },
		"no offering device": func(r *invitation.Record) { r.OfferingDevice = "" },
		"no mint time":       func(r *invitation.Record) { r.MintedAt = 0 },
	}
	for name, break_ := range cases {
		t.Run(name, func(t *testing.T) {
			s, _ := newStore(t)
			rec := record("inv-1")
			break_(&rec)
			if err := s.Put(rec); err == nil {
				t.Fatal("an unusable record was accepted")
			}
		})
	}
}

func TestListByChatAndSetState(t *testing.T) {
	s, _ := newStore(t)
	for _, id := range []string{"inv-1", "inv-2"} {
		if err := s.Put(record(id)); err != nil {
			t.Fatalf("Put %s: %v", id, err)
		}
	}

	other := record("inv-3")
	other.ChatID = "ffffffffffffffffffffffffffffffff"
	if err := s.Put(other); err != nil {
		t.Fatalf("Put other: %v", err)
	}

	held, err := s.ListByChat(chat.ChatID(testChatID))
	if err != nil {
		t.Fatalf("ListByChat: %v", err)
	}
	if len(held) != 2 {
		t.Fatalf("want 2 invitations for the conversation, got %d", len(held))
	}

	if err := s.SetState("inv-1", events.InvitationSuperseded); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	got, err := s.Get("inv-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != events.InvitationSuperseded {
		t.Fatalf("state did not stick: %q", got.State)
	}

	if got.Secret != "" {
		t.Fatal("a retired invitation kept a secret nobody will ever dial")
	}
	if got.MsgID != "inv-1" || got.Fingerprint != testFP {
		t.Fatalf("the tombstone lost what identifies it: %v", got.LogValue())
	}
}

func TestSetStateRefusesToOverwriteAnOutcome(t *testing.T) {
	s, _ := newStore(t)
	if err := s.Put(record("inv-1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.SetState("inv-1", events.InvitationAccepted); err != nil {
		t.Fatalf("SetState accepted: %v", err)
	}
	if err := s.SetState("inv-1", events.InvitationSuperseded); !errors.Is(err, invitation.ErrSettled) {
		t.Fatalf("stamping over an outcome returned %v, want ErrSettled", err)
	}
	got, err := s.Get("inv-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != events.InvitationAccepted {
		t.Fatalf("the outcome was rewritten to %q", got.State)
	}
}

func TestPutRefusesASecretOnARecordThatIsNoLongerLive(t *testing.T) {
	s, _ := newStore(t)
	rec := record("inv-1")
	rec.State = events.InvitationAccepted
	if err := s.Put(rec); err == nil {
		t.Fatal("a settled record was stored still holding its secret")
	}
	rec.Secret = ""
	if err := s.Put(rec); err != nil {
		t.Fatalf("a settled record without a secret was refused: %v", err)
	}
}

func TestDeleteDropsTheRecordAndTheIndex(t *testing.T) {
	s, _ := newStore(t)
	if err := s.Put(record("inv-1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Delete("inv-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get("inv-1"); !errors.Is(err, invitation.ErrNotFound) {
		t.Fatalf("record survived: %v", err)
	}
	held, err := s.ListByChat(chat.ChatID(testChatID))
	if err != nil {
		t.Fatalf("ListByChat: %v", err)
	}
	if len(held) != 0 {
		t.Fatalf("the index still names %d invitation(s)", len(held))
	}

	if err := s.Delete("inv-1"); err != nil {
		t.Fatalf("second Delete: %v", err)
	}
}

func TestNoteHoldsNoSecret(t *testing.T) {
	s, _ := newStore(t)
	n := invitation.Note{MsgID: "inv-1", HandleID: "handle-1", Fingerprint: testFP, MintedAt: 90, ExpiresAt: 99}
	if err := s.PutNote(n); err != nil {
		t.Fatalf("PutNote: %v", err)
	}
	got, err := s.GetNote(testFP, 0)
	if err != nil {
		t.Fatalf("GetNote: %v", err)
	}
	if got != n {
		t.Fatalf("round trip changed the note: %+v", got)
	}
	if _, err := s.GetNote("someone-else", 0); !errors.Is(err, invitation.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestRecordNeverLogsTheSecret(t *testing.T) {
	rendered := record("inv-1").LogValue().String()
	if strings.Contains(rendered, testSecret) {
		t.Fatalf("the log value carries the secret: %s", rendered)
	}
	for i := 4; i <= len(testSecret); i++ {
		if strings.Contains(rendered, testSecret[:i]) {
			t.Fatalf("the log value carries a %d-character prefix of the secret: %s", i, rendered)
		}
	}
	for _, want := range []string{"inv-1", logid.Short(testChatID), logid.Short(testFP), logid.Short(testDevice)} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("the log value dropped %q, which a log line needs: %s", want, rendered)
		}
	}
}

func TestLogValuesCarryNoFullLengthIDs(t *testing.T) {
	full := []string{testChatID, testFP, testDevice}
	cases := map[string]string{
		"record": record("inv-1").LogValue().String(),
		"note": invitation.Note{
			MsgID:       "inv-1",
			HandleID:    "0123456789abcdef0123456789abcdef",
			Fingerprint: testFP,
			MintedAt:    90,
			ExpiresAt:   99,
		}.LogValue().String(),
	}
	for name, rendered := range cases {
		t.Run(name, func(t *testing.T) {
			for _, id := range full {
				if strings.Contains(rendered, id) {
					t.Fatalf("the log value carries a full-length id: %s", rendered)
				}
			}
			if strings.Contains(rendered, "0123456789abcdef0123456789abcdef") {
				t.Fatalf("the log value carries a full-length handle: %s", rendered)
			}
		})
	}
}

func TestSweptRowTakesTheSecretWithIt(t *testing.T) {
	s, st := newStore(t)
	log := events.New(st, nil, nil)
	if err := s.Put(record("inv-1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := log.AppendInbound(events.InboundParams{
		ChatID: chat.ChatID(testChatID),
		Kind:   events.KindInvitation,
		MsgID:  "inv-1",
		Status: events.DecryptOK,
	}); err != nil {
		t.Fatalf("AppendInbound: %v", err)
	}
	if _, _, err := log.DeleteByChat(chat.ChatID(testChatID)); err != nil {
		t.Fatalf("DeleteByChat: %v", err)
	}
	if _, err := s.Get("inv-1"); !errors.Is(err, invitation.ErrNotFound) {
		t.Fatalf("the secret outlived the conversation it belonged to: %v", err)
	}
	held, err := s.ListByChat(chat.ChatID(testChatID))
	if err != nil {
		t.Fatalf("ListByChat: %v", err)
	}
	if len(held) != 0 {
		t.Fatalf("the index outlived the conversation: %d row(s)", len(held))
	}
}

func TestListSpansEveryConversation(t *testing.T) {
	s, _ := newStore(t)

	second := record("inv-2")
	second.ChatID = "99887766554433221100ffeeddccbbaa"
	for _, rec := range []invitation.Record{record("inv-1"), second, record("inv-3")} {
		if err := s.Put(rec); err != nil {
			t.Fatalf("Put %s: %v", rec.MsgID, err)
		}
	}

	got, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("List returned %d records, want 3", len(got))
	}
	seen := map[string]string{}
	for _, rec := range got {
		seen[rec.MsgID] = rec.ChatID

		if rec.Secret != testSecret {
			t.Errorf("%s: secret = %q, want the stored one", rec.MsgID, rec.Secret)
		}
	}
	if seen["inv-2"] != second.ChatID {
		t.Errorf("inv-2 came back under chat %q, want %q", seen["inv-2"], second.ChatID)
	}
	if len(seen) != 3 {
		t.Errorf("List returned duplicates: %v", seen)
	}
}

func TestListOnAnEmptyStore(t *testing.T) {
	s, _ := newStore(t)
	got, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("List returned %d records, want none", len(got))
	}
}

func TestListIgnoresMyOwnAttemptNotes(t *testing.T) {
	s, _ := newStore(t)
	if err := s.PutNote(invitation.Note{
		MsgID:       "inv-1",
		Fingerprint: testFP,
		HandleID:    "handle-1",
		MintedAt:    1234567800,
		ExpiresAt:   1234567890,
	}); err != nil {
		t.Fatalf("PutNote: %v", err)
	}
	got, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("List returned %d records, want none — a note is not an invitation offered to me", len(got))
	}
}

func TestListNotesReturnsOnlyMyOwnAttempts(t *testing.T) {
	s, _ := newStore(t)
	if err := s.Put(record("inv-offered-to-me")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	mine := invitation.Note{
		MsgID:       "inv-i-offered",
		Fingerprint: testFP,
		HandleID:    "handle-1",
		MintedAt:    1234567800,
		ExpiresAt:   1234567890,
	}
	if err := s.PutNote(mine); err != nil {
		t.Fatalf("PutNote: %v", err)
	}
	notes, err := s.ListNotes()
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if len(notes) != 1 {
		t.Fatalf("ListNotes returned %d notes, want 1", len(notes))
	}
	if notes[0] != mine {
		t.Fatalf("the note came back changed: %+v", notes[0])
	}

	if err := s.DeleteNote(testFP, 0); err != nil {
		t.Fatalf("DeleteNote: %v", err)
	}
	notes, err = s.ListNotes()
	if err != nil {
		t.Fatalf("ListNotes after delete: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("ListNotes returned %d notes after the attempt concluded, want none", len(notes))
	}
}

func TestPutNoteRefusesANoteWithNoRow(t *testing.T) {
	s, _ := newStore(t)
	err := s.PutNote(invitation.Note{
		Fingerprint: testFP,
		HandleID:    "handle-1",
		MintedAt:    1234567800,
		ExpiresAt:   1234567890,
	})
	if err == nil {
		t.Fatal("a note with no row id was accepted")
	}
}
