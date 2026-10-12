package main

import (
	"encoding/json"
	"fmt"
	"time"

	"haoma-frontend/internal/ipc"
)

const aliasConvergeTimeout = 180 * time.Second

func assertAliasConverges(devA, devB *rig, aSeesPeer, bSeesPeer string) map[string]error {
	out := map[string]error{}
	const crosses = "an alias crosses to a sibling"
	const cleared = "a cleared alias is not resurrected"
	const unprompted = "a rename crosses with no sync verb"

	before, err := peerAlias(devA, aSeesPeer)
	if err != nil {
		err = fmt.Errorf("could not read %s's alias for the contact: %w", devA.name, err)
		out[crosses] = err
		out[cleared] = fmt.Errorf("not reached: %w", err)
		out[unprompted] = fmt.Errorf("not reached: %w", err)
		return out
	}
	defer func() {
		if rErr := setAlias(devA, aSeesPeer, before); rErr != nil {
			logf("[%s] could not restore the alias: %v", devA.name, rErr)
		}
	}()

	name := "boss " + nextCorrID()
	if err := setAlias(devA, aSeesPeer, name); err != nil {
		err = fmt.Errorf("could not set %s's alias: %w", devA.name, err)
		out[crosses] = err
		out[cleared] = fmt.Errorf("not reached: %w", err)
		out[unprompted] = fmt.Errorf("not reached: %w", err)
		return out
	}
	if err := driveSync(devB, false); err != nil {
		logf("[%s] could not drive the sync after the alias was set: %v", devB.name, err)
	}
	if err := awaitAlias(devB, bSeesPeer, func(got string) bool { return got == name }); err != nil {
		out[crosses] = fmt.Errorf("%s never saw the alias %s set: %w", devB.name, devA.name, err)
		out[cleared] = fmt.Errorf("not reached: the alias did not cross")
		out[unprompted] = fmt.Errorf("not reached: the alias did not cross")
		return out
	}
	logf("[%s] inherited the alias %s set on the contact", devB.name, devA.name)
	out[crosses] = nil

	if err := setAlias(devB, bSeesPeer, ""); err != nil {
		out[cleared] = fmt.Errorf("could not clear %s's alias: %w", devB.name, err)
		out[unprompted] = fmt.Errorf("not reached: the clear did not apply locally")
		return out
	}
	if err := driveSync(devA, false); err != nil {
		logf("[%s] could not drive the sync after the alias was cleared: %v", devA.name, err)
	}
	if err := awaitAlias(devA, aSeesPeer, func(got string) bool { return got == "" }); err != nil {
		out[cleared] = fmt.Errorf(
			"%s still shows the alias after %s cleared it — the clear did not cross: %w",
			devA.name, devB.name, err)
		out[unprompted] = fmt.Errorf("not reached: the clear did not cross")
		return out
	}

	if err := driveSync(devB, false); err != nil {
		logf("[%s] could not drive the sweep back: %v", devB.name, err)
	}
	if err := awaitAlias(devB, bSeesPeer, func(got string) bool { return got == "" }); err != nil {
		out[cleared] = fmt.Errorf(
			"the alias came back on %s after a sweep — a stored name refilled a cleared one: %w",
			devB.name, err)
		out[unprompted] = fmt.Errorf("not reached: the clear was undone")
		return out
	}
	logf("[%s<->%s] a cleared alias stayed cleared on both devices", devA.name, devB.name)
	out[cleared] = nil

	marker := "trigger " + nextCorrID()
	if err := setAlias(devA, aSeesPeer, marker); err != nil {
		out[unprompted] = fmt.Errorf("could not rename the contact on %s: %w", devA.name, err)
		return out
	}
	logf("[%s] renamed a contact and asked nothing to sync — waiting on the push alone", devA.name)
	if err := awaitAlias(devB, bSeesPeer, func(got string) bool { return got == marker }); err != nil {
		out[unprompted] = fmt.Errorf(
			"%s never saw the rename; nothing put it on the rail, so it waits for a sweep caused by something else: %w",
			devB.name, err)
		return out
	}
	logf("[%s] a rename crossed with no sync verb and no restart", devB.name)
	out[unprompted] = nil
	return out
}

func awaitAlias(r *rig, peerID string, want func(string) bool) error {
	deadline := time.Now().Add(aliasConvergeTimeout)
	last := "\x00"
	var read bool
	for time.Now().Before(deadline) {
		got, err := peerAlias(r, peerID)
		if err == nil {
			read = true
			if want(got) {
				return nil
			}

			if got != last {
				logf("[%s] alias now: %q", r.name, got)
				last = got
			}
		}
		rigSleep(syncPollInterval, "poll: alias converge")
	}
	if !read {
		return fmt.Errorf("the alias was never read at all after %s", aliasConvergeTimeout)
	}
	return fmt.Errorf("after %s the last reading was %q", aliasConvergeTimeout, last)
}

func peerAlias(r *rig, peerID string) (string, error) {
	peers, err := listPeers(r, 30*time.Second)
	if err != nil {
		return "", err
	}
	for _, p := range peers {
		if p.ID == peerID {
			return p.Alias, nil
		}
	}
	return "", fmt.Errorf("%s holds no contact with routing id %s", r.name, short(peerID))
}

func setAlias(r *rig, peerID, alias string) error {
	sub, cancel := r.subscribe()
	defer cancel()
	if err := r.send(ipc.FrameSetAlias, ipc.SetAliasRequest{PeerID: peerID, Alias: alias}); err != nil {
		return fmt.Errorf("set_alias send: %w", err)
	}
	f, err := awaitFrame(sub, 30*time.Second, func(f ipc.Frame) bool {
		return f.Type == ipc.FrameAliasUpdated || f.Type == ipc.FrameError
	})
	if err != nil {
		return fmt.Errorf("await alias_updated: %w", err)
	}
	if f.Type == ipc.FrameError {
		return frameErr("set_alias", f)
	}
	var resp ipc.AliasUpdatedResponse
	if err := json.Unmarshal(f.Payload, &resp); err != nil {
		return fmt.Errorf("decode alias_updated: %w", err)
	}
	return nil
}
