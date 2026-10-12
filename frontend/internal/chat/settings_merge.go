package chat

import (
	"errors"
	"fmt"
)

type SettingsUpdate struct {
	Muted        bool
	Receipts     bool
	NickOverride string

	At SettingsStamps
}

func (s *Store) MergeSettings(id ChatID, in SettingsUpdate) (moved []string, err error) {
	if id == "" {
		return nil, errors.New("chat: MergeSettings: empty id")
	}
	err = s.mutate(id, func(c Chat) error {
		b := baseOf(c)
		if b == nil {
			return fmt.Errorf("chat: MergeSettings: unknown chat type %T", c)
		}
		if boolWins(in.At.Muted, b.SettingsAt.Muted, in.Muted, b.NotificationsMuted) {
			b.NotificationsMuted = in.Muted
			b.SettingsAt.Muted = in.At.Muted
			moved = append(moved, "muted")
		}
		if boolWins(in.At.Receipts, b.SettingsAt.Receipts, in.Receipts, b.DisableReadReceipts) {
			b.DisableReadReceipts = in.Receipts
			b.SettingsAt.Receipts = in.At.Receipts
			moved = append(moved, "receipts")
		}
		if stringWins(in.At.NickOverride, b.SettingsAt.NickOverride, in.NickOverride, b.NickOverride) {
			b.NickOverride = in.NickOverride
			b.SettingsAt.NickOverride = in.At.NickOverride
			moved = append(moved, "nick_override")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return moved, nil
}

func wins(inAt, localAt int64) (take bool, settled bool) {
	switch {
	case inAt > localAt:
		return true, true
	case inAt < localAt:
		return false, true
	default:
		return false, false
	}
}

func boolWins(inAt, localAt int64, in, local bool) bool {
	take, settled := wins(inAt, localAt)
	if settled {
		return take && in != local
	}
	return in && !local
}

func stringWins(inAt, localAt int64, in, local string) bool {
	take, settled := wins(inAt, localAt)
	if settled {
		return take && in != local
	}
	return in > local
}
