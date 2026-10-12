package tui_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/tui"
)

const (
	arrowsAlicePC    = "000000000000000000000000000a11ce"
	arrowsAlicePhone = "000000000000000000000000000a1fff"
	arrowsAliceChat  = "chat-alice"
)

var (
	arrowOn  = tui.ColorPresenceArrowOn
	arrowOff = tui.ColorPresenceArrowOff
)

func presenceScript(chatPresence ipc.ChatPresenceChangedPayload, onRow bool) func(context.Context, *ipc.Session) {
	peers := []ipc.PeerEntry{
		{ID: arrowsAlicePC, Nick: "alicepc", Label: "alicepc", ChatID: arrowsAliceChat, LastPassiveAt: time.Now().Unix()},
		{ID: arrowsAlicePhone, Nick: "alicephone", Label: "alicephone", ChatID: arrowsAliceChat, LastPassiveAt: time.Now().Unix()},
	}
	chats := []ipc.ChatEntry{{
		ChatID: arrowsAliceChat, Kind: ipc.ChatKindDirect, PeerID: arrowsAlicePC,
		Label: "alicechat", Members: []string{arrowsAlicePC, arrowsAlicePhone},
	}}
	chatPresence.ChatID = arrowsAliceChat
	if onRow {
		chats[0].Accepting, chats[0].Sending = chatPresence.Accepting, chatPresence.Sending
		chats[0].Chatty, chats[0].Effective = chatPresence.Chatty, chatPresence.Effective
	}
	return func(_ context.Context, s *ipc.Session) {
		peersSent := false
		for {
			f, err := s.Recv()
			if err != nil {
				return
			}
			send := func(typ ipc.FrameType, id string, payload any) {
				out, err := ipc.NewFrame(typ, id, payload)
				if err != nil {
					return
				}
				_ = s.Send(out)
			}
			switch f.Type {
			case ipc.FrameListPeers:
				send(ipc.FramePeersListed, f.ID, ipc.PeersListedResponse{Peers: peers})
				if !peersSent {
					peersSent = true
					send(ipc.FramePresenceChanged, "", ipc.PresenceChangedPayload{PeerID: arrowsAlicePC, Accepting: true, Effective: "accepting"})
					send(ipc.FramePresenceChanged, "", ipc.PresenceChangedPayload{PeerID: arrowsAlicePhone, Sending: true, Chatty: "available", Effective: "available"})
				}
			case ipc.FrameListInheritedContacts:
				send(ipc.FrameInheritedContactsListed, f.ID, ipc.InheritedContactsListedResponse{Linked: true})
			case ipc.FrameListChats:
				send(ipc.FrameChatsListed, f.ID, ipc.ChatsListedResponse{Chats: chats})
				if !onRow {
					send(ipc.FrameChatPresenceChanged, "", chatPresence)
				}
			}
		}
	}
}

type presenceDrawn struct {
	up, down, name tcell.Color
}

func presenceOnScreen(sim tcell.SimulationScreen, name string) (presenceDrawn, string, bool) {
	cells, w, h := simCells(sim)
	runeAt := func(row, col int) rune {
		r := cells[row*w+col].Runes
		if len(r) == 0 || r[0] == 0 {
			return ' '
		}
		return r[0]
	}
	fg := func(row, col int) tcell.Color {
		c, _, _ := cells[row*w+col].Style.Decompose()
		return c
	}
	needle := []rune(name)
	for row := 0; row < h; row++ {
		var line strings.Builder
		for col := 0; col < w; col++ {
			line.WriteRune(runeAt(row, col))
		}
		nameCol, arrowCol := -1, -1
		for col := 0; col+len(needle) <= w && nameCol < 0; col++ {
			match := true
			for i, r := range needle {
				if runeAt(row, col+i) != r {
					match = false
					break
				}
			}
			if match {
				nameCol = col
			}
		}
		for col := 0; col+1 < w && arrowCol < 0; col++ {
			if runeAt(row, col) == '↑' && runeAt(row, col+1) == '↓' {
				arrowCol = col
			}
		}
		if nameCol >= 0 && arrowCol >= 0 {
			return presenceDrawn{fg(row, arrowCol), fg(row, arrowCol+1), fg(row, nameCol)}, line.String(), true
		}
	}
	return presenceDrawn{}, "", false
}

func waitForPresence(t *testing.T, sim tcell.SimulationScreen, name string, want presenceDrawn, what string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		got, line, ok := presenceOnScreen(sim, name)
		if ok && got == want {
			return
		}
		if time.Now().After(deadline) {
			if !ok {
				t.Fatalf("%s: no row holds %q and the arrows; screen:\n%s", what, name, screenText(sim))
			}
			t.Fatalf("%s: the %q row %q draws %+v, want %+v; screen:\n%s", what, name, line, got, want, screenText(sim))
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func showPane(t *testing.T, sim tcell.SimulationScreen, cmd, needle string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		typeCommand(sim, cmd)
		for wait := time.Now().Add(time.Second); time.Now().Before(wait); time.Sleep(25 * time.Millisecond) {
			if strings.Contains(screenText(sim), needle) {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never showed %q; screen:\n%s", cmd, needle, screenText(sim))
		}
	}
}

func openFirstChat(t *testing.T, sim tcell.SimulationScreen, header string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
		sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
		for wait := time.Now().Add(500 * time.Millisecond); time.Now().Before(wait); time.Sleep(25 * time.Millisecond) {
			if strings.Contains(screenText(sim), header) {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the chat window never opened; screen:\n%s", screenText(sim))
		}
	}
}

func TestPresenceArrows_ContactsRowsArePerPairingAndTheChatIsTheChat(t *testing.T) {
	chatPresence := ipc.ChatPresenceChangedPayload{Accepting: true, Sending: true, Chatty: "busy", Effective: "busy"}
	dir, addr, cancel := spinScriptedIPCServer(t, presenceScript(chatPresence, false))
	defer cancel()
	_, sim := launchApp(t, dir, addr)
	normalRow := tview.Styles.PrimaryTextColor

	showPane(t, sim, "/contacts", "alicephone")
	waitForPresence(t, sim, "alicepc", presenceDrawn{arrowOn, arrowOff, normalRow}, "the first pairing's contacts row")
	waitForPresence(t, sim, "alicephone", presenceDrawn{arrowOff, arrowOn, tui.ColorPresenceAvailable}, "the second pairing's contacts row")

	showPane(t, sim, "/chats", "alicechat")
	waitForPresence(t, sim, "alicechat", presenceDrawn{arrowOn, arrowOn, tui.ColorPresenceBusy}, "the /chats row")

	openFirstChat(t, sim, "↑↓ alicepc")
	waitForPresence(t, sim, "alicepc", presenceDrawn{arrowOn, arrowOn, tui.ColorPresenceBusy}, "the chat header")
	if text := screenText(sim); strings.Contains(text, "[busy]") || strings.Contains(text, "●") {
		t.Errorf("the chat header still draws the round chip or the state word; screen:\n%s", text)
	}

	showPane(t, sim, "/contacts", "alicephone")
	waitForPresence(t, sim, "alicepc", presenceDrawn{arrowOn, arrowOff, normalRow}, "the first pairing's contacts row, after the chat")
}

func TestPresenceArrows_TheChatRowCarriesThePresenceWithNoPush(t *testing.T) {
	chatPresence := ipc.ChatPresenceChangedPayload{Sending: true, Chatty: "away", Effective: "away"}
	dir, addr, cancel := spinScriptedIPCServer(t, presenceScript(chatPresence, true))
	defer cancel()
	_, sim := launchApp(t, dir, addr)
	normalRow := tview.Styles.PrimaryTextColor

	showPane(t, sim, "/contacts", "alicephone")
	waitForPresence(t, sim, "alicepc", presenceDrawn{arrowOn, arrowOff, normalRow}, "the first pairing's contacts row")
	waitForPresence(t, sim, "alicephone", presenceDrawn{arrowOff, arrowOn, tui.ColorPresenceAvailable}, "the second pairing's contacts row")

	showPane(t, sim, "/chats", "alicechat")
	waitForPresence(t, sim, "alicechat", presenceDrawn{arrowOff, arrowOn, tui.ColorPresenceAway}, "the /chats row")

	openFirstChat(t, sim, "↑↓ alicepc")
	waitForPresence(t, sim, "alicepc", presenceDrawn{arrowOff, arrowOn, tui.ColorPresenceAway}, "the chat header")

	showPane(t, sim, "/contacts", "alicephone")
	waitForPresence(t, sim, "alicepc", presenceDrawn{arrowOn, arrowOff, normalRow}, "the first pairing's contacts row")
}

func TestPresenceArrows_APushKeepsTheChatsCursorOnItsChat(t *testing.T) {
	push := make(chan struct{}, 1)
	chats := []ipc.ChatEntry{
		{ChatID: arrowsAliceChat, Kind: ipc.ChatKindDirect, PeerID: arrowsAlicePC, Label: "alicechat"},
		{ChatID: "chat-bob", Kind: ipc.ChatKindDirect, PeerID: "00000000000000000000000000000b0b", Label: "bobchat"},
	}
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
		send := func(typ ipc.FrameType, id string, payload any) {
			if out, err := ipc.NewFrame(typ, id, payload); err == nil {
				_ = s.Send(out)
			}
		}
		for {
			select {
			case f, ok := <-in:
				if !ok {
					return
				}
				switch f.Type {
				case ipc.FrameListPeers:
					send(ipc.FramePeersListed, f.ID, ipc.PeersListedResponse{})
				case ipc.FrameListChats:
					send(ipc.FrameChatsListed, f.ID, ipc.ChatsListedResponse{Chats: chats})
				}
			case <-push:
				send(ipc.FrameChatPresenceChanged, "", ipc.ChatPresenceChangedPayload{
					ChatID: arrowsAliceChat, Chatty: "busy", Effective: "busy",
				})
			}
		}
	})
	defer cancel()
	_, sim := launchApp(t, dir, addr)

	selected := func(name string) bool {
		_, bg, _ := styleOfCellHolding(t, sim, name).Decompose()
		return bg == tview.Styles.PrimaryTextColor
	}
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); !cond(); time.Sleep(25 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("%s; screen:\n%s", what, screenText(sim))
			}
		}
	}

	showPane(t, sim, "/chats", "bobchat")
	waitFor("the cursor never reached the second chat", func() bool {
		if selected("bobchat") {
			return true
		}
		sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
		time.Sleep(50 * time.Millisecond)
		return selected("bobchat")
	})

	push <- struct{}{}
	waitForPresence(t, sim, "alicechat", presenceDrawn{arrowOff, arrowOff, tui.ColorPresenceBusy}, "the pushed chat")
	if !selected("bobchat") {
		t.Fatalf("after the push the cursor left the second chat; screen:\n%s", screenText(sim))
	}
}

func TestPresenceArrows_NoHumanStateIsTheNormalColour(t *testing.T) {
	chatPresence := ipc.ChatPresenceChangedPayload{Accepting: true, Effective: "accepting"}
	dir, addr, cancel := spinScriptedIPCServer(t, presenceScript(chatPresence, false))
	defer cancel()
	_, sim := launchApp(t, dir, addr)

	showPane(t, sim, "/contacts", "alicephone")
	waitForPresence(t, sim, "alicephone", presenceDrawn{arrowOff, arrowOn, tui.ColorPresenceAvailable}, "the second pairing's contacts row")
	showPane(t, sim, "/chats", "alicechat")
	waitForPresence(t, sim, "alicechat", presenceDrawn{arrowOn, arrowOff, tview.Styles.PrimaryTextColor}, "the /chats row")
	openFirstChat(t, sim, "↑↓ alicepc")
	waitForPresence(t, sim, "alicepc", presenceDrawn{arrowOn, arrowOff, tview.Styles.TitleColor}, "the chat header")
}
