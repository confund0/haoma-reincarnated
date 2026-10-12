package main

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"haoma-frontend/internal/events"
	"haoma-frontend/internal/logid"
)

const crossHourOfferTTL = 90 * time.Second

const (
	gapOfferLifetimeLine = "gap-fill offer lifetime overridden"

	holdGapFillFetchOnLine = "seeded fault on: this device takes a sibling offer on gap-fill and fetches nothing for it, so such a file stays pending"

	ignoreSiblingOfferOnLine = "seeded fault on: this device ignores every sibling offer, so no file I send from my other device reaches it"

	gapFetchHeldLine = "sibling offer on gap-fill: taken; the seeded fault holds its fetch"

	siblingFetchAskLine = "sibling fetch: asking haomad for the blob"

	keptOfferDroppedLine = "file request: kept offer dropped after its hour"

	gapAskedAgainLine = "sibling offer on gap-fill: download failed; the file is asked for again"

	ipcListeningLine = "IPC listening (TLS)"
)

const (
	haomadFetchBeginLine  = "fetch begin"
	haomadFetchFailedLine = "fetch failed permanent"

	haomadBlobGone = "blob_gone"
)

const (
	firstGapOfferTimeout = syncConvergeTimeout + fileRequestSpacing + gapFillSendHops*(torSendStall+outboxRetryDelay)

	crossHourDropTimeout = crossHourOfferTTL + time.Minute + pathLineTimeout

	triggerLongestWait = 90 * time.Second

	crossHourRefetchTimeout = fetchAllowance + triggerLongestWait + gapFillSendHops*(torSendStall+outboxRetryDelay) + fetchAllowance
)

const (
	crossHourControl = "file across the hour control"
	crossHourHeld    = "file across the hour: the first offer is taken and not fetched"
	crossHourDropped = "file across the hour: the responder drops the blob after its hour"
	crossHourGone    = "file across the hour: the old fetch finds the blob gone and the file is asked for again"
	crossHourBytes   = "file across the hour: a new staging and the bytes on my other device"
)

var crossHourVerdicts = []string{crossHourControl, crossHourHeld, crossHourDropped, crossHourGone, crossHourBytes}

type namedCheck struct {
	name string
	err  error
}

func assertFileAcrossTheHour(devA, devB, peer *rig, aSeesPeer, bSeesPeer string) []namedCheck {
	got := map[string]error{}
	done := map[string]bool{}
	set := func(name string, err error) {
		got[name], done[name] = err, true
	}

	failRest := func(err error) {
		rest := fmt.Errorf("not asserted: %w", err)
		for _, n := range crossHourVerdicts {
			if !done[n] {
				set(n, err)
				err = rest
			}
		}
	}

	notAsserted := func(err error) {
		for _, n := range crossHourVerdicts {
			if !done[n] {
				set(n, fmt.Errorf("not asserted: %w", err))
			}
		}
	}
	collect := func() []namedCheck {
		out := make([]namedCheck, 0, len(crossHourVerdicts))
		for _, n := range crossHourVerdicts {
			if !done[n] {

				got[n] = fmt.Errorf("not reached: the case returned without a verdict here")
			}
			out = append(out, namedCheck{n, got[n]})
		}
		return out
	}

	if err := devicesAtDebugLevel(devA, devB); err != nil {
		notAsserted(err)
		return collect()
	}

	settings := newCrossHourSettings(devA, devB, relaunchHaoma)
	defer func() {
		if err := settings.restoreAll(); err != nil {
			logf("[%s<->%s] COULD NOT PUT BACK THE CASE'S SETTINGS: %v", devA.name, devB.name, err)
		}
	}()

	ttl := crossHourOfferTTL.String()
	logf("[%s] starting haoma again with %s=%s", devA.name, gapOfferTTLEnv, ttl)
	mark := haomaLogMark(devA)
	if err := settings.shortHourOnA(); err != nil {
		notAsserted(fmt.Errorf("could not start %s's haoma again: %w", devA.name, err))
		return collect()
	}
	if err := awaitStartLines(devA, mark, startLine{gapOfferLifetimeLine, "lifetime=" + ttl}); err != nil {
		notAsserted(err)
		return collect()
	}
	logf("[%s] starting haoma again with %s=1 and %s=1", devB.name, holdGapFillFetchEnv, seedIgnoreSiblingOfferEnv)
	mark = haomaLogMark(devB)
	if err := settings.holdOnB(); err != nil {
		notAsserted(fmt.Errorf("could not start %s's haoma again: %w", devB.name, err))
		return collect()
	}
	if err := awaitStartLines(devB, mark, startLine{holdGapFillFetchOnLine, ""}, startLine{ignoreSiblingOfferOnLine, ""}); err != nil {
		notAsserted(err)
		return collect()
	}

	servesFrom := haomaLogMark(devA)

	caption := "a file across the blob's hour " + nextCorrID()
	res, err := sendFile(devA, peer, aSeesPeer, caption)
	if err != nil {
		set(crossHourControl, fmt.Errorf("send from %s to the contact failed: %w", devA.name, err))
		notAsserted(fmt.Errorf("the control send did not land"))
		return collect()
	}
	res.caption = caption
	set(crossHourControl, nil)
	msgID := res.msgID
	wantMsgID := logid.Short(msgID)

	if err := awaitRowOn(devB, bSeesPeer, msgID, syncConvergeTimeout); err != nil {
		failRest(err)
		return collect()
	}
	logf("[%s] holds the row of msg_id=%s with no transfer; asking it to sync, so the walk's end asks %s for the file", devB.name, short(msgID), devA.name)
	if err := driveSync(devB, false); err != nil {

		logf("[%s] the sync verb failed (%v); waiting for the local trigger instead", devB.name, err)
	}
	held, err := awaitLogLine(devB, rigLogLines, "haoma", firstGapOfferTimeout,
		lineWant{gapFetchHeldLine, map[string]string{"msg_id": wantMsgID}})
	if err != nil {
		failRest(fmt.Errorf("%w. What the logs show: %s", err, gapFillPathSeen(devA, devB, msgID)))
		return collect()
	}
	first, _ := logFieldValue(held, " staging_id")
	if first == "" {
		failRest(fmt.Errorf("%s's %q line for msg_id=%s names no staging id", devB.name, gapFetchHeldLine, short(msgID)))
		return collect()
	}
	if _, err := awaitLogLine(devA, rigLogLines, "haoma", pathLineTimeout,
		lineWant{fileServedLine, map[string]string{"msg_id": wantMsgID, "staging_id": first, "outcome": "served"}}); err != nil {
		failRest(fmt.Errorf("%s took staging %s for msg_id=%s, but %s did not stage it for this file: %w", devB.name, first, short(msgID), devA.name, err))
		return collect()
	}
	if err := noFetchYet(devB, wantMsgID, first); err != nil {
		failRest(err)
		return collect()
	}
	logf("[%s] took the offer of msg_id=%s, staging %s, and fetched nothing", devB.name, short(msgID), first)
	set(crossHourHeld, nil)

	logf("[%s] waiting up to %s for its hour of %s to end and the blob to go", devA.name, crossHourDropTimeout, ttl)
	if _, err := awaitLogLine(devA, rigLogLines, "haoma", crossHourDropTimeout,
		lineWant{keptOfferDroppedLine, map[string]string{"msg_id": wantMsgID, "staging_id": first}}); err != nil {
		failRest(err)
		return collect()
	}
	if err := noFetchYet(devB, wantMsgID, first); err != nil {
		failRest(err)
		return collect()
	}
	logf("[%s] dropped the blob and the kept offer of msg_id=%s, staging %s", devA.name, short(msgID), first)
	set(crossHourDropped, nil)

	mark = haomaLogMark(devA)
	if err := settings.restoreA(); err != nil {
		failRest(fmt.Errorf("could not start %s's haoma again with the shipped hour: %w", devA.name, err))
		return collect()
	}
	if err := awaitStartedWithout(devA, mark, gapOfferLifetimeLine); err != nil {
		failRest(err)
		return collect()
	}

	mark = haomaLogMark(devB)
	if err := settings.restoreB(); err != nil {
		failRest(fmt.Errorf("could not start %s's haoma again without the fault: %w", devB.name, err))
		return collect()
	}
	gone := []string{holdGapFillFetchOnLine}
	if !settings.runIgnore {
		gone = append(gone, ignoreSiblingOfferOnLine)
	}
	if err := awaitStartedWithout(devB, mark, gone...); err != nil {
		failRest(err)
		return collect()
	}
	deadline := time.Now().Add(crossHourRefetchTimeout)
	left := func() time.Duration { return time.Until(deadline) }
	pathSeen := func(err error) error {
		return fmt.Errorf("%w. What the logs show: %s", err, gapFillPathSeen(devA, devB, msgID))
	}
	if _, err := awaitLogLine(devB, rigLogLines, "haoma", left(),
		lineWant{siblingFetchAskLine, map[string]string{"msg_id": wantMsgID, "staging_id": first}}); err != nil {
		failRest(fmt.Errorf("%s's backend-ready sweep did not ask haomad for the held offer: %w", devB.name, err))
		return collect()
	}
	failed, err := awaitLogLine(devB, daemonLogLines, "haomad", left(),
		lineWant{haomadFetchFailedLine, map[string]string{"msg_id": first, "reason": haomadBlobGone}})
	if err != nil {
		failRest(pathSeen(fmt.Errorf("%s's haomad did not fail the fetch of staging %s with reason=%s: %w", devB.name, first, haomadBlobGone, err)))
		return collect()
	}
	if !strings.Contains(failed, "peer 404") && !strings.Contains(failed, "peer 410") {
		failRest(fmt.Errorf("%s's haomad failed staging %s with reason=%s but names neither a 404 nor a 410: %s", devB.name, first, haomadBlobGone, failed))
		return collect()
	}
	if _, err := awaitLogLine(devB, rigLogLines, "haoma", left(),
		lineWant{gapAskedAgainLine, map[string]string{"msg_id": wantMsgID, "staging_id": first, "fail_reason": haomadBlobGone}}); err != nil {
		failRest(pathSeen(fmt.Errorf("%s did not clear the take and ask again after the blob was gone: %w", devB.name, err)))
		return collect()
	}
	logf("[%s] the fetch of staging %s found the blob gone; msg_id=%s is asked for again", devB.name, first, short(msgID))
	set(crossHourGone, nil)

	second, err := awaitStagedAgain(devA, servesFrom, wantMsgID, first, left())
	if err != nil {
		failRest(pathSeen(err))
		return collect()
	}
	logf("[%s] staged msg_id=%s again, staging %s (the first was %s)", devA.name, short(msgID), second, first)
	set(crossHourBytes, awaitFileOnSiblingWithin(devA, devB, bSeesPeer, res, sealRouteGapFill, events.DirOut, max(left(), pathLineTimeout)))
	return collect()
}

var startLineTimeout = pathLineTimeout

type startLine struct {
	msg, part string
}

func awaitStartLines(r *rig, mark logMark, want ...startLine) error {
	deadline := time.Now().Add(startLineTimeout)
	for {
		lines, err := mark.lines()
		if err != nil {
			return err
		}
		var missing []string
		for _, s := range want {
			if !hasStartLine(lines, s) {
				missing = append(missing, fmt.Sprintf("%q %s", s.msg, s.part))
			}
		}
		if len(missing) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s's haoma started again but its log has no %s since then, after %s — the setting did not reach the process",
				r.name, strings.Join(missing, ", "), startLineTimeout)
		}
		time.Sleep(time.Second)
	}
}

func awaitStartedWithout(r *rig, mark logMark, absent ...string) error {
	deadline := time.Now().Add(startLineTimeout)
	for {
		lines, err := mark.lines()
		if err != nil {
			return err
		}
		if hasStartLine(lines, startLine{ipcListeningLine, ""}) {
			for _, msg := range absent {
				if hasStartLine(lines, startLine{msg, ""}) {
					return fmt.Errorf("%s's haoma started again with a setting the case took off: its log has %q since the start", r.name, msg)
				}
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s's haoma started again but its log has no %q since then, after %s", r.name, ipcListeningLine, startLineTimeout)
		}
		time.Sleep(time.Second)
	}
}

func hasStartLine(lines []string, s startLine) bool {
	for _, line := range lines {
		if hasLogMsg(line, s.msg) && strings.Contains(line, s.part) {
			return true
		}
	}
	return false
}

func awaitRowOn(r *rig, peerID, msgID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		rows, err := timelineRows(r, peerID, syncConvergeTimeout)
		if err == nil {
			if _, ok := rows[msgID]; ok {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s never showed the row of msg_id=%s after %s (last read error: %v)", r.name, short(msgID), timeout, err)
		}
		time.Sleep(syncPollInterval)
	}
}

type lineWant struct {
	msg    string
	fields map[string]string
}

func (w lineWant) matches(line string) bool {
	if !hasLogMsg(line, w.msg) {
		return false
	}
	for k, v := range w.fields {
		if got, ok := logFieldValue(line, " "+k); !ok || got != v {
			return false
		}
	}
	return true
}

func (w lineWant) String() string {
	keys := make([]string, 0, len(w.fields))
	for k := range w.fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := strconv.Quote(w.msg)
	for _, k := range keys {
		out += " " + k + "=" + w.fields[k]
	}
	return out
}

func awaitLogLine(r *rig, read func(*rig) ([]string, error), which string, timeout time.Duration, want lineWant) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		lines, err := read(r)
		if err != nil {
			return "", err
		}
		for _, line := range lines {
			if want.matches(line) {
				return line, nil
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("%s's %s log has no %s line after %s", r.name, which, want, timeout)
		}
		time.Sleep(syncPollInterval)
	}
}

func noFetchYet(devB *rig, msgID, staging string) error {
	lines, err := rigLogLines(devB)
	if err != nil {
		return err
	}
	if n := countMatching(lines, lineWant{siblingFetchAskLine, map[string]string{"msg_id": msgID}}); n > 0 {
		return fmt.Errorf("%s asked haomad %d time(s) for a fetch of msg_id=%s while the seeded fault holds it — the hold did not hold", devB.name, n, msgID)
	}
	dlines, err := daemonLogLines(devB)
	if err != nil {
		return err
	}
	if n := countMatching(dlines, lineWant{haomadFetchBeginLine, map[string]string{"msg_id": staging}}); n > 0 {
		return fmt.Errorf("%s's haomad began %d fetch(es) of staging %s while the seeded fault holds it — something other than the take posted it", devB.name, n, staging)
	}
	return nil
}

func countMatching(lines []string, want lineWant) int {
	n := 0
	for _, line := range lines {
		if want.matches(line) {
			n++
		}
	}
	return n
}

func awaitStagedAgain(devA *rig, mark logMark, msgID, first string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		lines, err := mark.lines()
		if err != nil {
			return "", err
		}
		second, found, err := stagedAgain(lines, msgID, first)
		switch {
		case err != nil:
			return "", fmt.Errorf("%s: %w", devA.name, err)
		case found:
			return second, nil
		case time.Now().After(deadline):
			return "", fmt.Errorf("%s did not serve msg_id=%s again after it dropped staging %s, within %s", devA.name, msgID, first, timeout)
		}
		time.Sleep(syncPollInterval)
	}
}

func stagedAgain(lines []string, msgID, first string) (second string, found bool, err error) {
	dropped := false
	for _, line := range lines {
		if !dropped {
			dropped = lineWant{keptOfferDroppedLine, map[string]string{"msg_id": msgID, "staging_id": first}}.matches(line)
			continue
		}
		served := lineWant{fileServedLine, map[string]string{"msg_id": msgID}}
		if !served.matches(line) {
			continue
		}
		outcome, _ := logFieldValue(line, " outcome")
		staging, _ := logFieldValue(line, " staging_id")
		switch {
		case outcome != "served":
			return "", true, fmt.Errorf("after it dropped staging %s it served msg_id=%s with outcome=%s staging %s — the kept offer outlived its hour", first, msgID, outcome, staging)
		case staging == "" || staging == first:
			return "", true, fmt.Errorf("after it dropped staging %s it served msg_id=%s under staging %q, want a new one", first, msgID, staging)
		}
		return staging, true, nil
	}
	return "", false, nil
}

type relaunchFunc func(r *rig, set func(*rig)) error

func relaunchHaoma(r *rig, set func(*rig)) error { return r.relaunchHaomaWith(set) }

type crossHourSettings struct {
	devA, devB *rig
	relaunch   relaunchFunc
	runIgnore  bool
	aSet, bSet bool
}

func newCrossHourSettings(devA, devB *rig, relaunch relaunchFunc) *crossHourSettings {
	return &crossHourSettings{devA: devA, devB: devB, relaunch: relaunch, runIgnore: devB.ignoreSiblingOffer}
}

func (s *crossHourSettings) shortHourOnA() error {
	s.aSet = true
	return s.relaunch(s.devA, func(r *rig) { r.gapOfferTTL = crossHourOfferTTL.String() })
}

func (s *crossHourSettings) holdOnB() error {
	s.bSet = true
	return s.relaunch(s.devB, func(r *rig) {
		r.holdGapFillFetch = true
		r.ignoreSiblingOffer = true
	})
}

func (s *crossHourSettings) restoreA() error {
	if !s.aSet {
		return nil
	}
	logf("[%s] starting haoma again with the shipped hour", s.devA.name)
	if err := s.relaunch(s.devA, func(r *rig) { r.gapOfferTTL = "" }); err != nil {
		return err
	}
	s.aSet = false
	return nil
}

func (s *crossHourSettings) restoreB() error {
	if !s.bSet {
		return nil
	}
	logf("[%s] starting haoma again without the case's faults", s.devB.name)
	if err := s.relaunch(s.devB, func(r *rig) {
		r.holdGapFillFetch = false
		r.ignoreSiblingOffer = s.runIgnore
	}); err != nil {
		return err
	}
	s.bSet = false
	return nil
}

func (s *crossHourSettings) restoreAll() error {
	var errs []error
	if err := s.restoreB(); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", s.devB.name, err))
	}
	if err := s.restoreA(); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", s.devA.name, err))
	}
	return errors.Join(errs...)
}
