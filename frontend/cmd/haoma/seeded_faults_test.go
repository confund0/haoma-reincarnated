//go:build faults

package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/files"
	"haoma-frontend/internal/syncrail"
)

func TestSaltFault_DropsTheFirstSaltOnly(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	ctx := context.Background()

	resetSaltDropFault := func() {
		saltDropFault.once = sync.Once{}
		saltDropFault.f = nil
	}
	t.Setenv(dropFirstDestroySaltEnv, "1")
	resetSaltDropFault()
	t.Cleanup(func() {
		resetSaltDropFault()
		saltDropFault.once.Do(func() {})
	})
	if destroySaltDropFault() == nil {
		t.Fatalf("%s=1 did not turn the fault on", dropFirstDestroySaltEnv)
	}
	t.Setenv(dropFirstDestroySaltEnv, "")
	if destroySaltDropFault() == nil {
		t.Fatal("the setting was read again after the first call")
	}

	salt := []byte("fault-salt-abcde")
	applyDestroySalt(ctx, d, railSiblingID, syncrail.NewDestroySaltBody(salt, 1), salt)
	if _, held, _ := d.linkedDevices.SiblingSalt(railSiblingID); held {
		t.Fatal("the fault is on and the first salt was stored")
	}
	applyDestroySalt(ctx, d, railSiblingID, syncrail.NewDestroySaltBody(salt, 1), salt)
	if _, held, _ := d.linkedDevices.SiblingSalt(railSiblingID); !held {
		t.Fatal("the second salt was dropped too; the fault drops only the first")
	}

	t.Setenv(dropFirstDestroySaltEnv, "")
	if dropFirstDestroySaltFromEnv() != nil {
		t.Fatal("an empty setting turned the fault on")
	}
	t.Setenv(dropFirstDestroySaltEnv, "yes")
	if dropFirstDestroySaltFromEnv() != nil {
		t.Fatal("a value other than 1 turned the fault on")
	}
}

func TestNoEarlyReceipt_TheReceiptAfterTheDownloadStillBringsTheKey(t *testing.T) {
	p := newKeyFirstPair(t)
	p.receiver.skipEarlyReceipt = true
	plaintext := []byte("a contact's file with no early receipt\n")
	msgID := p.send(plaintext)

	p.toReceiver()
	if n := len(p.receiverSends()); n != 0 {
		t.Fatalf("receiver sent %d message(s) on arrival under the seeded fault, want 0", n)
	}
	p.downloadDone(msgID)
	if n := len(p.receiverSends()); n != 1 {
		t.Fatalf("receiver sent %d message(s) after the download, want 1: the receipt", n)
	}
	p.toSender()
	if p.keysSent() != 1 {
		t.Fatalf("keys shipped = %d, want 1", p.keysSent())
	}
	p.toReceiver()
	p.sealed(msgID, plaintext)
}

func TestIgnoreEarlyReceipt_TheKeyAnswersTheReceiptAfterTheDownload(t *testing.T) {
	p := newKeyFirstPair(t)
	p.sender.ignoreEarlyReceipt = true
	plaintext := []byte("the early receipt goes unanswered\n")
	msgID := p.send(plaintext)
	p.toReceiver()
	p.toSender()
	if p.keysSent() != 0 {
		t.Fatalf("keys shipped for the early receipt = %d, want 0 under the seeded fault", p.keysSent())
	}
	p.downloadDone(msgID)
	p.toSender()
	if p.keysSent() != 1 {
		t.Fatalf("keys shipped for the receipt after the download = %d, want 1", p.keysSent())
	}
	p.toReceiver()
	p.sealed(msgID, plaintext)
}

func TestSiblingOffer_SeededFaultLandsTheRowAndFetchesNothing(t *testing.T) {
	d, stub, chatID := newSiblingReceiver(t)
	d.ignoreSiblingOffer = true
	f := newSiblingFile(t)
	w := siblingOfferWrapper(t, f, "")

	processInboxEntry(context.Background(), d, railEntry(railSiblingID, "env-sib-fault", sealSiblingOffer(t, d, w, offerFor(f))))

	stub.mu.Lock()
	fetchCalls := stub.fetchCalls
	stub.mu.Unlock()
	if fetchCalls != 0 {
		t.Fatalf("fetch calls = %d, want 0 under the seeded fault", fetchCalls)
	}
	_, body := fileRow(t, d, chatID, w.MsgID)
	if body.State != string(files.StatePending) {
		t.Errorf("state = %q, want pending", body.State)
	}
	if _, err := d.files.GetSiblingOffer(w.MsgID); !errors.Is(err, files.ErrSiblingOfferNotFound) {
		t.Errorf("a key was held under the seeded fault (err=%v)", err)
	}
}

func fetchesFor(stub *filesStub, staging string) int {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	n := 0
	for _, id := range stub.haomadIDs {
		if id == staging {
			n++
		}
	}
	return n
}

func TestGapSiblingOffer_SeededHoldTakesTheOfferAndFetchesNothing(t *testing.T) {
	buf := captureRailLog(t)
	d, stub, _ := newAskingDevice(t)
	d.holdGapFillFetch = true
	f := newSiblingFile(t)
	id := newMsgID(t)
	landGapFile(t, d, id, true, f, 100)

	handleGapSiblingOffer(context.Background(), d, railSiblingID, gapOfferFor(t, id, f, stagingFor(1), "tok-gap-1"))
	waitForLog(t, buf, `msg="sibling offer on gap-fill: taken; the seeded fault holds its fetch"`,
		"msg_id="+id, "staging_id="+stagingFor(1), "env="+holdGapFillFetchEnv, "outcome=fetch_held")
	if fetches, _, _ := stubCounts(stub); fetches != 0 {
		t.Fatalf("fetches = %d under the seeded fault, want 0", fetches)
	}
	meta, err := d.files.GetMeta(id)
	if err != nil || !meta.GapFill || meta.StagingID != stagingFor(1) || meta.State != files.StatePending {
		t.Fatalf("meta %+v (%v); want a pending gap-fill take of staging 1", meta, err)
	}
	if held, err := d.files.GetSiblingOffer(id); err != nil || held.StagingID != stagingFor(1) {
		t.Fatalf("held offer %+v (%v); want the offer of staging 1", held, err)
	}
	if _, wanted := wantedOf(t, d)[id]; wanted {
		t.Fatal("the file is still wanted after its offer was taken")
	}

	endWalk(d)
	for _, ids := range fileRequestsSent(t, d, stub) {
		for _, got := range ids {
			if got == id {
				t.Fatalf("a held take was asked for again: %v", ids)
			}
		}
	}

	sweepSiblingFetches(context.Background(), d)
	if fetches, _, _ := stubCounts(stub); fetches != 0 {
		t.Fatalf("the sweep posted %d fetch(es) under the seeded fault, want 0", fetches)
	}
	waitForLog(t, buf, `msg="sibling fetch sweep: the seeded fault holds this fetch"`, "msg_id="+id)

	d.holdGapFillFetch = false
	sweepSiblingFetches(context.Background(), d)
	if n := fetchesFor(stub, stagingFor(1)); n != 1 {
		t.Fatalf("fetches of staging 1 after the fault = %d, want 1", n)
	}
	fileFetchStateHandler(context.Background(), d)(backendapi.FileFetchEvent{
		MsgID: stagingFor(1), State: string(files.StateFailedPermanent), Reason: backendapi.FetchFailBlobGone,
	})
	if _, err := d.files.GetMeta(id); !errors.Is(err, files.ErrMetaNotFound) {
		t.Fatalf("metadata kept after the blob was gone (err=%v)", err)
	}
	if _, wanted := wantedOf(t, d)[id]; !wanted {
		t.Fatal("the file is not wanted again after its blob was gone")
	}
	waitForLog(t, buf, `msg="sibling offer on gap-fill: download failed; the file is asked for again"`,
		"msg_id="+id, "staging_id="+stagingFor(1), "fail_reason="+backendapi.FetchFailBlobGone)
	endWalk(d)
	asked := false
	for _, ids := range fileRequestsSent(t, d, stub) {
		for _, got := range ids {
			asked = asked || got == id
		}
	}
	if !asked {
		t.Fatal("the next walk did not ask for the file again")
	}
}

func TestSiblingOffer_SeededHoldLeavesOtherRoutesAlone(t *testing.T) {
	d, stub, chatID := newAskingDevice(t)
	d.holdGapFillFetch = true
	f := newSiblingFile(t)

	w := siblingOfferWrapper(t, f, "")
	processInboxEntry(context.Background(), d, railEntry(railSiblingID, "env-sib-hold", sealSiblingOffer(t, d, w, offerFor(f))))
	if n := fetchesFor(stub, siblingTestStaging); n != 1 {
		t.Fatalf("fetches of the offer at send = %d under the fault, want 1", n)
	}
	sweepSiblingFetches(context.Background(), d)
	if n := fetchesFor(stub, siblingTestStaging); n != 2 {
		t.Fatalf("fetches of the offer at send after the sweep = %d, want 2", n)
	}

	other := newMsgID(t)
	cw, offer, entry := contactOffer(other)
	ingestFileOffer(context.Background(), d, chatID, entry, cw, offer)
	if n := fetchesFor(stub, other); n != 1 {
		t.Fatalf("fetches of the contact's file = %d under the fault, want 1", n)
	}
}

func TestHoldGapFillFetchFromEnv(t *testing.T) {
	for raw, want := range map[string]bool{"": false, "1": true, "yes": false, "0": false} {
		t.Setenv(holdGapFillFetchEnv, raw)
		if got := holdGapFillFetchFromEnv(); got != want {
			t.Errorf("%s=%q: got %t, want %t", holdGapFillFetchEnv, raw, got, want)
		}
	}
}

func TestFileRequest_SeededFaultServesNothing(t *testing.T) {
	d, stub, chatID := newServingDaemon(t)
	buf := captureRailLog(t)
	d.ignoreFileRequest = true
	id, _ := seedServableFile(t, d, chatID, servableSent)

	requestFiles(t, d, "env-req-1", id)
	waitForLog(t, buf, `msg="file request: ignored by the seeded fault; nothing is served"`, "env="+ignoreFileRequestEnv)
	time.Sleep(200 * time.Millisecond)
	if c := stub.calls(); len(c.stages) != 0 || len(c.sentTo) != 0 {
		t.Fatalf("stagings = %d, sends = %d; want none", len(c.stages), len(c.sentTo))
	}
	if strings.Contains(buf.String(), "file request: served") {
		t.Fatal("the seeded fault served a file")
	}
}

func TestIgnoreFileRequestFromEnv(t *testing.T) {
	for raw, want := range map[string]bool{"": false, "1": true, "yes": false, "0": false} {
		t.Setenv(ignoreFileRequestEnv, raw)
		if got := ignoreFileRequestFromEnv(); got != want {
			t.Errorf("%s=%q: got %t, want %t", ignoreFileRequestEnv, raw, got, want)
		}
	}
}
