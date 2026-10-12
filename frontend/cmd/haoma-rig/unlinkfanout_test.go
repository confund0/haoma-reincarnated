package main

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/logid"
)

func TestNoticeRetiredLines_MatchesOnlyTheNoticeRetiringThatPairing(t *testing.T) {
	const slave = "beef0000000000000000000000000000"
	const other = "cafe0000000000000000000000000000"
	line := func(peerID, outcome string) string {
		return `time=2026-10-01T10:00:00Z level=INFO msg="` + noticeRetiredLine + `" peer_id=` +
			logid.Short(peerID) + ` kept_peer_id=` + logid.Short(other) + ` outcome=` + outcome
	}
	cases := []struct {
		name  string
		lines []string
		want  int
	}{
		{"the notice retired that pairing", []string{line(slave, noticeRetiredOutcome)}, 1},
		{"the notice retired another pairing", []string{line(other, noticeRetiredOutcome)}, 0},
		{"the old count rule's outcome", []string{line(slave, "retired")}, 0},
		{"another message naming the pairing", []string{
			`time=2026-10-01T10:00:00Z level=INFO msg="peer retired" peer_id=` + logid.Short(slave) + ` outcome=` + noticeRetiredOutcome,
		}, 0},
		{"the full id, not the on-disk form", []string{
			`time=2026-10-01T10:00:00Z level=INFO msg="` + noticeRetiredLine + `" peer_id=` + slave + ` outcome=` + noticeRetiredOutcome,
		}, 0},
	}
	for _, c := range cases {
		if got := noticeRetiredLines(c.lines, slave); got != c.want {
			t.Errorf("%s: matched %d, want %d", c.name, got, c.want)
		}
	}
}

func TestChatMovedLines_MatchesOnlyTheRetireMovingThatChat(t *testing.T) {
	const chatID = "c0ffee00000000000000000000000000"
	const otherChat = "d00d0000000000000000000000000000"
	const stays = "beef0000000000000000000000000000"
	const goes = "cafe0000000000000000000000000000"
	line := func(chat, primary, outcome, trigger string) string {
		return `time=2026-10-01T10:00:00Z level=INFO msg="` + chatMovedLine + `" chat_id=` + logid.Short(chat) +
			` peer_id=` + logid.Short(goes) + ` trigger=` + trigger + ` outcome=` + outcome +
			` primary_peer_id=` + logid.Short(primary) + ` routes_dropped=1 routes_left=1`
	}
	cases := []struct {
		name  string
		lines []string
		want  int
	}{
		{"the retire moved that chat", []string{line(chatID, stays, chatMovedOutcome, chatMovedTrigger)}, 1},
		{"the sweep moved it", []string{line(chatID, stays, chatMovedOutcome, "backend_ready")}, 0},
		{"a route dropped, the primary kept", []string{line(chatID, stays, "retired_routes_dropped", chatMovedTrigger)}, 0},
		{"another chat", []string{line(otherChat, stays, chatMovedOutcome, chatMovedTrigger)}, 0},
		{"moved onto another pairing", []string{line(chatID, goes, chatMovedOutcome, chatMovedTrigger)}, 0},
		{"another message", []string{
			`time=2026-10-01T10:00:00Z level=INFO msg="chat.updated pushed" chat_id=` + logid.Short(chatID) +
				` trigger=retire outcome=` + chatMovedOutcome + ` primary_peer_id=` + logid.Short(stays),
		}, 0},
	}
	for _, c := range cases {
		if got := chatMovedLines(c.lines, chatID, stays); got != c.want {
			t.Errorf("%s: matched %d, want %d", c.name, got, c.want)
		}
	}
}

func TestChatMovedTo(t *testing.T) {
	const chatID, stays, goes = "chat-1", "peer-stays", "peer-goes"
	live := []ipc.PeerEntry{{ID: stays}, {ID: goes, RetiredAt: 1}}
	moved := []ipc.ChatEntry{{ChatID: chatID, PeerID: stays, Members: []string{stays}}}
	cases := []struct {
		name  string
		chats []ipc.ChatEntry
		peers []ipc.PeerEntry
		ok    bool
	}{
		{"moved, live, retired pairing out", moved, live, true},
		{"still on the retired pairing", []ipc.ChatEntry{{ChatID: chatID, PeerID: goes, Members: []string{goes, stays}}}, live, false},
		{"moved, but still routes to the retired pairing", []ipc.ChatEntry{{ChatID: chatID, PeerID: stays, Members: []string{stays, goes}}}, live, false},
		{"moved onto a retired pairing", moved, []ipc.PeerEntry{{ID: stays, RetiredAt: 1}}, false},
		{"moved onto a pairing the peer list does not hold", moved, []ipc.PeerEntry{{ID: goes, RetiredAt: 1}}, false},
		{"the chat is gone", []ipc.ChatEntry{{ChatID: "chat-2", PeerID: stays}}, live, false},
	}
	for _, c := range cases {
		err := chatMovedTo(c.chats, c.peers, chatID, stays, goes)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", c.name, err, c.ok)
		}
	}
}

func TestUnlinkFanoutChecks_ListsEveryCheck(t *testing.T) {
	seen := map[string]bool{}
	for _, name := range unlinkFanoutChecks {
		if seen[name] {
			t.Errorf("check %q is listed twice", name)
		}
		seen[name] = true
	}
	src, err := os.ReadFile("unlinkfanout.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`res\.add\("([^"]+)"`).FindAllStringSubmatch(string(src), -1) {
		if !seen[m[1]] {
			t.Errorf("the phase records %q, but unlinkFanoutChecks does not list it", m[1])
		}
	}
}

func TestChatMovedLine_IsWhatHaomaWrites(t *testing.T) {
	src, err := os.ReadFile("../haoma/retired_routes.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`routesDroppedMsg = "` + chatMovedLine + `"`,
		`routesOutcomeMoved          = "` + chatMovedOutcome + `"`,
		`moveChatOffRetired(d, dc, peers, prefer, "` + chatMovedTrigger + `")`,
	} {
		if !strings.Contains(string(src), want) {
			t.Errorf("cmd/haoma/retired_routes.go does not hold %s", want)
		}
	}
}
