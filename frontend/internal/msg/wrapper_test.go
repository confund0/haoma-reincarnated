package msg_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"haoma-frontend/internal/msg"
)

const testMsgID = "0123456789abcdef0123456789abcdef"

func TestBuildText_RoundTrip(t *testing.T) {
	w, err := msg.BuildText(7, 1742643890, testMsgID, "hello world", 0, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if w.V != msg.Version || w.Seq != 7 || w.Ts != 1742643890 || w.Kind != msg.KindText {
		t.Errorf("wrapper drift: %+v", w)
	}
	if w.MsgID != testMsgID {
		t.Errorf("MsgID = %q, want %q", w.MsgID, testMsgID)
	}
	raw, err := msg.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	got, err := msg.Unmarshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Seq != 7 || got.Ts != 1742643890 || got.Kind != msg.KindText {
		t.Errorf("round-trip drift: %+v", got)
	}
	if got.MsgID != testMsgID {
		t.Errorf("round-trip MsgID = %q, want %q", got.MsgID, testMsgID)
	}
	if got.ExpireSeconds != 0 {
		t.Errorf("round-trip ExpireSeconds = %d, want 0", got.ExpireSeconds)
	}
	body, err := got.Text()
	if err != nil {
		t.Fatal(err)
	}
	if body.Text != "hello world" {
		t.Errorf("text = %q, want %q", body.Text, "hello world")
	}
}

func TestBuildText_CarriesExpireSeconds(t *testing.T) {
	w, err := msg.BuildText(1, 1, testMsgID, "t", 60, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := msg.Marshal(w)
	got, _ := msg.Unmarshal(raw)
	if got.ExpireSeconds != 60 {
		t.Errorf("ExpireSeconds = %d, want 60", got.ExpireSeconds)
	}
}

func TestBuildText_CarriesReplyTo(t *testing.T) {
	rt := &msg.ReplyTo{MsgID: "abc123", Text: "the original message"}
	w, err := msg.BuildText(1, 1, testMsgID, "reply!", 0, "", "", rt)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := msg.Marshal(w)
	got, _ := msg.Unmarshal(raw)
	body, err := got.Text()
	if err != nil {
		t.Fatal(err)
	}
	if body.ReplyTo == nil {
		t.Fatalf("ReplyTo nil after round-trip; body=%+v", body)
	}
	if body.ReplyTo.MsgID != "abc123" || body.ReplyTo.Text != "the original message" {
		t.Errorf("ReplyTo drift: %+v", body.ReplyTo)
	}
}

func TestBuildText_RejectsReplyToMissingMsgID(t *testing.T) {
	_, err := msg.BuildText(1, 1, testMsgID, "hi", 0, "", "", &msg.ReplyTo{MsgID: "", Text: "x"})
	if !errors.Is(err, msg.ErrMissingField) {
		t.Fatalf("err = %v, want ErrMissingField", err)
	}
}

func TestBuildText_OmitsReplyToWhenNil(t *testing.T) {
	w, err := msg.BuildText(1, 1, testMsgID, "hi", 0, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := msg.Marshal(w)
	if bytes.Contains(raw, []byte("reply_to")) {
		t.Errorf("nil ReplyTo leaked reply_to key to wire: %s", raw)
	}
}

func TestNewID_Length(t *testing.T) {
	id, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != 32 {
		t.Errorf("NewID len = %d, want 32", len(id))
	}

	id2, _ := msg.NewID()
	if id == id2 {
		t.Errorf("NewID returned identical ids on back-to-back calls: %q", id)
	}
}

func TestBuildText_RejectsZeroSeq(t *testing.T) {
	_, err := msg.BuildText(0, 1, testMsgID, "hi", 0, "", "", nil)
	if !errors.Is(err, msg.ErrMissingField) {
		t.Fatalf("err = %v, want ErrMissingField", err)
	}
}

func TestBuildText_RejectsZeroTs(t *testing.T) {
	_, err := msg.BuildText(1, 0, testMsgID, "hi", 0, "", "", nil)
	if !errors.Is(err, msg.ErrMissingField) {
		t.Fatalf("err = %v, want ErrMissingField", err)
	}
}

func TestBuildText_RejectsEmptyMsgID(t *testing.T) {
	_, err := msg.BuildText(1, 1, "", "hi", 0, "", "", nil)
	if !errors.Is(err, msg.ErrMissingField) {
		t.Fatalf("err = %v, want ErrMissingField", err)
	}
}

func TestUnmarshal_RejectsBadVersion(t *testing.T) {
	bad := `{"v":99,"seq":1,"ts":1,"msg_id":"` + testMsgID + `","kind":"text","body":{"text":"x"}}`
	_, err := msg.Unmarshal([]byte(bad))
	if !errors.Is(err, msg.ErrUnsupportedVersion) {
		t.Fatalf("err = %v, want ErrUnsupportedVersion", err)
	}
}

func TestUnmarshal_RejectsV1(t *testing.T) {

	v1 := `{"v":1,"seq":1,"ts":1,"kind":"text","body":{"text":"x"}}`
	_, err := msg.Unmarshal([]byte(v1))
	if !errors.Is(err, msg.ErrUnsupportedVersion) {
		t.Fatalf("err = %v, want ErrUnsupportedVersion", err)
	}
}

func TestUnmarshal_RejectsMissingFields(t *testing.T) {
	id := testMsgID
	cases := []struct {
		name, body string
	}{
		{"seq=0", `{"v":2,"seq":0,"ts":1,"msg_id":"` + id + `","kind":"text","body":{"text":"x"}}`},
		{"ts=0", `{"v":2,"seq":1,"ts":0,"msg_id":"` + id + `","kind":"text","body":{"text":"x"}}`},
		{"ts negative", `{"v":2,"seq":1,"ts":-5,"msg_id":"` + id + `","kind":"text","body":{"text":"x"}}`},
		{"empty kind", `{"v":2,"seq":1,"ts":1,"msg_id":"` + id + `","kind":"","body":{"text":"x"}}`},
		{"empty msg_id", `{"v":2,"seq":1,"ts":1,"msg_id":"","kind":"text","body":{"text":"x"}}`},
		{"missing msg_id", `{"v":2,"seq":1,"ts":1,"kind":"text","body":{"text":"x"}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := msg.Unmarshal([]byte(c.body))
			if !errors.Is(err, msg.ErrMissingField) {
				t.Errorf("err = %v, want ErrMissingField", err)
			}
		})
	}
}

func TestUnmarshal_RejectsMalformedJSON(t *testing.T) {
	_, err := msg.Unmarshal([]byte("not json"))
	if err == nil || !strings.Contains(err.Error(), "decode wrapper") {
		t.Fatalf("err = %v, want decode wrapper error", err)
	}
}

func TestText_RejectsWrongKind(t *testing.T) {
	w := &msg.Wrapper{V: msg.Version, Seq: 1, Ts: 1, MsgID: testMsgID, Kind: "reaction", Body: json.RawMessage(`{}`)}
	_, err := w.Text()
	if err == nil || !strings.Contains(err.Error(), "not \"text\"") {
		t.Fatalf("err = %v, want kind-mismatch error", err)
	}
}

func TestMarshal_RejectsNil(t *testing.T) {
	_, err := msg.Marshal(nil)
	if err == nil {
		t.Fatal("expected error on nil wrapper")
	}
}

const testTargetMsgID = "abcdef0123456789abcdef0123456789"

func TestBuildEdit_RoundTrip(t *testing.T) {
	w, err := msg.BuildEdit(3, 1742643900, testMsgID, testTargetMsgID, "fixed typo", 0)
	if err != nil {
		t.Fatal(err)
	}
	if w.Kind != msg.KindEdit {
		t.Errorf("Kind = %q, want %q", w.Kind, msg.KindEdit)
	}
	raw, err := msg.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	got, err := msg.Unmarshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	body, err := got.Edit()
	if err != nil {
		t.Fatal(err)
	}
	if body.Target != testTargetMsgID {
		t.Errorf("target = %q, want %q", body.Target, testTargetMsgID)
	}
	if body.Text != "fixed typo" {
		t.Errorf("text = %q, want %q", body.Text, "fixed typo")
	}
}

func TestBuildEdit_CarriesExpireSeconds(t *testing.T) {
	w, err := msg.BuildEdit(1, 1, testMsgID, testTargetMsgID, "t", 3600)
	if err != nil {
		t.Fatal(err)
	}
	if w.ExpireSeconds != 3600 {
		t.Errorf("ExpireSeconds = %d, want 3600", w.ExpireSeconds)
	}
}

func TestBuildEdit_RejectsEmptyTarget(t *testing.T) {
	_, err := msg.BuildEdit(1, 1, testMsgID, "", "x", 0)
	if !errors.Is(err, msg.ErrMissingField) {
		t.Fatalf("err = %v, want ErrMissingField", err)
	}
}

func TestBuildEdit_RejectsEmptyText(t *testing.T) {
	_, err := msg.BuildEdit(1, 1, testMsgID, testTargetMsgID, "", 0)
	if !errors.Is(err, msg.ErrMissingField) {
		t.Fatalf("err = %v, want ErrMissingField", err)
	}
}

func TestEdit_RejectsWrongKind(t *testing.T) {
	w := &msg.Wrapper{V: msg.Version, Seq: 1, Ts: 1, MsgID: testMsgID, Kind: msg.KindText, Body: json.RawMessage(`{}`)}
	_, err := w.Edit()
	if err == nil || !strings.Contains(err.Error(), "not \"edit\"") {
		t.Fatalf("err = %v, want kind-mismatch error", err)
	}
}

func TestEdit_RejectsMissingFields(t *testing.T) {
	cases := []struct {
		name, body string
	}{
		{"empty target", `{"target":"","text":"x"}`},
		{"empty text", `{"target":"` + testTargetMsgID + `","text":""}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := &msg.Wrapper{V: msg.Version, Seq: 1, Ts: 1, MsgID: testMsgID, Kind: msg.KindEdit, Body: json.RawMessage(c.body)}
			_, err := w.Edit()
			if !errors.Is(err, msg.ErrMissingField) {
				t.Errorf("err = %v, want ErrMissingField", err)
			}
		})
	}
}

func TestBuildDelete_RoundTrip(t *testing.T) {
	w, err := msg.BuildDelete(4, 1742643910, testMsgID, testTargetMsgID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if w.Kind != msg.KindDelete {
		t.Errorf("Kind = %q, want %q", w.Kind, msg.KindDelete)
	}
	raw, err := msg.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	got, err := msg.Unmarshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	body, err := got.Delete()
	if err != nil {
		t.Fatal(err)
	}
	if body.Target != testTargetMsgID {
		t.Errorf("target = %q, want %q", body.Target, testTargetMsgID)
	}
}

func TestBuildDelete_CarriesExpireSeconds(t *testing.T) {
	w, err := msg.BuildDelete(1, 1, testMsgID, testTargetMsgID, 600)
	if err != nil {
		t.Fatal(err)
	}
	if w.ExpireSeconds != 600 {
		t.Errorf("ExpireSeconds = %d, want 600", w.ExpireSeconds)
	}
}

func TestBuildDelete_RejectsEmptyTarget(t *testing.T) {
	_, err := msg.BuildDelete(1, 1, testMsgID, "", 0)
	if !errors.Is(err, msg.ErrMissingField) {
		t.Fatalf("err = %v, want ErrMissingField", err)
	}
}

func TestBuildDelete_RejectsZeroSeq(t *testing.T) {
	_, err := msg.BuildDelete(0, 1, testMsgID, testTargetMsgID, 0)
	if !errors.Is(err, msg.ErrMissingField) {
		t.Fatalf("err = %v, want ErrMissingField", err)
	}
}

func TestDelete_RejectsWrongKind(t *testing.T) {
	w := &msg.Wrapper{V: msg.Version, Seq: 1, Ts: 1, MsgID: testMsgID, Kind: msg.KindText, Body: json.RawMessage(`{}`)}
	_, err := w.Delete()
	if err == nil || !strings.Contains(err.Error(), "not \"delete\"") {
		t.Fatalf("err = %v, want kind-mismatch error", err)
	}
}

func TestDelete_RejectsMissingTarget(t *testing.T) {
	w := &msg.Wrapper{V: msg.Version, Seq: 1, Ts: 1, MsgID: testMsgID, Kind: msg.KindDelete, Body: json.RawMessage(`{"target":""}`)}
	_, err := w.Delete()
	if !errors.Is(err, msg.ErrMissingField) {
		t.Fatalf("err = %v, want ErrMissingField", err)
	}
}

func TestBuildReaction_RoundTrip(t *testing.T) {
	w, err := msg.BuildReaction(5, 1742643920, testMsgID, testTargetMsgID, "👍", 0)
	if err != nil {
		t.Fatal(err)
	}
	if w.Kind != msg.KindReaction {
		t.Errorf("Kind = %q, want %q", w.Kind, msg.KindReaction)
	}
	raw, err := msg.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	got, err := msg.Unmarshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	body, err := got.Reaction()
	if err != nil {
		t.Fatal(err)
	}
	if body.Target != testTargetMsgID {
		t.Errorf("target = %q, want %q", body.Target, testTargetMsgID)
	}
	if body.Emoji != "👍" {
		t.Errorf("emoji = %q, want %q", body.Emoji, "👍")
	}
}

func TestBuildReaction_AcceptsEmptyEmoji(t *testing.T) {
	w, err := msg.BuildReaction(1, 1, testMsgID, testTargetMsgID, "", 0)
	if err != nil {
		t.Fatalf("empty emoji rejected: %v", err)
	}
	body, err := w.Reaction()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Emoji != "" {
		t.Errorf("emoji = %q, want empty", body.Emoji)
	}
}

func TestBuildReaction_CarriesExpireSeconds(t *testing.T) {
	w, err := msg.BuildReaction(1, 1, testMsgID, testTargetMsgID, "❤️", 300)
	if err != nil {
		t.Fatal(err)
	}
	if w.ExpireSeconds != 300 {
		t.Errorf("ExpireSeconds = %d, want 300", w.ExpireSeconds)
	}
}

func TestBuildReaction_RejectsEmptyTarget(t *testing.T) {
	_, err := msg.BuildReaction(1, 1, testMsgID, "", "👍", 0)
	if !errors.Is(err, msg.ErrMissingField) {
		t.Fatalf("err = %v, want ErrMissingField", err)
	}
}

func TestBuildReaction_RejectsZeroSeq(t *testing.T) {
	_, err := msg.BuildReaction(0, 1, testMsgID, testTargetMsgID, "👍", 0)
	if !errors.Is(err, msg.ErrMissingField) {
		t.Fatalf("err = %v, want ErrMissingField", err)
	}
}

func TestReaction_RejectsWrongKind(t *testing.T) {
	w := &msg.Wrapper{V: msg.Version, Seq: 1, Ts: 1, MsgID: testMsgID, Kind: msg.KindText, Body: json.RawMessage(`{}`)}
	_, err := w.Reaction()
	if err == nil || !strings.Contains(err.Error(), "not \"reaction\"") {
		t.Fatalf("err = %v, want kind-mismatch error", err)
	}
}

func TestReaction_RejectsMissingTarget(t *testing.T) {
	w := &msg.Wrapper{V: msg.Version, Seq: 1, Ts: 1, MsgID: testMsgID, Kind: msg.KindReaction, Body: json.RawMessage(`{"target":"","emoji":"👍"}`)}
	_, err := w.Reaction()
	if !errors.Is(err, msg.ErrMissingField) {
		t.Fatalf("err = %v, want ErrMissingField", err)
	}
}

func TestBuildRead_RoundTrip(t *testing.T) {
	targets := []string{testTargetMsgID, "abcdef0123456789abcdef0123456780"}
	w, err := msg.BuildRead(6, 1742643930, testMsgID, targets, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if w.Kind != msg.KindRead {
		t.Errorf("Kind = %q, want %q", w.Kind, msg.KindRead)
	}
	raw, err := msg.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	got, err := msg.Unmarshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	body, err := got.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(body.Targets) != 2 {
		t.Fatalf("targets len = %d, want 2", len(body.Targets))
	}
	if body.Targets[0] != targets[0] || body.Targets[1] != targets[1] {
		t.Errorf("targets = %v, want %v", body.Targets, targets)
	}
}

func TestBuildRead_CarriesExpireSeconds(t *testing.T) {
	w, err := msg.BuildRead(1, 1, testMsgID, []string{testTargetMsgID}, 120, "")
	if err != nil {
		t.Fatal(err)
	}
	if w.ExpireSeconds != 120 {
		t.Errorf("ExpireSeconds = %d, want 120", w.ExpireSeconds)
	}
}

func TestBuildRead_RejectsEmptyTargets(t *testing.T) {
	_, err := msg.BuildRead(1, 1, testMsgID, nil, 0, "")
	if !errors.Is(err, msg.ErrMissingField) {
		t.Fatalf("err = %v, want ErrMissingField", err)
	}
	_, err = msg.BuildRead(1, 1, testMsgID, []string{}, 0, "")
	if !errors.Is(err, msg.ErrMissingField) {
		t.Fatalf("err (empty slice) = %v, want ErrMissingField", err)
	}
}

func TestBuildRead_RejectsZeroSeq(t *testing.T) {
	_, err := msg.BuildRead(0, 1, testMsgID, []string{testTargetMsgID}, 0, "")
	if !errors.Is(err, msg.ErrMissingField) {
		t.Fatalf("err = %v, want ErrMissingField", err)
	}
}

func TestRead_RejectsWrongKind(t *testing.T) {
	w := &msg.Wrapper{V: msg.Version, Seq: 1, Ts: 1, MsgID: testMsgID, Kind: msg.KindText, Body: json.RawMessage(`{}`)}
	_, err := w.Read()
	if err == nil || !strings.Contains(err.Error(), "not \"read\"") {
		t.Fatalf("err = %v, want kind-mismatch error", err)
	}
}

func TestRead_RejectsEmptyTargets(t *testing.T) {
	w := &msg.Wrapper{V: msg.Version, Seq: 1, Ts: 1, MsgID: testMsgID, Kind: msg.KindRead, Body: json.RawMessage(`{"targets":[]}`)}
	_, err := w.Read()
	if !errors.Is(err, msg.ErrMissingField) {
		t.Fatalf("err = %v, want ErrMissingField", err)
	}
}

func TestBuildText_CarriesPresenceState(t *testing.T) {
	for _, state := range []string{msg.PresenceAvailable, msg.PresenceAway, msg.PresenceBusy} {
		w, err := msg.BuildText(1, 1, testMsgID, "hi", 0, state, "", nil)
		if err != nil {
			t.Fatalf("state=%q: %v", state, err)
		}
		raw, _ := msg.Marshal(w)
		got, _ := msg.Unmarshal(raw)
		body, err := got.Text()
		if err != nil {
			t.Fatal(err)
		}
		if body.PresenceState != state {
			t.Errorf("state=%q: PresenceState = %q", state, body.PresenceState)
		}
	}
}

func TestBuildText_EmptyPresenceState_OmitsField(t *testing.T) {
	w, err := msg.BuildText(1, 1, testMsgID, "hi", 0, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := msg.Marshal(w)
	if strings.Contains(string(raw), "presence_state") {
		t.Errorf("empty presence state should be omitted, raw = %s", raw)
	}
	got, _ := msg.Unmarshal(raw)
	body, _ := got.Text()
	if body.PresenceState != "" {
		t.Errorf("decoded PresenceState = %q, want empty", body.PresenceState)
	}
}

func TestBuildText_RejectsUnknownPresenceState(t *testing.T) {
	_, err := msg.BuildText(1, 1, testMsgID, "hi", 0, "lurking", "", nil)
	if !errors.Is(err, msg.ErrMissingField) {
		t.Fatalf("err = %v, want ErrMissingField", err)
	}
}

func TestText_DecodesPreC3WrapperWithoutPresenceState(t *testing.T) {
	raw := `{"v":2,"seq":1,"ts":1,"msg_id":"` + testMsgID + `","kind":"text","body":{"text":"x"}}`
	got, err := msg.Unmarshal([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	body, err := got.Text()
	if err != nil {
		t.Fatal(err)
	}
	if body.Text != "x" || body.PresenceState != "" {
		t.Errorf("decoded body = %+v", body)
	}
}

func TestBuildRead_CarriesPresenceState(t *testing.T) {
	for _, state := range []string{msg.PresenceAvailable, msg.PresenceAway, msg.PresenceBusy} {
		w, err := msg.BuildRead(1, 1, testMsgID, []string{testTargetMsgID}, 0, state)
		if err != nil {
			t.Fatalf("state=%q: %v", state, err)
		}
		raw, _ := msg.Marshal(w)
		got, _ := msg.Unmarshal(raw)
		body, err := got.Read()
		if err != nil {
			t.Fatal(err)
		}
		if body.PresenceState != state {
			t.Errorf("state=%q: PresenceState = %q", state, body.PresenceState)
		}
	}
}

func TestBuildRead_EmptyPresenceState_OmitsField(t *testing.T) {
	w, err := msg.BuildRead(1, 1, testMsgID, []string{testTargetMsgID}, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := msg.Marshal(w)
	if strings.Contains(string(raw), "presence_state") {
		t.Errorf("empty presence state should be omitted, raw = %s", raw)
	}
}

func TestBuildRead_RejectsUnknownPresenceState(t *testing.T) {
	_, err := msg.BuildRead(1, 1, testMsgID, []string{testTargetMsgID}, 0, "lurking")
	if !errors.Is(err, msg.ErrMissingField) {
		t.Fatalf("err = %v, want ErrMissingField", err)
	}
}

func TestBuildPresence_RoundTrip(t *testing.T) {
	for _, state := range []string{msg.PresenceAvailable, msg.PresenceAway, msg.PresenceBusy} {
		w, err := msg.BuildPresence(7, 1742643940, testMsgID, state, 0)
		if err != nil {
			t.Fatalf("Build state=%q: %v", state, err)
		}
		if w.Kind != msg.KindPresence {
			t.Errorf("Kind = %q, want %q", w.Kind, msg.KindPresence)
		}
		raw, err := msg.Marshal(w)
		if err != nil {
			t.Fatal(err)
		}
		got, err := msg.Unmarshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		body, err := got.Presence()
		if err != nil {
			t.Fatalf("Presence() state=%q: %v", state, err)
		}
		if body.State != state {
			t.Errorf("decoded state = %q, want %q", body.State, state)
		}
	}
}

func TestBuildPresence_RejectsUnknownState(t *testing.T) {
	_, err := msg.BuildPresence(1, 1, testMsgID, "lurking", 0)
	if !errors.Is(err, msg.ErrMissingField) {
		t.Fatalf("err = %v, want ErrMissingField", err)
	}
	_, err = msg.BuildPresence(1, 1, testMsgID, "", 0)
	if !errors.Is(err, msg.ErrMissingField) {
		t.Fatalf("empty state err = %v, want ErrMissingField", err)
	}
}

func TestBuildPresence_RejectsZeroSeq(t *testing.T) {
	_, err := msg.BuildPresence(0, 1, testMsgID, msg.PresenceAvailable, 0)
	if !errors.Is(err, msg.ErrMissingField) {
		t.Fatalf("err = %v, want ErrMissingField", err)
	}
}

func TestPresence_RejectsWrongKind(t *testing.T) {
	w := &msg.Wrapper{V: msg.Version, Seq: 1, Ts: 1, MsgID: testMsgID, Kind: msg.KindText, Body: json.RawMessage(`{"state":"available"}`)}
	_, err := w.Presence()
	if err == nil || !strings.Contains(err.Error(), "not \"presence\"") {
		t.Fatalf("err = %v, want kind-mismatch error", err)
	}
}

func TestPresence_RejectsUnknownState(t *testing.T) {
	w := &msg.Wrapper{V: msg.Version, Seq: 1, Ts: 1, MsgID: testMsgID, Kind: msg.KindPresence, Body: json.RawMessage(`{"state":"vacationing"}`)}
	_, err := w.Presence()
	if !errors.Is(err, msg.ErrMissingField) {
		t.Fatalf("err = %v, want ErrMissingField", err)
	}
}

const testToken = "Q5oF1aQpJ1xVbwVN4f6PcWcL4lWCNW5kdeJ-l7Q9OVo"
const testSha256 = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

func TestBuildFileOffer_RoundTrip(t *testing.T) {
	w, err := msg.BuildFileOffer(3, 1742643890, testMsgID, testToken, "/files/"+testToken, "flies-fucking.mpg", 1024*1024, "video/mpeg", testSha256, 600)
	if err != nil {
		t.Fatal(err)
	}
	if w.Kind != msg.KindFileOffer {
		t.Errorf("kind = %q, want %q", w.Kind, msg.KindFileOffer)
	}
	if w.ExpireSeconds != 600 {
		t.Errorf("expire = %d, want 600", w.ExpireSeconds)
	}
	raw, err := msg.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	got, err := msg.Unmarshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	body, err := got.FileOffer()
	if err != nil {
		t.Fatal(err)
	}
	if body.Token != testToken || body.UrlPath != "/files/"+testToken {
		t.Errorf("token/url drift: %+v", body)
	}
	if body.Name != "flies-fucking.mpg" || body.Size != 1024*1024 || body.Mime != "video/mpeg" {
		t.Errorf("metadata drift: %+v", body)
	}
	if body.Sha256Ciphertext != testSha256 {
		t.Errorf("sha256 = %q, want %q", body.Sha256Ciphertext, testSha256)
	}
}

func TestBuildFileOffer_RejectsMissingFields(t *testing.T) {
	cases := map[string]func() error{
		"empty token": func() error {
			_, e := msg.BuildFileOffer(1, 1, testMsgID, "", "/files/x", "n", 1, "m", testSha256, 0)
			return e
		},
		"empty path": func() error {
			_, e := msg.BuildFileOffer(1, 1, testMsgID, testToken, "", "n", 1, "m", testSha256, 0)
			return e
		},
		"zero size": func() error {
			_, e := msg.BuildFileOffer(1, 1, testMsgID, testToken, "/files/x", "n", 0, "m", testSha256, 0)
			return e
		},
		"empty sha256": func() error {
			_, e := msg.BuildFileOffer(1, 1, testMsgID, testToken, "/files/x", "n", 1, "m", "", 0)
			return e
		},
		"empty msg id": func() error {
			_, e := msg.BuildFileOffer(1, 1, "", testToken, "/files/x", "n", 1, "m", testSha256, 0)
			return e
		},
		"zero seq": func() error {
			_, e := msg.BuildFileOffer(0, 1, testMsgID, testToken, "/files/x", "n", 1, "m", testSha256, 0)
			return e
		},
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			err := fn()
			if !errors.Is(err, msg.ErrMissingField) {
				t.Fatalf("err = %v, want ErrMissingField", err)
			}
		})
	}
}

func TestFileOffer_RejectsWrongKind(t *testing.T) {
	w := &msg.Wrapper{V: msg.Version, Seq: 1, Ts: 1, MsgID: testMsgID, Kind: msg.KindText, Body: json.RawMessage(`{}`)}
	_, err := w.FileOffer()
	if err == nil || !strings.Contains(err.Error(), "not \"file_offer\"") {
		t.Fatalf("err = %v, want kind-mismatch error", err)
	}
}

func TestBuildFileReceipt_RoundTrip(t *testing.T) {
	w, err := msg.BuildFileReceipt(4, 100, testMsgID, testToken, 0)
	if err != nil {
		t.Fatal(err)
	}
	if w.Kind != msg.KindFileReceipt {
		t.Errorf("kind = %q, want %q", w.Kind, msg.KindFileReceipt)
	}
	raw, _ := msg.Marshal(w)
	got, _ := msg.Unmarshal(raw)
	body, err := got.FileReceipt()
	if err != nil {
		t.Fatal(err)
	}
	if body.Token != testToken {
		t.Errorf("token drift: %q", body.Token)
	}
}

func TestBuildFileReceipt_RejectsEmptyToken(t *testing.T) {
	_, err := msg.BuildFileReceipt(1, 1, testMsgID, "", 0)
	if !errors.Is(err, msg.ErrMissingField) {
		t.Fatalf("err = %v, want ErrMissingField", err)
	}
}

func TestBuildFileKey_RoundTrip(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	nonce := make([]byte, 24)
	for i := range nonce {
		nonce[i] = byte(0x80 + i)
	}
	w, err := msg.BuildFileKey(5, 200, testMsgID, testToken, key, nonce, 0)
	if err != nil {
		t.Fatal(err)
	}
	if w.Kind != msg.KindFileKey {
		t.Errorf("kind = %q, want %q", w.Kind, msg.KindFileKey)
	}
	raw, _ := msg.Marshal(w)
	got, _ := msg.Unmarshal(raw)
	body, gotKey, gotNonce, err := got.FileKey()
	if err != nil {
		t.Fatal(err)
	}
	if body.Token != testToken {
		t.Errorf("token drift: %q", body.Token)
	}
	if string(gotKey) != string(key) {
		t.Errorf("key bytes drift: got %x", gotKey)
	}
	if string(gotNonce) != string(nonce) {
		t.Errorf("nonce bytes drift: got %x", gotNonce)
	}
}

func TestBuildFileKey_RejectsBadSizes(t *testing.T) {
	short := make([]byte, 16)
	long := make([]byte, 33)
	nonce := make([]byte, 24)
	if _, err := msg.BuildFileKey(1, 1, testMsgID, testToken, short, nonce, 0); !errors.Is(err, msg.ErrMissingField) {
		t.Errorf("short key err = %v, want ErrMissingField", err)
	}
	if _, err := msg.BuildFileKey(1, 1, testMsgID, testToken, long, nonce, 0); !errors.Is(err, msg.ErrMissingField) {
		t.Errorf("long key err = %v, want ErrMissingField", err)
	}
	good := make([]byte, 32)
	if _, err := msg.BuildFileKey(1, 1, testMsgID, testToken, good, make([]byte, 23), 0); !errors.Is(err, msg.ErrMissingField) {
		t.Errorf("short nonce err = %v, want ErrMissingField", err)
	}
}

func TestFileKey_RejectsWrongKind(t *testing.T) {
	w := &msg.Wrapper{V: msg.Version, Seq: 1, Ts: 1, MsgID: testMsgID, Kind: msg.KindText, Body: json.RawMessage(`{}`)}
	_, _, _, err := w.FileKey()
	if err == nil || !strings.Contains(err.Error(), "not \"file_key\"") {
		t.Fatalf("err = %v, want kind-mismatch error", err)
	}
}

const testCallID = "call-fedcba9876543210fedcba9876543210"

func TestBuildCallOffer_RoundTrip(t *testing.T) {
	tokens := map[string]string{msg.ModalityAudio: "tok-audio-caller"}
	key := bytes.Repeat([]byte{0xAB}, msg.CallOutboundKeyBytes)
	w, err := msg.BuildCallOffer(3, 1742643890, testMsgID, testCallID, []string{msg.ModalityAudio}, tokens, key, 0)
	if err != nil {
		t.Fatal(err)
	}
	if w.Kind != msg.KindCallOffer {
		t.Errorf("kind = %q, want %q", w.Kind, msg.KindCallOffer)
	}
	raw, _ := msg.Marshal(w)
	got, err := msg.Unmarshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	body, err := got.CallOffer()
	if err != nil {
		t.Fatal(err)
	}
	if body.CallID != testCallID {
		t.Errorf("call_id = %q, want %q", body.CallID, testCallID)
	}
	if len(body.Modalities) != 1 || body.Modalities[0] != msg.ModalityAudio {
		t.Errorf("modalities = %v, want [audio]", body.Modalities)
	}
	if body.Tokens[msg.ModalityAudio] != "tok-audio-caller" {
		t.Errorf("tokens[audio] = %q, want %q", body.Tokens[msg.ModalityAudio], "tok-audio-caller")
	}
	if !bytes.Equal(body.OutboundKey, key) {
		t.Errorf("outbound_key not round-tripped")
	}
}

func TestBuildCallOffer_RejectsEmptyCallID(t *testing.T) {
	_, err := msg.BuildCallOffer(1, 1, testMsgID, "", []string{msg.ModalityAudio}, nil, nil, 0)
	if !errors.Is(err, msg.ErrMissingField) {
		t.Errorf("empty call_id err = %v, want ErrMissingField", err)
	}
}

func TestBuildCallOffer_RejectsEmptyModalities(t *testing.T) {
	_, err := msg.BuildCallOffer(1, 1, testMsgID, testCallID, nil, nil, nil, 0)
	if !errors.Is(err, msg.ErrMissingField) {
		t.Errorf("empty modalities err = %v, want ErrMissingField", err)
	}
}

func TestBuildCallOffer_RejectsBadKeyLength(t *testing.T) {
	_, err := msg.BuildCallOffer(1, 1, testMsgID, testCallID, []string{msg.ModalityAudio}, nil, []byte{1, 2, 3}, 0)
	if !errors.Is(err, msg.ErrMissingField) {
		t.Errorf("short key err = %v, want ErrMissingField", err)
	}
}

func TestCallOffer_RejectsBadKeyOnDecode(t *testing.T) {

	raw := []byte(`{"v":2,"seq":1,"ts":1,"msg_id":"` + testMsgID + `","kind":"call_offer","body":{"call_id":"` + testCallID + `","modalities":["audio"],"outbound_key":"AQID"}}`)
	w, err := msg.Unmarshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.CallOffer(); err == nil {
		t.Fatalf("CallOffer() with 3-byte key did not error")
	}
}

func TestBuildCallAccept_RoundTrip(t *testing.T) {
	tokens := map[string]string{msg.ModalityAudio: "tok-audio-callee"}
	key := bytes.Repeat([]byte{0xCD}, msg.CallOutboundKeyBytes)
	w, err := msg.BuildCallAccept(2, 1, testMsgID, testCallID, []string{msg.ModalityAudio}, tokens, key, 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := msg.Marshal(w)
	got, _ := msg.Unmarshal(raw)
	body, err := got.CallAccept()
	if err != nil {
		t.Fatal(err)
	}
	if body.CallID != testCallID {
		t.Errorf("call_id = %q, want %q", body.CallID, testCallID)
	}
	if body.Tokens[msg.ModalityAudio] != "tok-audio-callee" {
		t.Errorf("tokens[audio] = %q, want %q", body.Tokens[msg.ModalityAudio], "tok-audio-callee")
	}
	if !bytes.Equal(body.OutboundKey, key) {
		t.Errorf("outbound_key not round-tripped")
	}
}

func TestBuildCallReject_RoundTripWithReason(t *testing.T) {
	w, err := msg.BuildCallReject(2, 1, testMsgID, testCallID, msg.CallRejectUserDeclined, 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := msg.Marshal(w)
	got, _ := msg.Unmarshal(raw)
	body, err := got.CallReject()
	if err != nil {
		t.Fatal(err)
	}
	if body.Reason != msg.CallRejectUserDeclined {
		t.Errorf("reason = %q, want %q", body.Reason, msg.CallRejectUserDeclined)
	}
}

func TestBuildCallReject_AllowsEmptyReason(t *testing.T) {
	if _, err := msg.BuildCallReject(1, 1, testMsgID, testCallID, "", 0); err != nil {
		t.Errorf("empty reason should be allowed: %v", err)
	}
}

func TestBuildCallEnd_RoundTrip(t *testing.T) {
	w, err := msg.BuildCallEnd(4, 1, testMsgID, testCallID, 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := msg.Marshal(w)
	got, _ := msg.Unmarshal(raw)
	body, err := got.CallEnd()
	if err != nil {
		t.Fatal(err)
	}
	if body.CallID != testCallID {
		t.Errorf("call_id = %q, want %q", body.CallID, testCallID)
	}
}

func TestCallOffer_RejectsWrongKind(t *testing.T) {
	w := &msg.Wrapper{V: msg.Version, Seq: 1, Ts: 1, MsgID: testMsgID, Kind: msg.KindText, Body: json.RawMessage(`{}`)}
	if _, err := w.CallOffer(); err == nil || !strings.Contains(err.Error(), "not \"call_offer\"") {
		t.Fatalf("err = %v, want kind-mismatch error", err)
	}
}

const testRotationID = "rot-0123456789abcdef0123456789abcdef"
const testNewAddress = "abcdefghijklmnopqrstuvwxyz234567abcdefghijklmnopqrstuvwx"

func TestBuildRotateRequest_RoundTrip(t *testing.T) {
	w, err := msg.BuildRotateRequest(4, 1742643890, testMsgID, testRotationID, 1742643889, 0)
	if err != nil {
		t.Fatal(err)
	}
	if w.Kind != msg.KindRotateRequest {
		t.Errorf("kind = %q, want %q", w.Kind, msg.KindRotateRequest)
	}
	raw, _ := msg.Marshal(w)
	got, err := msg.Unmarshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	body, err := got.RotateRequest()
	if err != nil {
		t.Fatal(err)
	}
	if body.RotationID != testRotationID {
		t.Errorf("rotation_id = %q, want %q", body.RotationID, testRotationID)
	}
	if body.ProposedAt != 1742643889 {
		t.Errorf("proposed_at = %d, want 1742643889", body.ProposedAt)
	}
}

func TestBuildRotateRequest_RejectsEmptyRotationID(t *testing.T) {
	_, err := msg.BuildRotateRequest(1, 1, testMsgID, "", 1, 0)
	if !errors.Is(err, msg.ErrMissingField) {
		t.Errorf("empty rotation_id err = %v, want ErrMissingField", err)
	}
}

func TestBuildRotateRequest_RejectsZeroProposedAt(t *testing.T) {
	_, err := msg.BuildRotateRequest(1, 1, testMsgID, testRotationID, 0, 0)
	if !errors.Is(err, msg.ErrMissingField) {
		t.Errorf("zero proposed_at err = %v, want ErrMissingField", err)
	}
}

func TestBuildRotateAccept_RoundTrip(t *testing.T) {
	w, err := msg.BuildRotateAccept(2, 1742643891, testMsgID, testRotationID, 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := msg.Marshal(w)
	got, _ := msg.Unmarshal(raw)
	body, err := got.RotateAccept()
	if err != nil {
		t.Fatal(err)
	}
	if body.RotationID != testRotationID {
		t.Errorf("rotation_id = %q, want %q", body.RotationID, testRotationID)
	}
}

func TestBuildRotateAddress_RoundTrip(t *testing.T) {
	w, err := msg.BuildRotateAddress(3, 1742643892, testMsgID, testRotationID, testNewAddress, 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := msg.Marshal(w)
	got, _ := msg.Unmarshal(raw)
	body, err := got.RotateAddress()
	if err != nil {
		t.Fatal(err)
	}
	if body.RotationID != testRotationID {
		t.Errorf("rotation_id = %q, want %q", body.RotationID, testRotationID)
	}
	if body.NewAddress != testNewAddress {
		t.Errorf("new_address = %q, want %q", body.NewAddress, testNewAddress)
	}
}

func TestBuildRotateAddress_RejectsEmptyAddress(t *testing.T) {
	_, err := msg.BuildRotateAddress(1, 1, testMsgID, testRotationID, "", 0)
	if !errors.Is(err, msg.ErrMissingField) {
		t.Errorf("empty new_address err = %v, want ErrMissingField", err)
	}
}

func TestBuildRotateConfirm_RoundTrip(t *testing.T) {
	w, err := msg.BuildRotateConfirm(4, 1742643893, testMsgID, testRotationID, 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := msg.Marshal(w)
	got, _ := msg.Unmarshal(raw)
	body, err := got.RotateConfirm()
	if err != nil {
		t.Fatal(err)
	}
	if body.RotationID != testRotationID {
		t.Errorf("rotation_id = %q, want %q", body.RotationID, testRotationID)
	}
}

func TestBuildRotateCancel_RoundTrip(t *testing.T) {
	w, err := msg.BuildRotateCancel(5, 1742643894, testMsgID, testRotationID, msg.RotateCancelUserDeclined, 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := msg.Marshal(w)
	got, _ := msg.Unmarshal(raw)
	body, err := got.RotateCancel()
	if err != nil {
		t.Fatal(err)
	}
	if body.RotationID != testRotationID {
		t.Errorf("rotation_id = %q, want %q", body.RotationID, testRotationID)
	}
	if body.Reason != msg.RotateCancelUserDeclined {
		t.Errorf("reason = %q, want %q", body.Reason, msg.RotateCancelUserDeclined)
	}
}

func TestBuildRotateCancel_AcceptsEmptyReason(t *testing.T) {
	if _, err := msg.BuildRotateCancel(1, 1, testMsgID, testRotationID, "", 0); err != nil {
		t.Errorf("empty reason rejected: %v", err)
	}
}

func TestRotateRequest_RejectsWrongKind(t *testing.T) {
	w := &msg.Wrapper{V: msg.Version, Seq: 1, Ts: 1, MsgID: testMsgID, Kind: msg.KindText, Body: json.RawMessage(`{}`)}
	if _, err := w.RotateRequest(); err == nil || !strings.Contains(err.Error(), "not \"rotate_request\"") {
		t.Fatalf("err = %v, want kind-mismatch error", err)
	}
}
