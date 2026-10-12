package tui_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"haoma-frontend/internal/ipc"
)

const (
	primarySimChatID = "chat-primary-sim"
	primarySimOldID  = "0000000000000000000000000000a110"
	primarySimLiveID = "0000000000000000000000000000a11c"
)

func chatPrimaryScript(release <-chan struct{}, sent chan<- ipc.SendTextRequest) func(context.Context, *ipc.Session) {
	peers := []ipc.PeerEntry{
		{ID: primarySimLiveID, Label: "sim-alice", ChatID: primarySimChatID},
		{ID: primarySimOldID, Label: "sim-alice", ChatID: primarySimChatID, RetiredAt: 1_790_859_324},
	}
	before := ipc.ChatEntry{ChatID: primarySimChatID, Kind: ipc.ChatKindDirect, PeerID: primarySimOldID, Label: "sim-alice"}
	after := before
	after.PeerID = primarySimLiveID

	return func(_ context.Context, s *ipc.Session) {
		reply := func(f ipc.Frame, typ ipc.FrameType, payload any) bool {
			out, err := ipc.NewFrame(typ, f.ID, payload)
			return err == nil && s.Send(out) == nil
		}
		for {
			f, err := s.Recv()
			if err != nil {
				return
			}
			switch f.Type {
			case ipc.FrameListPeers:
				if !reply(f, ipc.FramePeersListed, ipc.PeersListedResponse{Peers: peers}) {
					return
				}
			case ipc.FrameListChats:
				if !reply(f, ipc.FrameChatsListed, ipc.ChatsListedResponse{Chats: []ipc.ChatEntry{before}}) {
					return
				}
			case ipc.FrameEnsureChat:
				if !reply(f, ipc.FrameChatEnsured, ipc.ChatEnsuredResponse{Peer: peers[1], Chat: before}) {
					return
				}
			case ipc.FrameListTimeline:
				if !reply(f, ipc.FrameTimelinePage, ipc.TimelinePageResponse{PeerID: primarySimOldID}) {
					return
				}
				<-release
				push, err := ipc.NewFrame(ipc.FrameChatUpdated, "", ipc.ChatUpdatedPayload{Chat: after})
				if err != nil || s.Send(push) != nil {
					return
				}
			case ipc.FrameSendText:
				var req ipc.SendTextRequest
				if err := json.Unmarshal(f.Payload, &req); err == nil {
					sent <- req
				}
				if !reply(f, ipc.FrameTextSent, ipc.SendTextResponse{EnvelopeID: "env-primary-sim"}) {
					return
				}
			}
		}
	}
}

func TestChatWindow_FollowsTheChatPrimaryOffARetiredPairing(t *testing.T) {
	release := make(chan struct{})
	sent := make(chan ipc.SendTextRequest, 4)
	dir, addr, cancel := spinScriptedIPCServer(t, chatPrimaryScript(release, sent))
	defer cancel()

	_, sim := launchApp(t, dir, addr)
	typeCommand(sim, "/chats")
	waitForScreen(t, sim, "sim-alice")
	sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
	waitForScreen(t, sim, "sim-alice — retired")

	close(release)
	waitForScreenGone(t, sim, "sim-alice — retired")

	typeCommand(sim, "after the move")
	select {
	case req := <-sent:
		if req.PeerID != primarySimLiveID {
			t.Fatalf("the text was sent by %s, want the live pairing %s", req.PeerID, primarySimLiveID)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no text was sent from the window after the chat moved; last frame:\n%s", screenText(sim))
	}
}
