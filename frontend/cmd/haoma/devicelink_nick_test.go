package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/signal"
	"haoma-frontend/internal/store"
)

const nickLinkPeerID = "0000000000000000000000000000c0de"

const bundleNickAt int64 = 1756200000

func identityBundlePlaintext(t *testing.T, selfNick string, selfNickAt int64) []byte {
	t.Helper()
	remote, err := signal.Bootstrap(1)
	if err != nil {
		t.Fatalf("bootstrap the offering identity: %v", err)
	}
	privArr := remote.IdentityKeyPair.PrivateKey().Serialize()
	msgID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	wrapper, err := msg.BuildDeviceLinkIdentity(1, time.Now().Unix(), msgID,
		remote.IdentityKeyPair.PublicKey().Serialize(), privArr[:], selfNick, selfNickAt)
	if err != nil {
		t.Fatalf("build identity bundle: %v", err)
	}
	plain, err := msg.Marshal(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	return plain
}

func openSentIdentityBundle(t *testing.T, d *daemon, stub *haomadStub) *msg.DeviceLinkIdentityBody {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	msgID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	inbound, err := msg.BuildText(7, time.Now().Unix(), msgID, "hello", 0, "", "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	blob, peerCipher := encryptAsAliceKeepingCipher(t, d, nickLinkPeerID, inbound)
	processInboxEntry(ctx, d, railEntry(nickLinkPeerID, "env-nick-link-1", blob))

	before := len(stub.SentPayloads)
	if err := d.sendIdentityBundle(ctx, nickLinkPeerID); err != nil {
		t.Fatalf("sendIdentityBundle: %v", err)
	}
	sent := stub.SentPayloads[before:]
	if len(sent) != 1 {
		t.Fatalf("the bundle produced %d sends, want exactly 1", len(sent))
	}
	plain, err := peerCipher.Decrypt(ctx, "bob-self", sent[0])
	if err != nil {
		t.Fatalf("the joining device could not open the bundle: %v", err)
	}
	wrapper, err := msg.Unmarshal(plain)
	if err != nil {
		t.Fatal(err)
	}
	if wrapper.Kind != msg.KindDeviceLinkIdentity {
		t.Fatalf("the send is kind %q, want %q", wrapper.Kind, msg.KindDeviceLinkIdentity)
	}
	body, err := wrapper.DeviceLinkIdentity()
	if err != nil {
		t.Fatalf("decode the sent bundle: %v", err)
	}
	return body
}

func TestDeviceLink_Bundle_CarriesTheSelfNick(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)

	if _, err := d.setSelfNick("mara"); err != nil {
		t.Fatalf("set the offering device's nick: %v", err)
	}

	body := openSentIdentityBundle(t, d, stub)
	if body.SelfNick != "mara" {
		t.Fatalf("the bundle has self_nick %q, want %q. The joining device acts on this field, not on a log line", body.SelfNick, "mara")
	}

	held, err := loadSelfNickState(d.store)
	if err != nil {
		t.Fatalf("read this device's own stamp: %v", err)
	}
	if body.SelfNickAt != held.at {
		t.Fatalf("the bundle has self_nick_at %d, want %d — the stamp this device holds", body.SelfNickAt, held.at)
	}
	if body.SelfNickAt == 0 {
		t.Fatal("the bundle has no stamp at all")
	}
}

func TestDeviceLink_Bundle_OmitsThePlaceholderSelfNick(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)

	if err := d.loadSelfNickInto(); err != nil {
		t.Fatalf("load the offering device's nick: %v", err)
	}
	if got := d.selfNick(); got != defaultSelfNick {
		t.Fatalf("the fixture is not a first-run device: nick is %q, want %q", got, defaultSelfNick)
	}

	body := openSentIdentityBundle(t, d, stub)
	if body.SelfNick != "" {
		t.Fatalf("the bundle carries self_nick %q; a device still on the placeholder must carry none", body.SelfNick)
	}
}

func TestDeviceLink_Adopt_TakesTheSelfNickFromTheBundle(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	if err := d.loadSelfNickInto(); err != nil {
		t.Fatal(err)
	}

	if _, _, err := d.adoptIdentityBundle(identityBundlePlaintext(t, "mara", bundleNickAt)); err != nil {
		t.Fatalf("adoptIdentityBundle: %v", err)
	}

	if got := d.selfNick(); got != "mara" {
		t.Errorf("the daemon's cached nick is %q, want %q", got, "mara")
	}
	stored, isDefault, err := loadSelfNick(d.store)
	if err != nil {
		t.Fatalf("read the stored nick: %v", err)
	}
	if stored != "mara" {
		t.Errorf("the stored nick is %q, want %q — the cache moved and the store did not", stored, "mara")
	}
	if isDefault {
		t.Error("the joining device still reports the placeholder, so it would keep drawing the status line asking for a nick it already has")
	}

	cur, err := loadSelfNickState(d.store)
	if err != nil {
		t.Fatalf("read the stored stamp: %v", err)
	}
	if cur.at != bundleNickAt {
		t.Errorf("the stored stamp is %d, want %d — adoption stamped the name locally", cur.at, bundleNickAt)
	}
}

func TestDeviceLink_Adopt_RefusesAnInvalidSelfNickAndKeepsItsOwn(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	if _, err := d.setSelfNick("joiner"); err != nil {
		t.Fatalf("set the joining device's own nick: %v", err)
	}

	fingerprint, reason, err := d.adoptIdentityBundle(identityBundlePlaintext(t, "ma\x01ra", bundleNickAt))
	if err != nil {
		t.Fatalf("a refused nick failed the whole link (reason %q): %v — the identity still installed and the link must stand", reason, err)
	}
	if fingerprint == "" {
		t.Fatal("adoption returned no fingerprint, so the identity did not install")
	}

	if got := d.selfNick(); got != "joiner" {
		t.Errorf("the daemon's cached nick is %q, want %q — a refused nick overwrote it", got, "joiner")
	}
	stored, _, err := loadSelfNick(d.store)
	if err != nil {
		t.Fatalf("read the stored nick: %v", err)
	}
	if stored != "joiner" {
		t.Errorf("the stored nick is %q, want %q — a refused nick reached the store", stored, "joiner")
	}
}

func TestDeviceLink_Adopt_ABundleWithNoNickLeavesAnExistingNameAlone(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	if _, err := d.setSelfNick("joiner"); err != nil {
		t.Fatalf("set the joining device's own nick: %v", err)
	}

	if _, _, err := d.adoptIdentityBundle(identityBundlePlaintext(t, "", bundleNickAt)); err != nil {
		t.Fatalf("adoptIdentityBundle: %v", err)
	}

	if got := d.selfNick(); got != "joiner" {
		t.Errorf("the daemon's cached nick is %q, want %q — a bundle carrying no nick overwrote it", got, "joiner")
	}
	stored, isDefault, err := loadSelfNick(d.store)
	if err != nil {
		t.Fatalf("read the stored nick: %v", err)
	}
	if stored != "joiner" {
		t.Errorf("the stored nick is %q, want %q — a bundle carrying no nick reached the store", stored, "joiner")
	}

	if isDefault {
		t.Error("the joining device now reports the placeholder, so something wrote over the name it had")
	}
}

func TestDeviceLink_Adopt_ABundleWithNoNickWritesNoRowOnAFirstRunDevice(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	if err := d.loadSelfNickInto(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.store.Get([]byte(settingNickKey)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the fixture is not a first-run device: the nick row already exists (err = %v)", err)
	}

	if _, _, err := d.adoptIdentityBundle(identityBundlePlaintext(t, "", bundleNickAt)); err != nil {
		t.Fatalf("adoptIdentityBundle: %v", err)
	}

	if _, err := d.store.Get([]byte(settingNickKey)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a bundle carrying no nick wrote a nick row (err = %v); the row must stay absent until the user sets one", err)
	}
	if got := d.selfNick(); got != defaultSelfNick {
		t.Errorf("the daemon's cached nick is %q, want %q", got, defaultSelfNick)
	}
	if !d.selfNickIsDefault() {
		t.Error("the joining device stopped reporting the placeholder, so it would no longer ask the user to set a nick")
	}
}

func TestDeviceLink_UnsolicitedBundle_DoesNotRenameThisDevice(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	if _, err := d.setSelfNick("mine"); err != nil {
		t.Fatalf("set this device's nick: %v", err)
	}

	const peerID = "00000000000000000000000000005556"
	processInboxEntry(ctx, d, railEntry(peerID, "env-hostile-nick-1",
		hostileIdentityBundle(t, d, "attacker")))

	if got := d.selfNick(); got != "mine" {
		t.Errorf("the daemon's cached nick is %q, want %q — an unsolicited bundle renamed this device", got, "mine")
	}
	stored, _, err := loadSelfNick(d.store)
	if err != nil {
		t.Fatalf("read the stored nick: %v", err)
	}
	if stored != "mine" {
		t.Errorf("the stored nick is %q, want %q — an unsolicited bundle reached the store", stored, "mine")
	}
}

func TestDeviceLink_Adopt_TellsTheAttachedClients(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	if err := d.loadSelfNickInto(); err != nil {
		t.Fatal(err)
	}
	client := dialTest(t, ctx, addr, certPath, token)

	if _, _, err := d.adoptIdentityBundle(identityBundlePlaintext(t, "mara", bundleNickAt)); err != nil {
		t.Fatalf("adoptIdentityBundle: %v", err)
	}

	f := readUntil(t, ctx, client, ipc.FrameNick)
	var p ipc.NickPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		t.Fatalf("decode the pushed nick: %v", err)
	}
	if p.Nick != "mara" {
		t.Errorf("the push carries nick %q, want %q", p.Nick, "mara")
	}
	if p.IsDefault {
		t.Error("the push still says the nick is the placeholder, so a client would keep drawing the status line asking for a name")
	}
}
