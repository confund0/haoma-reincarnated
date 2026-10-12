package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"haoma-frontend/internal/events"
	"haoma-frontend/internal/syncrail"
)

func TestGapFill_IncomingDeleteBeatsNewerLocalEdit_EndToEnd(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)
	msgID := seedText(t, d, chatID, "original")
	if _, err := d.events.ApplyEdit(msgID, "newer local edit", 1742800000, ""); err != nil {
		t.Fatal(err)
	}

	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{
		RangeEnd: 9,
		Rows: []syncrail.RowSnapshot{{
			Fingerprint: railFingerprint,
			MsgID:       msgID,
			Kind:        string(events.KindText),
			DeletedAt:   1742700000,
		}},
	})

	got, err := d.events.GetByMsgID(msgID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DeletedAt == 0 {
		t.Fatal("row not tombstoned; a newer local edit must not survive an incoming delete")
	}
	if len(got.Body) != 0 {
		t.Fatal("tombstoned row still carries a body")
	}
}

func TestGapFill_LocalDeleteNotResurrectedByIncomingEdit_EndToEnd(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)
	msgID := seedText(t, d, chatID, "original")
	if _, err := d.events.ApplyDelete(msgID, 1742700000, ""); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(events.TextBody{Text: "back from the dead"})
	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{
		RangeEnd: 9,
		Rows: []syncrail.RowSnapshot{{
			Fingerprint: railFingerprint,
			MsgID:       msgID,
			Kind:        string(events.KindText),
			Body:        body,
			EditedAt:    1742800000,
		}},
	})

	got, err := d.events.GetByMsgID(msgID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DeletedAt == 0 {
		t.Fatal("local delete was undone by an incoming edit")
	}
	if len(got.Body) != 0 {
		t.Fatal("deleted row picked up the incoming edit's body")
	}
}
