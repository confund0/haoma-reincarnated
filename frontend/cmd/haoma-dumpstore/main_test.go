package main

import (
	"strings"
	"testing"
)

func TestInvitationRecordRendersWithoutItsSecret(t *testing.T) {
	const secret = "correct-horse-battery-staple-secret"
	raw := []byte(`{"msg_id":"inv-1","chat_id":"aabb","fingerprint":"ccdd","secret":"` +
		secret + `","minted_at":1742640000,"expires_at":1742650000,"state":"live"}`)

	group, key := classify([]byte("invitation:inv-1"))
	if group != "invitation" {
		t.Fatalf("classify put the record in group %q, want its own group", group)
	}
	if key != "invitation:inv-1" {
		t.Fatalf("rendered key = %q", key)
	}

	out := renderValue(group, raw)
	for i := 4; i <= len(secret); i++ {
		if strings.Contains(out, secret[:i]) {
			t.Fatalf("the dump carries a %d-character prefix of the secret:\n%s", i, out)
		}
	}
	for _, want := range []string{"inv-1", "aabb", "ccdd", "1742650000", "live"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the dump dropped %q, which the record is read for:\n%s", want, out)
		}
	}
}

func TestInvitationNoteAndIndexKeepTheirOwnGroups(t *testing.T) {
	if group, _ := classify([]byte("invitation-note:ccdd")); group != "invitation-note" {
		t.Fatalf("note group = %q", group)
	}
	if group, _ := classify([]byte("invitation-by-chat:aabb:inv-1")); group != "invitation-by-chat" {
		t.Fatalf("index group = %q", group)
	}
	out := renderValue("invitation-note", []byte(`{"handle_id":"h-1","fingerprint":"ccdd","expires_at":1742650000}`))
	if !strings.Contains(out, "h-1") {
		t.Fatalf("the note did not render: %s", out)
	}
}
