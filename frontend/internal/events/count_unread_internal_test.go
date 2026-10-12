package events

import (
	"encoding/json"
	"testing"
	"time"

	"haoma-frontend/internal/chat"
)

func TestCountUnread_FiltersEachExcludedKindInIsolation(t *testing.T) {
	st := newTestStore(t)
	now := time.Unix(1742643890, 0)
	l := New(st, NewBus(), func() time.Time { return now })
	chatID := chat.ChatID("alice")
	body, err := json.Marshal(TextBody{Text: "x"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := l.append(Event{
		ChatID: chatID, Direction: DirOut, Kind: KindText, MsgID: "out-1",
		DisplayTs: 1742643880, DecryptStatus: DecryptOK, Body: body,
	}, nil); err != nil {
		t.Fatal(err)
	}

	if _, err := l.append(Event{
		ChatID: chatID, Direction: DirIn, Kind: KindText, MsgID: "failed-1",
		DisplayTs: 1742643881, DecryptStatus: DecryptFailed, RawBlob: []byte{1, 2, 3},
	}, nil); err != nil {
		t.Fatal(err)
	}

	if _, err := l.append(Event{
		ChatID: chatID, Direction: DirIn, Kind: KindReaction, MsgID: "reaction-1",
		DisplayTs: 1742643882, DecryptStatus: DecryptOK, Body: body,
	}, nil); err != nil {
		t.Fatal(err)
	}

	if _, err := l.append(Event{
		ChatID: chatID, Direction: DirIn, Kind: KindText, MsgID: "read-1",
		DisplayTs: 1742643883, DecryptStatus: DecryptOK, Body: body, ReadAt: 1742643900,
	}, nil); err != nil {
		t.Fatal(err)
	}

	if _, err := l.append(Event{
		ChatID: chatID, Direction: DirIn, Kind: KindText, MsgID: "unread-1",
		DisplayTs: 1742643884, DecryptStatus: DecryptOK, Body: body,
	}, nil); err != nil {
		t.Fatal(err)
	}

	got, err := l.CountUnread(chatID)
	if err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Fatalf("CountUnread = %d, want 1 — exactly the row that passes every filter; "+
			"the outbound, decrypt-failed, reaction and already-read rows must each be excluded on their own", got)
	}
}
