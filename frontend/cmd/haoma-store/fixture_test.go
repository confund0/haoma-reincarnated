package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/files"
	"haoma-frontend/internal/linkeddevice"
	"haoma-frontend/internal/peerstate"
	"haoma-frontend/internal/store"
)

func TestMain(m *testing.M) {
	store.DefaultKDFParams = store.KDFParams{
		Time: 1, Memory: 8, Threads: 1, KeyLen: 32, SaltLen: 16,
	}
	os.Exit(m.Run())
}

const fixturePassphrase = "fixture"

var aliceIdentity = bytes.Repeat([]byte{0xa1}, 33)

var eddieIdentity = bytes.Repeat([]byte{0xed}, 33)

type builder struct {
	t       *testing.T
	dataDir string
	st      *store.Store
	chats   *chat.Store
	log     *events.Log
	files   *files.Manager
	meta    *peerstate.Meta
	devices *linkeddevice.Store
	now     time.Time
}

func newStore(t *testing.T, accountName string) *builder {
	return newStoreOwnedBy(t, accountName, aliceIdentity)
}

func newStoreOwnedBy(t *testing.T, accountName string, identity []byte) *builder {
	t.Helper()
	account := filepath.Join(t.TempDir(), accountName)
	dataDir := filepath.Join(account, "frontend")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := store.Unlock(dataDir, fixturePassphrase)
	if err != nil {
		t.Fatalf("create fixture store: %v", err)
	}
	now := time.Date(2026, 8, 15, 15, 0, 0, 0, time.Local)
	fm, err := files.NewManager(st, dataDir)
	if err != nil {
		t.Fatalf("files manager: %v", err)
	}
	if err := st.Put([]byte(identityKey), identity); err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	return &builder{
		t:       t,
		dataDir: dataDir,
		st:      st,
		chats:   chat.NewStore(st),
		log:     events.New(st, events.NewBus(), func() time.Time { return now }),
		files:   fm,
		meta:    peerstate.NewMeta(st),
		devices: linkeddevice.New(st),
		now:     now,
	}
}

func (b *builder) close() string {
	b.t.Helper()
	if err := b.st.Lock(); err != nil {
		b.t.Fatalf("close fixture store: %v", err)
	}
	b.st = nil
	return b.dataDir
}

func (b *builder) pairedDirectly(peerID, fingerprint, nick string) chat.ChatID {
	b.t.Helper()
	b.knowContact(peerID, fingerprint, nick)
	dc, err := b.chats.CreateDirect(peerID)
	if err != nil {
		b.t.Fatalf("create conversation by pairing: %v", err)
	}
	return dc.ID
}

func (b *builder) receivedFromSibling(chatID chat.ChatID, fingerprint, peerID, nick string) chat.ChatID {
	b.t.Helper()
	if _, err := b.chats.CreateDirectEdgeless(chatID, fingerprint); err != nil {
		b.t.Fatalf("create conversation by backfill: %v", err)
	}
	if peerID != "" {
		b.knowContact(peerID, fingerprint, nick)
		if err := b.chats.AttachPeerID(chatID, peerID); err != nil {
			b.t.Fatalf("attach pairing: %v", err)
		}
	}
	return chatID
}

func (b *builder) knowContact(peerID, fingerprint, nick string) {
	b.t.Helper()
	if _, _, err := b.meta.SetFingerprint(peerID, fingerprint); err != nil {
		b.t.Fatalf("set fingerprint: %v", err)
	}
	if _, err := b.meta.SetNick(peerID, nick, b.now.Unix()); err != nil {
		b.t.Fatalf("set nick: %v", err)
	}
}

func (b *builder) textOut(chatID chat.ChatID, msgID, text, deliveryState string, at time.Time) {
	b.t.Helper()
	body, err := json.Marshal(events.TextBody{Text: text})
	if err != nil {
		b.t.Fatal(err)
	}
	if _, err := b.log.AppendOutbound(events.OutboundParams{
		ChatID:        chatID,
		Kind:          events.KindText,
		DisplayTs:     at.Unix(),
		MsgID:         msgID,
		EnvelopeID:    "env-" + msgID,
		Body:          body,
		DeliveryState: deliveryState,
	}); err != nil {
		b.t.Fatalf("append outbound: %v", err)
	}
}

func (b *builder) mirroredTextOut(chatID chat.ChatID, msgID, text string, at time.Time) {
	b.t.Helper()
	body, err := json.Marshal(events.TextBody{Text: text})
	if err != nil {
		b.t.Fatal(err)
	}
	if _, err := b.log.AppendOutbound(events.OutboundParams{
		ChatID:              chatID,
		Kind:                events.KindText,
		DisplayTs:           at.Unix(),
		MsgID:               msgID,
		Body:                body,
		MirroredFromSibling: true,
	}); err != nil {
		b.t.Fatalf("append mirrored outbound: %v", err)
	}
}

func (b *builder) textIn(chatID chat.ChatID, msgID, text string, at time.Time) {
	b.t.Helper()
	body, err := json.Marshal(events.TextBody{Text: text})
	if err != nil {
		b.t.Fatal(err)
	}
	if _, err := b.log.AppendInbound(events.InboundParams{
		ChatID:     chatID,
		Kind:       events.KindText,
		DisplayTs:  at.Unix(),
		SenderTs:   at.Unix(),
		MsgID:      msgID,
		EnvelopeID: "env-" + msgID,
		Body:       body,
	}); err != nil {
		b.t.Fatalf("append inbound: %v", err)
	}
}

func (b *builder) markReadCursor(chatID chat.ChatID, at time.Time) {
	b.t.Helper()
	if _, err := b.chats.BumpActivity(chatID, at.Unix()); err != nil {
		b.t.Fatalf("bump activity: %v", err)
	}
	if _, err := b.chats.ClearUnread(chatID); err != nil {
		b.t.Fatalf("clear unread: %v", err)
	}
}

func (b *builder) markRead(msgID string, at time.Time) {
	b.t.Helper()
	if _, _, err := b.log.ApplyInboundReadAt(msgID, at.Unix()); err != nil {
		b.t.Fatalf("mark read: %v", err)
	}
}

func (b *builder) attachment(chatID chat.ChatID, msgID, name, caption, transferState string, at time.Time) {
	b.t.Helper()
	body, err := json.Marshal(map[string]any{
		"name": name, "size": 1024, "state": transferState, "caption": caption,
	})
	if err != nil {
		b.t.Fatal(err)
	}
	if _, err := b.log.AppendInbound(events.InboundParams{
		ChatID:     chatID,
		Kind:       events.KindFile,
		DisplayTs:  at.Unix(),
		SenderTs:   at.Unix(),
		MsgID:      msgID,
		EnvelopeID: "env-" + msgID,
		Body:       body,
	}); err != nil {
		b.t.Fatalf("append file event: %v", err)
	}
	if err := b.files.PutMeta(files.Metadata{
		MsgID:            msgID,
		ChatID:           chatID,
		Direction:        files.DirIn,
		Token:            "token-" + msgID,
		OriginalName:     name,
		Size:             1024,
		Sha256Ciphertext: "00ff",
		State:            files.State(transferState),
		CreatedAt:        at.Unix(),
	}); err != nil {
		b.t.Fatalf("put file row: %v", err)
	}
}

func (b *builder) pairedButPersonUnknown(peerID, nick string) chat.ChatID {
	b.t.Helper()
	if _, err := b.meta.SetNick(peerID, nick, b.now.Unix()); err != nil {
		b.t.Fatalf("set nick: %v", err)
	}
	dc, err := b.chats.CreateDirect(peerID)
	if err != nil {
		b.t.Fatalf("create conversation: %v", err)
	}
	return dc.ID
}

func (b *builder) alias(peerID, name string, at time.Time) {
	b.t.Helper()
	if _, err := b.meta.SetAlias(peerID, name, at.Unix()); err != nil {
		b.t.Fatalf("set alias: %v", err)
	}
}

func (b *builder) disableReadReceipts(chatID chat.ChatID) {
	b.t.Helper()
	if err := b.chats.SetDisableReadReceipts(chatID, true); err != nil {
		b.t.Fatalf("disable read receipts: %v", err)
	}
}

func (b *builder) linkDevice(peerID string) {
	b.t.Helper()
	if _, err := b.devices.Link(peerID, b.now.Unix()); err != nil {
		b.t.Fatalf("link device: %v", err)
	}
	own, err := b.st.Get([]byte(identityKey))
	if err != nil {
		b.t.Fatalf("read own identity: %v", err)
	}
	if err := b.st.Put([]byte(remoteIdentityPrefix+peerID), own); err != nil {
		b.t.Fatalf("seed sibling identity: %v", err)
	}
}

func (b *builder) linkDeviceWithoutSession(peerID string) {
	b.t.Helper()
	if _, err := b.devices.Link(peerID, b.now.Unix()); err != nil {
		b.t.Fatalf("link device: %v", err)
	}
}

func openSides(t *testing.T, dirA, dirB string) (*side, *side) {
	t.Helper()
	t.Setenv(envPassphrase, fixturePassphrase)
	a, err := openSide(dirA, "a", "", t.TempDir())
	if err != nil {
		t.Fatalf("open a: %v", err)
	}
	t.Cleanup(a.close)
	b, err := openSide(dirB, "b", "", t.TempDir())
	if err != nil {
		t.Fatalf("open b: %v", err)
	}
	t.Cleanup(b.close)
	return a, b
}

func openOne(t *testing.T, dir string) *side {
	t.Helper()
	t.Setenv(envPassphrase, fixturePassphrase)
	s, err := openSide(dir, "a", "", t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(s.close)
	return s
}

const bobFingerprint = "3f9a1c2d4e5f60718293a4b5c6d7e8f9"

func pairOfDevices(t *testing.T) (*builder, *builder, chat.ChatID, chat.ChatID) {
	t.Helper()

	a := newStore(t, "alice")
	chatA := a.pairedDirectly("pairing-a1", bobFingerprint, "Bob")

	b := newStore(t, "alice-linked")
	chatB := b.receivedFromSibling(chatA, bobFingerprint, "pairing-b1", "Bob")

	return a, b, chatA, chatB
}

func pairOfDevicesBeforeRepair(t *testing.T) (*builder, *builder, chat.ChatID, chat.ChatID) {
	t.Helper()
	a := newStore(t, "alice")
	chatA := a.pairedDirectly("pairing-a1", bobFingerprint, "Bob")
	b := newStore(t, "alice-linked")
	chatB := b.receivedFromSibling(chatA, bobFingerprint, "", "")
	return a, b, chatA, chatB
}

func pairOfDevicesDifferentChatIDs(t *testing.T) (*builder, *builder, chat.ChatID, chat.ChatID) {
	t.Helper()
	a := newStore(t, "alice")
	chatA := a.pairedDirectly("pairing-a1", bobFingerprint, "Bob")
	b := newStore(t, "alice-linked")
	chatB := b.receivedFromSibling("2222222222222222bbbbbbbbbbbbbbbb", bobFingerprint, "pairing-b1", "Bob")
	return a, b, chatA, chatB
}

func atSeconds(seconds int) time.Time {
	return time.Date(2026, 8, 15, 15, 0, 0, 0, time.Local).Add(time.Duration(seconds) * time.Second)
}

func at(minutes int) time.Time {
	return time.Date(2026, 8, 15, 15, 0, 0, 0, time.Local).Add(time.Duration(minutes) * time.Minute)
}

func runDiffInto(t *testing.T, a, b *side) (*diffResult, string) {
	t.Helper()
	var out capture
	res, err := runDiff(a, b, &out)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	return res, out.String()
}

func runFindInto(t *testing.T, sides []*side, f findFilter, idsOnly bool) (*findResult, string) {
	t.Helper()
	var out capture
	res, err := runFind(sides, f, idsOnly, &out)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	return res, out.String()
}

type capture struct{ b []byte }

func (c *capture) Write(p []byte) (int, error) {
	c.b = append(c.b, p...)
	return len(p), nil
}

func (c *capture) String() string { return string(c.b) }

func mustNotContain(t *testing.T, report, want string) {
	t.Helper()
	if contains(report, want) {
		t.Fatalf("report holds %q and should not\n--- report ---\n%s", want, report)
	}
}

func mustContain(t *testing.T, report, want string) {
	t.Helper()
	if !contains(report, want) {
		t.Fatalf("report does not hold %q\n--- report ---\n%s", want, report)
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
