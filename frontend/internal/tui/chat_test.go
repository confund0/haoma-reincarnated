package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"haoma-frontend/internal/ipc"
)

func TestFormatFileSize_Boundaries(t *testing.T) {
	cases := []struct {
		in   uint64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{4_613_734, "4.4 MB"},
		{2 * 1 << 30, "2.00 GB"},
	}
	for _, c := range cases {
		if got := formatFileSize(c.in); got != c.want {
			t.Errorf("formatFileSize(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFileArrow_StateAndDirectionMatrix(t *testing.T) {
	cases := []struct {
		dir, state string
		want       string
	}{
		{"out", "ready", "[green]<-[-]"},
		{"in", "ready", "[green]->[-]"},
		{"out", "downloading", "[gray]<-[-]"},
		{"in", "awaiting_key", "[gray]->[-]"},
		{"out", "failed_permanent", "[red]✗-[-]"},
		{"in", "expired", "[red]✗>[-]"},
	}
	for _, c := range cases {
		if got := fileArrow(c.dir, c.state); got != c.want {
			t.Errorf("fileArrow(%q,%q) = %q, want %q", c.dir, c.state, got, c.want)
		}
	}
}

func TestRenderFileStateChip_DownloadingPercent(t *testing.T) {
	fb := fileBodyMin{State: "downloading", Size: 1000, BytesReceived: 230}
	got := renderFileStateChip(fb, 0)
	if !strings.Contains(got, "23%") {
		t.Errorf("downloading chip should report 23%%, got %q", got)
	}

	got = renderFileStateChip(fb, 500)
	if !strings.Contains(got, "50%") {
		t.Errorf("live progress should win, got %q", got)
	}
}

func TestRenderFileStateChip_TerminalStates(t *testing.T) {
	cases := []struct {
		fb       fileBodyMin
		contains string
	}{
		{fileBodyMin{State: "ready"}, "ready"},
		{fileBodyMin{State: "awaiting_key"}, "awaiting key"},
		{fileBodyMin{State: "failed_permanent", LastError: "sha256 mismatch"}, "sha256 mismatch"},
		{fileBodyMin{State: "expired"}, "expired"},
		{fileBodyMin{State: ""}, "pending"},
	}
	for _, c := range cases {
		got := renderFileStateChip(c.fb, 0)
		if !strings.Contains(got, c.contains) {
			t.Errorf("state %q chip = %q, want substring %q", c.fb.State, got, c.contains)
		}
	}
}

func TestRenderFileStateChip_FailedTransient(t *testing.T) {
	withErr := renderFileStateChip(fileBodyMin{State: "failed_transient", LastError: "TTL expired"}, 0)
	if !strings.Contains(withErr, "failed (transient):") {
		t.Errorf("failed_transient with err = %q, want \"failed (transient):\" prefix", withErr)
	}
	if !strings.Contains(withErr, "TTL expired") {
		t.Errorf("failed_transient with err = %q, want LastError appended", withErr)
	}
	if strings.Contains(withErr, "retrying") {
		t.Errorf("failed_transient should not say \"retrying\" any more, got %q", withErr)
	}
	bare := renderFileStateChip(fileBodyMin{State: "failed_transient"}, 0)
	if !strings.Contains(bare, "failed (transient)") {
		t.Errorf("failed_transient bare = %q, want \"failed (transient)\"", bare)
	}
}

func TestRenderEntry_FileRowShape(t *testing.T) {
	cp := newChatPage("chat-x", "peer-y", "bob", 0)
	body, _ := json.Marshal(fileBodyMin{
		Name:  "kjv.txt",
		Size:  4_613_734,
		State: "downloading",
	})
	raw, _ := json.Marshal(map[string]any{
		"recv_seq":   uint64(1),
		"chat_id":    "chat-x",
		"direction":  "in",
		"kind":       "file",
		"display_ts": int64(1),
		"msg_id":     "m-file-1",
		"body":       json.RawMessage(body),
	})
	cp.appendEvent(raw)
	rendered := cp.renderEntry(cp.entries[0])
	for _, want := range []string{"kjv.txt", "4.4 MB", "downloading", "[gray]->[-]"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("file row missing %q: %q", want, rendered)
		}
	}
}

func TestRenderEntry_FileRowDeletedTombstone(t *testing.T) {
	cp := newChatPage("chat-x", "peer-y", "bob", 0)
	body, _ := json.Marshal(fileBodyMin{Name: "secret.bin", Size: 1024, State: "ready"})
	raw, _ := json.Marshal(map[string]any{
		"recv_seq":   uint64(1),
		"chat_id":    "chat-x",
		"direction":  "out",
		"kind":       "file",
		"display_ts": int64(1),
		"deleted_at": int64(99),
		"msg_id":     "m-file-1",
		"body":       json.RawMessage(body),
	})
	cp.appendEvent(raw)
	rendered := cp.renderEntry(cp.entries[0])
	if !strings.Contains(rendered, "[file deleted]") {
		t.Errorf("tombstoned file row should show [file deleted], got %q", rendered)
	}
	if strings.Contains(rendered, "secret.bin") {
		t.Errorf("tombstoned row leaked the original name: %q", rendered)
	}
}

func TestUpdateFileProgress_LiveCounter(t *testing.T) {
	cp := newChatPage("chat-x", "peer-y", "bob", 0)
	body, _ := json.Marshal(fileBodyMin{Name: "big.bin", Size: 1000, State: "downloading"})
	raw, _ := json.Marshal(map[string]any{
		"recv_seq":   uint64(1),
		"chat_id":    "chat-x",
		"direction":  "in",
		"kind":       "file",
		"display_ts": int64(1),
		"msg_id":     "m-file-1",
		"body":       json.RawMessage(body),
	})
	cp.appendEvent(raw)
	cp.updateFileProgress("m-file-1", 750)
	if cp.fileProgress["m-file-1"] != 750 {
		t.Errorf("fileProgress = %d, want 750", cp.fileProgress["m-file-1"])
	}
	rendered := cp.renderEntry(cp.entries[0])
	if !strings.Contains(rendered, "75%") {
		t.Errorf("post-progress render missing 75%%: %q", rendered)
	}
}

func TestApplyTitle_Retired(t *testing.T) {
	cp := newChatPage("chat-abc", "peer123abcdef", "alice", 1776956500)

	cp.applyTitle("available")

	title := cp.view.GetTitle()
	if !strings.Contains(title, GlyphRetired) {
		t.Errorf("retired title missing glyph %q: %q", GlyphRetired, title)
	}
	if !strings.Contains(title, "retired 2026-04-23") {
		t.Errorf("retired title missing date: %q", title)
	}
	if strings.Contains(title, "●") || strings.Contains(title, "○") || strings.Contains(title, "◐") {
		t.Errorf("retired title should not carry presence chip: %q", title)
	}
}

func TestSetDeliveryState_DoesNotDowngradeFromRead(t *testing.T) {
	cp := newChatPage("chat-x", "peer-y", "bob", 0)

	raw, _ := json.Marshal(map[string]any{
		"recv_seq":       uint64(1),
		"chat_id":        "chat-x",
		"direction":      "out",
		"kind":           "text",
		"display_ts":     int64(1),
		"envelope_id":    "env-1",
		"msg_id":         "m-1",
		"delivery_state": "sent",
		"body":           map[string]any{"text": "hi"},
	})
	cp.appendEvent(raw)
	if cp.entries[0].delivery != "sent" {
		t.Fatalf("seed delivery = %q, want sent", cp.entries[0].delivery)
	}

	if !cp.setDeliveryState("env-1", "read") {
		t.Fatalf("setDeliveryState(env-1, read) returned false")
	}
	if cp.entries[0].delivery != "read" {
		t.Fatalf("after read promotion delivery = %q, want read", cp.entries[0].delivery)
	}

	if cp.setDeliveryState("env-1", "sent") {
		t.Errorf("setDeliveryState should refuse downgrade from read; returned true")
	}
	if cp.entries[0].delivery != "read" {
		t.Errorf("delivery downgraded to %q; want still read", cp.entries[0].delivery)
	}
}

func TestApplyTitle_PresenceChip(t *testing.T) {
	cases := []struct {
		label string
		glyph string
		word  string
	}{
		{"available", GlyphPresenceOnline, "[available]"},
		{"away", GlyphPresenceOnline, "[away]"},
		{"busy", GlyphPresenceOnline, "[busy]"},
		{"accepting", GlyphPresenceAccepting, "[accepting]"},
		{"unknown", GlyphPresenceUnknown, "[unknown]"},
	}
	for _, c := range cases {
		cp := newChatPage("chat-abc", "peer123abcdef", "bob", 0)
		cp.applyTitle(c.label)
		title := cp.view.GetTitle()
		if !strings.Contains(title, c.glyph) {
			t.Errorf("label=%q: title %q missing glyph %q", c.label, title, c.glyph)
		}
		if !strings.Contains(title, c.word) {
			t.Errorf("label=%q: title %q missing wording %q", c.label, title, c.word)
		}
	}
}

func TestDeleteByRecvSeq_RemovesAndShiftsEnvIndex(t *testing.T) {
	cp := newChatPage("chat-abc", "peer-bob", "bob", 0)

	mk := func(recvSeq uint64, envID, direction string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{
			"recv_seq":    recvSeq,
			"chat_id":     "chat-abc",
			"direction":   direction,
			"kind":        "text",
			"display_ts":  1700000000,
			"envelope_id": envID,
			"body":        map[string]any{"text": "x"},
		})
		return b
	}

	cp.appendEvent(mk(1, "env-1", "out"))
	cp.appendEvent(mk(2, "", "in"))
	cp.appendEvent(mk(3, "env-3", "out"))

	if got := cp.envIndex["env-3"]; got != 2 {
		t.Fatalf("pre-delete env-3 index = %d, want 2", got)
	}

	if !cp.deleteByRecvSeq(2) {
		t.Fatalf("deleteByRecvSeq(2) = false, want true")
	}
	if len(cp.entries) != 2 {
		t.Errorf("len(entries) = %d, want 2", len(cp.entries))
	}
	if got := cp.envIndex["env-1"]; got != 0 {
		t.Errorf("env-1 index = %d, want 0 (unchanged)", got)
	}
	if got := cp.envIndex["env-3"]; got != 1 {
		t.Errorf("env-3 index after delete = %d, want 1 (shifted down)", got)
	}

	if !cp.deleteByRecvSeq(1) {
		t.Fatalf("deleteByRecvSeq(1) = false, want true")
	}
	if _, ok := cp.envIndex["env-1"]; ok {
		t.Errorf("env-1 still in envIndex after its row was deleted")
	}

	if cp.deleteByRecvSeq(999) {
		t.Errorf("deleteByRecvSeq(999) = true, want false for missing row")
	}
}

func TestUpsertEvent_AppendsThenReplacesByMsgID(t *testing.T) {
	cp := newChatPage("chat-abc", "peer-bob", "bob", 0)

	mk := func(recvSeq uint64, msgID, envID, direction, text string, editedAt int64) json.RawMessage {
		b, _ := json.Marshal(map[string]any{
			"recv_seq":    recvSeq,
			"chat_id":     "chat-abc",
			"direction":   direction,
			"kind":        "text",
			"display_ts":  1700000000,
			"envelope_id": envID,
			"msg_id":      msgID,
			"edited_at":   editedAt,
			"body":        map[string]any{"text": text},
		})
		return b
	}

	cp.upsertEvent(mk(1, "mid-1", "env-1", "out", "orignal", 0))
	if len(cp.entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1 after first append", len(cp.entries))
	}
	if got := cp.msgIDIndex["mid-1"]; got != 0 {
		t.Errorf("msgIDIndex mid-1 = %d, want 0", got)
	}

	cp.setDeliveryState("env-1", "sent")
	if cp.entries[0].delivery != "sent" {
		t.Fatalf("setup: delivery = %q, want 'sent'", cp.entries[0].delivery)
	}

	cp.upsertEvent(mk(1, "mid-1", "env-1", "out", "corrected", 1700000050))
	if len(cp.entries) != 1 {
		t.Errorf("len(entries) = %d, want 1 after upsert (no append)", len(cp.entries))
	}
	if cp.entries[0].delivery != "sent" {
		t.Errorf("delivery dropped on upsert: %q", cp.entries[0].delivery)
	}
	if cp.entries[0].envID != "env-1" {
		t.Errorf("envID dropped on upsert: %q", cp.entries[0].envID)
	}

	got := cp.renderEntry(cp.entries[0])
	if !strings.Contains(got, "corrected") {
		t.Errorf("rendered line missing new text: %q", got)
	}
	if !strings.Contains(got, "(edited)") {
		t.Errorf("rendered line missing (edited) badge: %q", got)
	}

	cp.upsertEvent(mk(2, "mid-2", "", "in", "from bob", 0))
	if len(cp.entries) != 2 {
		t.Errorf("len(entries) = %d, want 2 after new-id upsert", len(cp.entries))
	}

	cp.upsertEvent(mk(3, "", "", "in", "legacy", 0))
	if len(cp.entries) != 3 {
		t.Errorf("len(entries) = %d, want 3 after no-msg_id upsert", len(cp.entries))
	}
}

func TestCollectEditable_FiltersByOwnershipAndWindow(t *testing.T) {
	cp := newChatPage("chat-abc", "peer-bob", "bob", 0)
	now := int64(1700000000)
	cutoff := now - editWindowSeconds

	mk := func(msgID, direction, kind, decryptStatus, text string, displayTs int64) json.RawMessage {
		b, _ := json.Marshal(map[string]any{
			"recv_seq":       1,
			"chat_id":        "chat-abc",
			"direction":      direction,
			"kind":           kind,
			"display_ts":     displayTs,
			"msg_id":         msgID,
			"decrypt_status": decryptStatus,
			"body":           map[string]any{"text": text},
		})
		return b
	}

	cp.appendEvent(mk("ok-1", "out", "text", "", "fresh", now-60))
	cp.appendEvent(mk("too-old", "out", "text", "", "yesterday", cutoff-1))
	cp.appendEvent(mk("inbound", "in", "text", "", "theirs", now-60))
	cp.appendEvent(mk("", "out", "text", "", "legacy", now-60))
	cp.appendEvent(mk("failed", "out", "text", "failed", "bad", now-60))
	cp.appendEvent(mk("tc", "out", "timer_change", "", "bc", now-60))

	got := collectEditable(cp)
	if len(got) != 0 {
		t.Errorf("past-dated rows should all be out of window; got %d candidates", len(got))
	}

	cp2 := newChatPage("chat-abc", "peer-bob", "bob", 0)
	realNow := time.Now().Unix()
	cp2.appendEvent(mk("ok-1", "out", "text", "", "fresh", realNow-60))
	cp2.appendEvent(mk("inbound", "in", "text", "", "theirs", realNow-60))
	cp2.appendEvent(mk("", "out", "text", "", "legacy", realNow-60))
	cp2.appendEvent(mk("failed", "out", "text", "failed", "bad", realNow-60))
	cp2.appendEvent(mk("tc", "out", "timer_change", "", "bc", realNow-60))

	got = collectEditable(cp2)
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1 (only ok-1)", len(got))
	}
	if got[0].msgID != "ok-1" {
		t.Errorf("candidate msgID = %q, want 'ok-1'", got[0].msgID)
	}
	if got[0].text != "fresh" {
		t.Errorf("candidate text = %q, want 'fresh'", got[0].text)
	}
}

func TestBuildContactsTable_RotationColumn(t *testing.T) {
	peers := []ipc.PeerEntry{
		{ID: "alice", Alias: "alice"},
		{ID: "bob", Alias: "bob"},
		{ID: "carol", Alias: "carol"},
		{ID: "dave", Alias: "dave", RetiredAt: 1776956500},
	}
	rot := func(id string) rotationCellInfo {
		switch id {
		case "alice":
			return rotationCellInfo{Direction: "we"}
		case "bob":
			return rotationCellInfo{Direction: "they"}
		case "dave":
			return rotationCellInfo{Direction: "we"}
		}
		return rotationCellInfo{}
	}
	tbl := buildContactsTable(peers, nil, rot, func(string) {})

	if got := tbl.GetCell(1, 1).Text; got != GlyphRotationOurs {
		t.Errorf("alice (we) marker = %q, want %q", got, GlyphRotationOurs)
	}
	if got := tbl.GetCell(2, 1).Text; got != GlyphRotationTheirs {
		t.Errorf("bob (they) marker = %q, want %q", got, GlyphRotationTheirs)
	}
	if got := tbl.GetCell(3, 1).Text; got != "" {
		t.Errorf("carol (no rotation) marker = %q, want empty", got)
	}
	if got := tbl.GetCell(4, 1).Text; got != "" {
		t.Errorf("retired peer marker = %q, want empty", got)
	}
}

func TestBuildChatsTable_RotationColumn(t *testing.T) {
	chats := []ipc.ChatEntry{
		{ChatID: "dm-alice", Kind: ipc.ChatKindDirect, PeerID: "alice", Label: "alice"},
		{ChatID: "dm-bob", Kind: ipc.ChatKindDirect, PeerID: "bob", Label: "bob"},
		{ChatID: "grp", Kind: ipc.ChatKindGroup, GroupName: "team", Label: "team"},
	}
	rot := func(id string) rotationCellInfo {
		switch id {
		case "alice":
			return rotationCellInfo{Direction: "we"}
		case "bob":
			return rotationCellInfo{Direction: "they"}
		}
		return rotationCellInfo{}
	}
	tbl := buildChatsTable(chats, nil, nil, rot, func(string) {})

	if got := tbl.GetCell(1, 1).Text; got != GlyphRotationOurs {
		t.Errorf("alice DM marker = %q, want %q", got, GlyphRotationOurs)
	}
	if got := tbl.GetCell(2, 1).Text; got != GlyphRotationTheirs {
		t.Errorf("bob DM marker = %q, want %q", got, GlyphRotationTheirs)
	}
	if got := tbl.GetCell(3, 1).Text; got != "" {
		t.Errorf("group row marker = %q, want empty", got)
	}
}

func TestBuildContactsTable_PresenceColumn(t *testing.T) {
	peers := []ipc.PeerEntry{
		{ID: "alice", Alias: "alice"},
		{ID: "bob", Alias: "bob"},
		{ID: "carol", Alias: "carol", RetiredAt: 1776956500},
	}
	cache := map[string]string{
		"alice": "available",

		"carol": "available",
	}
	resolve := func(id string) string {
		if v, ok := cache[id]; ok {
			return v
		}
		return ""
	}
	tbl := buildContactsTable(peers, resolve, nil, func(string) {})

	if got := tbl.GetCell(1, 0).Text; got != GlyphPresenceOnline {
		t.Errorf("alice presence cell = %q, want %q", got, GlyphPresenceOnline)
	}
	if got := tbl.GetCell(2, 0).Text; got != GlyphPresenceUnknown {
		t.Errorf("bob presence cell = %q, want %q", got, GlyphPresenceUnknown)
	}
	if got := tbl.GetCell(3, 0).Text; got != "—" {
		t.Errorf("retired peer presence cell = %q, want %q", got, "—")
	}
}

func TestBuildChatsTable_PresenceColumn(t *testing.T) {
	chats := []ipc.ChatEntry{
		{ChatID: "chat-dm", Kind: ipc.ChatKindDirect, PeerID: "alice", Label: "alice"},
		{ChatID: "chat-grp", Kind: ipc.ChatKindGroup, GroupName: "team", Label: "team"},
	}
	resolve := func(id string) string {
		if id == "alice" {
			return "busy"
		}
		return ""
	}
	tbl := buildChatsTable(chats, nil, resolve, nil, func(string) {})

	if got := tbl.GetCell(1, 0).Text; got != GlyphPresenceOnline {
		t.Errorf("DM presence cell = %q, want %q", got, GlyphPresenceOnline)
	}
	if got := tbl.GetCell(2, 0).Text; got != "" {
		t.Errorf("group presence cell = %q, want empty", got)
	}
}

func TestBuildContactsTable_RetiredSuffix(t *testing.T) {
	peers := []ipc.PeerEntry{
		{ID: "live", Alias: "alice", LastPassiveAt: 1776000000},
		{ID: "gone", Alias: "bob", RetiredAt: 1776956500},
	}
	tbl := buildContactsTable(peers, nil, nil, func(string) {})

	liveAlias := tbl.GetCell(1, 3).Text
	if strings.Contains(liveAlias, "retired") {
		t.Errorf("live peer should not have retired suffix: %q", liveAlias)
	}
	retiredAlias := tbl.GetCell(2, 3).Text
	if !strings.Contains(retiredAlias, "bob") || !strings.Contains(retiredAlias, "retired 2026-04-23") {
		t.Errorf("retired peer missing suffix: %q", retiredAlias)
	}
}

func TestRenderEventJSON_DeletedTombstone(t *testing.T) {
	cp := newChatPage("chat-abc", "peer-bob", "bob", 0)

	mk := func(direction string, editedAt, deletedAt int64, text string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{
			"recv_seq":   1,
			"chat_id":    "chat-abc",
			"direction":  direction,
			"kind":       "text",
			"display_ts": 1700000000,
			"msg_id":     "mid-x",
			"edited_at":  editedAt,
			"deleted_at": deletedAt,
			"body":       map[string]any{"text": text},
		})
		return b
	}

	got := cp.renderEventJSON(mk("out", 0, 1700000100, ""), "<-")
	if !strings.Contains(got, "[message deleted]") {
		t.Errorf("outbound deleted missing placeholder: %q", got)
	}
	if !strings.Contains(got, "<-") {
		t.Errorf("outbound deleted missing out-arrow: %q", got)
	}

	got = cp.renderEventJSON(mk("in", 0, 1700000100, ""), "->")
	if !strings.Contains(got, "[message deleted]") {
		t.Errorf("inbound deleted missing placeholder: %q", got)
	}
	if !strings.Contains(got, "->") {
		t.Errorf("inbound deleted missing in-arrow: %q", got)
	}

	got = cp.renderEventJSON(mk("out", 1700000050, 1700000100, "was edited"), "<-")
	if !strings.Contains(got, "[message deleted]") {
		t.Errorf("deleted-edited row should show placeholder: %q", got)
	}
	if strings.Contains(got, "(edited)") {
		t.Errorf("deleted-edited row should NOT show (edited) badge: %q", got)
	}
	if strings.Contains(got, "was edited") {
		t.Errorf("deleted-edited row must not leak body text: %q", got)
	}
}

func TestCollectDeletable_FiltersCorrectly(t *testing.T) {
	cp := newChatPage("chat-abc", "peer-bob", "bob", 0)
	realNow := time.Now().Unix()

	mk := func(msgID, direction, kind, decryptStatus, text string, displayTs, deletedAt int64) json.RawMessage {
		b, _ := json.Marshal(map[string]any{
			"recv_seq":       1,
			"chat_id":        "chat-abc",
			"direction":      direction,
			"kind":           kind,
			"display_ts":     displayTs,
			"msg_id":         msgID,
			"decrypt_status": decryptStatus,
			"deleted_at":     deletedAt,
			"body":           map[string]any{"text": text},
		})
		return b
	}

	cp.appendEvent(mk("ok-1", "out", "text", "", "fresh", realNow-60, 0))
	cp.appendEvent(mk("already", "out", "text", "", "tombstoned", realNow-60, realNow))
	cp.appendEvent(mk("inbound", "in", "text", "", "theirs", realNow-60, 0))
	cp.appendEvent(mk("", "out", "text", "", "legacy", realNow-60, 0))
	cp.appendEvent(mk("failed", "out", "text", "failed", "bad", realNow-60, 0))
	cp.appendEvent(mk("tc", "out", "timer_change", "", "bc", realNow-60, 0))
	cp.appendEvent(mk("too-old", "out", "text", "", "ancient", realNow-editWindowSeconds-1, 0))

	got := collectDeletable(cp)
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1 (only ok-1)", len(got))
	}
	if got[0].msgID != "ok-1" {
		t.Errorf("candidate msgID = %q, want 'ok-1'", got[0].msgID)
	}
}

func TestUpsertEvent_ReplacesWithTombstone(t *testing.T) {
	cp := newChatPage("chat-abc", "peer-bob", "bob", 0)

	mk := func(deletedAt int64, text string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{
			"recv_seq":    1,
			"chat_id":     "chat-abc",
			"direction":   "out",
			"kind":        "text",
			"display_ts":  1700000000,
			"envelope_id": "env-1",
			"msg_id":      "mid-1",
			"deleted_at":  deletedAt,
			"body":        map[string]any{"text": text},
		})
		return b
	}

	cp.upsertEvent(mk(0, "original"))
	cp.setDeliveryState("env-1", "sent")

	cp.upsertEvent(mk(1700000100, ""))
	if len(cp.entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1 after tombstone upsert", len(cp.entries))
	}
	if cp.entries[0].delivery != "sent" {
		t.Errorf("delivery marker dropped on tombstone: %q", cp.entries[0].delivery)
	}
	got := cp.renderEntry(cp.entries[0])
	if !strings.Contains(got, "[message deleted]") {
		t.Errorf("rendered line missing placeholder after tombstone: %q", got)
	}
	if strings.Contains(got, "original") {
		t.Errorf("rendered line still leaks prior text: %q", got)
	}
}

func TestRenderEventJSON_Reaction(t *testing.T) {
	cp := newChatPage("chat-abc", "peer-bob", "bob", 0)

	mk := func(reactor, emojiStr string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{
			"recv_seq":       1,
			"chat_id":        "chat-abc",
			"direction":      "in",
			"kind":           "reaction",
			"display_ts":     1700000000,
			"sender_peer_id": reactor,
			"body":           map[string]any{"target_msg_id": "mid-x", "emoji": emojiStr},
		})
		return b
	}

	got := cp.renderEventJSON(mk("", "👍"), "")
	if !strings.Contains(got, "* me") {
		t.Errorf("self-reaction missing '* me': %q", got)
	}
	if !strings.Contains(got, "👍") {
		t.Errorf("self-reaction missing emoji: %q", got)
	}

	got = cp.renderEventJSON(mk("peer-bob", "❤️"), "")
	if !strings.Contains(got, "* bob") {
		t.Errorf("peer-reaction should resolve to nickname 'bob': %q", got)
	}
	if !strings.Contains(got, "❤️") {
		t.Errorf("peer-reaction missing emoji: %q", got)
	}

	got = cp.renderEventJSON(mk("peer-bob", ""), "")
	if !strings.Contains(got, "removed reaction") {
		t.Errorf("removal reaction missing audit text: %q", got)
	}
}

func TestOutboundArrow_StateMapping(t *testing.T) {
	cases := []struct {
		state, want string
	}{
		{"", "[gray]<-[-]"},
		{"enqueued", "[gray]<-[-]"},
		{"sent", "[green]<-[-]"},
		{"delivered", "[green]<-[-]"},
		{"read", "[green]<<[-]"},
		{"failed", "[red]✗-[-]"},
	}
	for _, c := range cases {
		t.Run(c.state, func(t *testing.T) {
			if got := outboundArrow(c.state); got != c.want {
				t.Errorf("outboundArrow(%q) = %q, want %q", c.state, got, c.want)
			}
		})
	}
}

func TestInboundArrow_AlwaysBlue(t *testing.T) {
	if got := inboundArrow(); got != "[blue]->[-]" {
		t.Errorf("inboundArrow() = %q, want '[blue]->[-]'", got)
	}
}

func TestRenderEntry_OutboundReflectsLiveDeliveryState(t *testing.T) {
	cp := newChatPage("chat-abc", "peer-bob", "bob", 0)
	b, _ := json.Marshal(map[string]any{
		"recv_seq":    1,
		"chat_id":     "chat-abc",
		"direction":   "out",
		"kind":        "text",
		"display_ts":  1700000000,
		"envelope_id": "env-1",
		"msg_id":      "mid-1",
		"body":        map[string]any{"text": "hi"},
	})
	cp.appendEvent(b)

	if got := cp.renderEntry(cp.entries[0]); !strings.Contains(got, "[gray]<-[-]") {
		t.Errorf("pre-state render missing gray pending arrow: %q", got)
	}
	cp.setDeliveryState("env-1", "sent")
	if got := cp.renderEntry(cp.entries[0]); !strings.Contains(got, "[green]<-[-]") {
		t.Errorf("post-sent render missing green arrow: %q", got)
	}
	cp.setDeliveryState("env-1", "failed")
	if got := cp.renderEntry(cp.entries[0]); !strings.Contains(got, "[red]✗-[-]") {
		t.Errorf("post-failed render missing red arrow: %q", got)
	}
}

func TestUpsertEvent_ReadReceiptFlipsArrowToReadGlyph(t *testing.T) {
	cp := newChatPage("chat-abc", "peer-bob", "bob", 0)
	original, _ := json.Marshal(map[string]any{
		"recv_seq":       1,
		"chat_id":        "chat-abc",
		"direction":      "out",
		"kind":           "text",
		"display_ts":     1700000000,
		"envelope_id":    "env-1",
		"msg_id":         "mid-1",
		"delivery_state": "delivered",
		"body":           map[string]any{"text": "hi"},
	})
	cp.appendEvent(original)
	if got := cp.renderEntry(cp.entries[0]); !strings.Contains(got, "[green]<-[-]") {
		t.Fatalf("baseline render missing green delivered arrow: %q", got)
	}

	updated, _ := json.Marshal(map[string]any{
		"recv_seq":       1,
		"chat_id":        "chat-abc",
		"direction":      "out",
		"kind":           "text",
		"display_ts":     1700000000,
		"envelope_id":    "env-1",
		"msg_id":         "mid-1",
		"delivery_state": "read",
		"read_at":        1700000050,
		"body":           map[string]any{"text": "hi"},
	})
	cp.upsertEvent(updated)

	if got := cp.renderEntry(cp.entries[0]); !strings.Contains(got, "[green]<<[-]") {
		t.Errorf("post-receipt render missing read glyph: %q", got)
	}
}

func TestAppendEvent_OutOfOrderInsertionByDisplayTs(t *testing.T) {
	cp := newChatPage("chat-abc", "peer-bob", "bob", 0)

	mkText := func(recvSeq uint64, displayTs int64, msgID, text string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{
			"recv_seq":   recvSeq,
			"chat_id":    "chat-abc",
			"direction":  "out",
			"kind":       "text",
			"display_ts": displayTs,
			"msg_id":     msgID,
			"body":       map[string]any{"text": text},
		})
		return b
	}
	mkReaction := func(recvSeq uint64, displayTs, at int64, target, emoji string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{
			"recv_seq":   recvSeq,
			"chat_id":    "chat-abc",
			"direction":  "out",
			"kind":       "reaction",
			"display_ts": displayTs,
			"body":       map[string]any{"target_msg_id": target, "emoji": emoji, "at": at},
		})
		return b
	}

	cp.appendEvent(mkText(1, 1700000000, "mid-old", "old message"))
	cp.appendEvent(mkText(2, 1700000300, "mid-mid", "newer message 1"))
	cp.appendEvent(mkText(3, 1700000600, "mid-new", "newer message 2"))

	cp.appendEvent(mkReaction(4, 1700000000, 1700000900, "mid-old", "👍"))

	if len(cp.entries) != 4 {
		t.Fatalf("got %d entries, want 4", len(cp.entries))
	}
	var k1, k2 rawEvent
	_ = json.Unmarshal(cp.entries[1].evJSON, &k1)
	_ = json.Unmarshal(cp.entries[2].evJSON, &k2)
	if k1.Kind != "reaction" {
		t.Errorf("entries[1].Kind = %q, want reaction (sorted right after target)", k1.Kind)
	}
	if k2.Kind != "text" || k2.MsgID != "mid-mid" {
		t.Errorf("entries[2] = %+v, want text mid-mid (reaction must not displace newer rows)", k2)
	}
}

func TestCollectReactable_AcceptsBothDirections(t *testing.T) {
	cp := newChatPage("chat-abc", "peer-bob", "bob", 0)

	mk := func(msgID, direction, kind, decryptStatus, text string, deletedAt int64) json.RawMessage {
		b, _ := json.Marshal(map[string]any{
			"recv_seq":       1,
			"chat_id":        "chat-abc",
			"direction":      direction,
			"kind":           kind,
			"display_ts":     time.Now().Unix(),
			"msg_id":         msgID,
			"decrypt_status": decryptStatus,
			"deleted_at":     deletedAt,
			"body":           map[string]any{"text": text},
		})
		return b
	}

	cp.appendEvent(mk("own", "out", "text", "", "ours", 0))
	cp.appendEvent(mk("theirs", "in", "text", "", "theirs", 0))
	cp.appendEvent(mk("dead", "out", "text", "", "tombstoned", 1700000000))
	cp.appendEvent(mk("nope", "in", "text", "failed", "", 0))
	cp.appendEvent(mk("", "out", "text", "", "no-msg-id", 0))
	cp.appendEvent(mk("breadcrumb", "out", "timer_change", "", "", 0))

	got := collectReactable(cp)
	if len(got) != 2 {
		t.Fatalf("got %d candidates, want 2 (own + theirs)", len(got))
	}
	have := map[string]bool{}
	for _, c := range got {
		have[c.msgID] = true
	}
	if !have["own"] || !have["theirs"] {
		t.Errorf("missing expected candidates: have=%v", have)
	}
}
