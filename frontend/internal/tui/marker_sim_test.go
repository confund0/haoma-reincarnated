package tui_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gdamore/tcell/v2"

	"haoma-frontend/internal/ipc"
)

const (
	simPeerID = "aaaabbbbccccddddeeeeffff00001111"
	simChatID = "chat-marker-sim"
)

func markerScript(state string, mirrored bool) func(ipc.FrameType) (ipc.FrameType, any, bool) {
	return rowScript(map[string]any{
		"kind": "text",
		"body": map[string]any{"text": "marker under test"},
	}, state, mirrored)
}

func fileMarkerScript(state string, mirrored bool) func(ipc.FrameType) (ipc.FrameType, any, bool) {
	return rowScript(map[string]any{
		"kind": "file",
		"body": map[string]any{"name": "marker under test", "size": 1024, "state": "ready"},
	}, state, mirrored)
}

func rowScript(extra map[string]any, state string, mirrored bool) func(ipc.FrameType) (ipc.FrameType, any, bool) {
	row := map[string]any{
		"recv_seq":       uint64(1),
		"chat_id":        simChatID,
		"direction":      "out",
		"display_ts":     int64(1_700_000_000),
		"msg_id":         "m-sim-1",
		"delivery_state": state,
	}
	for k, v := range extra {
		row[k] = v
	}
	if mirrored {
		row["mirrored_from_sibling"] = true
	}
	raw, _ := json.Marshal(row)

	return func(t ipc.FrameType) (ipc.FrameType, any, bool) {
		switch t {
		case ipc.FrameListPeers:
			return ipc.FramePeersListed, ipc.PeersListedResponse{
				Peers: []ipc.PeerEntry{{
					ID:     simPeerID,
					Label:  "sim-peer",
					ChatID: simChatID,
				}},
			}, true
		case ipc.FrameListChats:
			return ipc.FrameChatsListed, ipc.ChatsListedResponse{
				Chats: []ipc.ChatEntry{{
					ChatID: simChatID,
					Kind:   ipc.ChatKindDirect,
					PeerID: simPeerID,
					Label:  "sim-peer",
				}},
			}, true
		case ipc.FrameEnsureChat:

			return ipc.FrameChatEnsured, ipc.ChatEnsuredResponse{
				Peer: ipc.PeerEntry{ID: simPeerID, Label: "sim-peer", ChatID: simChatID},
				Chat: ipc.ChatEntry{ChatID: simChatID, Kind: ipc.ChatKindDirect, PeerID: simPeerID, Label: "sim-peer"},
			}, true
		case ipc.FrameListTimeline:
			return ipc.FrameTimelinePage, ipc.TimelinePageResponse{
				PeerID: simPeerID,
				Events: []json.RawMessage{raw},
			}, true
		}
		return "", nil, false
	}
}

func findMarkerCell(t *testing.T, sim tcell.SimulationScreen, needle string) (tcell.Style, tcell.Style, string) {
	t.Helper()
	cells, w, h := simCells(sim)
	runeAt := func(row, col int) rune {
		r := cells[row*w+col].Runes
		if len(r) == 0 || r[0] == 0 {
			return ' '
		}
		return r[0]
	}
	for row := 0; row < h; row++ {
		bodyCol := -1
		for col := 0; col+len([]rune(needle)) <= w; col++ {
			match := true
			for i, want := range []rune(needle) {
				if runeAt(row, col+i) != want {
					match = false
					break
				}
			}
			if match {
				bodyCol = col
				break
			}
		}
		if bodyCol < 0 {
			continue
		}
		for col := bodyCol; col >= 0; col-- {
			if r := runeAt(row, col); r != '<' && r != '✗' {
				continue
			}

			for col > 0 {
				if prev := runeAt(row, col-1); prev != '<' && prev != '✗' {
					break
				}
				col--
			}
			first := cells[row*w+col]
			second := cells[row*w+col+1]
			return first.Style, second.Style, string(first.Runes) + string(second.Runes)
		}
		t.Fatalf("the line carrying %q has no delivery marker before it", needle)
	}
	t.Fatalf("no painted line carried %q; screen:\n%s", needle, screenText(sim))
	return tcell.StyleDefault, tcell.StyleDefault, ""
}

func fgOf(s tcell.Style) tcell.Color {
	fg, _, _ := s.Decompose()
	return fg
}

func TestChatMarker_PaintedStateAndColour(t *testing.T) {
	cases := []struct {
		name     string
		state    string
		mirrored bool
		want     string
		deliverr tcell.Color
		readCol  tcell.Color
	}{
		{"pending", "enqueued", false, "<-", tcell.ColorGray, tcell.ColorGray},
		{"sent", "sent", false, "<-", tcell.ColorGreen, tcell.ColorGreen},
		{"read", "read", false, "<<", tcell.ColorGreen, tcell.ColorGreen},
		{"failed", "failed", false, "✗-", tcell.ColorRed, tcell.ColorRed},

		{"from my other device", "sent", true, "<-", tcell.ColorPurple, tcell.ColorGreen},
		{"read, from my other device", "read", true, "<<", tcell.ColorPurple, tcell.ColorGreen},

		{"pending, from my other device", "enqueued", true, "<-", tcell.ColorGray, tcell.ColorGray},
		{"failed, from my other device", "failed", true, "✗-", tcell.ColorRed, tcell.ColorRed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, addr, cancel := spinScriptedIPCServer(t, funcScript(markerScript(c.state, c.mirrored)))
			defer cancel()
			_, sim := launchApp(t, dir, addr)

			typeCommand(sim, "/chats")
			waitForScreen(t, sim, "sim-peer")

			sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
			sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
			waitForScreen(t, sim, "marker under test")

			deliv, read, glyphs := findMarkerCell(t, sim, "marker under test")
			if glyphs != c.want {
				t.Errorf("painted marker = %q, want %q", glyphs, c.want)
			}
			if got := fgOf(deliv); got != c.deliverr {
				t.Errorf("delivery glyph colour = %v, want %v", got, c.deliverr)
			}
			if got := fgOf(read); got != c.readCol {
				t.Errorf("read glyph colour = %v, want %v — this is what tells the operator which device sent it", got, c.readCol)
			}
		})
	}
}

func funcScript(answer func(ipc.FrameType) (ipc.FrameType, any, bool)) func(context.Context, *ipc.Session) {
	return func(_ context.Context, s *ipc.Session) {
		for {
			f, err := s.Recv()
			if err != nil {
				return
			}
			typ, payload, ok := answer(f.Type)
			if !ok {
				continue
			}
			out, err := ipc.NewFrame(typ, f.ID, payload)
			if err != nil {
				return
			}
			if err := s.Send(out); err != nil {
				return
			}
		}
	}
}

func TestFileMarker_PaintedLadderAndProvenance(t *testing.T) {
	cases := []struct {
		name     string
		state    string
		mirrored bool
		want     string
		head     tcell.Color
		shaft    tcell.Color
	}{
		{"offer not yet delivered", "enqueued", false, "<-", tcell.ColorGray, tcell.ColorGray},
		{"offer delivered, content not pulled", "sent", false, "<-", tcell.ColorGray, tcell.ColorGreen},
		{"content available", "delivered", false, "<-", tcell.ColorGreen, tcell.ColorGreen},
		{"opened", "read", false, "<<", tcell.ColorGreen, tcell.ColorGreen},
		{"failed", "failed", false, "✗-", tcell.ColorRed, tcell.ColorRed},
		{"available, from my other device", "delivered", true, "<-", tcell.ColorGreen, tcell.ColorPurple},
		{"opened, from my other device", "read", true, "<<", tcell.ColorGreen, tcell.ColorPurple},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, addr, cancel := spinScriptedIPCServer(t, funcScript(fileMarkerScript(c.state, c.mirrored)))
			defer cancel()
			_, sim := launchApp(t, dir, addr)

			typeCommand(sim, "/chats")
			waitForScreen(t, sim, "sim-peer")
			sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
			sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
			waitForScreen(t, sim, "marker under test")

			head, shaft, glyphs := findMarkerCell(t, sim, "marker under test")
			if glyphs != c.want {
				t.Errorf("painted marker = %q, want %q", glyphs, c.want)
			}
			if got := fgOf(head); got != c.head {
				t.Errorf("first glyph colour = %v, want %v — this is what the peer's daemon did with the offer", got, c.head)
			}
			if got := fgOf(shaft); got != c.shaft {
				t.Errorf("second glyph colour = %v, want %v", got, c.shaft)
			}
		})
	}
}
