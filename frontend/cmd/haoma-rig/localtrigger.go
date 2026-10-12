package main

import (
	"fmt"
	"strings"
	"time"

	"haoma-frontend/internal/events"
)

const (
	localTriggerFireTimeout = 3 * time.Minute

	localTriggerConvergeTimeout = 5 * time.Minute
)

const localTriggerMarker = "synctrigger: journal growth started a sweep"

var localTriggerVerdicts = []string{
	"local trigger fires on journal growth",
	"the trigger's own sweep converges the read axis",
}

func assertLocalTrigger(devA, devB, peer *rig, aSeesPeer, bSeesPeer, peerSeesA string) map[string]error {
	out := map[string]error{}
	failAll := func(err error) map[string]error {
		for _, n := range localTriggerVerdicts {
			out[n] = err
		}
		return out
	}

	if err := devicesAtDebugLevel(devA, devB, peer); err != nil {
		return failAll(err)
	}

	aChatID, err := chatIDForPeer(devA, aSeesPeer, syncConvergeTimeout)
	if err != nil {
		return failAll(fmt.Errorf("could not resolve %s's own chat with %s: %w", devA.name, peer.name, err))
	}

	bSub, bCancel := devB.subscribe()
	defer bCancel()

	d, err := deliverText(peer, devA, peerSeesA, "slice5 local trigger "+nextCorrID())
	if err != nil {
		return failAll(fmt.Errorf("peer send to %s: %w", devA.name, err))
	}
	msgID := d.msgID
	logf("[%s] local-trigger row msg_id=%s landed on %s — the same send fans out to %s", peer.name, short(msgID), devA.name, devB.name)

	if _, err := awaitInboundText(bSub, msgID, mirrorTimeout); err != nil {
		return failAll(fmt.Errorf("%s never received the row the same send delivered to %s — nothing here has a copy to converge: %w", devB.name, devA.name, err))
	}

	if rows, rErr := timelineRows(devB, bSeesPeer, syncConvergeTimeout); rErr == nil {
		if ev, held := rows[msgID]; held && ev.ReadAt > 0 {
			return failAll(fmt.Errorf("%s already shows msg_id=%s read before %s read anything — this gate no longer isolates the trigger", devB.name, short(msgID), devA.name))
		}
	}

	preFired, err := countLocalTriggerSweeps(devA)
	if err != nil {
		return failAll(err)
	}
	preObserved, err := totalInboundClaimObserved(devB, msgID)
	if err != nil {
		return failAll(err)
	}

	logf("[%s] reading the row — nothing else is driven from here on", devA.name)
	if _, err := deviceReadsRow(devA, aChatID, readTimeout); err != nil {
		return failAll(fmt.Errorf("mark-read on %s: %w", devA.name, err))
	}

	out["local trigger fires on journal growth"] = func() error {
		n, err := awaitCountGrew("local trigger sweeps on "+devA.name,
			preFired, localTriggerFireTimeout, func() (int, error) {
				return countLocalTriggerSweeps(devA)
			})
		if err != nil {
			return fmt.Errorf("%s never started a sweep of its own after its journal grew — the phone-in-pocket case is exactly this, and nothing starts convergence: %w", devA.name, err)
		}
		logf("[%s] the journal grew and the trigger swept: %d -> %d", devA.name, preFired, n)
		return nil
	}()

	out["the trigger's own sweep converges the read axis"] = func() error {
		if err := out["local trigger fires on journal growth"]; err != nil {
			return fmt.Errorf("not asserted: the trigger never fired, so anything that converged here came from somewhere else")
		}

		if _, err := awaitCountGrew("inbound row observed on "+devB.name,
			preObserved, localTriggerConvergeTimeout, func() (int, error) {
				return totalInboundClaimObserved(devB, msgID)
			}); err != nil {
			return fmt.Errorf("%s never observed msg_id=%s arriving in %s's answer — the trigger fired but its sweep carried nothing: %w",
				devB.name, short(msgID), devA.name, err)
		}

		if err := awaitRows(devB, bSeesPeer, localTriggerConvergeTimeout,
			"the trigger's own sweep converges the read axis",
			"devB never showed the row read, with no sync driven and no restart",
			[]string{msgID},
			func(rows map[string]events.Event) bool {
				ev, ok := rows[msgID]
				return ok && ev.ReadAt > 0
			}); err != nil {
			return err
		}

		n, cErr := journalTouchCount(devB, events.MutatorApplyInboundReadAt, msgID)
		if cErr != nil {
			return cErr
		}
		if n != 1 {
			return fmt.Errorf("%s journalled the inherited read time %d time(s) for msg_id=%s, want exactly 1", devB.name, n, short(msgID))
		}
		logf("[%s] the read crossed with nobody asking: read time merged (journal touches=%d)", devB.name, n)
		return nil
	}()

	return completeLocalTriggerVerdicts(out)
}

func countLocalTriggerSweeps(r *rig) (int, error) {
	lines, err := rigLogLines(r)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, line := range lines {
		if strings.Contains(line, localTriggerMarker) {
			n++
		}
	}
	return n, nil
}

func completeLocalTriggerVerdicts(out map[string]error) map[string]error {
	for _, n := range localTriggerVerdicts {
		if _, ok := out[n]; !ok {
			out[n] = fmt.Errorf("assertLocalTrigger: verdict %q was never set — bug in the driver itself", n)
		}
	}
	return out
}
