package main

import (
	"encoding/json"
	"fmt"
	"time"

	"haoma-frontend/internal/ipc"
)

const selfNickConvergeTimeout = 120 * time.Second

const (
	selfNickCrosses = "a self-nick crosses to a sibling with no sync verb"
	selfNickNewer   = "a device already holding a self-nick takes a newer one"
)

func assertSelfNickConverges(devA, devB *rig) map[string]error {
	out := map[string]error{}

	bSub, bCancel := devB.subscribe()
	defer bCancel()

	first := "selfnick-a-" + nextCorrID()
	if err := setSelfNick(devA, first); err != nil {
		err = fmt.Errorf("could not set %s's own name: %w", devA.name, err)
		out[selfNickCrosses] = err
		out[selfNickNewer] = fmt.Errorf("not reached: %w", err)
		return out
	}
	logf("[%s] set its own name and asked nothing to sync — waiting on the push alone", devA.name)
	if err := awaitSelfNick(devB, bSub, first); err != nil {
		err = fmt.Errorf(
			"%s never saw %s's name; nothing put it on the rail, so one identity holds two names: %w",
			devB.name, devA.name, err)
		out[selfNickCrosses] = err
		out[selfNickNewer] = fmt.Errorf("not reached: %w", err)
		return out
	}
	logf("[%s] took %s's name with no sync verb and no restart", devB.name, devA.name)
	out[selfNickCrosses] = nil

	aSub, aCancel := devA.subscribe()
	second := "selfnick-b-" + nextCorrID()
	if err := setSelfNick(devB, second); err != nil {
		aCancel()
		out[selfNickNewer] = fmt.Errorf("could not set %s's own name: %w", devB.name, err)
		return out
	}
	err := awaitSelfNick(devA, aSub, second)
	aCancel()
	if err != nil {
		out[selfNickNewer] = fmt.Errorf(
			"not reached: %s never took %s's name, so the set-up for the newer-name check never happened: %w",
			devA.name, devB.name, err)
		return out
	}
	logf("[%s] took %s's name; both devices now hold one set on %s", devA.name, devB.name, devB.name)

	bSub2, bCancel2 := devB.subscribe()
	defer bCancel2()

	third := "selfnick-c-" + nextCorrID()
	if err := setSelfNick(devA, third); err != nil {
		out[selfNickNewer] = fmt.Errorf("could not rename %s: %w", devA.name, err)
		return out
	}
	logf("[%s] renamed itself over a name %s set — waiting on the push alone", devA.name, devB.name)
	if err := awaitSelfNick(devB, bSub2, third); err != nil {
		out[selfNickNewer] = fmt.Errorf(
			"%s kept the name it set itself against a newer one from %s: %w",
			devB.name, devA.name, err)
		return out
	}
	logf("[%s] took the newer name over its own", devB.name)
	out[selfNickNewer] = nil
	return out
}

func setSelfNick(r *rig, nick string) error {
	sub, cancel := r.subscribe()
	defer cancel()
	id, err := r.sendReq(ipc.FrameSetNick, ipc.SetNickRequest{Nick: nick})
	if err != nil {
		return fmt.Errorf("set_nick send: %w", err)
	}
	f, err := awaitFrame(sub, 30*time.Second, func(f ipc.Frame) bool {
		if f.Type == ipc.FrameError {
			return f.ID == id
		}
		return f.Type == ipc.FrameNick && selfNickOf(f) == nick
	})
	if err != nil {
		return fmt.Errorf("await the nick push on %s: %w", r.name, err)
	}
	if f.Type == ipc.FrameError {
		return frameErr("set_nick", f)
	}
	return nil
}

func awaitSelfNick(r *rig, sub <-chan ipc.Frame, want string) error {
	deadline := time.Now().Add(selfNickConvergeTimeout)
	last := "\x00"
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		f, err := awaitFrame(sub, remaining, func(f ipc.Frame) bool { return f.Type == ipc.FrameNick })
		if err != nil {
			break
		}
		got := selfNickOf(f)
		if f.ID != "" {

			return fmt.Errorf("[%s] a nick push arrived with correlation id %q; it is not a broadcast", r.name, f.ID)
		}
		if got == want {
			return nil
		}
		if got != last {
			logf("[%s] own name now: %q", r.name, got)
			last = got
		}
	}
	if last == "\x00" {
		return fmt.Errorf("%s reported no name at all within %s", r.name, selfNickConvergeTimeout)
	}
	return fmt.Errorf("after %s %s was still holding %q", selfNickConvergeTimeout, r.name, last)
}

func selfNickOf(f ipc.Frame) string {
	var p ipc.NickPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		return ""
	}
	return p.Nick
}
