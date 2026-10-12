package files_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/files"
)

func wantedIDs(t *testing.T, mgr *files.Manager) map[string]files.WantedFile {
	t.Helper()
	all, err := mgr.ListWantedFiles()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]files.WantedFile{}
	for _, w := range all {
		out[w.MsgID] = w
	}
	return out
}

func wantFile(t *testing.T, mgr *files.Manager, msgID string, chatID chat.ChatID) {
	t.Helper()
	added, err := mgr.AddWantedFile(files.WantedFile{MsgID: msgID, ChatID: chatID, DisplayTs: 100})
	if err != nil {
		t.Fatal(err)
	}
	if !added {
		t.Fatalf("%s not added to the wanted set", msgID)
	}
}

func TestAddWantedFile_OnlyWithoutMetadataAndOnlyOnce(t *testing.T) {
	mgr, _, _ := newManager(t)
	if err := mgr.PutMeta(files.Metadata{MsgID: testMsgA, ChatID: testChatID, Direction: files.DirIn, State: files.StatePending}); err != nil {
		t.Fatal(err)
	}
	added, err := mgr.AddWantedFile(files.WantedFile{MsgID: testMsgA, ChatID: testChatID})
	if err != nil || added {
		t.Fatalf("AddWantedFile on a file with metadata = (%t, %v), want (false, nil)", added, err)
	}

	wantFile(t, mgr, testMsgB, testChatID)
	if err := mgr.MarkWantedAsked([]string{testMsgB}, 777); err != nil {
		t.Fatal(err)
	}
	added, err = mgr.AddWantedFile(files.WantedFile{MsgID: testMsgB, ChatID: testChatID})
	if err != nil || added {
		t.Fatalf("second AddWantedFile = (%t, %v), want (false, nil)", added, err)
	}
	got := wantedIDs(t, mgr)
	if len(got) != 1 || got[testMsgB].AskedAt != 777 {
		t.Fatalf("wanted = %+v, want only %s, its turn kept", got, testMsgB)
	}
}

func TestWantedFile_ClearedByMetadataAndByDelete(t *testing.T) {
	mgr, _, _ := newManager(t)
	third := "0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c"
	for _, id := range []string{testMsgA, testMsgB, third} {
		wantFile(t, mgr, id, testChatID)
	}

	if err := mgr.PutMetaIfAbsent(files.Metadata{MsgID: testMsgA, ChatID: testChatID, Direction: files.DirIn, Token: "t", State: files.StateDownloading}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.PutMetaAndSiblingOffer(files.Metadata{
		MsgID: testMsgB, ChatID: testChatID, Direction: files.DirOut, State: files.StatePending,
		FetchedFromSibling: true, StagingID: "5a1b2c3d4e5f60718293a4b5c6d7e8f9",
	}, heldOffer(testMsgB)); err != nil {
		t.Fatal(err)
	}
	if err := mgr.DeleteMeta(third); err != nil {
		t.Fatal(err)
	}
	if got := wantedIDs(t, mgr); len(got) != 0 {
		t.Fatalf("wanted after metadata writes and a delete = %+v, want none", got)
	}
}

func TestWantedFile_ClearedWithItsChat(t *testing.T) {
	mgr, _, _ := newManager(t)
	other := chat.ChatID("ffeeddccbbaa99887766554433221100")
	wantFile(t, mgr, testMsgA, testChatID)
	wantFile(t, mgr, testMsgB, other)
	if _, err := mgr.DeleteMetaByChat(testChatID); err != nil {
		t.Fatal(err)
	}
	got := wantedIDs(t, mgr)
	if len(got) != 1 || got[testMsgB].MsgID == "" {
		t.Fatalf("wanted after the chat went = %+v, want only the other chat's file", got)
	}
}

func TestJanitor_TombstoneDropsAWantedFile(t *testing.T) {
	mgr, st, _ := newManager(t)
	bus := events.NewBus()
	log := events.New(st, bus, nil)
	jan := files.NewJanitor(mgr, bus, nil, nil)
	body, _ := json.Marshal(map[string]string{"name": "blob.bin", "state": "pending"})
	if _, err := log.AppendOutbound(events.OutboundParams{
		ChatID: testChatID, Kind: events.KindFile, SenderSeq: 1, MsgID: testMsgA, Body: body,
	}); err != nil {
		t.Fatal(err)
	}
	wantFile(t, mgr, testMsgA, testChatID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go jan.Run(ctx)
	waitForSubscribers(t, bus, 1, 1)
	if _, err := log.ApplyDelete(testMsgA, time.Now().Unix(), ""); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(wantedIDs(t, mgr)) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the wanted entry outlived its row: %+v", wantedIDs(t, mgr))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func heldOffer(msgID string) files.SiblingOffer {
	return files.SiblingOffer{
		MsgID: msgID, ServingPeerID: "0000000000000000000000000000d0d0", StagingID: "5a1b2c3d4e5f60718293a4b5c6d7e8f9",
		Token: "tok-for-me", UrlPath: "/files/tok-for-me",
		Key: bytes.Repeat([]byte{0x3c}, 32), Nonce: bytes.Repeat([]byte{0x7e}, 24),
		BlobSize: 5000, PlaintextSize: 4000, Sha256Ciphertext: strings.Repeat("b", 64),
	}
}

func gapMeta(msgID string) files.Metadata {
	return files.Metadata{
		MsgID: msgID, ChatID: testChatID, Direction: files.DirIn, State: files.StatePending,
		FetchedFromSibling: true, GapFill: true, StagingID: "5a1b2c3d4e5f60718293a4b5c6d7e8f9",
	}
}

func TestTakeGapSiblingOffer_CountsTheAsks(t *testing.T) {
	mgr, _, _ := newManager(t)
	if _, err := mgr.AddWantedFile(files.WantedFile{MsgID: testMsgA, ChatID: testChatID, Asks: 4}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.TakeGapSiblingOffer(gapMeta(testMsgA), heldOffer(testMsgA)); err != nil {
		t.Fatal(err)
	}
	m, err := mgr.GetMeta(testMsgA)
	if err != nil {
		t.Fatal(err)
	}
	if m.GapAsks != 5 {
		t.Errorf("GapAsks = %d, want 5: four before and this one", m.GapAsks)
	}
	if len(wantedIDs(t, mgr)) != 0 {
		t.Error("the file is still wanted after its offer was taken")
	}
	if err := mgr.TakeGapSiblingOffer(gapMeta(testMsgA), heldOffer(testMsgA)); !errors.Is(err, files.ErrMetaHeld) {
		t.Fatalf("second take err = %v, want ErrMetaHeld", err)
	}

	if err := mgr.TakeGapSiblingOffer(gapMeta(testMsgB), func() files.SiblingOffer {
		o := heldOffer(testMsgB)
		o.StagingID = "6b2c3d4e5f60718293a4b5c6d7e8f90a"
		return o
	}()); err != nil {
		t.Fatal(err)
	}
	if m, _ := mgr.GetMeta(testMsgB); m.GapAsks != 1 {
		t.Errorf("GapAsks of a file never wanted = %d, want 1", m.GapAsks)
	}
}

func TestRequeueGapFile(t *testing.T) {
	mgr, _, _ := newManager(t)
	if _, err := mgr.AddWantedFile(files.WantedFile{MsgID: testMsgA, ChatID: testChatID, Asks: 2}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.TakeGapSiblingOffer(gapMeta(testMsgA), heldOffer(testMsgA)); err != nil {
		t.Fatal(err)
	}
	dropped, err := mgr.RequeueGapFile(testMsgA, 555, func(m files.Metadata) int { return m.GapAsks })
	if err != nil {
		t.Fatal(err)
	}
	if dropped.ChatID != testChatID {
		t.Errorf("dropped row chat = %q", dropped.ChatID)
	}
	if _, err := mgr.GetMeta(testMsgA); !errors.Is(err, files.ErrMetaNotFound) {
		t.Errorf("metadata kept: %v", err)
	}
	if _, err := mgr.GetSiblingOffer(testMsgA); !errors.Is(err, files.ErrSiblingOfferNotFound) {
		t.Errorf("held key kept: %v", err)
	}
	if _, err := mgr.SiblingOfferMsgID("5a1b2c3d4e5f60718293a4b5c6d7e8f9"); !errors.Is(err, files.ErrSiblingOfferNotFound) {
		t.Errorf("staging entry kept: %v", err)
	}
	got := wantedIDs(t, mgr)[testMsgA]
	if got.Asks != 3 || got.DisplayTs != 555 || got.ChatID != testChatID {
		t.Errorf("wanted entry = %+v, want asks 3, display_ts 555, the chat", got)
	}

	if err := mgr.PutMetaAndSiblingOffer(files.Metadata{
		MsgID: testMsgB, ChatID: testChatID, Direction: files.DirOut, State: files.StateFailedPermanent,
		FetchedFromSibling: true, StagingID: "6b2c3d4e5f60718293a4b5c6d7e8f90a",
	}, func() files.SiblingOffer {
		o := heldOffer(testMsgB)
		o.StagingID = "6b2c3d4e5f60718293a4b5c6d7e8f90a"
		return o
	}()); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.RequeueGapFile(testMsgB, 1, func(files.Metadata) int { return 0 }); !errors.Is(err, files.ErrNotGapFill) {
		t.Fatalf("requeue of a download at send err = %v, want ErrNotGapFill", err)
	}
	if _, err := mgr.GetMeta(testMsgB); err != nil {
		t.Errorf("a download at send lost its metadata: %v", err)
	}
}

func TestResetWantedAsksAndFilledMarker(t *testing.T) {
	mgr, _, _ := newManager(t)
	if _, err := mgr.AddWantedFile(files.WantedFile{MsgID: testMsgA, ChatID: testChatID, Asks: 9, AskedAt: 50}); err != nil {
		t.Fatal(err)
	}
	n, err := mgr.ResetWantedAsks()
	if err != nil || n != 1 {
		t.Fatalf("ResetWantedAsks = (%d, %v)", n, err)
	}
	if w := wantedIDs(t, mgr)[testMsgA]; w.Asks != 0 || w.AskedAt != 0 {
		t.Errorf("after reset = %+v, want asks and asked_at zero", w)
	}
	if done, err := mgr.WantedFilled(); err != nil || done {
		t.Fatalf("WantedFilled before = (%t, %v)", done, err)
	}
	if err := mgr.MarkWantedFilled(); err != nil {
		t.Fatal(err)
	}
	if done, err := mgr.WantedFilled(); err != nil || !done {
		t.Fatalf("WantedFilled after = (%t, %v)", done, err)
	}
}

func TestListInboundInFlight_SkipsAFileFromMyOtherDevice(t *testing.T) {
	mgr, _, _ := newManager(t)
	if err := mgr.TakeGapSiblingOffer(gapMeta(testMsgA), heldOffer(testMsgA)); err != nil {
		t.Fatal(err)
	}
	if err := mgr.PutMeta(files.Metadata{MsgID: testMsgB, ChatID: testChatID, Direction: files.DirIn, Token: "t", State: files.StateDownloading}); err != nil {
		t.Fatal(err)
	}
	got, err := mgr.ListInboundInFlight()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].MsgID != testMsgB {
		t.Fatalf("in flight = %+v, want only the contact's own transfer", got)
	}
}

func TestRequeueGapFile_RefusesASettledFile(t *testing.T) {
	mgr, _, _ := newManager(t)
	m := gapMeta(testMsgA)
	if err := mgr.TakeGapSiblingOffer(m, heldOffer(testMsgA)); err != nil {
		t.Fatal(err)
	}
	if err := mgr.UpdateMeta(testMsgA, func(m *files.Metadata) error { m.State = files.StateReady; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.RequeueGapFile(testMsgA, 1, func(files.Metadata) int { return 0 }); !errors.Is(err, files.ErrGapFileSettled) {
		t.Fatalf("requeue of a sealed file err = %v, want ErrGapFileSettled", err)
	}
	if got, err := mgr.GetMeta(testMsgA); err != nil || got.State != files.StateReady {
		t.Fatalf("meta after the refused requeue = %+v (%v)", got, err)
	}
}

func TestMarkSiblingFileSealed_RefusesAnotherTakesMetadata(t *testing.T) {
	mgr, _, _ := newManager(t)
	if err := mgr.TakeGapSiblingOffer(gapMeta(testMsgA), heldOffer(testMsgA)); err != nil {
		t.Fatal(err)
	}
	old := gapMeta(testMsgA)
	old.StagingID = "6b2c3d4e5f60718293a4b5c6d7e8f90a"
	old.State = files.StateReady
	if err := mgr.MarkSiblingFileSealed(old); !errors.Is(err, files.ErrSiblingTakeReplaced) {
		t.Fatalf("err = %v, want ErrSiblingTakeReplaced", err)
	}
	if got, _ := mgr.GetMeta(testMsgA); got.State != files.StatePending || got.StagingID != gapMeta(testMsgA).StagingID {
		t.Fatalf("the newer take was overwritten: %+v", got)
	}
}
