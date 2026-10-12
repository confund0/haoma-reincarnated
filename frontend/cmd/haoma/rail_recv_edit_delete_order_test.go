package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"haoma-frontend/internal/events"
	"haoma-frontend/internal/msg"
)

func TestRail_MirroredDeleteThenEdit_KnownGap_BodyComesBack(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	seedKnownPeer(t, d, railPeerID, railFingerprint)

	ctx := context.Background()
	original := railText(t, "original", 4, time.Now().Unix(), 0)
	processInboxEntry(ctx, d, railEntry(railSiblingID, "env-rail-t", sealMirror(t, d, railFingerprint, original)))

	deleteID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	del, err := msg.BuildDelete(5, time.Now().Unix()+1, deleteID, original.MsgID, 0)
	if err != nil {
		t.Fatal(err)
	}
	processInboxEntry(ctx, d, railEntry(railSiblingID, "env-rail-d", sealMirror(t, d, railFingerprint, del)))

	editID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	edit, err := msg.BuildEdit(6, time.Now().Unix()+2, editID, original.MsgID, "resurrected", 0)
	if err != nil {
		t.Fatal(err)
	}
	processInboxEntry(ctx, d, railEntry(railSiblingID, "env-rail-e", sealMirror(t, d, railFingerprint, edit)))

	ev, err := d.events.GetByMsgID(original.MsgID)
	if err != nil {
		t.Fatal(err)
	}

	if ev.DeletedAt == 0 {
		t.Error("DeletedAt = 0, want left set from the mirrored delete")
	}
	if ev.EditedAt == 0 {
		t.Error("EditedAt = 0, want stamped by the mirrored edit (current gap behavior)")
	}
	var body events.TextBody
	if err := json.Unmarshal(ev.Body, &body); err != nil {
		t.Fatalf("Body not a TextBody after mirrored delete-then-edit: %v", err)
	}
	if body.Text != "resurrected" {
		t.Errorf("body.text = %q, want %q (current gap behavior)", body.Text, "resurrected")
	}
}

func TestRail_MirroredEditThenDelete_DeleteWins(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	seedKnownPeer(t, d, railPeerID, railFingerprint)

	ctx := context.Background()
	original := railText(t, "original", 4, time.Now().Unix(), 0)
	processInboxEntry(ctx, d, railEntry(railSiblingID, "env-rail-t", sealMirror(t, d, railFingerprint, original)))

	editID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	edit, err := msg.BuildEdit(5, time.Now().Unix()+1, editID, original.MsgID, "corrected", 0)
	if err != nil {
		t.Fatal(err)
	}
	processInboxEntry(ctx, d, railEntry(railSiblingID, "env-rail-e", sealMirror(t, d, railFingerprint, edit)))

	deleteID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	del, err := msg.BuildDelete(6, time.Now().Unix()+2, deleteID, original.MsgID, 0)
	if err != nil {
		t.Fatal(err)
	}
	processInboxEntry(ctx, d, railEntry(railSiblingID, "env-rail-d", sealMirror(t, d, railFingerprint, del)))

	ev, err := d.events.GetByMsgID(original.MsgID)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Body != nil {
		t.Errorf("Body = %q, want nil", string(ev.Body))
	}
	if ev.DeletedAt == 0 {
		t.Error("DeletedAt = 0, want set by the mirrored delete")
	}
}
