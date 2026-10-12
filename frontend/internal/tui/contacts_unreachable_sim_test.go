package tui_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"haoma-frontend/internal/ipc"
)

const simUnreachableWarning = "This contact does not answer. If you know they are gone, " +
	"use Delete peer at the bottom of this page. THIS ALSO DELETES THE CHAT HISTORY."

var simUnreachableOrange = tcell.GetColor("#fe8019")

func peersScript(peers []ipc.PeerEntry) func(context.Context, *ipc.Session) {
	return func(_ context.Context, s *ipc.Session) {
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
				send(ipc.FramePeersListed, ipc.PeersListedResponse{Peers: peers})
			case ipc.FrameListInheritedContacts:
				send(ipc.FrameInheritedContactsListed, ipc.InheritedContactsListedResponse{Linked: true})
			case ipc.FrameListChats:
				send(ipc.FrameChatsListed, ipc.ChatsListedResponse{})
			}
		}
	}
}

func modalText(t *testing.T, sim tcell.SimulationScreen, title string) string {
	t.Helper()
	lines := strings.Split(screenText(sim), "\n")
	top := -1
	for i, l := range lines {
		if strings.Contains(l, title) {
			top = i
			break
		}
	}
	if top < 0 {
		t.Fatalf("no box titled %q; screen:\n%s", title, screenText(sim))
	}
	topRow := []rune(lines[top])
	left, right := -1, -1
	for i, r := range topRow {
		if r == '┌' && left < 0 {
			left = i
		}
		if r == '┐' {
			right = i
		}
	}
	if left < 0 || right <= left {
		t.Fatalf("the box titled %q has no corners on its top row %q", title, lines[top])
	}
	var parts []string
	for _, l := range lines[top+1:] {
		row := []rune(l)
		if len(row) <= right {
			break
		}
		if row[left] == '└' {
			break
		}
		parts = append(parts, string(row[left+1:right]))
	}
	return flatten(strings.Join(parts, " "))
}

func TestContactsPane_AnUnreachablePairingIsOrange(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, peersScript([]ipc.PeerEntry{
		{ID: "peer-alive", Nick: "alice", Label: "alice", ChatID: "chat-alice", LastPassiveAt: time.Now().Unix()},
		{ID: "peer-bobgone", Nick: "bob", Label: "bob", ChatID: "chat-bob", Unreachable: true},
	}))
	defer cancel()
	_, sim := launchApp(t, dir, addr)

	typeCommand(sim, "/contacts")
	waitForScreen(t, sim, "peer-bobgone")
	waitForScreen(t, sim, "peer-alive")

	if fg, _, _ := styleAt(t, sim, "peer-bobgone").Decompose(); fg != simUnreachableOrange {
		t.Errorf("the unreachable pairing's peer id is drawn in %v, want orange", fg)
	}
	if fg, _, _ := styleAt(t, sim, "bob").Decompose(); fg != simUnreachableOrange {
		t.Errorf("the unreachable pairing's nick is drawn in %v, want orange", fg)
	}
	if fg, _, _ := styleAt(t, sim, "peer-alive").Decompose(); fg == simUnreachableOrange {
		t.Errorf("the live pairing is drawn orange")
	}
}

func TestContactsPane_TheEditModalOfAnUnreachablePairingOpensWithTheWarning(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, peersScript([]ipc.PeerEntry{
		{ID: "peer-bobgone", Nick: "bob", Label: "bob", ChatID: "chat-bob", Unreachable: true},
	}))
	defer cancel()
	_, sim := launchApp(t, dir, addr)

	typeCommand(sim, "/contacts")
	waitForScreen(t, sim, "peer-bobgone")

	sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyRune, 'e', tcell.ModNone)
	waitForScreen(t, sim, "edit contact")
	waitForScreen(t, sim, "does not answer")

	got := modalText(t, sim, "edit contact")
	if !strings.HasPrefix(got, flatten(simUnreachableWarning)) {
		t.Fatalf("the modal does not start with the warning.\nwant prefix: %q\ngot:         %q",
			flatten(simUnreachableWarning), got)
	}

	if !strings.Contains(got, "Delete peer") {
		t.Errorf("the modal lost its delete button: %q", got)
	}
	if fg, _, _ := styleAt(t, sim, "This contact does not answer.").Decompose(); fg != simUnreachableOrange {
		t.Errorf("the warning is drawn in %v, want orange", fg)
	}
}

func TestContactsPane_TheEditModalOfALivePairingHasNoWarning(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, peersScript([]ipc.PeerEntry{
		{ID: "peer-alive", Nick: "alice", Label: "alice", ChatID: "chat-alice"},
	}))
	defer cancel()
	_, sim := launchApp(t, dir, addr)

	typeCommand(sim, "/contacts")
	waitForScreen(t, sim, "peer-alive")

	sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyRune, 'e', tcell.ModNone)
	waitForScreen(t, sim, "edit contact")
	waitForScreen(t, sim, "Delete peer")

	if got := modalText(t, sim, "edit contact"); strings.Contains(got, "does not answer") {
		t.Fatalf("a live pairing's modal carries the warning: %q", got)
	}
}
