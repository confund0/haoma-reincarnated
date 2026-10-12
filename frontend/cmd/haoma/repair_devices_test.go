package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/coder/websocket"
	"go.mau.fi/libsignal/protocol"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/invitation"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/pair"
	"haoma-frontend/internal/peerstate"
	"haoma-frontend/internal/syncrail"
)

const (
	fpLow  = "05aa"
	fpHigh = "05bb"
)

type world struct {
	paired    map[[2]int]bool
	regMine   [2]uint32
	regTheirs [2]uint32

	devicesTheirs int
}

func (w world) inputsFor(m int, mine, theirs string) pairingInputs {
	in := pairingInputs{reported: true, mine: mine, theirs: theirs}
	for t := 0; t < w.devicesTheirs; t++ {
		if w.paired[[2]int{m, t}] {
			in.own = append(in.own, w.regTheirs[t])
		}
		if w.paired[[2]int{1 - m, t}] {
			in.sibling = append(in.sibling, w.regTheirs[t])
		}
	}
	return in
}

func (w world) inputsForTheirs(t int, mine, theirs string) pairingInputs {
	in := pairingInputs{reported: true, mine: theirs, theirs: mine}
	other := 1 - t
	for m := 0; m < 2; m++ {
		if w.paired[[2]int{m, t}] {
			in.own = append(in.own, w.regMine[m])
		}
		if w.devicesTheirs == 2 && w.paired[[2]int{m, other}] {
			in.sibling = append(in.sibling, w.regMine[m])
		}
	}
	return in
}

func planOffers(p pairingPlan, device uint32) bool {
	return p.offer && slices.Contains(p.missing, device)
}

func TestPairingPlan_EveryStartConvergesAndOnlyOneSideOffers(t *testing.T) {
	for _, devicesTheirs := range []int{1, 2} {
		pairs := [][2]int{{0, 0}, {1, 0}}
		if devicesTheirs == 2 {
			pairs = append(pairs, [2]int{0, 1}, [2]int{1, 1})
		}
		for mask := 1; mask < 1<<len(pairs); mask++ {
			for _, order := range [][2]string{{fpLow, fpHigh}, {fpHigh, fpLow}} {
				mine, theirs := order[0], order[1]
				w := world{
					paired:        map[[2]int]bool{},
					regMine:       [2]uint32{300, 100},
					regTheirs:     [2]uint32{900, 50},
					devicesTheirs: devicesTheirs,
				}
				for i, p := range pairs {
					if mask&(1<<i) != 0 {
						w.paired[p] = true
					}
				}
				name := fmt.Sprintf("devices=%d start=%b mine=%s", devicesTheirs, mask, mine)
				for round := 0; round < 4; round++ {
					var add [][2]int
					for _, p := range pairs {
						if w.paired[p] {
							continue
						}
						m, th := p[0], p[1]
						fromMe := planOffers(planPairings(w.inputsFor(m, mine, theirs)), w.regTheirs[th])
						fromThem := planOffers(planPairings(w.inputsForTheirs(th, mine, theirs)), w.regMine[m])
						if fromMe && fromThem {
							t.Fatalf("%s round %d: pairing %v offered from both ends", name, round, p)
						}
						if fromMe || fromThem {
							add = append(add, p)
						}
					}
					for _, p := range add {
						w.paired[p] = true
					}
				}
				for _, p := range pairs {
					if !w.paired[p] {
						t.Errorf("%s: pairing %v never made", name, p)
					}
				}
			}
		}
	}
}

func TestPairingPlan_Cases(t *testing.T) {
	cases := []struct {
		name        string
		in          pairingInputs
		wantMissing []uint32
		wantOffer   bool
		wantDevices int
		wantPaired  int
	}{
		{
			name:        "a new device and a contact with two devices: one offer each",
			in:          pairingInputs{sibling: []uint32{900, 50}, reported: true, mine: fpHigh, theirs: fpLow},
			wantMissing: []uint32{50, 900},
			wantOffer:   true, wantDevices: 2,
		},
		{
			name:        "an old device sees its sibling paired with a device it is not: lower fingerprint offers",
			in:          pairingInputs{own: []uint32{50}, sibling: []uint32{50, 900}, reported: true, mine: fpLow, theirs: fpHigh},
			wantMissing: []uint32{900},
			wantOffer:   true, wantDevices: 2, wantPaired: 1,
		},
		{
			name:        "the same with the higher fingerprint: it waits",
			in:          pairingInputs{own: []uint32{50}, sibling: []uint32{50, 900}, reported: true, mine: fpHigh, theirs: fpLow},
			wantMissing: []uint32{900},
			wantOffer:   false, wantDevices: 2, wantPaired: 1,
		},
		{
			name:        "paired with every device the sibling knows: nothing missing",
			in:          pairingInputs{own: []uint32{50, 900}, sibling: []uint32{900}, reported: true, mine: fpLow, theirs: fpHigh},
			wantDevices: 2, wantPaired: 2,
		},
		{
			name:        "no report from the sibling and no pairing here: one legacy offer",
			in:          pairingInputs{mine: fpHigh, theirs: fpLow},
			wantMissing: []uint32{0},
			wantOffer:   true,
		},
		{
			name:        "no report and a pairing here: nothing known to be missing",
			in:          pairingInputs{own: []uint32{50}, mine: fpLow, theirs: fpHigh},
			wantDevices: 1, wantPaired: 1,
		},
		{
			name:        "a live invitation from the person: nothing offered, whatever the rule says",
			in:          pairingInputs{own: []uint32{50}, sibling: []uint32{50, 900}, reported: true, invited: true, mine: fpLow, theirs: fpHigh},
			wantMissing: []uint32{900},
			wantOffer:   false, wantDevices: 2, wantPaired: 1,
		},
		{
			name:        "a pairing without a session yet: nothing offered until it can be named",
			in:          pairingInputs{pending: 1, sibling: []uint32{50, 900}, reported: true, mine: fpLow, theirs: fpHigh},
			wantDevices: 2, wantPaired: 1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := planPairings(c.in)
			if !reflect.DeepEqual(got.missing, c.wantMissing) {
				t.Errorf("missing = %+v, want %+v", got.missing, c.wantMissing)
			}
			if len(c.wantMissing) > 0 && got.offer != c.wantOffer {
				t.Errorf("offer = %v, want %v", got.offer, c.wantOffer)
			}
			if got.devices != c.wantDevices || got.paired != c.wantPaired {
				t.Errorf("devices/paired = %d/%d, want %d/%d", got.devices, got.paired, c.wantDevices, c.wantPaired)
			}
		})
	}
}

func TestChooseOfferTarget(t *testing.T) {
	plan := pairingPlan{offer: true, missing: []uint32{50, 900}}
	if got, open := chooseOfferTarget(plan, nil); got != 50 || !open {
		t.Errorf("no note: target %d open=%v, want 50 open", got, open)
	}
	one := []invitation.Note{{Device: 50, MintedAt: 10}}
	if got, open := chooseOfferTarget(plan, one); got != 900 || !open {
		t.Errorf("device 50 covered: target %d open=%v, want 900 open", got, open)
	}
	both := []invitation.Note{{Device: 50, MintedAt: 20}, {Device: 900, MintedAt: 10}}
	if got, open := chooseOfferTarget(plan, both); got != 900 || open {
		t.Errorf("both covered: target %d open=%v, want the oldest (900), not open", got, open)
	}
	legacy := pairingPlan{offer: true, legacy: true, missing: []uint32{0}}
	if got, _ := chooseOfferTarget(legacy, []invitation.Note{{MintedAt: 10}}); got != 0 {
		t.Errorf("legacy: target %d, want 0", got)
	}
	if !stillMissing(plan, 0) || stillMissing(pairingPlan{}, 0) || !stillMissing(plan, 900) || stillMissing(plan, 7) {
		t.Error("stillMissing: a device-less offer stands while any device is missing; a named one while it is")
	}
}

func TestPickPassOnRoute(t *testing.T) {
	two := []passOnCandidate{
		{peerID: "p-named", session: true, device: 900},
		{peerID: "p-other", session: true, device: 50},
	}
	cases := []struct {
		name      string
		cands     []passOnCandidate
		device    uint32
		wantRoute string
		wantWait  bool
	}{
		{"two routes, device 50", two, 50, "p-other", false},
		{"two routes, device 900", two, 900, "p-named", false},
		{"two routes, an unknown device: dropped", two, 7, "", false},
		{"two routes, no device named: the first", two, 0, "p-named", false},
		{"one route, its device", two[:1], 900, "p-named", false},
		{"one route, the other device: dropped", two[:1], 50, "", false},
		{"no route yet: waits", nil, 50, "", true},
		{"one route without a session: waits", []passOnCandidate{{peerID: "p-new"}}, 50, "", true},
		{"one with a session, one without, the other device: waits", []passOnCandidate{{peerID: "p-new"}, {peerID: "p-old", session: true, device: 7}}, 50, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			route, wait, _ := pickPassOnRoute(c.cands, c.device)
			if route != c.wantRoute || wait != c.wantWait {
				t.Errorf("route=%q wait=%v, want route=%q wait=%v", route, wait, c.wantRoute, c.wantWait)
			}
		})
	}
}

func registrationOf(t *testing.T, ctx context.Context, d *daemon, peerID string) uint32 {
	t.Helper()
	id, ok, err := d.stores.RemoteRegistrationID(ctx, protocol.NewSignalAddress(peerID, pair.DeviceID))
	if err != nil || !ok {
		t.Fatalf("no registration id for %s: %v %v", peerID, ok, err)
	}
	return id
}

func twoDevicesOfThePerson(t *testing.T, ctx context.Context, d *daemon, stub *haomadStub) (low, high string) {
	t.Helper()
	const second = "0000000000000000000000000000cafe"
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)
	firstMessageFrom(t, ctx, d, railPeerID, "env-dev-1")
	stub.WaitForSend(t, 1, 5*time.Second)
	if _, _, err := d.peerMeta.SetFingerprint(second, railFingerprint); err != nil {
		t.Fatal(err)
	}
	if err := d.chats.AttachPeerID(chatID, second); err != nil {
		t.Fatal(err)
	}
	firstMessageOnOPK(t, ctx, d, second, "env-dev-2", 1)
	stub.WaitForSend(t, 1, 5*time.Second)
	a, b := registrationOf(t, ctx, d, railPeerID), registrationOf(t, ctx, d, second)
	if a == b {
		t.Fatal("precondition: the two pairings record one registration id")
	}
	if a < b {
		return railPeerID, second
	}
	return second, railPeerID
}

func TestInvitation_PassOnFollowsTheNamedDevice(t *testing.T) {
	ctx := context.Background()
	for _, pick := range []string{"low", "high"} {
		t.Run(pick, func(t *testing.T) {
			stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
			d, _, _, _ := newTestDaemon(t, stub)
			linkSibling(t, d, railSiblingID)
			low, high := twoDevicesOfThePerson(t, ctx, d, stub)
			want, other := low, high
			if pick == "high" {
				want, other = high, low
			}

			body := passOnBody(time.Now().Add(24 * time.Hour).Unix())
			body.Device = registrationOf(t, ctx, d, want)
			forwardInvitation(ctx, d, railSiblingID, body)
			stub.WaitForSend(t, 1, 5*time.Second)

			if got := passOnsTo(stub, want); got != 1 {
				t.Fatalf("%d pass-on(s) to the named device, want one (sent to %v, kinds %q)", got, stub.SentPeerIDs, stub.SentKinds)
			}
			if got := passOnsTo(stub, other); got != 0 {
				t.Fatalf("%d pass-on(s) to the other device", got)
			}
		})
	}

	t.Run("a device this device has no pairing with: dropped", func(t *testing.T) {
		stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
		d, _, _, _ := newTestDaemon(t, stub)
		linkSibling(t, d, railSiblingID)
		low, high := twoDevicesOfThePerson(t, ctx, d, stub)
		before := stub.SendCalls

		body := passOnBody(time.Now().Add(24 * time.Hour).Unix())
		body.Device = registrationOf(t, ctx, d, low) ^ registrationOf(t, ctx, d, high) | 1
		done := make(chan struct{})
		go func() {
			defer close(done)
			forwardInvitation(ctx, d, railSiblingID, body)
		}()
		awaitDone(t, done, "the pass-on waited for a device the person does not have")
		if stub.SendCalls != before || passOnsTo(stub, low)+passOnsTo(stub, high) != 0 {
			t.Fatalf("a dropped invitation was sent: %d call(s) before, %d after", before, stub.SendCalls)
		}
		if applied, err := railMirrorApplied(d.store, body.ID); err != nil || applied {
			t.Fatalf("a dropped invitation was ledgered: %v %v", applied, err)
		}
	})
}

func TestContactsPushBody_CarriesThePairedDevices(t *testing.T) {
	ctx := context.Background()
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	low, high := twoDevicesOfThePerson(t, ctx, d, stub)

	body := contactsPushBody(d, []string{railFingerprint})
	if len(body.Contacts) != 1 {
		t.Fatalf("%d contacts pushed, want one", len(body.Contacts))
	}
	want := []uint32{registrationOf(t, ctx, d, low), registrationOf(t, ctx, d, high)}
	if got := body.Contacts[0].PairedDevices; !reflect.DeepEqual(got, want) {
		t.Errorf("paired devices = %v, want %v", got, want)
	}
	if body.Contacts[0].PairedDevicesAtMs <= 0 {
		t.Error("the report carries no time, so it reads as an older build's silence")
	}
}

func TestApplyContacts_KeepsTheSiblingReport(t *testing.T) {
	d := inheritedTestDaemon(t)
	linkSibling(t, d, railSiblingID)
	logger := slog.Default()
	snap := func(devices []uint32, at int64) []syncrail.ContactSnapshot {
		return []syncrail.ContactSnapshot{{Fingerprint: railFingerprint, PairedDevices: devices, PairedDevicesAtMs: at}}
	}
	applyContacts(d, logger, railSiblingID, snap([]uint32{50, 900}, 2000))
	applyContacts(d, logger, railSiblingID, snap([]uint32{50}, 1000))
	got, reported, err := siblingReport(d, railFingerprint)
	if err != nil || !reported || !reflect.DeepEqual(got, []uint32{50, 900}) {
		t.Fatalf("report = %v (reported %v, %v), want [50 900]: an older report replaced a newer one", got, reported, err)
	}
	applyContacts(d, logger, railSiblingID, snap(nil, 3000))
	if got, reported, _ := siblingReport(d, railFingerprint); !reported || len(got) != 0 {
		t.Fatalf("an empty report did not replace the list: %v (reported %v)", got, reported)
	}
	applyContacts(d, logger, railSiblingID, []syncrail.ContactSnapshot{{Fingerprint: railFingerprint}})
	if _, reported, _ := siblingReport(d, railFingerprint); !reported {
		t.Fatal("an older build's silence erased the report")
	}

	if _, err := d.linkedDevices.Unlink(railSiblingID, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if _, reported, _ := siblingReport(d, railFingerprint); reported {
		t.Fatal("a report from a device no longer linked still counts")
	}
}

func TestInheritedContacts_APartialPersonIsListed(t *testing.T) {
	ctx := context.Background()
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	wireInvitationStore(d)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)
	firstMessageFrom(t, ctx, d, railPeerID, "env-partial-1")
	stub.WaitForSend(t, 1, 5*time.Second)
	have := registrationOf(t, ctx, d, railPeerID)

	if got := inheritedContactsOf(t, d); len(got) != 0 {
		t.Fatalf("listed %+v before my linked device reported a second device", got)
	}
	if _, err := d.peerMeta.ObserveSiblingDevices(railFingerprint, railSiblingID, []uint32{have, have + 1}, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}

	mine := d.selfFingerprint()
	got := inheritedContactsOf(t, d)
	if len(got) != 1 {
		t.Fatalf("listed %d rows, want the partial person", len(got))
	}
	row := got[0]
	if row.Devices != 2 || row.Paired != 1 {
		t.Errorf("devices/paired = %d/%d, want 2/1 (\"1 of 2 devices\")", row.Devices, row.Paired)
	}
	_, _, reason, _, ok := pairingOfferPlan(ctx, d, railFingerprint, false)
	if mine < railFingerprint {
		if row.Waiting || row.Offers != 1 || !ok {
			t.Errorf("lower identity: row %+v, offer ok=%v (%s), want one offer", row, ok, reason)
		}
	} else {
		if !row.Waiting || row.Offers != 1 || ok || reason != reasonContactOffers {
			t.Errorf("higher identity: row %+v, offer ok=%v reason=%q, want waiting and contact_offers", row, ok, reason)
		}
	}
	_ = chatID
}

func TestInheritedContacts_TheHigherIdentityWaits(t *testing.T) {
	ctx := context.Background()
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	const lowest = "0000000000000000000000000000000000000000000000000000000000000001"
	if d.selfFingerprint() <= lowest {
		t.Fatal("precondition: this device's identity is not above the person's")
	}
	seedKnownPeer(t, d, railPeerID, lowest)
	firstMessageFrom(t, ctx, d, railPeerID, "env-wait-1")
	stub.WaitForSend(t, 1, 5*time.Second)
	have := registrationOf(t, ctx, d, railPeerID)
	if _, err := d.peerMeta.ObserveSiblingDevices(lowest, railSiblingID, []uint32{have + 1, have}, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	got := inheritedContactsOf(t, d)
	if len(got) != 1 || !got[0].Waiting || got[0].Offers != 1 || got[0].Paired != 1 || got[0].Devices != 2 {
		t.Fatalf("rows %+v, want one waiting row, 1 of 2 devices, one device to offer anyway", got)
	}
	if _, _, reason, _, ok := pairingOfferPlan(ctx, d, lowest, false); ok || reason != reasonContactOffers {
		t.Fatalf("the offer for a waiting row: ok=%v reason=%q, want contact_offers", ok, reason)
	}

	if _, plan, reason, _, ok := pairingOfferPlan(ctx, d, lowest, true); !ok || !slices.Equal(plan.missing, []uint32{have + 1}) {
		t.Fatalf("the forced offer: ok=%v reason=%q missing=%d, want the missing device", ok, reason, len(plan.missing))
	}
}

func TestInvitation_OffersToOnePersonDoNotRetireEachOther(t *testing.T) {
	backend := startOfferBackend(t)
	d, addr, certPath, token := newTestDaemon(t, &haomadStub{URL: backend.URL})
	wireInvitationStore(d)
	backend.watchNotes(d.invitations)
	linkSibling(t, d, railSiblingID)
	if _, err := d.chats.CreateDirectEdgeless(chat.ChatID("aa11bb22cc33dd44ee55ff6600778890"), railFingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := d.peerMeta.ObserveSiblingDevices(railFingerprint, railSiblingID, []uint32{900, 50}, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := d.peerMeta.MergePerson(railFingerprint, peerstate.MetaRecord{Nick: "alice", NickAt: 1}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	rowOf := func() ipc.InheritedContactEntry {
		t.Helper()
		got := inheritedContactsOf(t, d)
		if len(got) != 1 {
			t.Fatalf("%d rows, want the person", len(got))
		}
		return got[0]
	}
	if r := rowOf(); r.Offers != 2 || r.Open != 2 {
		t.Fatalf("before any offer: offers=%d open=%d, want 2/2", r.Offers, r.Open)
	}
	offerInvitationFrame(t, ctx, conn, railFingerprint)
	if r := rowOf(); r.Offers != 2 || r.Open != 1 {
		t.Fatalf("after one offer: offers=%d open=%d, want 2/1 — a device whose invitation stands is not open", r.Offers, r.Open)
	}
	offerInvitationFrame(t, ctx, conn, railFingerprint)
	if r := rowOf(); r.Open != 0 {
		t.Fatalf("after both offers: open=%d, want 0", r.Open)
	}
	if cancelled, _, _ := backend.snapshot(); len(cancelled) != 0 {
		t.Fatalf("the second offer cancelled %v; offers to one person's two devices must both stand", cancelled)
	}
	notes, err := d.invitations.NotesFor(railFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 2 || notes[0].Device != 50 || notes[1].Device != 900 {
		t.Fatalf("%d notes, want one for device 50 and one for device 900", len(notes))
	}

	offerInvitationFrame(t, ctx, conn, railFingerprint)
	cancelled, _, _ := backend.snapshot()
	if len(cancelled) != 1 || cancelled[0] != notes[0].HandleID {
		t.Fatalf("the third offer cancelled %v, want only the oldest (%s)", cancelled, notes[0].HandleID)
	}
	after, err := d.invitations.NotesFor(railFingerprint)
	if err != nil || len(after) != 2 || after[1].HandleID != notes[1].HandleID {
		t.Fatalf("%d notes after the third offer (%v), want the note for device 900 untouched", len(after), err)
	}
}

func TestInheritedContacts_ALiveInvitationFromThePersonStopsTheOffer(t *testing.T) {
	ctx := context.Background()
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	wireInvitationStore(d)
	linkSibling(t, d, railSiblingID)
	const higher = "ff00000000000000000000000000000000000000000000000000000000000001"
	if d.selfFingerprint() >= higher {
		t.Fatal("precondition: this device's identity is not the lower")
	}
	chatID := seedKnownPeer(t, d, railPeerID, higher)
	firstMessageFrom(t, ctx, d, railPeerID, "env-x-1")
	stub.WaitForSend(t, 1, 5*time.Second)
	have := registrationOf(t, ctx, d, railPeerID)

	if _, err := d.peerMeta.ObserveSiblingDevices(higher, railSiblingID, []uint32{have, have + 1}, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, ok := pairingOfferPlan(ctx, d, higher, false); !ok {
		t.Fatal("precondition: with no invitation standing, the rule says this device offers")
	}

	if err := d.invitations.Put(invitation.Record{
		MsgID: "inv-from-x", ChatID: string(chatID), Fingerprint: higher,
		OfferingDevice: invitationDeviceA, Secret: invitationTestSecret,
		MintedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix(),
		State: events.InvitationLive,
	}); err != nil {
		t.Fatal(err)
	}
	for _, force := range []bool{false, true} {
		if _, _, reason, _, ok := pairingOfferPlan(ctx, d, higher, force); ok || reason != reasonInvitationFromContact {
			t.Fatalf("force=%v: ok=%v reason=%q, want invitation_from_contact", force, ok, reason)
		}
	}
	got := inheritedContactsOf(t, d)
	if len(got) != 1 || !got[0].Invited || got[0].Waiting {
		t.Fatalf("rows %+v, want one row saying to accept the invitation", got)
	}

	if err := d.invitations.SetState("inv-from-x", events.InvitationFailed); err != nil {
		t.Fatal(err)
	}
	if _, _, reason, _, ok := pairingOfferPlan(ctx, d, higher, false); !ok {
		t.Fatalf("after the invitation settled: refused with %q", reason)
	}
}

func TestOwnContactDevices_AnOldPairingWithoutASessionDoesNotHoldOffersBack(t *testing.T) {
	ctx := context.Background()
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	dc, err := d.chats.CreateDirect(railPeerID)
	if err != nil {
		t.Fatal(err)
	}
	_ = dc
	if _, _, err := d.peerMeta.SetFingerprintAt(railPeerID, railFingerprint, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if _, pending, err := ownContactDevices(ctx, d, railFingerprint); err != nil || pending != 1 {
		t.Fatalf("a young pairing: pending=%d (%v), want 1", pending, err)
	}
	const old = "0000000000000000000000000000f00d"
	d2stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d2, _, _, _ := newTestDaemon(t, d2stub)
	if _, err := d2.chats.CreateDirect(old); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d2.peerMeta.SetFingerprintAt(old, railFingerprint, time.Now().Add(-pendingPairingWindow-time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, pending, err := ownContactDevices(ctx, d2, railFingerprint); err != nil || pending != 0 {
		t.Fatalf("a pairing older than the window: pending=%d (%v), want 0", pending, err)
	}
}

func offerRefusedWith(t *testing.T, ctx context.Context, conn *websocket.Conn, req ipc.InvitationOfferRequest) string {
	t.Helper()
	f, _ := ipc.NewFrame(ipc.FrameInvitationOffer, "offer-x", req)
	writeFrame(t, ctx, conn, f)
	got := readUntil(t, ctx, conn, ipc.FrameError)
	var p ipc.ErrorPayload
	if err := json.Unmarshal(got.Payload, &p); err != nil {
		t.Fatal(err)
	}
	return p.Code
}

func offerFrameAs(t *testing.T, ctx context.Context, conn *websocket.Conn, req ipc.InvitationOfferRequest) {
	t.Helper()
	f, _ := ipc.NewFrame(ipc.FrameInvitationOffer, "offer-y", req)
	writeFrame(t, ctx, conn, f)
	readUntil(t, ctx, conn, ipc.FrameInvitationOffered)
}

func twoDeviceOfferFixture(t *testing.T) (*daemon, *offerBackend, context.Context, *websocket.Conn) {
	t.Helper()
	backend := startOfferBackend(t)
	d, addr, certPath, token := newTestDaemon(t, &haomadStub{URL: backend.URL})
	wireInvitationStore(d)
	backend.watchNotes(d.invitations)
	linkSibling(t, d, railSiblingID)
	if _, err := d.chats.CreateDirectEdgeless(chat.ChatID("aa11bb22cc33dd44ee55ff6600778890"), railFingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := d.peerMeta.ObserveSiblingDevices(railFingerprint, railSiblingID, []uint32{900, 50}, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := d.peerMeta.MergePerson(railFingerprint, peerstate.MetaRecord{Nick: "alice", NickAt: 1}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return d, backend, ctx, dialTest(t, ctx, addr, certPath, token)
}

func TestInvitation_AnOpenOnlyOfferNeverReplacesAStandingOne(t *testing.T) {
	d, backend, ctx, conn := twoDeviceOfferFixture(t)
	req := ipc.InvitationOfferRequest{Fingerprint: railFingerprint, Force: true, OpenOnly: true}

	offerFrameAs(t, ctx, conn, req)
	first, err := d.invitations.GetNote(railFingerprint, 50)
	if err != nil {
		t.Fatalf("the first offer noted no invitation for device 50: %v", err)
	}

	if _, err := d.peerMeta.ObserveSiblingDevices(railFingerprint, railSiblingID, []uint32{50}, time.Now().UnixMilli()+1); err != nil {
		t.Fatal(err)
	}
	mintsBefore := backend.mintCount()
	if got := offerRefusedWith(t, ctx, conn, req); got != ipc.ReasonNothingOpen {
		t.Fatalf("the second copy was refused with %q, want nothing_open", got)
	}
	if cancelled, _, _ := backend.snapshot(); len(cancelled) != 0 {
		t.Fatalf("the refused copy cancelled %v; the invitation for device 50 must stand", cancelled)
	}
	if backend.mintCount() != mintsBefore {
		t.Fatal("the refused copy minted an onion")
	}
	if n, err := d.invitations.GetNote(railFingerprint, 50); err != nil || n.HandleID != first.HandleID {
		t.Fatalf("the note for device 50 moved (%v)", err)
	}

	offerFrameAs(t, ctx, conn, ipc.InvitationOfferRequest{Fingerprint: railFingerprint, Force: true})
	if cancelled, _, _ := backend.snapshot(); len(cancelled) != 1 || cancelled[0] != first.HandleID {
		t.Fatalf("a key press cancelled %v, want the old invitation for device 50", cancelled)
	}
}

func TestInvitation_AFailedNoteTakesTheOfferBack(t *testing.T) {
	d, backend, ctx, conn := twoDeviceOfferFixture(t)
	d.notePut = func(invitation.Note) error { return errors.New("disk full") }
	if got := offerRefusedWith(t, ctx, conn, ipc.InvitationOfferRequest{Fingerprint: railFingerprint, Force: true}); got != ipc.ReasonPersistFailed {
		t.Fatalf("refused with %q, want persist_failed", got)
	}
	if cancelled, _, _ := backend.snapshot(); len(cancelled) != 1 {
		t.Fatalf("cancelled %v, want the one rendezvous the offer published", cancelled)
	}
	if sends, _ := backend.sendCounts(); sends != 0 {
		t.Fatalf("%d send(s): the secret left although no note names its rendezvous", sends)
	}
}

func TestInvitation_TheOfferThatNamesNoDeviceIsTakenBack(t *testing.T) {
	d, backend, ctx, conn := twoDeviceOfferFixture(t)
	old := invitation.Note{MsgID: "inv-old", HandleID: "handle-old", Fingerprint: railFingerprint,
		MintedAt: time.Now().Unix() - 60, ExpiresAt: time.Now().Add(time.Hour).Unix()}
	if err := d.invitations.PutNote(old); err != nil {
		t.Fatal(err)
	}
	rows := inheritedContactsOf(t, d)
	if len(rows) != 1 || rows[0].Offers != 2 || rows[0].Open != 1 {
		t.Fatalf("rows %+v, want offers 2 and open 1: the offer naming no device covers one", rows)
	}
	offerFrameAs(t, ctx, conn, ipc.InvitationOfferRequest{Fingerprint: railFingerprint, Force: true, OpenOnly: true})
	if cancelled, _, _ := backend.snapshot(); len(cancelled) != 1 || cancelled[0] != "handle-old" {
		t.Fatalf("cancelled %v, want the offer that names no device", cancelled)
	}
	if _, err := d.invitations.GetNote(railFingerprint, 0); !errors.Is(err, invitation.ErrNotFound) {
		t.Fatalf("the note naming no device is still there (%v)", err)
	}
	if n, err := d.invitations.GetNote(railFingerprint, 50); err != nil || n.HandleID == "" {
		t.Fatalf("the named offer wrote no note for device 50 (%v)", err)
	}
}
