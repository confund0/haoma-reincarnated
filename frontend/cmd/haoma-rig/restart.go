package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
)

const restartRigCount = 2

const (
	restartQueryTimeout = 20 * time.Second
	restartWelcome      = 30 * time.Second
	restartOnionTimeout = 240 * time.Second
	restartOnionPoll    = 10 * time.Second
	restartSettleDelay  = 10 * time.Second
)

func runRestartFleet(a, b *rig) *pairingResult {
	res := &pairingResult{a: a.name, b: b.name}

	logf("[%s<->%s] pairing …", a.name, b.name)
	aSeesB, _, err := pairOnion(a, b)
	res.add("pair", err)
	if err != nil {
		logf("[%s<->%s] PAIR FAILED: %v", a.name, b.name, err)
		return res
	}

	time.Sleep(settleDelay)

	d, err := deliverText(a, b, aSeesB, fmt.Sprintf("pre-restart %s->%s", a.name, b.name))
	res.add("text", err)
	if err != nil {
		logf("[%s<->%s] pre-restart text FAILED: %v", a.name, b.name, err)
		return res
	}

	beforeOnion, err := onionSlots(a, restartQueryTimeout)
	if err == nil && len(beforeOnion) == 0 {
		err = fmt.Errorf("%s published no onion slot before the restart — nothing to compare against", a.name)
	}
	res.add("onion before", err)

	sub, cancel := a.subscribe()
	defer cancel()

	logf("[%s] restarting on the same tree …", a.name)
	if err := a.restart(); err != nil {
		res.add("restart", err)
		logf("[%s] RESTART FAILED: %v", a.name, err)
		return res
	}
	res.add("restart", nil)

	_, err = awaitFrame(sub, restartWelcome, func(f ipc.Frame) bool { return f.Type == ipc.FrameWelcome })
	res.add("welcome", err)

	time.Sleep(restartSettleDelay)

	chats, err := listChats(a, restartQueryTimeout)
	if err == nil && !chatsNamePeer(chats, aSeesB) {
		err = fmt.Errorf("chat list on %s no longer holds a chat for %s (%d chat(s) listed)", a.name, short(aSeesB), len(chats))
	}
	res.add("chats", err)

	peers, err := listPeers(a, restartQueryTimeout)
	if err == nil && !peersNameID(peers, aSeesB) {
		err = fmt.Errorf("peer list on %s no longer holds %s (%d peer(s) listed)", a.name, short(aSeesB), len(peers))
	}
	res.add("peer id", err)

	ids, err := timelineMsgIDs(a, aSeesB, restartQueryTimeout)
	if err == nil && !ids[d.msgID] {
		err = fmt.Errorf("timeline on %s no longer holds msg_id=%s (%d row(s) read)", a.name, short(d.msgID), len(ids))
	}
	res.add("history", err)

	if len(beforeOnion) > 0 {
		res.add("onion", awaitOnionSlots(a, beforeOnion, restartOnionTimeout))
	}

	for _, c := range res.checks {
		if !c.ok {
			logf("[%s<->%s] %s FAILED: %s", a.name, b.name, c.name, c.err)
		}
	}
	return res
}

func listChats(r *rig, timeout time.Duration) ([]ipc.ChatEntry, error) {
	sub, cancel := r.subscribe()
	defer cancel()
	if err := r.send(ipc.FrameListChats, nil); err != nil {
		return nil, fmt.Errorf("list_chats send: %w", err)
	}
	f, err := awaitFrame(sub, timeout, func(f ipc.Frame) bool {
		return f.Type == ipc.FrameChatsListed || f.Type == ipc.FrameError
	})
	if err != nil {
		return nil, fmt.Errorf("await chats_listed: %w", err)
	}
	if f.Type == ipc.FrameError {
		return nil, frameErr("list_chats", f)
	}
	var listed ipc.ChatsListedResponse
	if err := json.Unmarshal(f.Payload, &listed); err != nil {
		return nil, fmt.Errorf("decode chats_listed: %w", err)
	}
	return listed.Chats, nil
}

func chatsNamePeer(chats []ipc.ChatEntry, peerID string) bool {
	for _, c := range chats {
		if c.PeerID == peerID {
			return true
		}
	}
	return false
}

func listPeers(r *rig, timeout time.Duration) ([]ipc.PeerEntry, error) {
	sub, cancel := r.subscribe()
	defer cancel()
	if err := r.send(ipc.FrameListPeers, nil); err != nil {
		return nil, fmt.Errorf("list_peers send: %w", err)
	}
	f, err := awaitFrame(sub, timeout, func(f ipc.Frame) bool {
		return f.Type == ipc.FramePeersListed || f.Type == ipc.FrameError
	})
	if err != nil {
		return nil, fmt.Errorf("await peers_listed: %w", err)
	}
	if f.Type == ipc.FrameError {
		return nil, frameErr("list_peers", f)
	}
	var listed ipc.PeersListedResponse
	if err := json.Unmarshal(f.Payload, &listed); err != nil {
		return nil, fmt.Errorf("decode peers_listed: %w", err)
	}
	return listed.Peers, nil
}

func peersNameID(peers []ipc.PeerEntry, peerID string) bool {
	for _, p := range peers {
		if p.ID == peerID {
			return true
		}
	}
	return false
}

func timelineMsgIDs(r *rig, peerID string, timeout time.Duration) (map[string]bool, error) {
	sub, cancel := r.subscribe()
	defer cancel()
	if err := r.send(ipc.FrameListTimeline, ipc.ListTimelineRequest{PeerID: peerID, Limit: 100}); err != nil {
		return nil, fmt.Errorf("list_timeline send: %w", err)
	}
	f, err := awaitFrame(sub, timeout, func(f ipc.Frame) bool {
		return f.Type == ipc.FrameTimelinePage || f.Type == ipc.FrameError
	})
	if err != nil {
		return nil, fmt.Errorf("await timeline_page: %w", err)
	}
	if f.Type == ipc.FrameError {
		return nil, frameErr("list_timeline", f)
	}
	var page ipc.TimelinePageResponse
	if err := json.Unmarshal(f.Payload, &page); err != nil {
		return nil, fmt.Errorf("decode timeline_page: %w", err)
	}
	ids := make(map[string]bool, len(page.Events))
	for _, raw := range page.Events {
		var ev events.Event
		if json.Unmarshal(raw, &ev) == nil && ev.MsgID != "" {
			ids[ev.MsgID] = true
		}
	}
	return ids, nil
}

func onionSlots(r *rig, timeout time.Duration) ([]string, error) {
	sub, cancel := r.subscribe()
	defer cancel()
	if err := r.send(ipc.FrameTorInfo, nil); err != nil {
		return nil, fmt.Errorf("tor_info send: %w", err)
	}
	f, err := awaitFrame(sub, timeout, func(f ipc.Frame) bool {
		return f.Type == ipc.FrameTorInfoResponse || f.Type == ipc.FrameError
	})
	if err != nil {
		return nil, fmt.Errorf("await tor_info_response: %w", err)
	}
	if f.Type == ipc.FrameError {
		return nil, frameErr("tor_info", f)
	}
	var info ipc.TorInfoResponsePayload
	if err := json.Unmarshal(f.Payload, &info); err != nil {
		return nil, fmt.Errorf("decode tor_info_response: %w", err)
	}
	ids := make([]string, 0, len(info.Slots))
	for _, s := range info.Slots {
		if s.ServiceID != "" {
			ids = append(ids, s.ServiceID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func awaitOnionSlots(r *rig, want []string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	lastErr := fmt.Errorf("no tor_info answer yet")
	for time.Now().Before(deadline) {
		got, err := onionSlots(r, restartQueryTimeout)
		switch {
		case err != nil:
			lastErr = err
		case len(got) == 0:
			lastErr = fmt.Errorf("no onion published yet")
		case sameStrings(got, want):
			logf("[%s] onion republished unchanged (%s)", r.name, short(want[0]))
			return nil
		default:

			return fmt.Errorf("%s came back on a DIFFERENT onion: had %s, now %s",
				r.name, strings.Join(want, ","), strings.Join(got, ","))
		}
		time.Sleep(restartOnionPoll)
	}
	return fmt.Errorf("%s did not republish its onion within %s: %w", r.name, timeout, lastErr)
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
