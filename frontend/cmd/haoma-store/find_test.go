package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"haoma-frontend/internal/events"
	"haoma-frontend/internal/files"
	"haoma-frontend/internal/logid"
	"haoma-frontend/internal/store"
)

func oneDeviceWithHistory(t *testing.T) string {
	t.Helper()
	a := newStore(t, "alice")
	chatID := a.pairedDirectly("pairing-a1", bobFingerprint, "Bob")
	a.textOut(chatID, "a1b2c3d4e5f60718293a4b5c6d7e8f90", "I love you", events.DeliverySent, at(0))
	a.textOut(chatID, "b0b0b0b0111111112222222233333333", "running late", events.DeliveryRead, at(120))
	a.textIn(chatID, "c0c0c0c0444444445555555566666666", "no worries at all", at(121))
	a.markRead("c0c0c0c0444444445555555566666666", at(122))
	a.attachment(chatID, "d0d0d0d0777777778888888899999999", "holiday.jpg", "the beach", string(files.StateReady), at(5))
	return a.close()
}

func TestFind_ByFullMessageID(t *testing.T) {
	dir := oneDeviceWithHistory(t)
	s := openOne(t, dir)

	res, report := runFindInto(t, []*side{s}, findFilter{msgID: "a1b2c3d4e5f60718293a4b5c6d7e8f90"}, false)

	if len(res.hits) != 1 {
		t.Fatalf("%d hits, want 1\n%s", len(res.hits), report)
	}
	if res.hits[0].row.Text != "I love you" {
		t.Fatalf("hit text %q\n%s", res.hits[0].row.Text, report)
	}
}

func TestFind_ByShortenedMessageID(t *testing.T) {
	dir := oneDeviceWithHistory(t)
	s := openOne(t, dir)

	short := logid.Short("a1b2c3d4e5f60718293a4b5c6d7e8f90")
	if short == "a1b2c3d4e5f60718293a4b5c6d7e8f90" {
		t.Fatal("the fixture id must be long enough to be shortened")
	}

	res, report := runFindInto(t, []*side{s}, findFilter{msgID: short}, false)
	if len(res.hits) != 1 {
		t.Fatalf("%d hits for the shortened form %q, want 1\n%s", len(res.hits), short, report)
	}
	if res.hits[0].row.MsgID != "a1b2c3d4e5f60718293a4b5c6d7e8f90" {
		t.Fatalf("hit id %q, want the full form\n%s", res.hits[0].row.MsgID, report)
	}
}

func TestFind_PrintsTheFullMessageID(t *testing.T) {
	dir := oneDeviceWithHistory(t)
	s := openOne(t, dir)

	_, report := runFindInto(t, []*side{s}, findFilter{text: "I love you"}, false)
	mustContain(t, report, "a1b2c3d4e5f60718293a4b5c6d7e8f90")
	if strings.Contains(report, logid.Short("a1b2c3d4e5f60718293a4b5c6d7e8f90")) &&
		!strings.Contains(report, "a1b2c3d4e5f60718293a4b5c6d7e8f90") {
		t.Fatalf("the report printed only the shortened id\n%s", report)
	}
}

func TestFind_ByTextSubstring(t *testing.T) {
	dir := oneDeviceWithHistory(t)
	s := openOne(t, dir)

	res, report := runFindInto(t, []*side{s}, findFilter{text: "love"}, false)
	if len(res.hits) != 1 {
		t.Fatalf("%d hits, want 1\n%s", len(res.hits), report)
	}
	mustContain(t, report, `"I love you"`)
}

func TestFind_SubstringThatMatchesNothing(t *testing.T) {
	dir := oneDeviceWithHistory(t)
	s := openOne(t, dir)

	res, report := runFindInto(t, []*side{s}, findFilter{text: "nothing here says this"}, false)
	if len(res.hits) != 0 {
		t.Fatalf("%d hits, want 0\n%s", len(res.hits), report)
	}
	mustContain(t, report, "0 hits.")
	if res.scanned == 0 {
		t.Fatalf("nothing was read, so the empty answer means nothing\n%s", report)
	}
}

func TestFind_ByAttachmentName(t *testing.T) {
	dir := oneDeviceWithHistory(t)
	s := openOne(t, dir)

	res, report := runFindInto(t, []*side{s}, findFilter{file: "holiday"}, false)
	if len(res.hits) != 1 {
		t.Fatalf("%d hits, want 1\n%s", len(res.hits), report)
	}
	mustContain(t, report, "holiday.jpg")
	mustContain(t, report, "transfer ready")
}

func TestFind_ByAttachmentCaption(t *testing.T) {
	dir := oneDeviceWithHistory(t)
	s := openOne(t, dir)

	res, report := runFindInto(t, []*side{s}, findFilter{file: "the beach"}, false)
	if len(res.hits) != 1 {
		t.Fatalf("%d hits, want 1\n%s", len(res.hits), report)
	}
}

func TestFind_FiltersCompose(t *testing.T) {
	a := newStore(t, "alice")
	withBob := a.pairedDirectly("pairing-a1", bobFingerprint, "Bob")
	withCarol := a.pairedDirectly("pairing-a2", "cccc3333cccc3333cccc3333cccc3333", "Carol")
	a.textOut(withBob, "msg-bob-sent", "went out fine", events.DeliverySent, at(0))
	a.textOut(withBob, "msg-bob-stuck", "still waiting", events.DeliveryEnqueued, at(1))
	a.textOut(withCarol, "msg-carol-stuck", "also waiting", events.DeliveryEnqueued, at(2))
	dir := a.close()

	s := openOne(t, dir)

	both, report := runFindInto(t, []*side{s}, findFilter{conv: "Bob", delivery: "enqueued"}, false)
	if len(both.hits) != 1 {
		t.Fatalf("%d hits for Bob and enqueued, want 1\n%s", len(both.hits), report)
	}
	if both.hits[0].row.MsgID != "msg-bob-stuck" {
		t.Fatalf("hit %q, want msg-bob-stuck\n%s", both.hits[0].row.MsgID, report)
	}

	convOnly, _ := runFindInto(t, []*side{s}, findFilter{conv: "Bob"}, false)
	if len(convOnly.hits) != 2 {
		t.Fatalf("%d hits for Bob alone, want 2", len(convOnly.hits))
	}
	stateOnly, _ := runFindInto(t, []*side{s}, findFilter{delivery: "enqueued"}, false)
	if len(stateOnly.hits) != 2 {
		t.Fatalf("%d hits for enqueued alone, want 2", len(stateOnly.hits))
	}
}

func TestFind_ByReadState(t *testing.T) {
	dir := oneDeviceWithHistory(t)
	s := openOne(t, dir)

	read, report := runFindInto(t, []*side{s}, findFilter{read: "yes"}, false)
	for _, h := range read.hits {
		if !isRead(h.row) {
			t.Fatalf("a row that is not read came back for -read yes\n%s", report)
		}
	}
	if len(read.hits) == 0 {
		t.Fatalf("no read rows found, and the fixture holds two\n%s", report)
	}
	unread, _ := runFindInto(t, []*side{s}, findFilter{read: "no"}, false)
	for _, h := range unread.hits {
		if isRead(h.row) {
			t.Fatal("a row that is read came back for -read no")
		}
	}
	if len(read.hits)+len(unread.hits) != read.scanned {
		t.Fatalf("read %d plus unread %d does not add up to %d rows",
			len(read.hits), len(unread.hits), read.scanned)
	}
}

func TestFind_TimeWindowAroundAPoint(t *testing.T) {
	dir := oneDeviceWithHistory(t)
	s := openOne(t, dir)

	when, err := buildWindow("2026-08-15T15:00:00", "30m", "", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	near, report := runFindInto(t, []*side{s}, findFilter{when: when}, false)

	if len(near.hits) != 2 {
		t.Fatalf("%d hits within half an hour of three, want 2\n%s", len(near.hits), report)
	}

	whole, err := buildWindow("2026-08-15", "", "", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	day, _ := runFindInto(t, []*side{s}, findFilter{when: whole}, false)
	if len(day.hits) != 4 {
		t.Fatalf("%d hits on the whole day, want 4", len(day.hits))
	}
}

func TestFind_RejectsTwoWaysOfSayingWhen(t *testing.T) {
	if _, err := buildWindow("15:00", "", "14:00", "", time.Now()); err == nil {
		t.Fatal("giving -at together with -from was accepted")
	}
	if _, err := buildWindow("", "30m", "", "", time.Now()); err == nil {
		t.Fatal("giving -within without -at was accepted")
	}
	if _, err := buildWindow("", "", "16:00", "15:00", time.Now()); err == nil {
		t.Fatal("a window that ends before it starts was accepted")
	}
}

func TestFind_HitOnOneSideOnlyIsMarked(t *testing.T) {
	a, b, chatA, chatB := pairOfDevices(t)
	a.textOut(chatA, "msg-on-both", "we both have this", events.DeliverySent, at(1))
	b.textOut(chatB, "msg-on-both", "we both have this", events.DeliverySent, at(1))
	a.textOut(chatA, "msg-only-on-a", "only the first device has this", events.DeliverySent, at(2))
	dirA, dirB := a.close(), b.close()

	sideA, sideB := openSides(t, dirA, dirB)
	res, report := runFindInto(t, []*side{sideA, sideB}, findFilter{text: "this"}, false)

	byID := map[string][]hit{}
	for _, h := range res.hits {
		byID[h.row.MsgID] = append(byID[h.row.MsgID], h)
	}
	if len(byID["msg-on-both"]) != 2 {
		t.Fatalf("the shared message came back %d times, want 2\n%s", len(byID["msg-on-both"]), report)
	}
	for _, h := range byID["msg-on-both"] {
		if h.onlyHere {
			t.Fatalf("a message both stores hold was marked as being on one side only\n%s", report)
		}
	}
	lonely := byID["msg-only-on-a"]
	if len(lonely) != 1 {
		t.Fatalf("the lonely message came back %d times, want 1\n%s", len(lonely), report)
	}
	if !lonely[0].onlyHere {
		t.Fatalf("a message only one store holds was not marked\n%s", report)
	}
	if lonely[0].slot != "a" {
		t.Fatalf("the lonely message was attributed to %q, want a\n%s", lonely[0].slot, report)
	}
	mustContain(t, report, "only in a")
	mustContain(t, report, "in a ")
	mustContain(t, report, "in b ")
}

func TestFind_RefusesAStoreWhoseDaemonHoldsIt(t *testing.T) {
	dir := oneDeviceWithHistory(t)

	daemon, err := store.Unlock(dir, fixturePassphrase)
	if err != nil {
		t.Fatalf("hold the store: %v", err)
	}
	defer func() { _ = daemon.Lock() }()

	t.Setenv(envPassphrase, fixturePassphrase)
	if _, err := openSide(dir, "a", "", t.TempDir()); err == nil {
		t.Fatal("find opened a store another process holds")
	} else {
		mustContain(t, err.Error(), "its daemon is running")
		mustContain(t, err.Error(), "no flag to override")
	}
}

func TestFind_IDsModePrintsNothingButFullIDs(t *testing.T) {
	dir := oneDeviceWithHistory(t)
	s := openOne(t, dir)

	_, report := runFindInto(t, []*side{s}, findFilter{text: "love"}, true)
	lines := strings.Split(strings.TrimRight(report, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("%d lines, want 1\n%s", len(lines), report)
	}
	if lines[0] != "a1b2c3d4e5f60718293a4b5c6d7e8f90" {
		t.Fatalf("line %q, want the full message id and nothing else", lines[0])
	}
}

func TestFind_IDsModeDoesNotRepeatAMessageHeldByBothStores(t *testing.T) {
	a, b, chatA, chatB := pairOfDevices(t)
	a.textOut(chatA, "msg-on-both", "we both have this", events.DeliverySent, at(1))
	b.textOut(chatB, "msg-on-both", "we both have this", events.DeliverySent, at(1))
	dirA, dirB := a.close(), b.close()

	sideA, sideB := openSides(t, dirA, dirB)
	_, report := runFindInto(t, []*side{sideA, sideB}, findFilter{text: "this"}, true)

	lines := strings.Split(strings.TrimRight(report, "\n"), "\n")
	if len(lines) != 1 || lines[0] != "msg-on-both" {
		t.Fatalf("lines %v, want one line holding msg-on-both", lines)
	}
}

func TestFind_SaysItPrintsMessageText(t *testing.T) {
	dir := oneDeviceWithHistory(t)
	s := openOne(t, dir)

	_, report := runFindInto(t, []*side{s}, findFilter{text: "love"}, false)
	mustContain(t, report, "This prints the text of your messages.")
}

func TestFind_ArrivedFromShowsWhenThePairingWasMade(t *testing.T) {
	a := newStore(t, "alice")
	if _, _, err := a.meta.SetFingerprintAt("pairing-a1", bobFingerprint, at(-60).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.meta.SetFingerprintAt("pairing-a2", bobFingerprint, 0); err != nil {
		t.Fatal(err)
	}
	dc, err := a.chats.CreateDirect("pairing-a1")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.chats.AttachPeerID(dc.ID, "pairing-a2"); err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct{ msgID, from, text string }{
		{"e0e0e0e0111111112222222233333333", "pairing-a1", "from the stamped pairing"},
		{"f0f0f0f0111111112222222233333333", "pairing-a2", "from the old pairing"},
	} {
		body, err := json.Marshal(events.TextBody{Text: r.text})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.log.AppendInbound(events.InboundParams{
			ChatID:            dc.ID,
			Kind:              events.KindText,
			DisplayTs:         at(1).Unix(),
			SenderTs:          at(1).Unix(),
			MsgID:             r.msgID,
			EnvelopeID:        "env-" + r.msgID,
			Body:              body,
			ArrivedFromPeerID: r.from,
		}); err != nil {
			t.Fatal(err)
		}
	}
	s := openOne(t, a.close())

	_, report := runFindInto(t, []*side{s}, findFilter{text: "from the"}, false)
	mustContain(t, report, "paired at "+stamp(at(-60).Unix()))
	mustContain(t, report, "paired at unknown (a record from before the stamp)")
}
