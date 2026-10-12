package invitation

import (
	"haoma-frontend/internal/events"
)

func init() {
	events.RegisterCleanup(events.KindInvitation, invitationCleanup)
}

func invitationCleanup(ev events.Event) [][]byte {
	if ev.MsgID == "" {
		return nil
	}
	keys := [][]byte{recordKey(ev.MsgID)}
	if ev.ChatID != "" {
		keys = append(keys, indexKey(ev.ChatID, ev.MsgID))
	}
	return keys
}
