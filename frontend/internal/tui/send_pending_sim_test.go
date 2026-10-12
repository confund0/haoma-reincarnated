package tui_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"haoma-frontend/internal/ipc"
)

const pendingSimText = "typed before the session"

const noSessionYet = "No secure session with this contact yet. Try again in a moment."

func pendingScript(release <-chan struct{}, answer func(s *ipc.Session, f ipc.Frame) error) func(context.Context, *ipc.Session) {
	return chatScript(ipc.ChatEntry{WaitingForSession: true}, ipc.PeerEntry{}, release, answer)
}

func chatScript(chat ipc.ChatEntry, peer ipc.PeerEntry, release <-chan struct{}, answer func(s *ipc.Session, f ipc.Frame) error) func(context.Context, *ipc.Session) {
	waiting := ipc.ChatEntry{
		ChatID:            simChatID,
		Kind:              ipc.ChatKindDirect,
		PeerID:            simPeerID,
		Label:             "sim-peer",
		WaitingForSession: chat.WaitingForSession,
		WaitingForContact: chat.WaitingForContact,
	}
	row := ipc.PeerEntry{ID: simPeerID, Label: "sim-peer", ChatID: simChatID, RetiredAt: peer.RetiredAt}
	return func(_ context.Context, s *ipc.Session) {
		for {
			f, err := s.Recv()
			if err != nil {
				return
			}
			var typ ipc.FrameType
			var payload any
			switch f.Type {
			case ipc.FrameListPeers:
				typ, payload = ipc.FramePeersListed, ipc.PeersListedResponse{
					Peers: []ipc.PeerEntry{row},
				}
			case ipc.FrameListChats:
				typ, payload = ipc.FrameChatsListed, ipc.ChatsListedResponse{Chats: []ipc.ChatEntry{waiting}}
			case ipc.FrameEnsureChat:
				typ, payload = ipc.FrameChatEnsured, ipc.ChatEnsuredResponse{
					Peer: row,
					Chat: waiting,
				}
			case ipc.FrameListTimeline:
				typ, payload = ipc.FrameTimelinePage, ipc.TimelinePageResponse{PeerID: simPeerID}
			case ipc.FrameSendText:
				<-release
				if answer(s, f) != nil {
					return
				}
				continue
			default:
				continue
			}
			out, err := ipc.NewFrame(typ, f.ID, payload)
			if err != nil || s.Send(out) != nil {
				return
			}
		}
	}
}

func openPendingChat(t *testing.T, sim tcell.SimulationScreen) {
	t.Helper()
	typeCommand(sim, "/chats")
	waitForScreen(t, sim, "sim-peer")
	sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
	waitForScreen(t, sim, "Waiting for a secure session. Your messages go out when it is up.")
	typeCommand(sim, pendingSimText)
}

func TestChatWindow_TextStaysOnScreenWhenTheSendFails(t *testing.T) {
	release := make(chan struct{})
	dir, addr, cancel := spinScriptedIPCServer(t, pendingScript(release, func(s *ipc.Session, f ipc.Frame) error {
		out, err := ipc.NewFrame(ipc.FrameError, f.ID, ipc.ErrorPayload{Code: "encrypt_failed", Message: noSessionYet})
		if err != nil {
			return err
		}
		return s.Send(out)
	}))
	defer cancel()

	_, sim := launchApp(t, dir, addr)
	openPendingChat(t, sim)

	waitForScreen(t, sim, pendingSimText+" (sending)")

	close(release)
	waitForScreen(t, sim, "(failed: No secure session")
	waitForScreenGone(t, sim, "(sending)")
	if n := strings.Count(flatten(screenText(sim)), pendingSimText); n != 1 {
		t.Fatalf("the text is on screen %d times after the failure, want once; frame:\n%s", n, screenText(sim))
	}
}

func TestChatWindow_TextGivesWayToTheRowAndTheChatStarts(t *testing.T) {
	const startedAt int64 = 1_700_000_000
	release := make(chan struct{})
	dir, addr, cancel := spinScriptedIPCServer(t, pendingScript(release, func(s *ipc.Session, f ipc.Frame) error {
		row, _ := json.Marshal(map[string]any{
			"recv_seq":       uint64(1),
			"chat_id":        simChatID,
			"direction":      "out",
			"kind":           "text",
			"display_ts":     time.Now().Unix(),
			"msg_id":         "m-pending-1",
			"envelope_id":    "env-pending-1",
			"delivery_state": "enqueued",
			"body":           map[string]any{"text": pendingSimText},
		})
		push, err := ipc.NewFrame(ipc.FrameTimelineEvent, "", ipc.TimelineEventPayload{Event: row})
		if err != nil || s.Send(push) != nil {
			return err
		}
		reply, err := ipc.NewFrame(ipc.FrameTextSent, f.ID, ipc.SendTextResponse{
			EnvelopeID: "env-pending-1", MsgID: "m-pending-1", SenderSeq: 1,
		})
		if err != nil || s.Send(reply) != nil {
			return err
		}
		started, err := ipc.NewFrame(ipc.FrameChatUpdated, "", ipc.ChatUpdatedPayload{Chat: ipc.ChatEntry{
			ChatID:        simChatID,
			Kind:          ipc.ChatKindDirect,
			PeerID:        simPeerID,
			Label:         "sim-peer",
			ChatStartedAt: startedAt,
		}})
		if err != nil {
			return err
		}
		return s.Send(started)
	}))
	defer cancel()

	_, sim := launchApp(t, dir, addr)
	openPendingChat(t, sim)
	waitForScreen(t, sim, pendingSimText+" (sending)")

	close(release)
	want := "Chat started " + time.Unix(startedAt, 0).Format("2006-01-02 15:04:05")
	waitForScreen(t, sim, want)
	waitForScreenGone(t, sim, "Waiting for a secure session")
	waitForScreenGone(t, sim, "(sending)")
	if n := strings.Count(flatten(screenText(sim)), pendingSimText); n != 1 {
		t.Fatalf("the text is on screen %d times after the send, want once (the row); frame:\n%s", n, screenText(sim))
	}
}

func TestChatWindow_TheInviterWaitsForTheContact(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, chatScript(ipc.ChatEntry{WaitingForContact: true}, ipc.PeerEntry{}, nil, nil))
	defer cancel()
	_, sim := launchApp(t, dir, addr)
	typeCommand(sim, "/chats")
	waitForScreen(t, sim, "sim-peer")
	sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
	waitForScreen(t, sim, "Waiting for the contact to answer.")
	if strings.Contains(flatten(screenText(sim)), "Waiting for a secure session") {
		t.Fatalf("the inviter's window says the session is missing; frame:\n%s", screenText(sim))
	}
}

func TestChatWindow_ARefusedTextStaysOnScreen(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, chatScript(ipc.ChatEntry{}, ipc.PeerEntry{RetiredAt: 1_700_000_000}, nil,
		func(*ipc.Session, ipc.Frame) error {
			t.Error("the send went to the daemon for a retired pairing")
			return nil
		}))
	defer cancel()
	_, sim := launchApp(t, dir, addr)
	typeCommand(sim, "/chats")
	waitForScreen(t, sim, "sim-peer")
	sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
	waitForScreen(t, sim, "retired")
	typeCommand(sim, pendingSimText)
	waitForScreen(t, sim, pendingSimText+" (failed: peer retired)")
}
