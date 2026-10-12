package main

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/peerstate"
	"haoma-frontend/internal/presence"
)

const (
	alicePCID    = "000000000000000000000000000a11ce"
	alicePhoneID = "000000000000000000000000000a1fff"
	carolID      = "00000000000000000000000000ca401e"
	noChatID     = "0000000000000000000000000000beef"
)

type stepClock struct {
	mu     sync.Mutex
	timers []*stepTimer
}

type stepTimer struct {
	mu      sync.Mutex
	f       func()
	stopped bool
}

func (t *stepTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	was := !t.stopped
	t.stopped = true
	return was
}

func (t *stepTimer) Reset(time.Duration) bool { return false }

func (t *stepTimer) fire() {
	t.mu.Lock()
	stopped := t.stopped
	t.mu.Unlock()
	if !stopped {
		t.f()
	}
}

func (c *stepClock) afterFunc(_ time.Duration, f func()) presence.Timer {
	t := &stepTimer{f: f}
	c.mu.Lock()
	c.timers = append(c.timers, t)
	c.mu.Unlock()
	return t
}

func (c *stepClock) armedBy(t *testing.T, observe func()) *stepTimer {
	t.Helper()
	c.mu.Lock()
	before := len(c.timers)
	c.mu.Unlock()
	observe()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.timers) != before+1 {
		t.Fatalf("the observation armed %d timers, want 1", len(c.timers)-before)
	}
	return c.timers[before]
}

func personDaemon(t *testing.T) (d *daemon, clk *stepClock, aliceChat, carolChat chat.ChatID) {
	t.Helper()
	d = minimalDaemon(t, "http://127.0.0.1:1")
	clk = &stepClock{}
	d.presenceCache = presence.NewWithOptions(presence.Options{AfterFunc: clk.afterFunc})
	alice, err := d.chats.CreateDirect(alicePCID)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.chats.AttachPeerID(alice.ID, alicePhoneID); err != nil {
		t.Fatal(err)
	}
	carol, err := d.chats.CreateDirect(carolID)
	if err != nil {
		t.Fatal(err)
	}
	return d, clk, alice.ID, carol.ID
}

func chatPresenceOf(t *testing.T, d *daemon, id chat.ChatID) presence.Snapshot {
	t.Helper()
	c, err := d.chats.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	snap, ok := d.chatPresence(c.(*chat.DirectChat))
	if !ok {
		t.Fatalf("chat %s has no presence", id)
	}
	return snap
}

func TestChatPresence_TakesTheBestStateOverThePairingsOfTheChat(t *testing.T) {
	d, clk, aliceChat, _ := personDaemon(t)

	check := func(step, want string) {
		t.Helper()
		if got := chatPresenceOf(t, d, aliceChat).Effective; got != want {
			t.Errorf("%s: the chat presence is %q, want %q", step, got, want)
		}
	}

	check("no row on either pairing", presence.EffectiveUnknown)

	phoneTech := clk.armedBy(t, func() { d.presenceCache.ObserveTechnical(alicePhoneID) })
	check("blue on the second pairing only", presence.EffectiveAccepting)

	phoneHuman := clk.armedBy(t, func() { d.presenceCache.ObserveHuman(alicePhoneID, presence.EffectiveAvailable) })
	check("green on the second pairing only", presence.EffectiveAvailable)

	pcTech := clk.armedBy(t, func() { d.presenceCache.ObserveTechnical(alicePCID) })
	check("blue on the first, green on the second", presence.EffectiveAvailable)
	if got := d.presenceCache.Snapshot(alicePCID).Effective; got != presence.EffectiveAccepting {
		t.Fatalf("the first pairing's own row is %q, want accepting: the cache itself must not change", got)
	}

	phoneHuman.fire()
	check("the second pairing's green expired", presence.EffectiveAccepting)

	pcTech.fire()
	phoneTech.fire()
	check("everything expired", presence.EffectiveUnknown)
}

func TestChatPresence_ASingleDeviceChatIsItsPairing(t *testing.T) {
	d, _, _, carolChat := personDaemon(t)
	d.presenceCache.ObserveTechnical(carolID)
	if got, want := chatPresenceOf(t, d, carolChat), d.presenceCache.Snapshot(carolID); got != want {
		t.Errorf("Carol's chat presence = %+v, want her own row %+v", got, want)
	}
	if dc := d.chatOfPeer(noChatID); dc != nil {
		t.Errorf("a peer with no chat routes into chat %s", dc.ID)
	}
}

func TestChatPresence_ARetiredPairingOutOfTheRoutingSetDoesNotColourTheChatPresence(t *testing.T) {
	d, _, aliceChat, _ := personDaemon(t)
	if _, _, err := d.chats.DropRoutes(aliceChat, alicePCID, []string{alicePhoneID}); err != nil {
		t.Fatal(err)
	}
	d.presenceCache.ObserveHuman(alicePhoneID, presence.EffectiveAvailable)
	if got := chatPresenceOf(t, d, aliceChat).Effective; got != presence.EffectiveUnknown {
		t.Errorf("chat presence = %q, want unknown: the retired pairing left the routing set", got)
	}
}

func TestPresenceSnapshots_PeerRowsAreRawAndTheChatRowIsTheChatPresence(t *testing.T) {
	d, _, aliceChat, carolChat := personDaemon(t)
	d.presenceCache.ObserveHuman(alicePhoneID, presence.EffectiveAvailable)
	d.presenceCache.ObserveTechnical(carolID)

	var first, second ipc.PeerEntry
	applyPresenceSnapshot(d, alicePCID, &first)
	applyPresenceSnapshot(d, alicePhoneID, &second)
	if first.Effective != presence.EffectiveUnknown {
		t.Errorf("the first pairing's peer row is %q, want its own unknown", first.Effective)
	}
	if second.Effective != presence.EffectiveAvailable {
		t.Errorf("the second pairing's peer row is %q, want its own available", second.Effective)
	}

	entry := ipc.ChatEntry{ChatID: string(aliceChat), Kind: ipc.ChatKindDirect, PeerID: alicePCID}
	applyChatPresenceSnapshot(d, &entry, mustDirectChat(t, d, aliceChat))
	if entry.Effective != presence.EffectiveAvailable || entry.Chatty != presence.EffectiveAvailable {
		t.Errorf("Alice's chat row is %q/%q, want the chat presence, available", entry.Effective, entry.Chatty)
	}
	carol := ipc.ChatEntry{ChatID: string(carolChat), Kind: ipc.ChatKindDirect, PeerID: carolID}
	applyChatPresenceSnapshot(d, &carol, mustDirectChat(t, d, carolChat))
	if carol.Effective != presence.EffectiveAccepting {
		t.Errorf("Carol's chat row is %q, want her own accepting", carol.Effective)
	}

	d.presenceCache.ObserveHuman(alicePCID, presence.EffectiveBusy)
	bare := ipc.ChatEntry{ChatID: string(aliceChat), Kind: ipc.ChatKindDirect, PeerID: alicePCID}
	applyChatPresenceSnapshot(d, &bare, nil)
	if bare.Effective != presence.EffectiveUnknown {
		t.Errorf("a chat row built without its chat is %q, want unknown", bare.Effective)
	}
}

func mustDirectChat(t *testing.T, d *daemon, id chat.ChatID) *chat.DirectChat {
	t.Helper()
	dc, err := directChat(d, id)
	if err != nil {
		t.Fatal(err)
	}
	return dc
}

type presenceFrame struct {
	Type      ipc.FrameType
	Key       string
	Effective string
}

func presenceTap(t *testing.T, d *daemon) func() presenceFrame {
	t.Helper()
	next := presencePushTap(t, d)
	return func() presenceFrame {
		t.Helper()
		p := next()
		return presenceFrame{p.Type, p.Key, p.Effective}
	}
}

type presencePush struct {
	Type      ipc.FrameType
	Key       string
	Accepting bool
	Sending   bool
	Chatty    string
	Effective string
}

func presencePushTap(t *testing.T, d *daemon) func() presencePush {
	t.Helper()
	srv := httptest.NewServer(d.ipcSrv.Handler())
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer t")
	conn, _, err := websocket.Dial(ctx, "ws://"+strings.TrimPrefix(srv.URL, "http://")+"/ws",
		&websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "") })
	writeFrame(t, ctx, conn, mustFrame(ipc.FrameHello, "h", ipc.HelloPayload{ClientName: "test"}))
	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatalf("welcome: %v", err)
	}
	return func() presencePush {
		t.Helper()
		for {
			f := readNext(t, ctx, conn)
			switch f.Type {
			case ipc.FramePresenceChanged:
				var p ipc.PresenceChangedPayload
				if err := json.Unmarshal(f.Payload, &p); err != nil {
					t.Fatal(err)
				}
				return presencePush{f.Type, p.PeerID, p.Accepting, p.Sending, p.Chatty, p.Effective}
			case ipc.FrameChatPresenceChanged:
				var p ipc.ChatPresenceChangedPayload
				if err := json.Unmarshal(f.Payload, &p); err != nil {
					t.Fatal(err)
				}
				return presencePush{f.Type, p.ChatID, p.Accepting, p.Sending, p.Chatty, p.Effective}
			}
		}
	}
}

func waitUntil(t *testing.T, cond func() bool, what string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func expectFrame(t *testing.T, next func() presenceFrame, step string, want presenceFrame) {
	t.Helper()
	if got := next(); got != want {
		t.Fatalf("%s: pushed %+v, want %+v", step, got, want)
	}
}

func expectFrames(t *testing.T, next func() presenceFrame, step string, want ...presenceFrame) {
	t.Helper()
	left := map[presenceFrame]int{}
	for _, w := range want {
		left[w]++
	}
	for range want {
		got := next()
		if left[got] == 0 {
			t.Fatalf("%s: pushed %+v, want the frames %+v", step, got, want)
		}
		left[got]--
	}
}

func startPushLoop(t *testing.T, d *daemon) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		pushPresenceChanges(ctx, d)
	}()
	t.Cleanup(func() { cancel(); <-done })
	waitUntil(t, func() bool { return d.presenceCache.DirtySubscriberCount() == 1 }, "the push loop never subscribed")
}

func TestPushPresenceChanges_ShipsTheRowRawAndTheChatPresenceWhenItChanged(t *testing.T) {
	d, _, aliceChat, carolChat := personDaemon(t)
	next := presenceTap(t, d)
	waitUntil(t, func() bool { return d.ipcSrv.SessionCount() == 1 }, "the test client was never registered")
	startPushLoop(t, d)

	d.presenceCache.ObserveHuman(alicePhoneID, presence.EffectiveAvailable)
	expectFrames(t, next, "green on the second pairing",
		presenceFrame{ipc.FramePresenceChanged, alicePhoneID, presence.EffectiveAvailable},
		presenceFrame{ipc.FrameChatPresenceChanged, string(aliceChat), presence.EffectiveAvailable})

	d.presenceCache.ObserveTechnical(alicePCID)
	expectFrames(t, next, "blue on the first pairing",
		presenceFrame{ipc.FramePresenceChanged, alicePCID, presence.EffectiveAccepting},
		presenceFrame{ipc.FrameChatPresenceChanged, string(aliceChat), presence.EffectiveAvailable})

	d.presenceCache.ObserveTechnical(alicePCID)

	d.presenceCache.ObserveHuman(alicePCID, presence.EffectiveBusy)
	expectFrames(t, next, "busy on the first pairing",
		presenceFrame{ipc.FramePresenceChanged, alicePCID, presence.EffectiveBusy},
		presenceFrame{ipc.FrameChatPresenceChanged, string(aliceChat), presence.EffectiveBusy})

	d.presenceCache.ObserveTechnical(carolID)
	expectFrames(t, next, "Carol",
		presenceFrame{ipc.FramePresenceChanged, carolID, presence.EffectiveAccepting},
		presenceFrame{ipc.FrameChatPresenceChanged, string(carolChat), presence.EffectiveAccepting})

	d.presenceCache.ObserveTechnical(noChatID)
	expectFrame(t, next, "a peer with no chat", presenceFrame{ipc.FramePresenceChanged, noChatID, presence.EffectiveAccepting})
	d.presenceCache.ObserveHuman(carolID, presence.EffectiveAway)
	expectFrames(t, next, "the end marker",
		presenceFrame{ipc.FramePresenceChanged, carolID, presence.EffectiveAway},
		presenceFrame{ipc.FrameChatPresenceChanged, string(carolChat), presence.EffectiveAway})
}

func TestPushPresenceChanges_AnObservationCorrectsAStaleChatPresence(t *testing.T) {
	d, _, aliceChat, _ := personDaemon(t)
	next := presenceTap(t, d)
	waitUntil(t, func() bool { return d.ipcSrv.SessionCount() == 1 }, "the test client was never registered")
	startPushLoop(t, d)

	d.presenceCache.ObserveTechnical(alicePhoneID)
	expectFrames(t, next, "the phone answers",
		presenceFrame{ipc.FramePresenceChanged, alicePhoneID, presence.EffectiveAccepting},
		presenceFrame{ipc.FrameChatPresenceChanged, string(aliceChat), presence.EffectiveAccepting})

	stale := presence.Snapshot{Effective: presence.EffectiveUnknown}
	d.chatPresenceMu.Lock()
	d.chatPresencePushed[aliceChat] = stale
	push(d.ipcSrv, ipc.FrameChatPresenceChanged, "", chatPresencePayload(aliceChat, stale))
	d.chatPresenceMu.Unlock()
	expectFrame(t, next, "the stale chat presence", presenceFrame{ipc.FrameChatPresenceChanged, string(aliceChat), presence.EffectiveUnknown})

	d.presenceCache.ObserveTechnical(alicePhoneID)
	expectFrame(t, next, "the phone answers again", presenceFrame{ipc.FrameChatPresenceChanged, string(aliceChat), presence.EffectiveAccepting})

	d.presenceCache.ObserveTechnical(alicePhoneID)
	d.presenceCache.ObserveTechnical(noChatID)
	expectFrame(t, next, "the end marker", presenceFrame{ipc.FramePresenceChanged, noChatID, presence.EffectiveAccepting})
}

func TestPushInitialPresence_ShipsRowsRawAndEachChatPresenceOnce(t *testing.T) {
	d, _, aliceChat, carolChat := personDaemon(t)
	d.presenceCache.ObserveHuman(alicePhoneID, presence.EffectiveAvailable)
	d.presenceCache.ObserveTechnical(alicePCID)
	d.presenceCache.ObserveTechnical(carolID)
	d.ipcSrv.OnSession = func(_ context.Context, sess *ipc.Session) {
		newSessionDispatcher(d).pushInitialPresence(sess)
		for {
			if _, err := sess.Recv(); err != nil {
				return
			}
		}
	}
	next := presenceTap(t, d)

	want := map[presenceFrame]bool{
		{ipc.FramePresenceChanged, alicePCID, presence.EffectiveAccepting}:             true,
		{ipc.FramePresenceChanged, alicePhoneID, presence.EffectiveAvailable}:          true,
		{ipc.FramePresenceChanged, carolID, presence.EffectiveAccepting}:               true,
		{ipc.FrameChatPresenceChanged, string(aliceChat), presence.EffectiveAvailable}: true,
		{ipc.FrameChatPresenceChanged, string(carolChat), presence.EffectiveAccepting}: true,
	}
	for range len(want) {
		got := next()
		if !want[got] {
			t.Fatalf("start-up snapshot: unexpected or repeated %+v", got)
		}
		delete(want, got)
	}
}

func TestFold_IntoAChatWithAPairing_PushesTheChatPresence(t *testing.T) {
	d := minimalDaemon(t, "http://127.0.0.1:1")
	d.peerMeta = peerstate.NewMeta(d.store)
	for _, id := range []string{alicePCID, alicePhoneID} {
		if _, _, err := d.peerMeta.SetFingerprint(id, "fp-alice"); err != nil {
			t.Fatal(err)
		}
	}
	alice, err := d.chats.CreateDirect(alicePCID)
	if err != nil {
		t.Fatal(err)
	}
	d.presenceCache.ObserveHuman(alicePhoneID, presence.EffectiveAvailable)
	next := presenceTap(t, d)
	waitUntil(t, func() bool { return d.ipcSrv.SessionCount() == 1 }, "the test client was never registered")

	folded, fresh, err := d.createDirectWithDefaults(alicePhoneID)
	if err != nil || fresh || folded.ID != alice.ID {
		t.Fatalf("precondition: the phone's pairing did not fold into Alice's chat (fresh=%v, err=%v)", fresh, err)
	}
	expectFrame(t, next, "after the fold", presenceFrame{ipc.FrameChatPresenceChanged, string(alice.ID), presence.EffectiveAvailable})
}

func TestFold_IntoAChatWithNoPairingYet_PushesTheChatPresence(t *testing.T) {
	d := minimalDaemon(t, "http://127.0.0.1:1")
	d.peerMeta = peerstate.NewMeta(d.store)
	if _, _, err := d.peerMeta.SetFingerprint(alicePhoneID, "fp-alice"); err != nil {
		t.Fatal(err)
	}
	noPairing, err := d.chats.CreateDirectEdgeless(chat.ChatID("chat-from-my-other-device"), "fp-alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := d.chatPresence(noPairing); ok {
		t.Fatal("precondition: a chat with no pairing has a presence")
	}
	d.presenceCache.ObserveTechnical(alicePhoneID)
	next := presenceTap(t, d)
	waitUntil(t, func() bool { return d.ipcSrv.SessionCount() == 1 }, "the test client was never registered")

	folded, fresh, err := d.createDirectWithDefaults(alicePhoneID)
	if err != nil || fresh || folded.ID != noPairing.ID {
		t.Fatalf("precondition: the pairing did not fold into the chat with no pairing (fresh=%v, err=%v)", fresh, err)
	}
	expectFrame(t, next, "after the fold", presenceFrame{ipc.FrameChatPresenceChanged, string(noPairing.ID), presence.EffectiveAccepting})
}

func TestDropRoutesAndDropPairing_PushTheChatPresence(t *testing.T) {
	const aliceTabletID = "000000000000000000000000000a1bbb"
	d, _, aliceChat, _ := personDaemon(t)
	if err := d.chats.AttachPeerID(aliceChat, aliceTabletID); err != nil {
		t.Fatal(err)
	}
	d.presenceCache.ObserveHuman(alicePCID, presence.EffectiveBusy)
	d.presenceCache.ObserveTechnical(alicePhoneID)
	d.presenceCache.ObserveHuman(aliceTabletID, presence.EffectiveAvailable)
	next := presenceTap(t, d)
	waitUntil(t, func() bool { return d.ipcSrv.SessionCount() == 1 }, "the test client was never registered")

	after, changed, err := d.dropRoutes(aliceChat, alicePhoneID, []string{alicePCID})
	if err != nil || !changed || after.PeerID != alicePhoneID {
		t.Fatalf("precondition: the first pairing did not move to the phone (changed=%v, err=%v)", changed, err)
	}
	expectFrame(t, next, "the busy pairing left", presenceFrame{ipc.FrameChatPresenceChanged, string(aliceChat), presence.EffectiveAvailable})

	if _, err := d.dropPairing(aliceChat, aliceTabletID); err != nil {
		t.Fatal(err)
	}
	expectFrame(t, next, "the green pairing left", presenceFrame{ipc.FrameChatPresenceChanged, string(aliceChat), presence.EffectiveAccepting})
}

func TestRoutingSetWrites_AllPushTheChatPresence(t *testing.T) {
	allowed := map[string]string{
		"DropRoutes":   "dropRoutes",
		"DropPairing":  "dropPairing",
		"AttachPeerID": "foldByFingerprint",
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	seen := map[string]int{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				want, watched := allowed[sel.Sel.Name]
				if !watched {
					return true
				}
				seen[sel.Sel.Name]++
				if fn.Name.Name != want {
					t.Errorf("%s: %s calls %s; a write to a routing set goes through %s, which pushes the chat presence",
						fset.Position(call.Pos()), fn.Name.Name, sel.Sel.Name, want)
				}
				return true
			})
		}
	}
	for method := range allowed {
		if seen[method] == 0 {
			t.Errorf("no call to %s found: this check reads nothing", method)
		}
	}
}
