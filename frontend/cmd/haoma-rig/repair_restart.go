package main

import (
	"fmt"
	"time"

	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/logid"
)

const (
	reattachedWaitMsg = "invitation: the wait for the person was attached again"

	invitationAnsweredMsg = "invitation: the person answered — a device that could only read about them can now reach them"
)

const reattachLogTimeout = 60 * time.Second

const completionSettle = 5 * time.Second

const pairedFallbackTimeout = 60 * time.Second

type repairRestart struct {
	stopped bool

	linesBefore int

	answeredBefore int
}

func runRepairRestartStop(res *pairingResult, slave *rig, msgID string) repairRestart {
	before, err := countLogLines(slave, reattachedWaitMsg, "invitation_id", msgID)
	if err != nil {
		res.add("slave's haoma stops", fmt.Errorf("could not read %s's log before the stop: %w", slave.name, err))
		return repairRestart{}
	}
	answeredBefore, err := countLogLines(slave, invitationAnsweredMsg, "invitation_id", msgID)
	if err != nil {
		res.add("slave's haoma stops", fmt.Errorf("could not read %s's log before the stop: %w", slave.name, err))
		return repairRestart{}
	}
	logf("[%s] stopping haoma — the person answers while it is down; haomad stays up", slave.name)
	if err := slave.stopHaoma(); err != nil {
		res.add("slave's haoma stops", err)
		return repairRestart{}
	}
	res.add("slave's haoma stops", nil)
	return repairRestart{stopped: true, linesBefore: before, answeredBefore: answeredBefore}
}

func runRepairRestartVerdicts(res *pairingResult, restart repairRestart, slave, peer *rig,
	msgID, slaveSeesPeer, peerSeesMaster, peerSeesSlave string, pairedErr error) {
	if !restart.stopped {
		repairRestartNotAsserted(res, "the slave's haoma was never stopped")
		return
	}
	res.add("slave attached the wait again after the restart",
		awaitLogLineGrowth(slave, reattachedWaitMsg, "invitation_id", msgID, restart.linesBefore, reattachLogTimeout))

	peers, err := listPeers(peer, syncConvergeTimeout)
	if err != nil {
		res.add("peer holds exactly two pairings", fmt.Errorf("could not read %s's peer list: %w", peer.name, err))
	} else {
		res.add("peer holds exactly two pairings", assertLivePairingsExactly(peer, peers, peerSeesMaster, peerSeesSlave))
	}

	if pairedErr != nil {
		res.add("slave completed this invitation once",
			fmt.Errorf("not asserted: the offering device never completed"))
		res.add("slave holds one pairing with the person",
			fmt.Errorf("not asserted: the offering device never completed"))
		return
	}
	time.Sleep(completionSettle)
	res.add("slave completed this invitation once", func() error {
		n, err := countLogLines(slave, invitationAnsweredMsg, "invitation_id", msgID)
		if err != nil {
			return fmt.Errorf("could not read %s's log: %w", slave.name, err)
		}
		if got := n - restart.answeredBefore; got != 1 {
			return fmt.Errorf("%s's log says this invitation completed %d time(s) after the restart, want exactly once — "+
				"two waits collected one handshake", slave.name, got)
		}
		return nil
	}())

	mine, err := listPeers(slave, syncConvergeTimeout)
	if err != nil {
		res.add("slave holds one pairing with the person", fmt.Errorf("could not read %s's peer list: %w", slave.name, err))
		return
	}
	res.add("slave holds one pairing with the person", assertLivePairingsExactly(slave, mine, slaveSeesPeer))
}

func repairRestartNotAsserted(res *pairingResult, why string) {
	for _, name := range []string{
		"slave attached the wait again after the restart",
		"peer holds exactly two pairings",
		"slave completed this invitation once",
		"slave holds one pairing with the person",
	} {
		res.add(name, fmt.Errorf("not asserted: %s", why))
	}
}

func confirmPairedAfterRestart(slave *rig, msgID string, pairedErr error) (string, error) {
	deadline := time.Now().Add(pairedFallbackTimeout)
	var last error
	for {
		id, err := pairedFromListAndLog(slave, msgID)
		if err == nil {
			logf("[%s] peer.paired did not reach the client after the restart; the peer list and the log confirm the pairing %s",
				slave.name, short(id))
			return id, nil
		}
		last = err
		if time.Now().After(deadline) {
			return "", fmt.Errorf("%w; and the second way did not confirm it either: %v", pairedErr, last)
		}
		time.Sleep(3 * time.Second)
	}
}

func pairedFromListAndLog(slave *rig, msgID string) (string, error) {
	peers, err := listPeers(slave, syncConvergeTimeout)
	if err != nil {
		return "", fmt.Errorf("could not read %s's peer list: %w", slave.name, err)
	}
	var live []string
	for _, p := range peers {
		if !p.Sibling && p.RetiredAt == 0 {
			live = append(live, p.ID)
		}
	}
	if len(live) != 1 {
		return "", fmt.Errorf("%s holds %d live pairing(s) with other people, want exactly one: %v", slave.name, len(live), shortAll(live))
	}
	logged, found, err := answeredPeerID(slave, msgID)
	switch {
	case err != nil:
		return "", err
	case !found:
		return "", fmt.Errorf("%s's log holds no line saying this invitation completed", slave.name)
	case logged != logid.Short(live[0]):
		return "", fmt.Errorf("%s's log says this invitation completed as %s, but the peer list holds %s",
			slave.name, logged, short(live[0]))
	}
	return live[0], nil
}

func answeredPeerID(r *rig, msgID string) (string, bool, error) {
	lines, err := rigLogLines(r)
	if err != nil {
		return "", false, err
	}
	onDisk := logid.Short(msgID)
	for _, line := range lines {
		if !hasLogMsg(line, invitationAnsweredMsg) {
			continue
		}
		if v, ok := logFieldValue(line, "invitation_id"); !ok || v != onDisk {
			continue
		}
		peerID, _ := logFieldValue(line, "peer_id")
		return peerID, true, nil
	}
	return "", false, nil
}

func countLogLines(r *rig, msg, key, want string) (int, error) {
	lines, err := rigLogLines(r)
	if err != nil {
		return 0, err
	}
	onDisk := logid.Short(want)
	n := 0
	for _, line := range lines {
		if !hasLogMsg(line, msg) {
			continue
		}
		if v, ok := logFieldValue(line, key); ok && v == onDisk {
			n++
		}
	}
	return n, nil
}

func awaitLogLineGrowth(r *rig, msg, key, want string, before int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		n, err := countLogLines(r, msg, key, want)
		if err == nil && n > before {
			logf("[%s] log: %q for %s", r.name, msg, short(want))
			return nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("could not read %s's log: %w", r.name, err)
			}
			return fmt.Errorf("%s's log has %d line(s) %q naming %s, as before the stop — "+
				"the wait was not attached again, so whatever completed the pairing was not the path under test",
				r.name, n, msg, short(want))
		}
		time.Sleep(2 * time.Second)
	}
}

func assertLivePairingsExactly(r *rig, peers []ipc.PeerEntry, ids ...string) error {
	want := map[string]bool{}
	for _, id := range ids {
		if id == "" {
			return fmt.Errorf("not asserted: a pairing this check needs was never named")
		}
		want[id] = true
	}
	var live []string
	for _, p := range peers {
		if p.Sibling || p.RetiredAt != 0 {
			continue
		}
		live = append(live, p.ID)
	}
	if len(live) != len(want) {
		return fmt.Errorf("%s holds %d live pairing(s) with other people, want %d: %v", r.name, len(live), len(want), shortAll(live))
	}
	for _, id := range live {
		if !want[id] {
			return fmt.Errorf("%s holds a live pairing %s this run never made (live: %v)", r.name, short(id), shortAll(live))
		}
	}
	return nil
}

func shortAll(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = short(id)
	}
	return out
}
