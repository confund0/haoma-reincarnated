package main

import (
	"encoding/json"
	"testing"
	"time"

	"haoma-frontend/internal/ipc"
)

func unreadFrame(t *testing.T, chatID string, count uint32) ipc.Frame {
	t.Helper()
	p, err := json.Marshal(ipc.ChatUnreadChangedPayload{ChatID: chatID, UnreadCount: count})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return ipc.Frame{Type: ipc.FrameChatUnreadChanged, Payload: p}
}

func TestAwaitChatUnreadCount(t *testing.T) {
	const chatID = "ccccccccdddddddd"

	for _, tc := range []struct {
		name    string
		frames  []ipc.Frame
		want    uint32
		wantErr bool
	}{
		{
			name:   "the named chat reaching the wanted count matches",
			frames: []ipc.Frame{unreadFrame(t, chatID, 0)},
			want:   0,
		},
		{
			name: "earlier pushes on the same chat are skipped, not matched",
			frames: []ipc.Frame{
				unreadFrame(t, chatID, 4),
				unreadFrame(t, chatID, 2),
				unreadFrame(t, chatID, 0),
			},
			want: 0,
		},
		{
			name:    "another chat reaching zero does not match",
			frames:  []ipc.Frame{unreadFrame(t, "eeeeeeeeffffffff", 0)},
			want:    0,
			wantErr: true,
		},
		{
			name:    "the same chat at another count does not match",
			frames:  []ipc.Frame{unreadFrame(t, chatID, 3)},
			want:    0,
			wantErr: true,
		},
		{
			name:    "no push at all times out",
			want:    0,
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ch := make(chan ipc.Frame, len(tc.frames)+1)
			for _, f := range tc.frames {
				ch <- f
			}
			err := awaitChatUnreadCount(ch, chatID, tc.want, 100*time.Millisecond)
			if tc.wantErr && err == nil {
				t.Errorf("wait returned nil, want an error — it matched a push it must ignore")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("wait returned %v, want nil — it ignored the push it must match", err)
			}
		})
	}
}
