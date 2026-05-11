package tui

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAppendEvent_ReactionSplicesBeforeNewerMessages(t *testing.T) {
	cp := newChatPage("chat-abc", "peer-bob", "bob", 0)

	mk := func(recvSeq uint64, displayTs int64, kind, direction, msgID string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{
			"recv_seq":   recvSeq,
			"chat_id":    "chat-abc",
			"direction":  direction,
			"kind":       kind,
			"display_ts": displayTs,
			"msg_id":     msgID,
			"body":       map[string]any{"text": "x"},
		})
		return b
	}

	cp.appendEvent(mk(1, 1700000000, "text", "out", "mid-target"))
	cp.appendEvent(mk(2, 1700000300, "text", "out", "mid-newer"))

	react, _ := json.Marshal(map[string]any{
		"recv_seq":       uint64(3),
		"chat_id":        "chat-abc",
		"direction":      "in",
		"kind":           "reaction",
		"display_ts":     int64(1700000000),
		"sender_peer_id": "peer-bob",
		"body":           map[string]any{"target_msg_id": "mid-target", "emoji": "👍"},
	})
	cp.appendEvent(react)

	if len(cp.entries) != 3 {
		t.Fatalf("len(entries) = %d, want 3", len(cp.entries))
	}
	var second rawEvent
	_ = json.Unmarshal(cp.entries[1].evJSON, &second)
	if second.Kind != "reaction" {
		t.Fatalf("entries[1] kind = %q, want reaction (must splice between target and newer text)", second.Kind)
	}
	var third rawEvent
	_ = json.Unmarshal(cp.entries[2].evJSON, &third)
	if third.Kind != "text" {
		t.Fatalf("entries[2] kind = %q, want text (newer message stays at bottom)", third.Kind)
	}

	got := cp.view.GetText(true)
	tIdx := strings.Index(got, "x")
	rIdx := strings.Index(got, "👍")
	if tIdx == -1 || rIdx == -1 {
		t.Fatalf("rendered chat missing pieces: %q", got)
	}
	if tIdx > rIdx {
		t.Errorf("rendered order: reaction(👍) appears before any text — got %q", got)
	}
}

func TestAppendEvent_ReactionTargetWithSameSecondNeighbours(t *testing.T) {
	cp := newChatPage("chat-abc", "peer-bob", "bob", 0)

	mk := func(recvSeq uint64, displayTs int64, kind, msgID, text string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{
			"recv_seq":   recvSeq,
			"chat_id":    "chat-abc",
			"direction":  "out",
			"kind":       kind,
			"display_ts": displayTs,
			"msg_id":     msgID,
			"body":       map[string]any{"text": text},
		})
		return b
	}

	cp.appendEvent(mk(1, 1700000000, "text", "mid-target", "hi"))
	cp.appendEvent(mk(2, 1700000000, "text", "mid-after", "and one more"))

	react, _ := json.Marshal(map[string]any{
		"recv_seq":       uint64(3),
		"chat_id":        "chat-abc",
		"direction":      "in",
		"kind":           "reaction",
		"display_ts":     int64(1700000000),
		"sender_peer_id": "peer-bob",
		"body":           map[string]any{"target_msg_id": "mid-target", "emoji": "👍"},
	})
	cp.appendEvent(react)

	var second rawEvent
	_ = json.Unmarshal(cp.entries[1].evJSON, &second)
	if second.Kind != "reaction" {
		t.Errorf("entries[1] kind = %q, want reaction (must splice immediately after its target, even if newer same-second text exists)", second.Kind)
	}
}

func TestPrependEvents_RegroupsReactionsAfterTarget(t *testing.T) {
	cp := newChatPage("chat-abc", "peer-bob", "bob", 0)

	mk := func(recvSeq uint64, displayTs int64, kind, msgID string, body map[string]any) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{
			"recv_seq":   recvSeq,
			"chat_id":    "chat-abc",
			"direction":  "out",
			"kind":       kind,
			"display_ts": displayTs,
			"msg_id":     msgID,
			"body":       body,
		})
		return raw
	}

	chrono := []json.RawMessage{
		mk(1, 1700000000, "text", "mid-target", map[string]any{"text": "hi"}),
		mk(2, 1700000000, "text", "mid-after", map[string]any{"text": "and one more"}),
		mk(3, 1700000000, "reaction", "", map[string]any{"target_msg_id": "mid-target", "emoji": "👍"}),
	}

	newestFirst := []json.RawMessage{chrono[2], chrono[1], chrono[0]}

	cp.prependEvents(newestFirst)

	if len(cp.entries) != 3 {
		t.Fatalf("len(entries) = %d, want 3", len(cp.entries))
	}
	var second rawEvent
	_ = json.Unmarshal(cp.entries[1].evJSON, &second)
	if second.Kind != "reaction" {
		t.Errorf("entries[1] kind = %q, want reaction (chat reopen must regroup reactions next to target)", second.Kind)
	}
}
