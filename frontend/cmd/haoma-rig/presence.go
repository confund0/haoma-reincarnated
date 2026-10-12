package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/msg"
)

const presenceMarkerTimeout = 60 * time.Second

const presenceRefusalWindow = 2 * time.Second

func driveNoReply(r *rig, sub <-chan ipc.Frame, t ipc.FrameType, payload any, stage string) error {
	id, err := r.sendReq(t, payload)
	if err != nil {
		return fmt.Errorf("%s send: %w", stage, err)
	}
	f, err := awaitFrame(sub, presenceRefusalWindow, func(f ipc.Frame) bool {
		return f.ID == id && f.Type == ipc.FrameError
	})
	if err == nil {
		return frameErr(stage, f)
	}
	return nil
}

func setPresenceMarker(r *rig, marker string) error {
	sub, cancel := r.subscribe()
	defer cancel()
	return driveNoReply(r, sub, ipc.FrameSetPresenceOverride,
		ipc.SetPresenceOverrideRequest{State: marker}, "set_presence_override")
}

func driveFocus(r *rig, chatID string, scroll int, stage string) error {
	if _, err := r.sendReq(ipc.FrameClientFocus,
		ipc.ClientFocusRequest{ChatID: chatID, ScrollPosition: scroll}); err != nil {
		return fmt.Errorf("%s send: %w", stage, err)
	}
	return nil
}

func clearPeerFocus(peer *rig) error {
	return driveFocus(peer, "", 0, "client_focus clear")
}

func awaitPresenceMarker(sub <-chan ipc.Frame, deviceName, peerID, marker string) error {
	_, err := awaitFrame(sub, presenceMarkerTimeout, func(f ipc.Frame) bool {
		if f.Type != ipc.FramePresenceChanged {
			return false
		}
		var p ipc.PresenceChangedPayload
		if json.Unmarshal(f.Payload, &p) != nil {
			return false
		}
		return p.PeerID == peerID && p.Chatty == marker
	})
	if err != nil {
		return fmt.Errorf("%s never saw the peer (%s) reach the human presence state %q: %w",
			deviceName, short(peerID), marker, err)
	}
	return nil
}

func peerChatWithMe(peer *rig, peerSeesA string) (string, error) {
	chats, err := listChats(peer, syncConvergeTimeout)
	if err != nil {
		return "", fmt.Errorf("could not read %s's chat list: %w", peer.name, err)
	}
	for _, c := range chats {
		if c.PeerID == peerSeesA {
			return c.ChatID, nil
		}
	}
	return "", fmt.Errorf("%s holds no conversation whose primary device is %s", peer.name, short(peerSeesA))
}

func assertPresenceTrigger(devA, devB, peer *rig, aSeesPeer, bSeesPeer, marker string, fire func(peerSub <-chan ipc.Frame) error) (controlErr, gateErr error) {
	subA, cancelA := devA.subscribe()
	defer cancelA()
	subB, cancelB := devB.subscribe()
	defer cancelB()
	peerSub, peerCancel := peer.subscribe()
	defer peerCancel()

	if err := setPresenceMarker(peer, marker); err != nil {
		return err, fmt.Errorf("not asserted: the marker was never set on %s", peer.name)
	}
	if err := fire(peerSub); err != nil {
		return err, fmt.Errorf("not asserted: the trigger was not driven")
	}
	controlErr = awaitPresenceMarker(subA, devA.name, aSeesPeer, marker)
	gateErr = awaitPresenceMarker(subB, devB.name, bSeesPeer, marker)
	return controlErr, gateErr
}

var presenceVerdicts = []string{
	"presence chat-open control",
	"presence chat-open fan-out",
	"presence status control",
	"presence status fan-out",
	"presence broadcast reaches a contact",
	"presence broadcast skips my own device",
}

func assertPresenceFanout(devA, devB, peer *rig, aSeesPeer, bSeesPeer, peerSeesA string) map[string]error {
	out := map[string]error{}
	fail := func(err error) map[string]error {
		for _, n := range presenceVerdicts {
			out[n] = err
		}
		return out
	}

	if err := primePeerSessionToward(peer, devB, bSeesPeer); err != nil {
		return fail(err)
	}
	chatID, err := peerChatWithMe(peer, peerSeesA)
	if err != nil {
		return fail(err)
	}
	logf("[%s] conversation with me is %s; driving the two presence triggers", peer.name, short(chatID))

	ctrl, gate := assertPresenceTrigger(devA, devB, peer, aSeesPeer, bSeesPeer, msg.PresenceBusy,
		func(_ <-chan ipc.Frame) error {
			defer func() {
				if err := clearPeerFocus(peer); err != nil {
					logf("[%s] FOCUS NOT CLEARED: %v — later assertions in this run may see rows auto-marked read", peer.name, err)
				}
			}()
			return driveFocus(peer, chatID, 1, "client_focus")
		})
	out["presence chat-open control"] = ctrl
	out["presence chat-open fan-out"] = gate

	ctrl, gate = assertPresenceTrigger(devA, devB, peer, aSeesPeer, bSeesPeer, msg.PresenceAway,
		func(peerSub <-chan ipc.Frame) error {
			return driveNoReply(peer, peerSub, ipc.FramePushPresence,
				ipc.PushPresenceRequest{Target: peerSeesA}, "push_presence")
		})
	out["presence status control"] = ctrl
	out["presence status fan-out"] = gate

	if err := setPresenceMarker(peer, ""); err != nil {
		logf("[%s] PRESENCE OVERRIDE NOT RESET: %v — later assertions in this run may see a marker state", peer.name, err)
	}

	reach, skip := assertPresenceBroadcast(devA, peer, peerSeesA)
	out["presence broadcast reaches a contact"] = reach
	out["presence broadcast skips my own device"] = skip
	return out
}

const broadcastWalkLine = "push_presence: broadcast walked the peer list"

func assertPresenceBroadcast(devA, peer *rig, peerSeesA string) (reachErr, skipErr error) {

	if err := devicesAtDebugLevel(devA); err != nil {
		return err, err
	}

	const marker = msg.PresenceBusy

	before, err := broadcastWalkLines(devA, marker)
	if err != nil {
		return err, err
	}

	peerSub, peerCancel := peer.subscribe()
	defer peerCancel()

	if err := setPresenceMarker(devA, marker); err != nil {
		return err, fmt.Errorf("not asserted: the marker was never set on %s", devA.name)
	}

	defer func() {
		if err := setPresenceMarker(devA, ""); err != nil {
			logf("[%s] PRESENCE OVERRIDE NOT RESET: %v — later assertions in this run may see a marker state", devA.name, err)
		}
	}()

	sub, cancel := devA.subscribe()
	defer cancel()
	if err := driveNoReply(devA, sub, ipc.FramePushPresence,
		ipc.PushPresenceRequest{Target: ""}, "push_presence broadcast"); err != nil {
		return err, fmt.Errorf("not asserted: the broadcast was refused")
	}

	reachErr = awaitPresenceMarker(peerSub, peer.name, peerSeesA, marker)

	after, err := broadcastWalkLines(devA, marker)
	if err != nil {
		return reachErr, err
	}
	if len(after) != len(before)+1 {
		return reachErr, fmt.Errorf("%s logged %d broadcast walk line(s) carrying %q, want exactly one more than the %d it held before the drive",
			devA.name, len(after), marker, len(before))
	}
	line := after[len(after)-1]
	skipped, ok := logFieldValue(line, "own_devices_skipped")
	if !ok {
		return reachErr, fmt.Errorf("%s's broadcast walk line carries no own_devices_skipped count: %s", devA.name, line)
	}
	if skipped == "0" {
		return reachErr, fmt.Errorf("%s's broadcast skipped none of its own devices — my other device was addressed with a peer-shaped presence envelope it cannot unseal: %s", devA.name, line)
	}
	enqueued, ok := logFieldValue(line, "devices_enqueued")
	if !ok {
		return reachErr, fmt.Errorf("%s's broadcast walk line carries no devices_enqueued count: %s", devA.name, line)
	}
	if enqueued == "0" {
		return reachErr, fmt.Errorf("%s's broadcast skipped %s of its own devices but sent to nothing at all — the skip proves nothing on a walk that addressed no contact: %s",
			devA.name, skipped, line)
	}
	logf("[%s] broadcast walk: own devices skipped=%s, contact devices enqueued=%s", devA.name, skipped, enqueued)
	return reachErr, nil
}

func broadcastWalkLines(r *rig, marker string) ([]string, error) {
	lines, err := rigLogLines(r)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range lines {
		if !strings.Contains(line, broadcastWalkLine) {
			continue
		}
		if v, ok := logFieldValue(line, "state"); ok && v == marker {
			out = append(out, line)
		}
	}
	return out, nil
}
