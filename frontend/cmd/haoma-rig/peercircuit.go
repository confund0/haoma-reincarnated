package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
)

const stallPeerCircuitEnv = "HAOMA_FAULT_STALL_PEER_CIRCUIT"

const (
	stallFaultOnLine      = "seeded fault on: requests to one peer stall on the tag of its first answer"
	stallFaultArmedLine   = "seeded fault armed: requests to the peer under this tag stall from now on"
	stallFaultStalledLine = "seeded fault: a request to the peer stalled"
	stallFaultThroughLine = "seeded fault: a request to the peer under a new tag got through"
	peerCircuitRenewLine  = "automatic new tor circuit for peer"

	peerCircuitInboundProof = `proof="inbound envelope"`

	routeFailNoAnswer    = "no answer in 30 s"
	peerCircuitCutReason = `reason="` + routeFailNoAnswer + `"`

	resendOnNewCircuitLine = "outbox: sending again at once on a new circuit"
)

const (
	peerCircuitHoldTimeout = 75 * time.Second

	peerCircuitArmTimeout = 3 * time.Minute

	peerInboundTimeout = republishWait

	peerCircuitThroughWait = 30 * time.Second

	peerCircuitLineTimeout = 30 * time.Second
)

func haomadLogMark(r *rig) logMark { return markLog(filepath.Join(r.root, "haomad.log")) }

func awaitHaomadLine(r *rig, mark logMark, msg, id string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		lines, err := mark.lines()
		if err != nil {
			return err
		}
		if countPeerLines(lines, msg, id) > 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s's haomad log has no %q for peer %s after %s", r.name, msg, short(id), timeout)
		}
		time.Sleep(time.Second)
	}
}

func countPeerLines(lines []string, msg, id string) int {
	n := 0
	for _, line := range lines {
		if !hasLogMsg(line, msg) {
			continue
		}
		if id != "" && lineField(line, "peer_id=") != short(id) {
			continue
		}
		n++
	}
	return n
}

func renewalLine(lines []string, peerID string) string {
	for _, line := range lines {
		if hasLogMsg(line, peerCircuitRenewLine) && lineField(line, "peer_id=") == short(peerID) {
			return line
		}
	}
	return ""
}

func countEnvelopeLines(lines []string, msg, envID string) int {
	n := 0
	for _, line := range lines {
		if hasLogMsg(line, msg) && lineField(line, "envelope_id=") == short(envID) {
			n++
		}
	}
	return n
}

func peerCircuitVerdict(lines []string, peerID, envID string, held bool) error {
	var problems []string
	if countPeerLines(lines, stallFaultArmedLine, peerID) == 0 {
		problems = append(problems, "the fault never armed, so nothing was measured")
	}
	if countPeerLines(lines, stallFaultStalledLine, peerID) == 0 {
		problems = append(problems, "no request stalled, so the renewal had nothing to fix")
	}
	switch n := countPeerLines(lines, peerCircuitRenewLine, peerID); n {
	case 1:
		line := renewalLine(lines, peerID)
		if !strings.Contains(line, peerCircuitInboundProof) {
			problems = append(problems, "the renewal's proof was not the inbound envelope")
		}
		if !strings.Contains(line, peerCircuitCutReason) {
			problems = append(problems, "the renewal was not the 30 s cut's")
		}
	case 0:
		problems = append(problems, "dev-a logged no automatic new tor circuit for the peer")
	default:
		problems = append(problems, fmt.Sprintf("dev-a logged %d automatic new tor circuits for the peer, want 1", n))
	}
	if countPeerLines(lines, stallFaultThroughLine, peerID) == 0 {
		problems = append(problems, "no request under a new tag got through: the renewal did not move the tag")
	}
	if countEnvelopeLines(lines, resendOnNewCircuitLine, envID) == 0 {
		problems = append(problems, "the outbox did not send the text again at once on a new circuit")
	}
	if !held {
		problems = append(problems, fmt.Sprintf("the peer did not hold dev-a's text within %s", peerCircuitHoldTimeout))
	}
	if len(problems) > 0 {
		return fmt.Errorf("automatic new circuit: %s", strings.Join(problems, "; "))
	}
	return nil
}

func peerTextReaches(from, to *rig, toPeerID, text string, timeout time.Duration) (string, error) {
	sub, cancel := to.subscribe()
	defer cancel()
	msgID, _, err := sendTextNoWait(from, toPeerID, text)
	if err != nil {
		return "", err
	}
	f, err := awaitFrame(sub, timeout, func(f ipc.Frame) bool {
		ev, ok := timelineEvent(f)
		return ok && ev.Direction == events.DirIn && ev.Kind == events.KindText && ev.MsgID == msgID
	})
	if err != nil {
		return "", fmt.Errorf("msg_id=%s did not reach %s within %s: %w", short(msgID), to.name, timeout, err)
	}
	ev, _ := timelineEvent(f)
	return string(ev.ChatID), nil
}

func restartHaomadWith(r *rig, set func(*rig)) error {
	r.stopHaomad()
	set(r)
	return r.restartHaomad()
}

func assertPeerCircuit(devA, peer *rig, aSeesPeer, peerSeesA string) (err error) {
	if !strings.EqualFold(devA.logLevel, "debug") {
		return fmt.Errorf("dev-a runs at -log-level=%q; this group reads the outbox's DEBUG line", devA.logLevel)
	}
	mark := haomadLogMark(devA)
	logf("[%s] starting haomad again with %s set to the peer %s", devA.name, stallPeerCircuitEnv, short(aSeesPeer))
	defer func() {
		restore := haomadLogMark(devA)
		logf("[%s] starting haomad again without %s", devA.name, stallPeerCircuitEnv)
		rerr := restartHaomadWith(devA, func(r *rig) { r.stallPeerCircuit = "" })
		if rerr == nil {
			if lines, lerr := restore.lines(); lerr == nil && countPeerLines(lines, stallFaultOnLine, "") > 0 {
				rerr = fmt.Errorf("haomad started again with the fault still on")
			}
		}
		if rerr != nil {
			logf("[%s] COULD NOT TAKE %s OFF AGAIN: %v", devA.name, stallPeerCircuitEnv, rerr)
			if err == nil {
				err = fmt.Errorf("could not take %s off: %w", stallPeerCircuitEnv, rerr)
			}
		}
	}()
	if err := restartHaomadWith(devA, func(r *rig) { r.stallPeerCircuit = aSeesPeer }); err != nil {
		return fmt.Errorf("start dev-a's haomad with the fault: %w", err)
	}
	if err := awaitHaomadLine(devA, mark, stallFaultOnLine, aSeesPeer, peerCircuitLineTimeout); err != nil {
		return fmt.Errorf("the fault did not reach haomad: %w", err)
	}

	chatID, err := peerTextReaches(peer, devA, peerSeesA, fmt.Sprintf("peer-circuit %s->%s", peer.name, devA.name), peerInboundTimeout)
	if err != nil {
		return fmt.Errorf("the peer's text to dev-a: %w", err)
	}

	if err := devA.send(ipc.FrameMarkRead, ipc.MarkReadRequest{ChatID: chatID}); err != nil {
		return fmt.Errorf("mark read on dev-a: %w", err)
	}
	if err := awaitHaomadLine(devA, mark, stallFaultArmedLine, aSeesPeer, peerCircuitArmTimeout); err != nil {
		return fmt.Errorf("measured nothing: %w", err)
	}

	peerSub, cancel := peer.subscribe()
	defer cancel()
	msgID, envID, err := sendTextNoWait(devA, aSeesPeer, fmt.Sprintf("peer-circuit %s->%s through a stall", devA.name, peer.name))
	if err != nil {
		return fmt.Errorf("dev-a's text: %w", err)
	}
	_, werr := awaitFrame(peerSub, peerCircuitHoldTimeout, func(f ipc.Frame) bool {
		ev, ok := timelineEvent(f)
		return ok && ev.Direction == events.DirIn && ev.Kind == events.KindText && ev.MsgID == msgID
	})

	lines, err := peerCircuitLines(mark, aSeesPeer, peerCircuitThroughWait)
	if err != nil {
		return fmt.Errorf("read dev-a's haomad log: %w", err)
	}
	return peerCircuitVerdict(lines, aSeesPeer, envID, werr == nil)
}

func peerCircuitLines(mark logMark, peerID string, wait time.Duration) ([]string, error) {
	deadline := time.Now().Add(wait)
	for {
		lines, err := mark.lines()
		if err != nil {
			return nil, err
		}
		if countPeerLines(lines, stallFaultThroughLine, peerID) > 0 || !time.Now().Before(deadline) {
			return lines, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
}
