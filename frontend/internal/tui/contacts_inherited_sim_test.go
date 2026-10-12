package tui_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/tui"
)

const (
	simFrameTitle   = "linked device"
	simFrameAll     = "[ pair all ]"
	simFrameEmpty   = "synchronized"
	simFrameDetails = "Real nick"
	simStateWaiting = "waiting"

	simStateOffering     = "Preparing the invitation."
	simStateOfferingNext = "Keep this device on."
	simStateOffered      = "invitation sent"
	simStateUnknown      = "no answer came back"

	simHintPair = "p:pair"
)

const (
	simPairedPeerID = "peer-alice"
	simCarolFP      = "fp-carol-1111"
	simDaveFP       = "fp-dave-2222"
)

type offerLog struct {
	mu  sync.Mutex
	got []string

	unforced int

	lists int

	openOnly []bool
}

func (l *offerLog) add(fingerprint string) {
	l.mu.Lock()
	l.got = append(l.got, fingerprint)
	l.mu.Unlock()
}

func (l *offerLog) seen() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.got...)
}

func contactsScript(contacts []ipc.InheritedContactEntry, log *offerLog, release <-chan struct{}) func(context.Context, *ipc.Session) {
	return contactsScriptAnswering(contacts, log, release, func(f ipc.Frame) (ipc.FrameType, any) {
		return ipc.FrameInvitationOffered, ipc.InvitationOfferedResponse{
			MsgID:     "inv-" + f.ID,
			ExpiresAt: time.Now().Add(24 * time.Hour).Unix(),
			Siblings:  1,
		}
	})
}

func contactsScriptAnswering(contacts []ipc.InheritedContactEntry, log *offerLog, release <-chan struct{},
	answer func(ipc.Frame) (ipc.FrameType, any)) func(context.Context, *ipc.Session) {
	return contactsScriptListing(func() []ipc.InheritedContactEntry { return contacts }, log, release, answer)
}

func contactsScriptListing(list func() []ipc.InheritedContactEntry, log *offerLog, release <-chan struct{},
	answer func(ipc.Frame) (ipc.FrameType, any)) func(context.Context, *ipc.Session) {
	return func(_ context.Context, s *ipc.Session) {
		in := make(chan ipc.Frame, 32)
		go func() {
			defer close(in)
			for {
				f, err := s.Recv()
				if err != nil {
					return
				}
				in <- f
			}
		}()

		send := func(id string, typ ipc.FrameType, payload any) {
			out, err := ipc.NewFrame(typ, id, payload)
			if err != nil {
				return
			}
			_ = s.Send(out)
		}

		var parked []ipc.Frame
		for {
			select {
			case f, ok := <-in:
				if !ok {
					return
				}
				switch f.Type {
				case ipc.FrameListPeers:
					send(f.ID, ipc.FramePeersListed, ipc.PeersListedResponse{
						Peers: []ipc.PeerEntry{{
							ID:     simPairedPeerID,
							Nick:   "alice",
							Label:  "alice",
							ChatID: "chat-alice",
						}},
					})
				case ipc.FrameListInheritedContacts:
					log.mu.Lock()
					log.lists++
					log.mu.Unlock()
					send(f.ID, ipc.FrameInheritedContactsListed, ipc.InheritedContactsListedResponse{
						Contacts: list(),
						Linked:   true,
					})
				case ipc.FrameListChats:
					send(f.ID, ipc.FrameChatsListed, ipc.ChatsListedResponse{})
				case ipc.FrameInvitationOffer:

					var p ipc.InvitationOfferRequest
					_ = json.Unmarshal(f.Payload, &p)
					log.add(p.Fingerprint)
					log.mu.Lock()
					if !p.Force {
						log.unforced++
					}
					log.openOnly = append(log.openOnly, p.OpenOnly)
					log.mu.Unlock()
					parked = append(parked, f)
				}
			case <-release:
				if len(parked) == 0 {
					continue
				}
				f := parked[0]
				parked = parked[1:]
				typ, payload := answer(f)
				send(f.ID, typ, payload)
			}
		}
	}
}

func simContacts() []ipc.InheritedContactEntry {
	return []ipc.InheritedContactEntry{
		{Fingerprint: simCarolFP, Nick: "carol", Label: "carol"},
		{Fingerprint: simDaveFP, Nick: "dave", Alias: "Dave at work", Label: "Dave at work"},
	}
}

func openContactsPane(t *testing.T, sim tcell.SimulationScreen) {
	t.Helper()
	typeCommand(sim, "/contacts")
	waitForScreen(t, sim, simFrameTitle)
}

func focusFrame(t *testing.T, sim tcell.SimulationScreen) {
	t.Helper()
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	waitForScreen(t, sim, "v:details")
}

func TestContactsPane_DrawsInheritedContactsInTheirOwnFrame(t *testing.T) {
	release := make(chan struct{})
	dir, addr, cancel := spinScriptedIPCServer(t, contactsScript(simContacts(), &offerLog{}, release))
	defer cancel()
	_, sim := launchApp(t, dir, addr)

	openContactsPane(t, sim)

	for _, want := range []string{
		"alice",
		simFrameTitle,
		"carol",
		"Dave at work",
		simFrameAll,
	} {
		if !strings.Contains(flatten(screenText(sim)), flatten(want)) {
			t.Fatalf("the window never painted %q; screen:\n%s", want, screenText(sim))
		}
	}

	if !strings.Contains(flatten(screenText(sim)), simPairedPeerID) {
		t.Fatalf("the paired row lost its peer id; screen:\n%s", screenText(sim))
	}
}

func TestContactsPane_NoLinkedDeviceDrawsNoFrame(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, func(_ context.Context, s *ipc.Session) {
		for {
			f, err := s.Recv()
			if err != nil {
				return
			}
			send := func(typ ipc.FrameType, payload any) {
				out, err := ipc.NewFrame(typ, f.ID, payload)
				if err != nil {
					return
				}
				_ = s.Send(out)
			}
			switch f.Type {
			case ipc.FrameListPeers:
				send(ipc.FramePeersListed, ipc.PeersListedResponse{
					Peers: []ipc.PeerEntry{{ID: simPairedPeerID, Nick: "alice", Label: "alice", ChatID: "chat-alice"}},
				})
			case ipc.FrameListInheritedContacts:

				send(ipc.FrameInheritedContactsListed, ipc.InheritedContactsListedResponse{
					Contacts: simContacts(),
					Linked:   false,
				})
			case ipc.FrameListChats:
				send(ipc.FrameChatsListed, ipc.ChatsListedResponse{})
			}
		}
	})
	defer cancel()
	_, sim := launchApp(t, dir, addr)

	typeCommand(sim, "/contacts")
	waitForScreen(t, sim, "alice")

	time.Sleep(300 * time.Millisecond)
	for _, gone := range []string{simFrameTitle, "carol", simFrameAll} {
		if strings.Contains(flatten(screenText(sim)), flatten(gone)) {
			t.Fatalf("the frame drew %q on a device with no linked device; screen:\n%s", gone, screenText(sim))
		}
	}

	if strings.Contains(flatten(screenText(sim)), "tab:switch") {
		t.Fatalf("the strip advertises the switch key with no frame to switch to; screen:\n%s", screenText(sim))
	}
}

func TestContactsPane_AnEmptyFrameSaysTheDevicesAgree(t *testing.T) {
	pushes := make(chan []ipc.InheritedContactEntry, 1)
	dir, addr, cancel := spinScriptedIPCServer(t, contactsPushScript(pushes))
	defer cancel()
	_, sim := launchApp(t, dir, addr)

	typeCommand(sim, "/contacts")
	waitForScreen(t, sim, simFrameEmpty)
}

func TestContactsPane_OffersOnePairingAtATime(t *testing.T) {
	log := &offerLog{}
	release := make(chan struct{})
	dir, addr, cancel := spinScriptedIPCServer(t, contactsScript(simContacts(), log, release))
	defer cancel()
	_, sim := launchApp(t, dir, addr)

	openContactsPane(t, sim)

	focusFrame(t, sim)
	sim.InjectKey(tcell.KeyRune, ' ', tcell.ModNone)
	waitForScreen(t, sim, "*")
	sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyRune, ' ', tcell.ModNone)

	sim.InjectKey(tcell.KeyRune, 'p', tcell.ModNone)

	waitForOffers(t, log, 1)
	waitForScreen(t, sim, simStateOffering)
	waitForScreen(t, sim, simStateOfferingNext)

	assertOffersStayAt(t, log, 1, 750*time.Millisecond)
	if got := log.seen()[0]; got != simCarolFP {
		t.Fatalf("first offer was for %q, want %q — the run follows the order of the pane", got, simCarolFP)
	}

	waitForScreen(t, sim, simStateWaiting)

	release <- struct{}{}
	waitForOffers(t, log, 2)
	if got := log.seen()[1]; got != simDaveFP {
		t.Fatalf("second offer was for %q, want %q", got, simDaveFP)
	}
	waitForScreen(t, sim, simStateOffered)

	release <- struct{}{}
	assertOffersStayAt(t, log, 2, 500*time.Millisecond)
}

func TestContactsPane_ASecondPressDoesNotDoubleOffer(t *testing.T) {
	log := &offerLog{}
	release := make(chan struct{})
	dir, addr, cancel := spinScriptedIPCServer(t, contactsScript(simContacts(), log, release))
	defer cancel()
	_, sim := launchApp(t, dir, addr)

	openContactsPane(t, sim)

	focusFrame(t, sim)
	sim.InjectKey(tcell.KeyRune, 'p', tcell.ModNone)
	waitForOffers(t, log, 1)

	sim.InjectKey(tcell.KeyRune, 'p', tcell.ModNone)
	assertOffersStayAt(t, log, 1, 750*time.Millisecond)

	release <- struct{}{}
	assertOffersStayAt(t, log, 1, 500*time.Millisecond)
}

func TestContactsPane_PairingKeysAreNotInTheFrame(t *testing.T) {
	release := make(chan struct{})
	dir, addr, cancel := spinScriptedIPCServer(t, contactsScript(simContacts(), &offerLog{}, release))
	defer cancel()
	_, sim := launchApp(t, dir, addr)

	openContactsPane(t, sim)
	focusFrame(t, sim)
	sim.InjectKey(tcell.KeyRune, 'f', tcell.ModNone)

	time.Sleep(300 * time.Millisecond)
	if strings.Contains(flatten(screenText(sim)), "peer fingerprint") {
		t.Fatalf("the fingerprint dialog opened from the frame; screen:\n%s", screenText(sim))
	}

	if strings.Contains(flatten(screenText(sim)), "f:fp") {
		t.Fatalf("the frame advertises a key it does not answer; screen:\n%s", screenText(sim))
	}
}

func TestContactsPane_DetailsReadsOutTheThreeNames(t *testing.T) {
	release := make(chan struct{})
	dir, addr, cancel := spinScriptedIPCServer(t, contactsScript(simContacts(), &offerLog{}, release))
	defer cancel()
	_, sim := launchApp(t, dir, addr)

	openContactsPane(t, sim)
	focusFrame(t, sim)

	sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyRune, 'v', tcell.ModNone)

	waitForScreen(t, sim, simFrameDetails)

	for _, want := range []string{
		"dave",
		"Local alias",
		"Fingerprint",
	} {
		if !strings.Contains(flatten(screenText(sim)), flatten(want)) {
			t.Fatalf("the readout never painted %q; screen:\n%s", want, screenText(sim))
		}
	}
}

func TestContactsPane_PairAllQueuesEveryContact(t *testing.T) {
	log := &offerLog{}
	release := make(chan struct{})
	dir, addr, cancel := spinScriptedIPCServer(t, contactsScript(simContacts(), log, release))
	defer cancel()
	_, sim := launchApp(t, dir, addr)

	openContactsPane(t, sim)
	focusFrame(t, sim)

	sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)

	waitForScreen(t, sim, simFrameAll)
	assertOffersStayAt(t, log, 0, 300*time.Millisecond)
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)

	waitForOffers(t, log, 1)
	assertOffersStayAt(t, log, 1, 500*time.Millisecond)
	release <- struct{}{}
	waitForOffers(t, log, 2)
	if got := log.seen(); got[0] != simCarolFP || got[1] != simDaveFP {
		t.Fatalf("offers = %v, want both contacts in the order the frame lists them", got)
	}
}

func contactsPushScript(pushes <-chan []ipc.InheritedContactEntry) func(context.Context, *ipc.Session) {
	return func(_ context.Context, s *ipc.Session) {
		in := make(chan ipc.Frame, 32)
		go func() {
			defer close(in)
			for {
				f, err := s.Recv()
				if err != nil {
					return
				}
				in <- f
			}
		}()

		send := func(id string, typ ipc.FrameType, payload any) {
			out, err := ipc.NewFrame(typ, id, payload)
			if err != nil {
				return
			}
			_ = s.Send(out)
		}

		answer := func(f ipc.Frame) {
			switch f.Type {
			case ipc.FrameListPeers:
				send(f.ID, ipc.FramePeersListed, ipc.PeersListedResponse{
					Peers: []ipc.PeerEntry{{
						ID:     simPairedPeerID,
						Nick:   "alice",
						Label:  "alice",
						ChatID: "chat-alice",
					}},
				})
			case ipc.FrameListInheritedContacts:
				send(f.ID, ipc.FrameInheritedContactsListed, ipc.InheritedContactsListedResponse{Linked: true})
			case ipc.FrameListChats:
				send(f.ID, ipc.FrameChatsListed, ipc.ChatsListedResponse{})
			}
		}

		for {
			select {
			case f, ok := <-in:
				if !ok {
					return
				}
				answer(f)
			case contacts := <-pushes:

				if !drainRequests(in, answer) {
					return
				}

				send("", ipc.FrameInheritedContactsChanged, ipc.InheritedContactsChangedPayload{
					Contacts: contacts,
					Linked:   true,
				})
			}
		}
	}
}

func drainRequests(in <-chan ipc.Frame, answer func(ipc.Frame)) bool {
	for {
		select {
		case f, ok := <-in:
			if !ok {
				return false
			}
			answer(f)
		default:
			return true
		}
	}
}

func TestContactsPane_APushFillsTheSectionWithNoCommandTyped(t *testing.T) {
	pushes := make(chan []ipc.InheritedContactEntry)
	dir, addr, cancel := spinScriptedIPCServer(t, contactsPushScript(pushes))
	defer cancel()
	_, sim := launchApp(t, dir, addr)

	typeCommand(sim, "/contacts")
	waitForScreen(t, sim, simPairedPeerID)

	waitForScreen(t, sim, simFrameEmpty)

	pushes <- simContacts()

	waitForScreen(t, sim, simFrameTitle)
	for _, want := range []string{"carol", "Dave at work", simFrameAll} {
		waitForScreen(t, sim, want)
	}
}

func TestContactsPane_APushCanEmptyTheFrame(t *testing.T) {
	pushes := make(chan []ipc.InheritedContactEntry)
	dir, addr, cancel := spinScriptedIPCServer(t, contactsPushScript(pushes))
	defer cancel()
	_, sim := launchApp(t, dir, addr)

	typeCommand(sim, "/contacts")
	waitForScreen(t, sim, simPairedPeerID)
	pushes <- simContacts()
	waitForScreen(t, sim, "carol")

	pushes <- []ipc.InheritedContactEntry{}
	waitForScreenGone(t, sim, "carol")

	waitForScreen(t, sim, simFrameEmpty)
}

func TestContactsPane_EachHalfAdvertisesItsOwnKeys(t *testing.T) {
	release := make(chan struct{})
	dir, addr, cancel := spinScriptedIPCServer(t, contactsScript(simContacts(), &offerLog{}, release))
	defer cancel()
	_, sim := launchApp(t, dir, addr)

	openContactsPane(t, sim)

	for _, want := range []string{"e:edit", "f:fp", "tab:switch"} {
		if !strings.Contains(flatten(screenText(sim)), flatten(want)) {
			t.Fatalf("the table never advertised %q; screen:\n%s", want, screenText(sim))
		}
	}
	if strings.Contains(flatten(screenText(sim)), flatten(simHintPair)) {
		t.Fatalf("the table advertises the pairing key, which belongs to the frame; screen:\n%s", screenText(sim))
	}

	focusFrame(t, sim)
	for _, want := range []string{"space:mark", simHintPair, "v:details", "x:stop", "tab:switch"} {
		if !strings.Contains(flatten(screenText(sim)), flatten(want)) {
			t.Fatalf("the frame never advertised %q; screen:\n%s", want, screenText(sim))
		}
	}
}

func TestContactsPane_EnterOnAContactOpensTheReadout(t *testing.T) {
	log := &offerLog{}
	release := make(chan struct{})
	dir, addr, cancel := spinScriptedIPCServer(t, contactsScript(simContacts(), log, release))
	defer cancel()
	_, sim := launchApp(t, dir, addr)

	openContactsPane(t, sim)
	focusFrame(t, sim)
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)

	waitForScreen(t, sim, simFrameDetails)
	assertOffersStayAt(t, log, 0, 750*time.Millisecond)

	sim.InjectKey(tcell.KeyEscape, 0, tcell.ModNone)
	waitForScreenGone(t, sim, simFrameDetails)
	sim.InjectKey(tcell.KeyRune, 'p', tcell.ModNone)
	waitForOffers(t, log, 1)
	if got := log.seen()[0]; got != simCarolFP {
		t.Fatalf("the pairing key offered a pairing for %q, want %q — the row the cursor was on", got, simCarolFP)
	}
}

func TestContactsPane_EnterStillOpensAPairedContact(t *testing.T) {
	release := make(chan struct{})
	dir, addr, cancel := spinScriptedIPCServer(t, contactsScript(simContacts(), &offerLog{}, release))
	defer cancel()
	_, sim := launchApp(t, dir, addr)

	openContactsPane(t, sim)

	sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)

	waitForScreen(t, sim, "alice")
	waitForScreenGone(t, sim, simFrameTitle)
}

func TestContactsPane_ALostAnswerDoesNotWedgeTheRun(t *testing.T) {
	log := &offerLog{}
	release := make(chan struct{})
	var dropped atomic.Bool
	dir, addr, cancel := spinScriptedIPCServer(t, func(ctx context.Context, s *ipc.Session) {
		if dropped.CompareAndSwap(false, true) {

			dropAfterFirstOffer(s, simContacts(), log)
			return
		}
		contactsScript(simContacts(), log, release)(ctx, s)
	})
	defer cancel()
	_, sim := launchApp(t, dir, addr)

	openContactsPane(t, sim)

	focusFrame(t, sim)
	sim.InjectKey(tcell.KeyRune, ' ', tcell.ModNone)
	waitForScreen(t, sim, "*")
	sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyRune, ' ', tcell.ModNone)
	sim.InjectKey(tcell.KeyRune, 'p', tcell.ModNone)

	waitForOffers(t, log, 1)
	if got := log.seen()[0]; got != simCarolFP {
		t.Fatalf("first offer was for %q, want %q", got, simCarolFP)
	}

	waitForScreen(t, sim, simStateUnknown)

	waitForOffers(t, log, 2)
	if got := log.seen()[1]; got != simDaveFP {
		t.Fatalf("second offer was for %q, want %q — the queue behind the lost answer must survive it", got, simDaveFP)
	}

	release <- struct{}{}
	assertOffersStayAt(t, log, 2, 750*time.Millisecond)
}

func dropAfterFirstOffer(s *ipc.Session, contacts []ipc.InheritedContactEntry, log *offerLog) {
	for {
		f, err := s.Recv()
		if err != nil {
			return
		}
		send := func(typ ipc.FrameType, payload any) {
			out, err := ipc.NewFrame(typ, f.ID, payload)
			if err != nil {
				return
			}
			_ = s.Send(out)
		}
		switch f.Type {
		case ipc.FrameListPeers:
			send(ipc.FramePeersListed, ipc.PeersListedResponse{
				Peers: []ipc.PeerEntry{{
					ID:     simPairedPeerID,
					Nick:   "alice",
					Label:  "alice",
					ChatID: "chat-alice",
				}},
			})
		case ipc.FrameListInheritedContacts:
			send(ipc.FrameInheritedContactsListed, ipc.InheritedContactsListedResponse{Contacts: contacts, Linked: true})
		case ipc.FrameListChats:
			send(ipc.FrameChatsListed, ipc.ChatsListedResponse{})
		case ipc.FrameInvitationOffer:
			var p ipc.InvitationOfferRequest
			_ = json.Unmarshal(f.Payload, &p)
			log.add(p.Fingerprint)
			return
		}
	}
}

func TestContactsPane_PairAllLeavesStandingInvitationsAlone(t *testing.T) {
	log := &offerLog{}
	release := make(chan struct{})
	dir, addr, cancel := spinScriptedIPCServer(t, contactsScript(simContacts(), log, release))
	defer cancel()
	_, sim := launchApp(t, dir, addr)

	openContactsPane(t, sim)
	focusFrame(t, sim)

	sim.InjectKey(tcell.KeyRune, 'p', tcell.ModNone)
	waitForOffers(t, log, 1)
	release <- struct{}{}
	waitForScreen(t, sim, simStateOffered)

	sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
	waitForScreen(t, sim, simFrameAll)
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)

	waitForOffers(t, log, 2)
	assertOffersStayAt(t, log, 2, 750*time.Millisecond)
	if got := log.seen(); got[0] != simCarolFP || got[1] != simDaveFP {
		t.Fatalf("offers = %v, want carol once and dave once — carol's invitation was already standing", got)
	}
}

func TestContactsPane_AnUnbuiltAnswerStillMeansTheInvitationWentOut(t *testing.T) {
	log := &offerLog{}
	release := make(chan struct{})
	dir, addr, cancel := spinScriptedIPCServer(t, contactsScriptAnswering(simContacts(), log, release,
		func(ipc.Frame) (ipc.FrameType, any) {
			return ipc.FrameError, ipc.ErrorPayload{Code: ipc.ReasonEncodeFrame, Message: "encode frame: failed"}
		}))
	defer cancel()
	_, sim := launchApp(t, dir, addr)

	openContactsPane(t, sim)
	focusFrame(t, sim)

	sim.InjectKey(tcell.KeyRune, 'p', tcell.ModNone)
	waitForOffers(t, log, 1)
	release <- struct{}{}
	waitForScreen(t, sim, simStateOffered)

	sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
	waitForScreen(t, sim, simFrameAll)
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)

	waitForOffers(t, log, 2)
	assertOffersStayAt(t, log, 2, 750*time.Millisecond)
	if got := log.seen(); got[0] != simCarolFP || got[1] != simDaveFP {
		t.Fatalf("offers = %v, want carol once and dave once — carol's invitation was standing behind the unbuilt answer", got)
	}
}

func TestContactsPane_TheRunCanBeStopped(t *testing.T) {
	log := &offerLog{}
	release := make(chan struct{})
	dir, addr, cancel := spinScriptedIPCServer(t, contactsScript(simContacts(), log, release))
	defer cancel()
	_, sim := launchApp(t, dir, addr)

	openContactsPane(t, sim)
	focusFrame(t, sim)

	sim.InjectKey(tcell.KeyRune, ' ', tcell.ModNone)
	waitForScreen(t, sim, "*")
	sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyRune, ' ', tcell.ModNone)
	sim.InjectKey(tcell.KeyRune, 'p', tcell.ModNone)
	waitForOffers(t, log, 1)
	waitForScreen(t, sim, simStateWaiting)

	sim.InjectKey(tcell.KeyRune, 'x', tcell.ModNone)

	waitForScreenGone(t, sim, simStateWaiting)
	release <- struct{}{}
	assertOffersStayAt(t, log, 1, 750*time.Millisecond)
}

func TestContactsPane_AnEmptyFrameNeverTakesTheCursor(t *testing.T) {
	pushes := make(chan []ipc.InheritedContactEntry, 1)
	dir, addr, cancel := spinScriptedIPCServer(t, contactsPushScript(pushes))
	defer cancel()
	_, sim := launchApp(t, dir, addr)

	typeCommand(sim, "/contacts")
	waitForScreen(t, sim, simFrameEmpty)

	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	time.Sleep(300 * time.Millisecond)
	sim.InjectKey(tcell.KeyUp, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	if strings.Contains(flatten(screenText(sim)), "v:details") {
		t.Fatalf("the cursor reached an empty frame; screen:\n%s", screenText(sim))
	}

	if strings.Contains(flatten(screenText(sim)), "tab:switch") {
		t.Fatalf("the strip offered the switch key with an empty frame; screen:\n%s", screenText(sim))
	}

	pushes <- simContacts()
	waitForScreen(t, sim, "carol")
}

func TestContactsPane_TheCursorLeavesAFrameThatEmpties(t *testing.T) {
	pushes := make(chan []ipc.InheritedContactEntry, 1)
	dir, addr, cancel := spinScriptedIPCServer(t, contactsPushScript(pushes))
	defer cancel()
	_, sim := launchApp(t, dir, addr)

	typeCommand(sim, "/contacts")
	waitForScreen(t, sim, simPairedPeerID)
	pushes <- simContacts()
	waitForScreen(t, sim, "carol")
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	waitForScreen(t, sim, "v:details")

	pushes <- []ipc.InheritedContactEntry{}
	waitForScreen(t, sim, simFrameEmpty)
	waitForScreenGone(t, sim, "v:details")

	sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyRune, 'f', tcell.ModNone)
	waitForScreen(t, sim, "peer fingerprint")
}

func TestContactsPane_TheFrameDrawsItsOwnCursor(t *testing.T) {
	release := make(chan struct{})
	dir, addr, cancel := spinScriptedIPCServer(t, contactsScript(simContacts(), &offerLog{}, release))
	defer cancel()
	_, sim := launchApp(t, dir, addr)

	openContactsPane(t, sim)

	if line := screenLineWith(t, sim, "carol"); strings.Contains(line, ">") {
		t.Fatalf("the frame drew a cursor while the table held it: %q", line)
	}

	focusFrame(t, sim)
	waitForLineWith(t, sim, "carol", ">")

	sim.InjectKey(tcell.KeyRune, ' ', tcell.ModNone)
	waitForLineWith(t, sim, "carol", "*")
	line := screenLineWith(t, sim, "carol")
	cursor := strings.Index(line, ">")
	mark := strings.Index(line, "*")
	name := strings.Index(line, "carol")
	if cursor < 0 || mark < 0 || !(mark < cursor && cursor < name) {
		t.Fatalf("mark, cursor and name are not in that order: %q", line)
	}

	sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	waitForLineWith(t, sim, "Dave at work", ">")
	if line := screenLineWith(t, sim, "carol"); strings.Contains(line, ">") {
		t.Fatalf("the cursor stayed on a line the keys had left: %q", line)
	}
}

func waitForLineWith(t *testing.T, sim tcell.SimulationScreen, needle, want string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("the line holding %q never gained %q; screen:\n%s", needle, want, screenText(sim))
		default:
			for _, line := range strings.Split(screenText(sim), "\n") {
				if strings.Contains(line, needle) && strings.Contains(line, want) {
					return
				}
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
}

func TestContactsPane_ADeviceOfMyOwnIsNotAContact(t *testing.T) {
	const siblingPeerID = "peer-my-other-device"
	dir, addr, cancel := spinScriptedIPCServer(t, func(_ context.Context, s *ipc.Session) {
		for {
			f, err := s.Recv()
			if err != nil {
				return
			}
			send := func(typ ipc.FrameType, payload any) {
				out, err := ipc.NewFrame(typ, f.ID, payload)
				if err != nil {
					return
				}
				_ = s.Send(out)
			}
			switch f.Type {
			case ipc.FrameListPeers:
				send(ipc.FramePeersListed, ipc.PeersListedResponse{
					Peers: []ipc.PeerEntry{
						{ID: siblingPeerID, Label: siblingPeerID, Sibling: true},
						{ID: simPairedPeerID, Nick: "alice", Label: "alice", ChatID: "chat-alice"},
					},
				})
			case ipc.FrameListInheritedContacts:
				send(ipc.FrameInheritedContactsListed, ipc.InheritedContactsListedResponse{Linked: true})
			case ipc.FrameListChats:
				send(ipc.FrameChatsListed, ipc.ChatsListedResponse{})
			}
		}
	})
	defer cancel()
	_, sim := launchApp(t, dir, addr)

	typeCommand(sim, "/contacts")

	waitForScreen(t, sim, simPairedPeerID)

	if strings.Contains(flatten(screenText(sim)), siblingPeerID) {
		t.Fatalf("a device of my own was drawn in the contacts table; screen:\n%s", screenText(sim))
	}
}

func TestContactsPane_TheFrameTitleCarriesTheLinkedDeviceArrows(t *testing.T) {
	const sibling = "peer-my-other-device"
	presence := make(chan ipc.PresenceChangedPayload, 2)
	dir, addr, cancel := spinScriptedIPCServer(t, func(_ context.Context, s *ipc.Session) {
		in := make(chan ipc.Frame, 16)
		go func() {
			defer close(in)
			for {
				f, err := s.Recv()
				if err != nil {
					return
				}
				in <- f
			}
		}()
		for {
			select {
			case f, ok := <-in:
				if !ok {
					return
				}
				send := func(typ ipc.FrameType, payload any) {
					out, err := ipc.NewFrame(typ, f.ID, payload)
					if err != nil {
						return
					}
					_ = s.Send(out)
				}
				switch f.Type {
				case ipc.FrameListPeers:

					send(ipc.FramePeersListed, ipc.PeersListedResponse{Peers: []ipc.PeerEntry{
						{ID: simPairedPeerID, Nick: "alice", Label: "alice", ChatID: "chat-alice"},
						{ID: sibling, Label: sibling, Sibling: true},
					}})
				case ipc.FrameListInheritedContacts:
					send(ipc.FrameInheritedContactsListed, ipc.InheritedContactsListedResponse{
						Contacts: simContacts(), Linked: true,
					})
				case ipc.FrameListChats:
					send(ipc.FrameChatsListed, ipc.ChatsListedResponse{})
				}
			case p := <-presence:
				p.PeerID = sibling
				out, err := ipc.NewFrame(ipc.FramePresenceChanged, "", p)
				if err != nil {
					return
				}
				_ = s.Send(out)
			}
		}
	})
	defer cancel()
	_, sim := launchApp(t, dir, addr)

	typeCommand(sim, "/contacts")
	waitForScreen(t, sim, "carol")

	waitForScreen(t, sim, "↑↓ "+simFrameTitle)
	waitForFrameArrows(t, sim, tui.ColorPresenceArrowOff, tui.ColorPresenceArrowOff)

	presence <- ipc.PresenceChangedPayload{Sending: true, Effective: "unknown"}
	waitForFrameArrows(t, sim, tui.ColorPresenceArrowOff, tui.ColorPresenceArrowOn)
	presence <- ipc.PresenceChangedPayload{Accepting: true, Effective: "accepting"}
	waitForFrameArrows(t, sim, tui.ColorPresenceArrowOn, tui.ColorPresenceArrowOff)
}

func waitForFrameArrows(t *testing.T, sim tcell.SimulationScreen, up, down tcell.Color) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		gotUp, _, _ := styleOfCellHolding(t, sim, "↑↓ "+simFrameTitle).Decompose()
		gotDown, _, _ := styleOfCellHolding(t, sim, "↓ "+simFrameTitle).Decompose()
		if gotUp == up && gotDown == down {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the frame's arrows are %v %v, want %v %v", gotUp, gotDown, up, down)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestContactsPane_TheCursorColoursTheNameRatherThanCoveringIt(t *testing.T) {
	release := make(chan struct{})
	dir, addr, cancel := spinScriptedIPCServer(t, contactsScript(simContacts(), &offerLog{}, release))
	defer cancel()
	_, sim := launchApp(t, dir, addr)

	openContactsPane(t, sim)
	focusFrame(t, sim)
	waitForLineWith(t, sim, "carol", ">")

	style := styleOfCellHolding(t, sim, "carol")
	fg, bg, _ := style.Decompose()
	if fg != tui.ColorContactsCursor {
		t.Errorf("the name under the cursor is %v, want the cursor colour %v", fg, tui.ColorContactsCursor)
	}
	if bg == tui.ColorContactsCursor {
		t.Errorf("the cursor filled the line with %v and hid the name inside it", bg)
	}

	other := styleOfCellHolding(t, sim, "Dave at work")
	if fg, _, _ := other.Decompose(); fg != tui.ColorContactsName {
		t.Errorf("a name away from the cursor is %v, want the name colour %v", fg, tui.ColorContactsName)
	}
}

func styleOfCellHolding(t *testing.T, sim tcell.SimulationScreen, want string) tcell.Style {
	t.Helper()
	cells, w, h := simCells(sim)
	runeAt := func(row, col int) rune {
		r := cells[row*w+col].Runes
		if len(r) == 0 || r[0] == 0 {
			return ' '
		}
		return r[0]
	}
	needle := []rune(want)
	for row := 0; row < h; row++ {
		for col := 0; col+len(needle) <= w; col++ {
			match := true
			for i, r := range needle {
				if runeAt(row, col+i) != r {
					match = false
					break
				}
			}
			if match {
				return cells[row*w+col].Style
			}
		}
	}
	t.Fatalf("nothing on screen holds %q; screen:\n%s", want, screenText(sim))
	return tcell.StyleDefault
}

func screenLineWith(t *testing.T, sim tcell.SimulationScreen, want string) string {
	t.Helper()
	for _, line := range strings.Split(screenText(sim), "\n") {
		if strings.Contains(line, want) {
			return line
		}
	}
	t.Fatalf("no painted line holds %q; screen:\n%s", want, screenText(sim))
	return ""
}

func waitForOffers(t *testing.T, log *offerLog, want int) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("only %d offer(s) arrived, want %d: %v", len(log.seen()), want, log.seen())
		default:
			if len(log.seen()) >= want {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func assertOffersStayAt(t *testing.T, log *offerLog, want int, howLong time.Duration) {
	t.Helper()
	deadline := time.Now().Add(howLong)
	for time.Now().Before(deadline) {
		if got := log.seen(); len(got) != want {
			t.Fatalf("offers = %v, want exactly %d in flight — every offer mints an onion, and correlated onions are what serialising them prevents", got, want)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestContactsPane_PairAllOffersEachMissingDeviceAndSkipsAWaitingRow(t *testing.T) {
	log := &offerLog{}
	release := make(chan struct{}, 4)
	contacts := []ipc.InheritedContactEntry{
		{Fingerprint: simCarolFP, Nick: "carol", Label: "carol", Devices: 2, Offers: 2},
		{Fingerprint: simDaveFP, Nick: "dave", Label: "dave", Devices: 2, Paired: 1, Offers: 1, Waiting: true},
	}
	dir, addr, cancel := spinScriptedIPCServer(t, contactsScript(contacts, log, release))
	defer cancel()
	_, sim := launchApp(t, dir, addr)

	openContactsPane(t, sim)
	waitForScreen(t, sim, "waiting for dave's")
	focusFrame(t, sim)
	sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
	waitForScreen(t, sim, simFrameAll)
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)

	waitForOffers(t, log, 1)
	assertOffersStayAt(t, log, 1, 300*time.Millisecond)
	release <- struct{}{}
	waitForOffers(t, log, 2)
	release <- struct{}{}
	assertOffersStayAt(t, log, 2, 500*time.Millisecond)
	if got := log.seen(); got[0] != simCarolFP || got[1] != simCarolFP {
		t.Fatalf("offers = %v, want carol twice and dave never", got)
	}
	log.mu.Lock()
	unforced := log.unforced
	log.mu.Unlock()
	if unforced != 0 {
		t.Fatalf("%d offer(s) of the run went without force; a run that started offering completes", unforced)
	}
}

func TestContactsPane_PairAnywayOnAWaitingRowAndNothingOnAnInvitedOne(t *testing.T) {
	log := &offerLog{}
	release := make(chan struct{}, 4)
	contacts := []ipc.InheritedContactEntry{
		{Fingerprint: simCarolFP, Nick: "carol", Label: "carol", Devices: 2, Paired: 1, Offers: 1, Invited: true},
		{Fingerprint: simDaveFP, Nick: "dave", Label: "dave", Devices: 2, Paired: 1, Offers: 1, Waiting: true},
	}
	dir, addr, cancel := spinScriptedIPCServer(t, contactsScript(contacts, log, release))
	defer cancel()
	_, sim := launchApp(t, dir, addr)

	openContactsPane(t, sim)
	waitForScreen(t, sim, "accept carol's")
	focusFrame(t, sim)
	sim.InjectKey(tcell.KeyRune, 'p', tcell.ModNone)
	assertOffersStayAt(t, log, 0, 300*time.Millisecond)
	sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyRune, 'p', tcell.ModNone)
	waitForOffers(t, log, 1)
	if got := log.seen(); got[0] != simDaveFP {
		t.Fatalf("offers = %v, want dave only", got)
	}
	log.mu.Lock()
	unforced := log.unforced
	log.mu.Unlock()
	if unforced != 0 {
		t.Fatal("pair anyway went without force")
	}
}

func contactsScriptPushing(contacts []ipc.InheritedContactEntry, log *offerLog, release <-chan struct{},
	pushes <-chan []ipc.InheritedContactEntry) func(context.Context, *ipc.Session) {
	return contactsScriptPushingWith(contactsScript(contacts, log, release), pushes)
}

func contactsScriptPushingWith(inner func(context.Context, *ipc.Session),
	pushes <-chan []ipc.InheritedContactEntry) func(context.Context, *ipc.Session) {
	return func(ctx context.Context, s *ipc.Session) {
		go func() {
			for {
				select {
				case set := <-pushes:
					out, err := ipc.NewFrame(ipc.FrameInheritedContactsChanged, "", ipc.InheritedContactsChangedPayload{
						Contacts: set,
						Linked:   true,
					})
					if err == nil {
						_ = s.Send(out)
					}
				case <-ctx.Done():
					return
				}
			}
		}()
		inner(ctx, s)
	}
}

func TestContactsPane_APushWithAnOpenDeviceOffersAgain(t *testing.T) {
	log := &offerLog{}
	release := make(chan struct{}, 8)
	pushes := make(chan []ipc.InheritedContactEntry, 8)
	start := []ipc.InheritedContactEntry{
		{Fingerprint: simCarolFP, Nick: "carol", Label: "carol", Devices: 1, Offers: 1, Open: 1},
		{Fingerprint: simDaveFP, Nick: "dave", Label: "dave", Devices: 1, Offers: 1, Open: 1},
	}
	dir, addr, cancel := spinScriptedIPCServer(t, contactsScriptPushing(start, log, release, pushes))
	defer cancel()
	_, sim := launchApp(t, dir, addr)

	openContactsPane(t, sim)
	focusFrame(t, sim)
	sim.InjectKey(tcell.KeyRune, 'p', tcell.ModNone)
	waitForOffers(t, log, 1)
	release <- struct{}{}
	assertOffersStayAt(t, log, 1, 300*time.Millisecond)

	pushes <- []ipc.InheritedContactEntry{
		{Fingerprint: simCarolFP, Nick: "carol", Label: "carol", Devices: 1, Offers: 1, Open: 0},
		{Fingerprint: simDaveFP, Nick: "dave", Label: "dave", Devices: 2, Offers: 2, Open: 2},
	}
	assertOffersStayAt(t, log, 1, 500*time.Millisecond)

	pushes <- []ipc.InheritedContactEntry{
		{Fingerprint: simCarolFP, Nick: "carol", Label: "carol", Devices: 2, Paired: 1, Offers: 1, Open: 1, Waiting: true},
	}
	assertOffersStayAt(t, log, 1, 500*time.Millisecond)
	pushes <- []ipc.InheritedContactEntry{
		{Fingerprint: simCarolFP, Nick: "carol", Label: "carol", Devices: 2, Paired: 1, Offers: 1, Open: 1, Invited: true},
	}
	assertOffersStayAt(t, log, 1, 500*time.Millisecond)

	pushes <- []ipc.InheritedContactEntry{
		{Fingerprint: simCarolFP, Nick: "carol", Label: "carol", Devices: 2, Paired: 1, Offers: 1, Open: 1},
	}
	waitForOffers(t, log, 2)
	release <- struct{}{}
	assertOffersStayAt(t, log, 2, 500*time.Millisecond)
	if got := log.seen(); got[1] != simCarolFP {
		t.Fatalf("offers = %v, want carol again", got)
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.unforced != 0 {
		t.Fatal("the offer again went without force")
	}
	if len(log.openOnly) != 2 || log.openOnly[0] || !log.openOnly[1] {
		t.Fatalf("open_only per offer = %v, want the press without it and the re-check with it", log.openOnly)
	}
}

func TestContactsPane_NothingOpenIsDone(t *testing.T) {
	log := &offerLog{}
	release := make(chan struct{}, 8)
	pushes := make(chan []ipc.InheritedContactEntry, 8)
	start := []ipc.InheritedContactEntry{
		{Fingerprint: simCarolFP, Nick: "carol", Label: "carol", Devices: 1, Offers: 1, Open: 1},
	}
	n := 0
	var mu sync.Mutex
	answer := func(ipc.Frame) (ipc.FrameType, any) {
		mu.Lock()
		defer mu.Unlock()
		n++
		if n == 1 {
			return ipc.FrameInvitationOffered, ipc.InvitationOfferedResponse{MsgID: "inv", ExpiresAt: time.Now().Add(time.Hour).Unix(), Siblings: 1}
		}
		return ipc.FrameError, ipc.ErrorPayload{Code: ipc.ReasonNothingOpen}
	}
	dir, addr, cancel := spinScriptedIPCServer(t, contactsScriptPushingWith(contactsScriptAnswering(start, log, release, answer), pushes))
	defer cancel()
	_, sim := launchApp(t, dir, addr)

	openContactsPane(t, sim)
	focusFrame(t, sim)
	sim.InjectKey(tcell.KeyRune, 'p', tcell.ModNone)
	waitForOffers(t, log, 1)
	release <- struct{}{}
	pushes <- []ipc.InheritedContactEntry{
		{Fingerprint: simCarolFP, Nick: "carol", Label: "carol", Devices: 2, Offers: 2, Open: 1},
	}
	waitForOffers(t, log, 2)
	release <- struct{}{}
	waitForScreen(t, sim, "invitation sent")
	assertOffersStayAt(t, log, 2, 400*time.Millisecond)
	if strings.Contains(flatten(screenText(sim)), "it did not go out") {
		t.Fatal("nothing_open was drawn as a failure")
	}
}

func TestContactsPane_APushDoesNotOfferAgainAfterARefusal(t *testing.T) {
	log := &offerLog{}
	release := make(chan struct{}, 8)
	pushes := make(chan []ipc.InheritedContactEntry, 8)
	start := []ipc.InheritedContactEntry{
		{Fingerprint: simCarolFP, Nick: "carol", Label: "carol", Devices: 1, Offers: 1, Open: 1},
	}
	refuse := func(ipc.Frame) (ipc.FrameType, any) {
		return ipc.FrameError, ipc.ErrorPayload{Code: ipc.ReasonSelfCheckFailed}
	}
	dir, addr, cancel := spinScriptedIPCServer(t, contactsScriptPushingWith(contactsScriptAnswering(start, log, release, refuse), pushes))
	defer cancel()
	_, sim := launchApp(t, dir, addr)

	openContactsPane(t, sim)
	focusFrame(t, sim)
	sim.InjectKey(tcell.KeyRune, 'p', tcell.ModNone)
	waitForOffers(t, log, 1)
	release <- struct{}{}
	waitForScreen(t, sim, "it did not go out")
	pushes <- []ipc.InheritedContactEntry{
		{Fingerprint: simCarolFP, Nick: "carol", Label: "carol", Devices: 2, Offers: 2, Open: 2},
	}
	assertOffersStayAt(t, log, 1, 600*time.Millisecond)
}

func TestContactsPane_APushDuringAnOfferIsCheckedWhenItEnds(t *testing.T) {
	var mu sync.Mutex
	current := []ipc.InheritedContactEntry{
		{Fingerprint: simCarolFP, Nick: "carol", Label: "carol", Devices: 1, Offers: 1, Open: 1},
	}
	list := func() []ipc.InheritedContactEntry {
		mu.Lock()
		defer mu.Unlock()
		return current
	}
	log := &offerLog{}
	release := make(chan struct{}, 8)
	pushes := make(chan []ipc.InheritedContactEntry, 8)
	offered := func(ipc.Frame) (ipc.FrameType, any) {
		return ipc.FrameInvitationOffered, ipc.InvitationOfferedResponse{MsgID: "inv", ExpiresAt: time.Now().Add(time.Hour).Unix(), Siblings: 1}
	}
	dir, addr, cancel := spinScriptedIPCServer(t, contactsScriptPushingWith(contactsScriptListing(list, log, release, offered), pushes))
	defer cancel()
	_, sim := launchApp(t, dir, addr)

	openContactsPane(t, sim)
	focusFrame(t, sim)
	readLists := func() int {
		log.mu.Lock()
		defer log.mu.Unlock()
		return log.lists
	}

	sim.InjectKey(tcell.KeyRune, 'p', tcell.ModNone)
	waitForOffers(t, log, 1)
	before := readLists()
	release <- struct{}{}
	assertOffersStayAt(t, log, 1, 500*time.Millisecond)
	if got := readLists(); got != before {
		t.Fatalf("%d list(s) asked for after an offer no push came during", got-before)
	}

	sim.InjectKey(tcell.KeyRune, 'p', tcell.ModNone)
	waitForOffers(t, log, 2)
	pushes <- []ipc.InheritedContactEntry{
		{Fingerprint: simCarolFP, Nick: "carol", Label: "carol", Devices: 2, Offers: 2, Open: 2},
	}
	waitForScreen(t, sim, "Keep this device on")
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	current = []ipc.InheritedContactEntry{
		{Fingerprint: simCarolFP, Nick: "carol", Label: "carol", Devices: 2, Offers: 2, Open: 1},
	}
	mu.Unlock()
	release <- struct{}{}
	waitForOffers(t, log, 3)
	release <- struct{}{}
	assertOffersStayAt(t, log, 3, 500*time.Millisecond)
}
