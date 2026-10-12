package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
)

func TestDispatch_SendEdit_OnOwnDeletedMessage_KnownGap_SendsAnEdit(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000000a"
	preEstablishSession(t, d, peerID)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendText, "s1", ipc.SendTextRequest{
		PeerID: peerID, Text: "oops",
	}))
	sent := readUntil(t, ctx, conn, ipc.FrameTextSent)
	var sp ipc.SendTextResponse
	_ = json.Unmarshal(sent.Payload, &sp)

	if _, err := d.events.ApplyDelete(sp.MsgID, time.Now().Unix(), ""); err != nil {
		t.Fatalf("ApplyDelete: %v", err)
	}

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendEdit, "e1", ipc.SendEditRequest{
		PeerID: peerID, TargetMsgID: sp.MsgID, Text: "fixed",
	}))
	editResp := readUntil(t, ctx, conn, ipc.FrameEditSent)
	var ep ipc.SendEditResponse
	if err := json.Unmarshal(editResp.Payload, &ep); err != nil {
		t.Fatal(err)
	}

	if stub.SendCalls != 2 {
		t.Errorf("haomad saw %d /send calls, want 2 (text + edit shipped despite the local delete — current gap behavior)", stub.SendCalls)
	}

	ev, err := d.events.GetByMsgID(sp.MsgID)
	if err != nil {
		t.Fatal(err)
	}
	if ev.DeletedAt == 0 {
		t.Error("DeletedAt = 0, want left set from the earlier ApplyDelete")
	}
	var tb events.TextBody
	if err := json.Unmarshal(ev.Body, &tb); err != nil {
		t.Fatalf("Body not a TextBody after editing a deleted row: %v", err)
	}
	if tb.Text != "fixed" {
		t.Errorf("Body text = %q, want %q (current gap behavior)", tb.Text, "fixed")
	}
}
