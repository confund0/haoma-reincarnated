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

const simInvitationSecret = "rendezvous-secret-must-never-paint-7f3a91"

func invitationRow(state string, expiresAt int64, direction string) map[string]any {
	return map[string]any{
		"kind":      "invitation",
		"direction": direction,
		"body": map[string]any{
			"expires_at": expiresAt,
			"state":      state,
			"secret":     simInvitationSecret,
		},
	}
}

func invitationScript(state string, expiresAt int64, direction string) func(ipc.FrameType) (ipc.FrameType, any, bool) {
	return rowScript(invitationRow(state, expiresAt, direction), "", false)
}

func styleAt(t *testing.T, sim tcell.SimulationScreen, needle string) tcell.Style {
	t.Helper()
	cells, w, h := simCells(sim)
	runeAt := func(row, col int) rune {
		r := cells[row*w+col].Runes
		if len(r) == 0 || r[0] == 0 {
			return ' '
		}
		return r[0]
	}
	want := []rune(needle)
	for row := 0; row < h; row++ {
		for col := 0; col+len(want) <= w; col++ {
			match := true
			for i, r := range want {
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
	t.Fatalf("no painted cell carried %q; screen:\n%s", needle, screenText(sim))
	return tcell.StyleDefault
}

func assertNoSecretOnScreen(t *testing.T, sim tcell.SimulationScreen) {
	t.Helper()
	cells, w, h := simCells(sim)
	var b strings.Builder
	for row := 0; row < h; row++ {
		for col := 0; col < w; col++ {
			runes := cells[row*w+col].Runes
			if len(runes) == 0 {
				continue
			}
			for _, r := range runes {
				if r == 0 || r == ' ' || (r >= 0x2500 && r <= 0x257F) {
					continue
				}
				b.WriteRune(r)
			}
		}
	}
	if strings.Contains(b.String(), simInvitationSecret) {
		t.Fatalf("the secret that addresses the rendezvous was painted; screen:\n%s", screenText(sim))
	}
}

func invitationDeadline(ts int64) string {
	return time.Unix(ts, 0).Format("2006-01-02 15:04 MST")
}

func TestInvitationRow_PaintsStateAndDeadlineAndNoSecret(t *testing.T) {
	future := time.Now().Add(72 * time.Hour).Unix()
	past := time.Now().Add(-72 * time.Hour).Unix()

	cases := []struct {
		name      string
		state     string
		expiresAt int64
		direction string
		want      string
		colour    tcell.Color
	}{
		{
			name: "offered to me, standing", state: "live", expiresAt: future, direction: "in",
			want:   "* invitation they offered - live - lapses " + invitationDeadline(future),
			colour: tcell.GetColor("#458588"),
		},
		{
			name: "offered by me, standing", state: "live", expiresAt: future, direction: "out",
			want:   "* invitation you offered - live - lapses " + invitationDeadline(future),
			colour: tcell.GetColor("#458588"),
		},
		{
			name: "past its deadline", state: "live", expiresAt: past, direction: "in",
			want:   "* invitation they offered - lapsed - stood until " + invitationDeadline(past),
			colour: tcell.GetColor("#8a8a8a"),
		},
		{
			name: "retired by a newer one", state: "superseded", expiresAt: future, direction: "in",
			want:   "* invitation they offered - retired by a newer invitation",
			colour: tcell.GetColor("#8a8a8a"),
		},
		{
			name: "accepted", state: "accepted", expiresAt: future, direction: "in",
			want:   "* invitation they offered - accepted - the pairing completed",
			colour: tcell.GetColor("#98971a"),
		},
		{
			name: "came to nothing", state: "failed", expiresAt: future, direction: "out",
			want:   "* invitation you offered - failed - no pairing came of it",
			colour: tcell.GetColor("#cc241d"),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, addr, cancel := spinScriptedIPCServer(t, funcScript(invitationScript(c.state, c.expiresAt, c.direction)))
			defer cancel()
			_, sim := launchApp(t, dir, addr)

			typeCommand(sim, "/chats")
			waitForScreen(t, sim, "sim-peer")
			sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
			sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
			waitForScreen(t, sim, c.want)

			stateWord := strings.SplitN(strings.SplitN(c.want, " - ", 2)[1], " ", 2)[0]
			if fg := fgOf(styleAt(t, sim, stateWord)); fg != c.colour {
				t.Errorf("%q painted in %v, want %v", stateWord, fg, c.colour)
			}
			assertNoSecretOnScreen(t, sim)
		})
	}
}

func TestInvitationRow_ChangesWhenThePairingCompletes(t *testing.T) {
	future := time.Now().Add(72 * time.Hour).Unix()
	answer := invitationScript("live", future, "in")

	release := make(chan struct{})
	dir, addr, cancel := spinScriptedIPCServer(t, func(_ context.Context, s *ipc.Session) {
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
			if err != nil || s.Send(out) != nil {
				return
			}
			if f.Type != ipc.FrameListTimeline {
				continue
			}
			<-release
			row := invitationRow("accepted", future, "in")
			row["recv_seq"] = uint64(1)
			row["chat_id"] = simChatID
			row["msg_id"] = "m-sim-1"
			row["display_ts"] = int64(1_700_000_000)
			raw, err := json.Marshal(row)
			if err != nil {
				return
			}
			push, err := ipc.NewFrame(ipc.FrameTimelineEvent, "", ipc.TimelineEventPayload{Event: raw})
			if err != nil || s.Send(push) != nil {
				return
			}
		}
	})
	defer cancel()

	_, sim := launchApp(t, dir, addr)
	typeCommand(sim, "/chats")
	waitForScreen(t, sim, "sim-peer")
	sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
	waitForScreen(t, sim, "live - lapses "+invitationDeadline(future))

	close(release)
	waitForScreen(t, sim, "* invitation they offered - accepted - the pairing completed")

	if strings.Contains(flatten(screenText(sim)), "live - lapses") {
		t.Fatalf("the standing row survived the completion; screen:\n%s", screenText(sim))
	}
	assertNoSecretOnScreen(t, sim)
}

func TestInvites_ListsAnInvitationWithoutTheSecret(t *testing.T) {
	future := time.Now().Add(72 * time.Hour).Unix()
	past := time.Now().Add(-time.Hour).Unix()

	listed := map[string]any{
		"invitations": []map[string]any{
			{
				"msg_id": "inv-1", "chat_id": "chat-a", "label": "alice",
				"direction": "in",
				"minted_at": past, "expires_at": future, "state": "live",
				"secret": simInvitationSecret,
			},
			{
				"msg_id": "inv-2", "chat_id": "chat-b", "label": "bob",
				"direction": "in",
				"minted_at": past, "expires_at": past, "state": "superseded",
				"secret": simInvitationSecret,
			},
		},
	}
	dir, addr, cancel := spinScriptedIPCServer(t, replyScript(map[ipc.FrameType]reply{
		ipc.FrameInvitationsList: {ipc.FrameInvitationsListed, listed},
	}))
	defer cancel()

	_, sim := launchApp(t, dir, addr)
	typeCommand(sim, "/invites")

	waitForScreen(t, sim, "Enter accepts one offered to you, Esc closes")
	waitForScreen(t, sim, "alice")
	waitForScreen(t, sim, "live - Enter accepts")
	waitForScreen(t, sim, invitationDeadline(future))

	waitForScreen(t, sim, "bob")
	waitForScreen(t, sim, "retired by a newer invitation")
	if strings.Contains(flatten(screenText(sim)), "retired by a newer invitation - Enter accepts") {
		t.Fatalf("a retired invitation offered an accept; screen:\n%s", screenText(sim))
	}

	assertNoSecretOnScreen(t, sim)
}

func TestInvites_ListsBothDirectionsAndOnlyAnswersOne(t *testing.T) {
	future := time.Now().Add(72 * time.Hour).Unix()

	listed := map[string]any{
		"invitations": []map[string]any{
			{
				"msg_id": "inv-in", "label": "alice", "direction": "in",
				"expires_at": future, "state": "live",
				"secret": simInvitationSecret,
			},
			{
				"msg_id": "inv-out", "label": "bob", "direction": "out",
				"expires_at": future, "state": "live",
				"secret": simInvitationSecret,
			},
		},
	}
	dir, addr, cancel := spinScriptedIPCServer(t, replyScript(map[ipc.FrameType]reply{
		ipc.FrameInvitationsList: {ipc.FrameInvitationsListed, listed},
	}))
	defer cancel()

	_, sim := launchApp(t, dir, addr)
	typeCommand(sim, "/invites")

	waitForScreen(t, sim, "they offered alice live - Enter accepts")
	waitForScreen(t, sim, "you offered bob live")
	if strings.Contains(flatten(screenText(sim)), "you offered bob live - Enter accepts") {
		t.Fatalf("an invitation this device offered was drawn as answerable; screen:\n%s", screenText(sim))
	}
	assertNoSecretOnScreen(t, sim)
}

func TestInvites_TheCursorFollowsItsRowAcrossARefresh(t *testing.T) {
	future := time.Now().Add(72 * time.Hour).Unix()

	entry := func(id, label string) map[string]any {
		return map[string]any{
			"msg_id": id, "label": label, "direction": "in",
			"expires_at": future, "state": "live",
		}
	}

	answers := [][]map[string]any{
		{entry("inv-alice", "alice"), entry("inv-bob", "bob")},
		{entry("inv-bob", "bob"), entry("inv-carol", "carol")},
	}

	asked := make(chan string, 1)
	release := make(chan struct{})
	calls := 0
	dir, addr, cancel := spinScriptedIPCServer(t, func(_ context.Context, s *ipc.Session) {
		for {
			f, err := s.Recv()
			if err != nil {
				return
			}
			switch f.Type {
			case ipc.FrameInvitationsList:
				idx := calls
				if idx >= len(answers) {
					idx = len(answers) - 1
				}
				calls++
				out, err := ipc.NewFrame(ipc.FrameInvitationsListed, f.ID, map[string]any{"invitations": answers[idx]})
				if err != nil || s.Send(out) != nil {
					return
				}
				if idx != 0 {
					continue
				}

				go func() {
					<-release
					row := invitationRow("live", future, "in")
					row["recv_seq"] = uint64(1)
					row["chat_id"] = simChatID
					row["msg_id"] = "inv-bob"
					row["display_ts"] = int64(1_700_000_000)
					raw, err := json.Marshal(row)
					if err != nil {
						return
					}
					push, err := ipc.NewFrame(ipc.FrameTimelineEvent, "", ipc.TimelineEventPayload{Event: raw})
					if err != nil {
						return
					}
					_ = s.Send(push)
				}()
			case ipc.FrameInvitationAccept:
				var req ipc.InvitationAcceptRequest
				_ = json.Unmarshal(f.Payload, &req)
				select {
				case asked <- req.MsgID:
				default:
				}
				out, err := ipc.NewFrame(ipc.FrameInvitationAccepted, f.ID, ipc.InvitationAcceptedResponse{
					MsgID: req.MsgID, PeerID: "peer-fresh-0001",
				})
				if err != nil || s.Send(out) != nil {
					return
				}
			}
		}
	})
	defer cancel()

	_, sim := launchApp(t, dir, addr)
	typeCommand(sim, "/invites")
	waitForScreen(t, sim, "alice")
	waitForScreen(t, sim, "bob")

	sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)

	close(release)
	waitForScreen(t, sim, "carol")

	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
	select {
	case got := <-asked:
		if got != "inv-bob" {
			t.Fatalf("the accept named row %q, want inv-bob — the cursor did not follow its invitation across the refresh", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("Enter on the list never asked the daemon to accept; screen:\n%s", screenText(sim))
	}
}

func TestInvites_TheCommandBarJumpClosesTheList(t *testing.T) {
	future := time.Now().Add(72 * time.Hour).Unix()

	lists := make(chan struct{}, 64)
	pushNow := make(chan struct{})
	dir, addr, cancel := spinScriptedIPCServer(t, func(_ context.Context, s *ipc.Session) {

		go func() {
			<-pushNow
			row := invitationRow("accepted", future, "out")
			row["recv_seq"] = uint64(1)
			row["chat_id"] = "chat-inherited"
			row["msg_id"] = "inv-somebody-elses"
			row["display_ts"] = int64(1_700_000_000)
			raw, err := json.Marshal(row)
			if err != nil {
				return
			}
			push, err := ipc.NewFrame(ipc.FrameTimelineEvent, "", ipc.TimelineEventPayload{Event: raw})
			if err != nil {
				return
			}
			_ = s.Send(push)
		}()
		for {
			f, err := s.Recv()
			if err != nil {
				return
			}
			if f.Type != ipc.FrameInvitationsList {
				continue
			}
			select {
			case lists <- struct{}{}:
			default:
			}
			out, err := ipc.NewFrame(ipc.FrameInvitationsListed, f.ID, map[string]any{
				"invitations": []map[string]any{{
					"msg_id": "inv-1", "label": "alice", "direction": "in",
					"expires_at": future, "state": "live",
				}},
			})
			if err != nil || s.Send(out) != nil {
				return
			}
		}
	})
	defer cancel()

	_, sim := launchApp(t, dir, addr)
	typeCommand(sim, "/invites")
	waitForScreen(t, sim, "live - Enter accepts")
	<-lists

	sim.InjectKey(tcell.KeyRune, '/', tcell.ModNone)
	waitForScreenGone(t, sim, "Enter accepts one offered to you")

	for len(lists) > 0 {
		<-lists
	}
	close(pushNow)
	waitForScreen(t, sim, "invitation accepted")
	select {
	case <-lists:
		t.Fatal("the closed list kept asking the daemon for invitations")
	default:
	}
}

func waitForScreenGone(t *testing.T, sim tcell.SimulationScreen, gone string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("%q never left the screen; last frame:\n%s", gone, screenText(sim))
		default:
			if !strings.Contains(flatten(screenText(sim)), flatten(gone)) {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
}

func TestInvites_ARefusalOfTheListSaysWhichOne(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, replyScript(map[ipc.FrameType]reply{
		ipc.FrameInvitationsList: {ipc.FrameError, ipc.ErrorPayload{
			Code:    ipc.ReasonStoreReadFailed,
			Message: "badger says " + simInvitationSecret,
		}},
	}))
	defer cancel()

	_, sim := launchApp(t, dir, addr)
	typeCommand(sim, "/invites")

	waitForScreen(t, sim, "The invitations could not be read.")
	waitForScreen(t, sim, "This is not an empty list")
	if strings.Contains(flatten(screenText(sim)), "No invitations.") {
		t.Fatalf("a failed list read as an empty one; screen:\n%s", screenText(sim))
	}
	assertNoSecretOnScreen(t, sim)
}

func TestRepair_ADaemonsMessageNeverReachesTheScreen(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, replyScript(map[ipc.FrameType]reply{
		ipc.FrameListChats: {ipc.FrameChatsListed, ipc.ChatsListedResponse{
			Chats: []ipc.ChatEntry{{
				ChatID:      "chat-inherited",
				Kind:        ipc.ChatKindDirect,
				Fingerprint: "ffff0000",
				Label:       "alice",
			}},
		}},
		ipc.FrameInvitationOffer: {ipc.FrameError, ipc.ErrorPayload{
			Code:    "some_future_reason",
			Message: "rendezvous secret " + simInvitationSecret + " could not be published",
		}},
	}))
	defer cancel()

	_, sim := launchApp(t, dir, addr)
	typeCommand(sim, "/re-pair alice")

	waitForScreen(t, sim, "The invitation was not offered.")
	waitForScreen(t, sim, "some_future_reason")
	assertNoSecretOnScreen(t, sim)
	if strings.Contains(flatten(screenText(sim)), "could not be published") {
		t.Fatalf("the daemon's own message reached the screen; screen:\n%s", screenText(sim))
	}
}

func TestInvites_EnterAcceptsTheRowUnderTheCursorAndReportsItOnce(t *testing.T) {
	future := time.Now().Add(72 * time.Hour).Unix()

	asked := make(chan string, 1)
	dir, addr, cancel := spinScriptedIPCServer(t, func(_ context.Context, s *ipc.Session) {
		for {
			f, err := s.Recv()
			if err != nil {
				return
			}
			switch f.Type {
			case ipc.FrameInvitationsList:
				out, err := ipc.NewFrame(ipc.FrameInvitationsListed, f.ID, map[string]any{
					"invitations": []map[string]any{{
						"msg_id": "inv-1", "chat_id": "chat-a", "label": "alice",
						"direction": "in", "expires_at": future, "state": "live",
						"secret": simInvitationSecret,
					}},
				})
				if err != nil || s.Send(out) != nil {
					return
				}
			case ipc.FrameInvitationAccept:
				var req ipc.InvitationAcceptRequest
				_ = json.Unmarshal(f.Payload, &req)
				select {
				case asked <- req.MsgID:
				default:
				}

				row := invitationRow("accepted", future, "in")
				row["recv_seq"] = uint64(1)
				row["chat_id"] = "chat-a"
				row["msg_id"] = req.MsgID
				row["display_ts"] = int64(1_700_000_000)
				raw, err := json.Marshal(row)
				if err != nil {
					return
				}
				push, err := ipc.NewFrame(ipc.FrameTimelineEvent, "", ipc.TimelineEventPayload{Event: raw})
				if err != nil || s.Send(push) != nil {
					return
				}
				out, err := ipc.NewFrame(ipc.FrameInvitationAccepted, f.ID, ipc.InvitationAcceptedResponse{
					MsgID:  req.MsgID,
					PeerID: "peer-fresh-0001",
					ChatID: "chat-a",
				})
				if err != nil || s.Send(out) != nil {
					return
				}
			}
		}
	})
	defer cancel()

	_, sim := launchApp(t, dir, addr)
	typeCommand(sim, "/invites")
	waitForScreen(t, sim, "live - Enter accepts")

	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)

	select {
	case got := <-asked:
		if got != "inv-1" {
			t.Fatalf("the accept named row %q, want inv-1", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("Enter on the list never asked the daemon to accept; screen:\n%s", screenText(sim))
	}

	waitForScreen(t, sim, "invitation accepted")
	waitForScreen(t, sim, "you can reach alice from this device now")
	waitForScreen(t, sim, "it joined the conversation you already had with them")
	if n := strings.Count(flatten(screenText(sim)), "invitation accepted"); n != 1 {
		t.Fatalf("the outcome was reported %d times, want once; screen:\n%s", n, screenText(sim))
	}
	assertNoSecretOnScreen(t, sim)
}

func TestInvitationRow_AnOutcomeNobodyAskedForIsStillReported(t *testing.T) {
	future := time.Now().Add(72 * time.Hour).Unix()

	dir, addr, cancel := spinScriptedIPCServer(t, func(_ context.Context, s *ipc.Session) {
		row := invitationRow("accepted", future, "out")
		row["recv_seq"] = uint64(1)
		row["chat_id"] = "chat-inherited"
		row["msg_id"] = "inv-offered-by-me"
		row["display_ts"] = int64(1_700_000_000)
		raw, err := json.Marshal(row)
		if err != nil {
			return
		}
		push, err := ipc.NewFrame(ipc.FrameTimelineEvent, "", ipc.TimelineEventPayload{Event: raw})
		if err != nil || s.Send(push) != nil {
			return
		}
		for {
			if _, err := s.Recv(); err != nil {
				return
			}
		}
	})
	defer cancel()

	_, sim := launchApp(t, dir, addr)

	waitForScreen(t, sim, "invitation accepted")
	waitForScreen(t, sim, "are paired now")
	assertNoSecretOnScreen(t, sim)
}

func TestInvites_EmptyListExplainsItself(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, replyScript(map[ipc.FrameType]reply{
		ipc.FrameInvitationsList: {ipc.FrameInvitationsListed, ipc.InvitationsListedResponse{}},
	}))
	defer cancel()

	_, sim := launchApp(t, dir, addr)
	typeCommand(sim, "/invites")
	waitForScreen(t, sim, "No invitations.")
	waitForScreen(t, sim, "When somebody's device cannot reach you, it offers a pairing here.")
	waitForScreen(t, sim, "When you run /re-pair, what you offered is listed here too.")
}

func TestRepair_OffersFromTheContactName(t *testing.T) {
	future := time.Now().Add(7 * 24 * time.Hour).Unix()
	dir, addr, cancel := spinScriptedIPCServer(t, replyScript(map[ipc.FrameType]reply{
		ipc.FrameListChats: {ipc.FrameChatsListed, ipc.ChatsListedResponse{
			Chats: []ipc.ChatEntry{{
				ChatID:      "chat-inherited",
				Kind:        ipc.ChatKindDirect,
				Fingerprint: "ffff0000",
				Label:       "alice",
			}},
		}},
		ipc.FrameInvitationOffer: {ipc.FrameInvitationOffered, ipc.InvitationOfferedResponse{
			MsgID:     "inv-1",
			ExpiresAt: future,
			Siblings:  2,
		}},
	}))
	defer cancel()

	_, sim := launchApp(t, dir, addr)
	typeCommand(sim, "/re-pair alice")

	waitForScreen(t, sim, "invitation offered")
	waitForScreen(t, sim, "handed to 2 of your devices to pass on")
	waitForScreen(t, sim, "it stands until "+invitationDeadline(future))
	assertNoSecretOnScreen(t, sim)
}

func TestRepair_RefusalReadsAsAdvice(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, replyScript(map[ipc.FrameType]reply{
		ipc.FrameListChats: {ipc.FrameChatsListed, ipc.ChatsListedResponse{
			Chats: []ipc.ChatEntry{{
				ChatID:      "chat-inherited",
				Kind:        ipc.ChatKindDirect,
				Fingerprint: "ffff0000",
				Label:       "alice",
			}},
		}},
		ipc.FrameInvitationOffer: {ipc.FrameError, ipc.ErrorPayload{
			Code:    "no_sibling",
			Message: "this device has no linked device to carry an invitation",
		}},
	}))
	defer cancel()

	_, sim := launchApp(t, dir, addr)
	typeCommand(sim, "/re-pair alice")

	waitForScreen(t, sim, "This device has no linked device to carry the invitation.")
	waitForScreen(t, sim, "Link this device to another of yours with /link-device")
	if strings.Contains(flatten(screenText(sim)), "no_sibling") {
		t.Fatalf("the raw refusal token reached the screen; screen:\n%s", screenText(sim))
	}
}

func TestRepair_UnknownContactSaysWhereToLook(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, replyScript(map[ipc.FrameType]reply{
		ipc.FrameListChats: {ipc.FrameChatsListed, ipc.ChatsListedResponse{}},
	}))
	defer cancel()

	_, sim := launchApp(t, dir, addr)
	typeCommand(sim, "/re-pair nobody")
	waitForScreen(t, sim, `no contact here is called "nobody"`)
	waitForScreen(t, sim, "check /chats for the name this device knows them by")
}
