package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/session"
)

func inboundEditDeleteFixture(t *testing.T, aliceID string) (*daemon, string, *session.Cipher) {
	t.Helper()
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)

	targetMsgID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	textWrapper, err := msg.BuildText(1, 1742643890, targetMsgID, "original", 0, "", "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	blob, aliceCipher := encryptAsAliceKeepingCipher(t, d, aliceID, textWrapper)
	processInboxEntry(context.Background(), d, backendapi.InboxEntry{
		ArrivalAt: time.Now().UnixNano(),
		PeerID:    aliceID,
		Envelope: backendapi.RawEnvelope{
			ID: "env-seed", Timestamp: 1742643890, From: "alice-onion",
			Kind: "text", Payload: blob, Mac: []byte{1, 2, 3, 4},
		},
	})

	rows, err := d.events.List(chatIDForPeer(t, d, aliceID), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("seed: persisted %d rows, want 1", len(rows))
	}
	return d, targetMsgID, aliceCipher
}

func deliverInboundReusingCipher(t *testing.T, d *daemon, aliceID string, aliceCipher *session.Cipher, w *msg.Wrapper, envID string) {
	t.Helper()
	plain, err := msg.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := aliceCipher.Encrypt(context.Background(), "bob-self", plain)
	if err != nil {
		t.Fatalf("alice encrypt: %v", err)
	}
	processInboxEntry(context.Background(), d, backendapi.InboxEntry{
		ArrivalAt: time.Now().UnixNano(),
		PeerID:    aliceID,
		Envelope: backendapi.RawEnvelope{
			ID: envID, Timestamp: w.Ts, From: "alice-onion",
			Kind: string(w.Kind), Payload: blob, Mac: []byte{1, 2, 3, 4},
		},
	})
}

func TestProcessInboxEntry_DeleteThenEdit_KnownGap_BodyComesBack(t *testing.T) {
	const aliceID = "0000000000000000000000000000aaaa"
	d, targetMsgID, aliceCipher := inboundEditDeleteFixture(t, aliceID)

	deleteMsgID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	delWrapper, err := msg.BuildDelete(2, 1742643900, deleteMsgID, targetMsgID, 0)
	if err != nil {
		t.Fatal(err)
	}
	deliverInboundReusingCipher(t, d, aliceID, aliceCipher, delWrapper, "env-del")

	editMsgID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	editWrapper, err := msg.BuildEdit(3, 1742643910, editMsgID, targetMsgID, "resurrected", 0)
	if err != nil {
		t.Fatal(err)
	}
	deliverInboundReusingCipher(t, d, aliceID, aliceCipher, editWrapper, "env-edit")

	got, err := d.events.GetByMsgID(targetMsgID)
	if err != nil {
		t.Fatal(err)
	}

	if got.DeletedAt != 1742643900 {
		t.Errorf("DeletedAt = %d, want 1742643900 (left untouched by the edit)", got.DeletedAt)
	}
	if got.EditedAt != 1742643910 {
		t.Errorf("EditedAt = %d, want 1742643910 (current gap behavior)", got.EditedAt)
	}
	var tb events.TextBody
	if err := json.Unmarshal(got.Body, &tb); err != nil {
		t.Fatalf("Body not a TextBody after delete-then-edit: %v", err)
	}
	if tb.Text != "resurrected" {
		t.Errorf("Body text = %q, want %q (current gap behavior)", tb.Text, "resurrected")
	}
}

func TestProcessInboxEntry_EditThenDelete_DeleteWins(t *testing.T) {
	const aliceID = "0000000000000000000000000000bbbb"
	d, targetMsgID, aliceCipher := inboundEditDeleteFixture(t, aliceID)

	editMsgID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	editWrapper, err := msg.BuildEdit(2, 1742643900, editMsgID, targetMsgID, "corrected", 0)
	if err != nil {
		t.Fatal(err)
	}
	deliverInboundReusingCipher(t, d, aliceID, aliceCipher, editWrapper, "env-edit")

	deleteMsgID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	delWrapper, err := msg.BuildDelete(3, 1742643910, deleteMsgID, targetMsgID, 0)
	if err != nil {
		t.Fatal(err)
	}
	deliverInboundReusingCipher(t, d, aliceID, aliceCipher, delWrapper, "env-del")

	got, err := d.events.GetByMsgID(targetMsgID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Body != nil {
		t.Errorf("Body = %q, want nil", string(got.Body))
	}
	if got.DeletedAt != 1742643910 {
		t.Errorf("DeletedAt = %d, want 1742643910", got.DeletedAt)
	}
}
