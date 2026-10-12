package main

import (
	"fmt"
	"time"

	"haoma-frontend/internal/ipc"
)

const settingsConvergeTimeout = 180 * time.Second

func assertChatSettingsConverge(devA, devB *rig, aChatID, bChatID string) map[string]error {
	out := map[string]error{}

	before, err := chatSettings(devA, aChatID)
	if err != nil {
		err = fmt.Errorf("could not read %s's settings: %w", devA.name, err)
		out["chat settings cross to a sibling"] = err
		out["a settings change converges with no sync verb"] = fmt.Errorf("not reached: %w", err)
		return out
	}
	restore := func() {
		if rErr := setChatSettings(devA, before); rErr != nil {
			logf("[%s] could not restore chat settings: %v", devA.name, rErr)
		}
	}
	defer restore()

	seed := before
	seed.NotificationsMuted = true
	seed.NickOverride = "seeded " + nextCorrID()
	if err := setChatSettings(devA, seed); err != nil {
		err = fmt.Errorf("could not seed %s's settings: %w", devA.name, err)
		out["chat settings cross to a sibling"] = err
		out["a settings change converges with no sync verb"] = fmt.Errorf("not reached: %w", err)
		return out
	}
	if err := driveSync(devB, false); err != nil {
		logf("[%s] could not drive the seeding sync: %v", devB.name, err)
	}
	if err := awaitChatSettings(devB, bChatID, func(p ipc.ChatSettingsPayload) bool {
		return p.NotificationsMuted && p.NickOverride == seed.NickOverride
	}); err != nil {
		out["chat settings cross to a sibling"] = fmt.Errorf(
			"%s never inherited the seeded settings from %s: %w", devB.name, devA.name, err)
		out["a settings change converges with no sync verb"] = fmt.Errorf("not reached: the seed did not cross")
		return out
	}

	cleared := seed
	cleared.NotificationsMuted = false
	cleared.NickOverride = ""
	if err := setChatSettings(devA, cleared); err != nil {
		out["chat settings cross to a sibling"] = fmt.Errorf("could not clear %s's settings: %w", devA.name, err)
		out["a settings change converges with no sync verb"] = fmt.Errorf("not reached: the clear did not apply locally")
		return out
	}
	if err := driveSync(devB, false); err != nil {
		logf("[%s] could not drive the clearing sync: %v", devB.name, err)
	}
	if err := awaitChatSettings(devB, bChatID, func(p ipc.ChatSettingsPayload) bool {
		return !p.NotificationsMuted && p.NickOverride == ""
	}); err != nil {
		out["chat settings cross to a sibling"] = fmt.Errorf(
			"%s still holds the old values after %s cleared them — a cleared setting does not cross: %w",
			devB.name, devA.name, err)
	} else {
		logf("[%s] inherited a cleared mute and a cleared nick from %s", devB.name, devA.name)
		out["chat settings cross to a sibling"] = nil
	}

	marker := "trigger " + nextCorrID()
	triggered := cleared
	triggered.NickOverride = marker
	if err := setChatSettings(devA, triggered); err != nil {
		out["a settings change converges with no sync verb"] = fmt.Errorf(
			"could not change %s's settings: %w", devA.name, err)
		return out
	}
	logf("[%s] changed a setting and asked nothing to sync — waiting on the trigger alone", devA.name)
	if err := awaitChatSettings(devB, bChatID, func(p ipc.ChatSettingsPayload) bool {
		return p.NickOverride == marker
	}); err != nil {
		out["a settings change converges with no sync verb"] = fmt.Errorf(
			"%s never saw the change; nothing armed a sweep, so the setting waits for one caused by something else: %w",
			devB.name, err)
		return out
	}
	logf("[%s] the trigger carried a settings change with no sync verb and no restart", devB.name)
	out["a settings change converges with no sync verb"] = nil
	return out
}

func awaitChatSettings(r *rig, chatID string, want func(ipc.ChatSettingsPayload) bool) error {
	deadline := time.Now().Add(settingsConvergeTimeout)
	last := ""
	for time.Now().Before(deadline) {
		p, err := chatSettings(r, chatID)
		if err == nil {
			if want(p) {
				return nil
			}

			now := fmt.Sprintf("muted=%v receipts_off=%v nick=%q retention=%d",
				p.NotificationsMuted, p.DisableReadReceipts, p.NickOverride, p.RetentionTTL)
			if now != last {
				logf("[%s] settings now: %s", r.name, now)
				last = now
			}
		}
		rigSleep(syncPollInterval, "poll: settings converge")
	}
	if last == "" {
		return fmt.Errorf("settings never read at all after %s", settingsConvergeTimeout)
	}
	return fmt.Errorf("after %s the last reading was %s", settingsConvergeTimeout, last)
}
