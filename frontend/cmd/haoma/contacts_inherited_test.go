package main

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/linkeddevice"
	"haoma-frontend/internal/peerstate"
	"haoma-frontend/internal/store"
	"haoma-frontend/internal/syncrail"
)

func peerEntryOf(t *testing.T, d *daemon, peerID string) ipc.PeerEntry {
	t.Helper()
	got, err := peerEntryFor(d, backendapi.Peer{ID: peerID})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func inheritedContactsOf(t *testing.T, d *daemon) []ipc.InheritedContactEntry {
	t.Helper()
	got, err := inheritedContacts(d)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func inheritedTestDaemon(t *testing.T) *daemon {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Unlock(dir, "test-pass")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Lock() })
	return &daemon{store: st, peerMeta: peerstate.NewMeta(st), chats: chat.NewStore(st)}
}

func seedInherited(t *testing.T, d *daemon, fingerprint, chatID string, rec peerstate.MetaRecord) {
	t.Helper()
	if _, err := d.peerMeta.MergePerson(fingerprint, rec); err != nil {
		t.Fatal(err)
	}
	if _, err := d.chats.CreateDirectEdgeless(chat.ChatID(chatID), fingerprint); err != nil {
		t.Fatal(err)
	}
}

func seedPaired(t *testing.T, d *daemon, fingerprint, peerID string, rec peerstate.MetaRecord) {
	t.Helper()
	if _, _, err := d.peerMeta.SetFingerprint(peerID, fingerprint); err != nil {
		t.Fatal(err)
	}
	if rec.Nick != "" {
		if _, err := d.peerMeta.SetNick(peerID, rec.Nick, rec.NickAt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.chats.CreateDirect(peerID); err != nil {
		t.Fatal(err)
	}
}

func TestInheritedContacts_ListsOnlyWhatTheOfferAccepts(t *testing.T) {
	d := inheritedTestDaemon(t)

	seedInherited(t, d, "fp-alice", "chat-alice", peerstate.MetaRecord{
		Nick: "alice", NickAt: 100, Alias: "Boss", AliasAt: 200,
	})

	seedPaired(t, d, "fp-bob", "peer-bob", peerstate.MetaRecord{Nick: "bob"})

	if _, err := d.peerMeta.MergePerson("fp-carol", peerstate.MetaRecord{Nick: "carol"}); err != nil {
		t.Fatal(err)
	}

	got := inheritedContactsOf(t, d)
	if len(got) != 1 {
		t.Fatalf("listed %d contacts, want 1 — got %+v", len(got), got)
	}
	if got[0].Fingerprint != "fp-alice" {
		t.Errorf("fingerprint = %q, want fp-alice", got[0].Fingerprint)
	}
	if got[0].Nick != "alice" || got[0].Alias != "Boss" {
		t.Errorf("nick=%q alias=%q, want alice/Boss — the columns are shared with the paired rows", got[0].Nick, got[0].Alias)
	}
	if got[0].Label != "Boss" {
		t.Errorf("label = %q, want Boss (the alias beats the name the contact chose)", got[0].Label)
	}

	for _, c := range got {
		if _, _, reason, detail, ok := pairingOfferPlan(context.Background(), d, c.Fingerprint, false); !ok {
			t.Errorf("the pane would show %q and the offer refuses it: %s (%s)", c.Fingerprint, reason, detail)
		}
	}

	for _, fp := range []string{"fp-bob", "fp-carol"} {
		if _, _, _, _, ok := pairingOfferPlan(context.Background(), d, fp, false); ok {
			t.Errorf("%q was left out of the list but the offer accepts it", fp)
		}
	}
}

func TestInheritedContacts_NamelessContactStillHasALabel(t *testing.T) {
	d := inheritedTestDaemon(t)
	const fp = "fp-0123456789abcdef"
	seedInherited(t, d, fp, "chat-nameless", peerstate.MetaRecord{})

	got := inheritedContactsOf(t, d)
	if len(got) != 1 {
		t.Fatalf("listed %d contacts, want 1", len(got))
	}
	if got[0].Label != peerstate.ShortID(fp) {
		t.Errorf("label = %q, want the short identity %q", got[0].Label, peerstate.ShortID(fp))
	}
}

func TestInheritedContacts_EmptyDeviceAnswersEmpty(t *testing.T) {
	d := inheritedTestDaemon(t)
	if got := inheritedContactsOf(t, d); len(got) != 0 {
		t.Fatalf("listed %d contacts on an empty device, want 0", len(got))
	}
}

func TestPeerEntry_CarriesThePersonBehindThePairing(t *testing.T) {
	d := inheritedTestDaemon(t)
	seedPaired(t, d, "fp-alice", "peer-alice-one", peerstate.MetaRecord{Nick: "alice"})
	seedPaired(t, d, "fp-alice", "peer-alice-two", peerstate.MetaRecord{Nick: "alice"})
	seedPaired(t, d, "fp-bob", "peer-bob", peerstate.MetaRecord{Nick: "bob"})

	for peerID, want := range map[string]string{
		"peer-alice-one": "fp-alice",
		"peer-alice-two": "fp-alice",
		"peer-bob":       "fp-bob",
	} {
		got := peerEntryOf(t, d, peerID)
		if got.Fingerprint != want {
			t.Errorf("%s carries fingerprint %q, want %q", peerID, got.Fingerprint, want)
		}
	}
}

func TestPeerEntry_SaysWhichPairingsAreMyOwnDevices(t *testing.T) {
	d := inheritedTestDaemon(t)
	d.linkedDevices = linkeddevice.New(d.store)
	seedPaired(t, d, "fp-alice", "peer-alice", peerstate.MetaRecord{Nick: "alice"})

	const mine = "peer-my-other-device"
	if _, err := d.linkedDevices.Link(mine, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}

	if got := peerEntryOf(t, d, mine); !got.Sibling {
		t.Errorf("a linked device's row does not say it is one: %+v", got)
	}
	if got := peerEntryOf(t, d, "peer-alice"); got.Sibling {
		t.Errorf("a contact's row says it is one of my own devices: %+v", got)
	}
}

func TestInheritedContacts_BackfillPushesTheChangedSet(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	conn := dialTest(t, ctx, addr, certPath, token)

	logger := slog.Default()

	applyContacts(d, logger, "", []syncrail.ContactSnapshot{{
		Fingerprint: "fp-alice",
		Nick:        "alice",
		NickAt:      100,
	}})
	if got := inheritedContactsChangedCount(t, ctx, conn); got != 0 {
		t.Fatalf("the contacts batch pushed %d contact(s), want 0 — a person with no conversation is not a row yet", got)
	}

	applyChats(d, logger, []syncrail.ChatSnapshot{{
		ChatID:      "chat-alice",
		Fingerprint: "fp-alice",
	}})
	if got := inheritedContactsChangedCount(t, ctx, conn); got != 1 {
		t.Fatalf("the chats batch pushed %d contact(s), want 1", got)
	}

	if got := inheritedContactsOf(t, d); len(got) != 1 || got[0].Fingerprint != "fp-alice" {
		t.Fatalf("the list answers %+v, want one row for fp-alice", got)
	}
}

func TestInheritedContacts_ABatchThatChangesNothingPushesNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	conn := dialTest(t, ctx, addr, certPath, token)

	logger := slog.Default()
	batch := []syncrail.ContactSnapshot{{Fingerprint: "fp-alice", Nick: "alice", NickAt: 100}}
	applyContacts(d, logger, "", batch)
	inheritedContactsChangedCount(t, ctx, conn)

	applyContacts(d, logger, "", batch)
	applyChats(d, logger, []syncrail.ChatSnapshot{{ChatID: "chat-alice", Fingerprint: "fp-alice"}})

	if got := inheritedContactsChangedCount(t, ctx, conn); got != 1 {
		t.Fatalf("the next push carried %d contact(s), want 1 — a repeated contacts batch pushed as though it had changed something", got)
	}
}

func inheritedContactsChangedCount(t *testing.T, ctx context.Context, conn *websocket.Conn) int {
	t.Helper()
	f := readUntil(t, ctx, conn, ipc.FrameInheritedContactsChanged)
	var p ipc.InheritedContactsChangedPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		t.Fatalf("decode pushed contacts: %v", err)
	}
	return len(p.Contacts)
}

func TestInheritedContacts_APairingTakesTheContactOutOfTheList(t *testing.T) {
	d := inheritedTestDaemon(t)
	seedInherited(t, d, "fp-alice", "chat-alice", peerstate.MetaRecord{Nick: "alice"})
	if got := inheritedContactsOf(t, d); len(got) != 1 {
		t.Fatalf("listed %d contacts before the pairing, want 1", len(got))
	}

	if _, _, err := d.peerMeta.SetFingerprint("peer-alice", "fp-alice"); err != nil {
		t.Fatal(err)
	}
	dc, err := d.chats.GetByFingerprint("fp-alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.chats.AttachPeerID(dc.ID, "peer-alice"); err != nil {
		t.Fatal(err)
	}

	if got := inheritedContactsOf(t, d); len(got) != 0 {
		t.Fatalf("listed %d contacts after the pairing, want 0 — got %+v", len(got), got)
	}
}

func TestInheritedContacts_APairingPushesTheChangedSet(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	conn := dialTest(t, ctx, addr, certPath, token)

	seedInherited(t, d, "fp-alice", "chat-alice", peerstate.MetaRecord{Nick: "alice"})
	if got := inheritedContactsOf(t, d); len(got) != 1 {
		t.Fatalf("listed %d contacts before the pairing, want 1", len(got))
	}

	if _, _, err := d.peerMeta.SetFingerprint("peer-alice", "fp-alice"); err != nil {
		t.Fatal(err)
	}
	dc, err := d.chats.GetByFingerprint("fp-alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.chats.AttachPeerID(dc.ID, "peer-alice"); err != nil {
		t.Fatal(err)
	}

	pushPeerPaired(ctx, d, "peer-alice", "invitation-accept")

	if got := readUntil(t, ctx, conn, ipc.FramePeerPaired); got.Type != ipc.FramePeerPaired {
		t.Fatalf("the paired frame did not arrive")
	}
	if got := inheritedContactsChangedCount(t, ctx, conn); got != 0 {
		t.Fatalf("the pushed set holds %d contact(s), want 0 — the person this device just paired with is no longer one it cannot reach", got)
	}
}

func TestPeerPaired_OnlyTheHelperSendsTheFrame(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg, ok := pkgs["main"]
	if !ok || len(pkg.Files) == 0 {
		t.Fatal("no daemon sources parsed — the guard would pass on an empty set")
	}

	var helper *ast.FuncDecl
	for _, f := range pkg.Files {
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if ok && fn.Recv == nil && fn.Name.Name == "pushPeerPaired" {
				helper = fn
			}
		}
	}
	if helper == nil {
		t.Fatal("pushPeerPaired is gone — the one function that sends the three frames together")
	}

	names := func(n ast.Node) map[string]bool {
		found := map[string]bool{}
		ast.Inspect(n, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.SelectorExpr:
				if id, ok := v.X.(*ast.Ident); ok {
					found[id.Name+"."+v.Sel.Name] = true
				}
			case *ast.Ident:
				found[v.Name] = true
			}
			return true
		})
		return found
	}

	sends := names(helper.Body)
	for _, want := range []string{
		"ipc.FramePeerPaired",
		"pushPeerMetaUpdated",
		"pushInheritedContactsChanged",
	} {
		if !sends[want] {
			t.Errorf("pushPeerPaired no longer sends %s — the three frames are only sent together because this one function sends them", want)
		}
	}

	checked := 0
	for path, f := range pkg.Files {
		checked++
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "FramePeerPaired" {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok || id.Name != "ipc" {
				return true
			}
			if sel.Pos() >= helper.Pos() && sel.End() <= helper.End() {
				return true
			}
			pos := fset.Position(sel.Pos())
			t.Errorf("%s:%d sends the paired frame outside pushPeerPaired\n\tcall pushPeerPaired instead — it sends the peer, the conversation and the contacts with no pairing together",
				filepath.Base(path), pos.Line)
			return true
		})
	}
	if checked == 0 {
		t.Fatal("every source was skipped — the guard checked nothing")
	}
}

func unreadableLinkedDevices(t *testing.T) *linkeddevice.Store {
	t.Helper()
	st, err := store.Unlock(t.TempDir(), "test-pass")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Lock(); err != nil {
		t.Fatal(err)
	}
	set := linkeddevice.New(st)
	if _, err := set.Get(); err == nil {
		t.Fatal("the fixture reads fine, so nothing is broken to test")
	}
	if _, err := set.Members(); err == nil {
		t.Fatal("the fixture lists fine, so nothing is broken to test")
	}
	return set
}

func TestPeerEntry_NoRowWhenTheSiblingCheckCannotBeAnswered(t *testing.T) {
	d := inheritedTestDaemon(t)
	seedPaired(t, d, "fp-alice", "peer-alice", peerstate.MetaRecord{Nick: "alice"})
	d.linkedDevices = unreadableLinkedDevices(t)

	got, err := peerEntryFor(d, backendapi.Peer{ID: "peer-alice"})
	if err == nil {
		t.Fatalf("a row was built from a sibling check that failed: %+v", got)
	}
	if got.ID != "" {
		t.Errorf("the refused row still carries fields: %+v", got)
	}
}

func TestInheritedContacts_ListRefusesWhenTheLinkedSetCannotBeRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	conn := dialTest(t, ctx, addr, certPath, token)

	seedInherited(t, d, "fp-alice", "chat-alice", peerstate.MetaRecord{Nick: "alice"})
	if got := inheritedContactsOf(t, d); len(got) != 1 {
		t.Fatalf("the fixture lists %d contacts, want 1", len(got))
	}
	d.linkedDevices = unreadableLinkedDevices(t)

	req, err := ipc.NewFrame(ipc.FrameListInheritedContacts, "req-1", struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	writeFrame(t, ctx, conn, req)

	f := readUntil(t, ctx, conn, ipc.FrameError)
	var p ipc.ErrorPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Code != reasonStoreReadFailed {
		t.Fatalf("refused with %q, want %q", p.Code, reasonStoreReadFailed)
	}
}

func TestInheritedContacts_NoPushWhenTheLinkedSetCannotBeRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	conn := dialTest(t, ctx, addr, certPath, token)

	seedInherited(t, d, "fp-alice", "chat-alice", peerstate.MetaRecord{Nick: "alice"})

	working := d.linkedDevices
	d.linkedDevices = unreadableLinkedDevices(t)
	pushInheritedContactsChanged(d, nil, "linked_set_unreadable")

	d.linkedDevices = working
	seedInherited(t, d, "fp-bob", "chat-bob", peerstate.MetaRecord{Nick: "bob"})
	pushInheritedContactsChanged(d, nil, "linked_set_readable")

	if got := inheritedContactsChangedCount(t, ctx, conn); got != 2 {
		t.Fatalf("the next push carried %d contact(s), want 2 — the push made on a linked-device read that failed went out", got)
	}
}

func TestPushPeerPaired_ArmsTheRailPushForTheSibling(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	armed := make(chan []string, 1)
	pusher := newContactsPusher(func(fps []string) {
		select {
		case armed <- fps:
		default:
		}
	})

	pusher.timing = contactsPushTiming{
		debounce: 10 * time.Millisecond,
		ceiling:  50 * time.Millisecond,
	}
	d.contactsPush = pusher

	const peerID = "peer-alice"
	if _, _, err := d.peerMeta.SetFingerprint(peerID, "fp-alice"); err != nil {
		t.Fatalf("SetFingerprint: %v", err)
	}

	pushPeerPaired(context.Background(), d, peerID, "invitation-accept")

	select {
	case fps := <-armed:
		for _, fp := range fps {
			if fp == "fp-alice" {
				return
			}
		}
		t.Fatalf("the push carried %v, not the person just paired with", fps)
	case <-time.After(3 * time.Second):
		t.Fatal("a completed pairing did not arm the push, so the other device waits for a sweep")
	}
}
