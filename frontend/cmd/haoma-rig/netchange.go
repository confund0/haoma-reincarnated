package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
)

const (
	netChangeKickedLine   = "network change: kicked"
	netChangeTorReset     = "network change: tor network reset"
	netChangeTorResetFail = "network change: tor network reset failed"
	netChangeLivenessDown = "tor network liveness: down"
	netChangeSourceTor    = "tor"
	netChangeSourceOS     = "os"
	peerKickResetLine     = "outbox next attempt reset"
	fetchFailedTransient  = "fetch failed transient"
	fetchBeginLine        = "fetch begin"
	fetchReadyHaomadLine  = "fetch ready"
	fileRetrySweepAPILine = "file retry sweep done"
)

const (
	netChangeBackoffLeft = 120 * time.Second

	netChangeBackoffWait = 6 * time.Minute

	netChangeTorWait = 30 * time.Second

	netChangeFetchTimeout = 3 * time.Minute

	netChangeSweepWait = 90 * time.Second

	netChangeSettle = 5 * time.Second

	netChangeGiveUp = "30m"

	netChangeGiveUpMargin = 5 * time.Minute

	netChangeWindow = 60 * time.Second

	netChangeInboundTimeout = 5 * time.Minute

	giveUpOverrideLine = "outbox give-up window overridden"
)

type rigTorControl struct {
	c  net.Conn
	br *bufio.Reader
}

func dialRigTor(r *rig) (*rigTorControl, error) {
	if !r.ownTor() {
		return nil, errors.New("this rig uses a shared tor (-system-tor); its network is not this group's to switch off")
	}
	return dialTorControl(r.torDir)
}

func dialTorControl(torDir string) (*rigTorControl, error) {
	raw, err := os.ReadFile(filepath.Join(torDir, "control.port"))
	if err != nil {
		return nil, fmt.Errorf("read tor's control port file: %w", err)
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(raw)), "\n")
	addr := strings.TrimPrefix(strings.TrimSpace(first), "PORT=")
	cookie, err := os.ReadFile(filepath.Join(torDir, "control.cookie"))
	if err != nil {
		return nil, fmt.Errorf("read tor's cookie: %w", err)
	}
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial tor's control port: %w", err)
	}
	t := &rigTorControl{c: c, br: bufio.NewReader(c)}
	if _, err := t.cmd("AUTHENTICATE " + hex.EncodeToString(cookie)); err != nil {
		c.Close()
		return nil, err
	}
	return t, nil
}

func (t *rigTorControl) close() { _ = t.c.Close() }

func (t *rigTorControl) cmd(line string) ([]string, error) {
	_ = t.c.SetDeadline(time.Now().Add(30 * time.Second))
	if _, err := fmt.Fprintf(t.c, "%s\r\n", line); err != nil {
		return nil, err
	}
	verb, _, _ := strings.Cut(line, " ")
	var out []string
	for {
		s, err := t.br.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("tor %s: %w", verb, err)
		}
		s = strings.TrimRight(s, "\r\n")
		if len(s) < 4 {
			return nil, fmt.Errorf("tor %s: short reply %q", verb, s)
		}
		if s[:3] != "250" {
			return nil, fmt.Errorf("tor %s: %s", verb, s)
		}
		out = append(out, s[4:])
		switch s[3] {
		case ' ':
			return out, nil
		case '+':
			for {
				d, err := t.br.ReadString('\n')
				if err != nil {
					return nil, fmt.Errorf("tor %s: %w", verb, err)
				}
				d = strings.TrimRight(d, "\r\n")
				if d == "." {
					break
				}
				out = append(out, d)
			}
		}
	}
}

func (t *rigTorControl) socksListener() (string, error) {
	lines, err := t.cmd("GETINFO net/listeners/socks")
	if err != nil {
		return "", err
	}
	for _, l := range lines {
		if v, ok := strings.CutPrefix(l, "net/listeners/socks="); ok {
			v = strings.TrimSpace(v)
			if f := strings.Fields(v); len(f) > 0 {
				v = f[0]
			}
			return strings.Trim(v, `"`), nil
		}
	}
	return "", fmt.Errorf("no net/listeners/socks in %q", lines)
}

type netChangeCheck struct {
	name string
	err  error
}

type netChangeObs struct {
	lines       []string
	fetchMsgID  string
	reenabledAt time.Time
	textNextAt  time.Time
	textHeldAt  time.Time
	source      string
	blobPulled  bool
	socksBefore string
	socksAfter  string
	inSentAt    time.Time
	inAckedAt   time.Time
}

type netChangeRun struct {
	line   string
	at     time.Time
	idx    int
	source string
}

func netChangeVerdict(o netChangeObs) error {
	var problems []string
	idField := logMatchID("msg_id", o.fetchMsgID)
	var (
		runs           []netChangeRun
		osRun          *netChangeRun
		osRuns         int
		torRuns        int
		failedBefore   bool
		readyAfter     bool
		sweepsAfter    int
		kicksBeforeRun int
		reset          netChangeRun
		resets         int
		resetFails     int
	)
	for i, line := range o.lines {
		at := lineTime(line)
		after := !at.IsZero() && !at.Before(o.reenabledAt)
		switch {
		case hasLogMsg(line, netChangeKickedLine) && after:
			runs = append(runs, netChangeRun{line: line, at: at, idx: i, source: lineField(line, "source=")})
		case hasLogMsg(line, netChangeTorReset) && after:
			resets++
			if reset.line == "" {
				reset = netChangeRun{line: line, at: at, idx: i}
			}
		case hasLogMsg(line, netChangeTorResetFail) && after:
			resetFails++
		case hasLogMsg(line, fetchFailedTransient) && strings.Contains(line, idField) && !after:
			failedBefore = true
		case hasLogMsg(line, fileRetrySweepAPILine) && after:
			sweepsAfter++
		}
	}
	for i := range runs {
		switch runs[i].source {
		case netChangeSourceOS:
			osRuns++
			if osRun == nil {
				osRun = &runs[i]
			}
		case netChangeSourceTor:
			torRuns++
		}
	}
	var first netChangeRun
	if len(runs) > 0 {
		first = runs[0]
	}
	for _, line := range o.lines {
		at := lineTime(line)
		if hasLogMsg(line, fetchReadyHaomadLine) && strings.Contains(line, idField) && first.line != "" && at.After(first.at) {
			readyAfter = true
		}
		if hasLogMsg(line, peerKickResetLine) && !at.Before(o.reenabledAt) && (first.line == "" || at.Before(first.at)) {
			if n, err := strconv.Atoi(lineField(line, "rows_moved=")); err == nil && n > 0 {
				kicksBeforeRun++
			}
		}
	}

	switch {
	case first.line == "":
		problems = append(problems, "dev-a logged no network change handler run after the network came back")
	default:
		if first.source != o.source {
			problems = append(problems, fmt.Sprintf("the handler ran with source=%q, want %q", first.source, o.source))
		}
		if n, _ := strconv.Atoi(lineField(first.line, "rows=")); n < 1 {
			problems = append(problems, "the handler reset no outbox row; the text was not waiting on its backoff, so nothing was measured")
		}
		if n, _ := strconv.Atoi(lineField(first.line, "fetches=")); n < 1 {
			problems = append(problems, "the handler retried no fetch")
		}
		if !first.at.Before(o.textNextAt) {
			problems = append(problems, "the handler ran after the text's own next attempt, so the backoff alone could explain the delivery")
		}
	}

	if osRuns > 1 {
		problems = append(problems, fmt.Sprintf("dev-a ran the handler with source=os %d times after the network came back, want 1", osRuns))
	}
	switch {
	case o.source == netChangeSourceTor && torRuns != 1:
		problems = append(problems, fmt.Sprintf("dev-a ran the handler with source=tor %d times, want 1", torRuns))
	case o.source == netChangeSourceOS && torRuns > 1:
		problems = append(problems, fmt.Sprintf("dev-a ran the handler with source=tor %d times after the os run, want at most 1 (the 60 s window)", torRuns))
	}
	if kicksBeforeRun > 0 {
		problems = append(problems, fmt.Sprintf("%d other reset(s) moved outbox rows between the network coming back and the handler; the group cannot credit the handler with the text", kicksBeforeRun))
	}
	switch {
	case o.textHeldAt.IsZero():
		problems = append(problems, "the peer never held dev-a's text")
	case !o.textHeldAt.Before(o.textNextAt):
		problems = append(problems, fmt.Sprintf("the peer held the text %s after the row's own next attempt: the backoff sent it, not the reset",
			o.textHeldAt.Sub(o.textNextAt).Round(time.Second)))
	case first.line != "" && o.textHeldAt.Before(first.at):
		problems = append(problems, "the peer held the text before the handler ran: something else sent it")
	}
	if !failedBefore {
		problems = append(problems, "the fetch never failed while the network was off, so its retry measured nothing")
	}
	if !readyAfter {
		problems = append(problems, "haomad did not pull the blob after the handler")
	}
	if sweepsAfter > 0 {
		problems = append(problems, fmt.Sprintf("%d retry sweep(s) through POST /files/retry-failed ran after the network came back; the fetch may be theirs", sweepsAfter))
	}
	if !o.blobPulled {
		problems = append(problems, "haoma does not hold the file")
	}

	if resetFails > 0 {
		problems = append(problems, fmt.Sprintf("haomad logged %d failed tor network reset(s) after the network came back", resetFails))
	}
	if osRun == nil {
		problems = append(problems, "not asserted: dev-a logged no source=os handler run after the network came back, so the tor network reset was not checked")
	} else {
		problems = append(problems, netChangeResetProblems(o, osRun.idx, first, reset.line, reset.idx, resets)...)

		switch {
		case o.inSentAt.IsZero():
			problems = append(problems, "the peer sent dev-a no text after the reset, so dev-a's onion after it was not tried")
		case o.inSentAt.Before(osRun.at) || o.inSentAt.Before(reset.at):
			problems = append(problems, "the peer's text to dev-a was sent before the source=os run or its reset, so it says nothing about the onion after them")
		case o.inAckedAt.IsZero():
			problems = append(problems, "the peer's text to dev-a got no sent_ack from dev-a's onion after the reset")
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("network change: %s", strings.Join(problems, "; "))
	}
	return nil
}

func netChangeResetProblems(o netChangeObs, osIdx int, first netChangeRun, reset string, resetIdx, resets int) []string {
	var problems []string
	switch {
	case reset == "":
		return append(problems, "haomad logged no tor network reset on the source=os run")
	case resets > 1:
		problems = append(problems, fmt.Sprintf("haomad reset tor's network %d times after the network came back, want 1", resets))
	}
	if resetIdx > osIdx {
		problems = append(problems, "haomad reset tor's network after the kick; the kicked rows dialled the old connections")
	}
	if first.source == netChangeSourceTor && resetIdx < first.idx {
		problems = append(problems, "haomad reset tor's network before the source=tor run; only source=os resets")
	}
	if got := lineField(reset, "socks_moved="); got != "false" {
		problems = append(problems, fmt.Sprintf("the reset logged socks_moved=%q: haomad's SOCKS pin did not hold", got))
	}
	switch {
	case o.socksAfter == "":
		problems = append(problems, "the group could not read tor's SOCKS listener after the reset")
	case o.socksAfter != o.socksBefore:
		problems = append(problems, fmt.Sprintf("tor's SOCKS listener moved from %s to %s over haomad's reset: its pin did not hold", o.socksBefore, o.socksAfter))
	}
	return problems
}

func fetchSettledFailed(lines []string, msgID string) bool {
	idField := logMatchID("msg_id", msgID)
	settled := false
	for _, line := range lines {
		if !strings.Contains(line, idField) {
			continue
		}
		switch {
		case hasLogMsg(line, fetchFailedTransient):
			settled = true
		case hasLogMsg(line, fetchBeginLine), hasLogMsg(line, fetchReadyHaomadLine):
			settled = false
		}
	}
	return settled
}

func awaitNetChangeLine(r *rig, mark logMark, msg, want string, timeout time.Duration) ([]string, error) {
	deadline := time.Now().Add(timeout)
	for {
		lines, err := mark.lines()
		if err != nil {
			return nil, err
		}
		for _, line := range lines {
			if hasLogMsg(line, msg) && strings.Contains(line, want) {
				return lines, nil
			}
		}
		if time.Now().After(deadline) {
			return lines, fmt.Errorf("%s's log has no %q line after %s", r.name, msg, timeout)
		}
		time.Sleep(time.Second)
	}
}

func assertNetChange(devA, peer *rig, aSeesPeer, peerSeesA string) []netChangeCheck {
	const verdictName = "network change: the text and the fetch go again with no manual retry"
	fail := func(err error) []netChangeCheck { return []netChangeCheck{{verdictName, err}} }
	if err := devicesAtDebugLevel(devA, peer); err != nil {
		return fail(fmt.Errorf("not asserted: %w", err))
	}
	if !devA.ownTor() {
		return fail(errors.New("not asserted: -system-tor shares one tor between the rigs; this group switches dev-a's own tor off"))
	}

	giveUpMark := haomadLogMark(devA)
	savedGiveUp := devA.giveUp
	logf("[%s] starting haomad again with the outbox give-up window at %s (was %q)", devA.name, netChangeGiveUp, savedGiveUp)
	if err := restartHaomadWith(devA, func(r *rig) { r.giveUp = netChangeGiveUp }); err != nil {
		return fail(fmt.Errorf("start dev-a's haomad with a %s give-up window: %w", netChangeGiveUp, err))
	}
	defer func() {
		logf("[%s] starting haomad again with its give-up window %q", devA.name, savedGiveUp)
		if err := restartHaomadWith(devA, func(r *rig) { r.giveUp = savedGiveUp }); err != nil {
			logf("[%s] COULD NOT PUT THE GIVE-UP WINDOW BACK: %v", devA.name, err)
		}
	}()
	giveUpLines, err := giveUpMark.lines()
	if err != nil {
		return fail(err)
	}
	giveUp, err := giveUpWindow(giveUpLines)
	if err != nil {
		return fail(fmt.Errorf("not asserted: %w", err))
	}
	api, err := backendClientFor(devA)
	if err != nil {
		return fail(err)
	}

	ctl, err := dialRigTor(devA)
	if err != nil {
		return fail(fmt.Errorf("dev-a's tor control: %w", err))
	}
	defer ctl.close()

	mark := haomadLogMark(devA)

	logf("[%s] stopping haoma; haomad keeps the file offer in its inbox", devA.name)
	if err := devA.stopHaoma(); err != nil {
		return fail(err)
	}
	networkOff := false
	defer func() {
		if networkOff {
			if err := networkBackOn(devA, ctl); err != nil {
				logf("[%s] COULD NOT PUT THE NETWORK BACK: %v", devA.name, err)
			}
		}
		devA.mu.Lock()
		down := devA.haoma == nil
		devA.mu.Unlock()
		if down {
			if err := devA.startHaoma(); err != nil {
				logf("[%s] COULD NOT START HAOMA AGAIN: %v", devA.name, err)
			}
		}
	}()

	file, err := sendFileNoWait(peer, peerSeesA, "netchange "+nextCorrID())
	if err != nil {
		return fail(fmt.Errorf("the peer's file to dev-a: %w", err))
	}
	envIDs, err := outboundEnvelopeIDs(peer, peerSeesA, file.msgID, syncConvergeTimeout)
	if err != nil {
		return fail(err)
	}
	envToA, err := enqueuedEnvelopeFor(peer, envIDs, peerSeesA)
	if err != nil {
		return fail(err)
	}
	if err := awaitInboxEnvelope(api, envToA, peerInboundTimeout); err != nil {
		return fail(fmt.Errorf("the file offer did not reach dev-a's haomad: %w", err))
	}

	socks, err := ctl.socksListener()
	if err != nil || socks == "" {
		return fail(fmt.Errorf("read dev-a's SOCKS listener: %q %v", socks, err))
	}
	if _, err := ctl.cmd("SETCONF SocksPort=" + socks); err != nil {
		return fail(fmt.Errorf("pin dev-a's SOCKS port: %w", err))
	}
	logf("[%s] tor: SOCKS pinned at %s; DisableNetwork=1", devA.name, socks)
	if _, err := ctl.cmd("SETCONF DisableNetwork=1"); err != nil {
		return fail(fmt.Errorf("switch dev-a's network off: %w", err))
	}
	networkOff = true

	sweepMark := haomadLogMark(devA)
	if err := devA.startHaoma(); err != nil {
		return fail(err)
	}

	textID, textEnv, err := sendTextNoWait(devA, aSeesPeer, "netchange "+devA.name+"->"+peer.name)
	if err != nil {
		return fail(fmt.Errorf("dev-a's text: %w", err))
	}
	if _, err := awaitNetChangeLine(devA, sweepMark, fileRetrySweepAPILine, "", netChangeSweepWait); err != nil {
		return fail(fmt.Errorf("not asserted: haoma's retry sweep at start did not run, so it could still run later: %w", err))
	}
	if _, err := awaitNetChangeLine(devA, mark, fetchFailedTransient, logMatchID("msg_id", file.msgID), netChangeSweepWait); err != nil {
		return fail(fmt.Errorf("measured nothing: %w", err))
	}
	nextAt, firstAt, err := awaitTextBackoff(api, textEnv, netChangeBackoffLeft, netChangeBackoffWait)
	if err != nil {
		return fail(fmt.Errorf("measured nothing: %w", err))
	}
	if err := giveUpLeftCheck(firstAt, giveUp, nextAt, netChangeGiveUpMargin); err != nil {
		return fail(fmt.Errorf("not asserted: %w", err))
	}
	time.Sleep(netChangeSettle)
	if lines, err := mark.lines(); err != nil || !fetchSettledFailed(lines, file.msgID) {
		return fail(fmt.Errorf("measured nothing: the fetch is not settled in failed_transient before the network comes back (%v)", err))
	}
	downSeen := false
	if lines, err := mark.lines(); err == nil {
		for _, l := range lines {
			if hasLogMsg(l, netChangeLivenessDown) {
				downSeen = true
			}
		}
	}

	peerSub, cancel := peer.subscribe()
	defer cancel()
	reenabledAt := time.Now()
	if _, err := ctl.cmd("SETCONF DisableNetwork=0"); err != nil {
		return fail(fmt.Errorf("switch dev-a's network on: %w", err))
	}
	networkOff = false
	if got, err := ctl.socksListener(); err != nil || got != socks {
		return fail(fmt.Errorf("not asserted: dev-a's tor listens for SOCKS at %q after the toggle, haomad dials %s (%v)", got, socks, err))
	}

	if err := unpinSocksPort(ctl, socks); err != nil {
		return fail(fmt.Errorf("not asserted: %w", err))
	}
	logf("[%s] tor: DisableNetwork=0, SocksPort auto again at %s; the text's own next attempt is %s away", devA.name, socks, time.Until(nextAt).Round(time.Second))

	source := netChangeSourceTor
	var trigger netChangeCheck
	var torRunAt time.Time
	if downSeen {
		if lines, err := awaitNetChangeLine(devA, mark, netChangeKickedLine, "source="+netChangeSourceTor, netChangeTorWait); err == nil {
			trigger = netChangeCheck{"network change trigger: tor NETWORK_LIVENESS (UP after DOWN)", nil}
			torRunAt = firstRunAt(lines, netChangeSourceTor, reenabledAt)
		}
	}
	var osCall netChangeCheck
	if trigger.name == "" {
		source = netChangeSourceOS
		why := "tor logged no NETWORK_LIVENESS DOWN while the network was off"
		if downSeen {
			why = fmt.Sprintf("tor logged DOWN, but no handler run with source=tor came in %s", netChangeTorWait)
		}
		logf("[%s] FALLBACK: %s; calling POST /network/changed", devA.name, why)
		trigger = netChangeCheck{"network change trigger: FALLBACK POST /network/changed (" + why + ")", postNetworkChanged(api)}
	}

	var heldAt time.Time
	if _, err := awaitFrame(peerSub, time.Until(nextAt)+30*time.Second, func(f ipc.Frame) bool {
		ev, ok := timelineEvent(f)
		return ok && ev.Direction == events.DirIn && ev.Kind == events.KindText && ev.MsgID == textID
	}); err == nil {
		heldAt = time.Now()
	}
	pulled := awaitContactFetch(devA, file.msgID, netChangeFetchTimeout) == nil

	if source == netChangeSourceTor {
		if wait := time.Until(torRunAt.Add(netChangeWindow + 2*time.Second)); wait > 0 {
			logf("[%s] waiting %s for haomad's window before the source=os call", devA.name, wait.Round(time.Second))
			time.Sleep(wait)
		}
		logf("[%s] calling POST /network/changed: the tor network reset is checked on every path", devA.name)
		osCall = netChangeCheck{"network change: POST /network/changed after the tor run (the tor network reset)", postNetworkChanged(api)}
	}
	socksAfter, err := ctl.socksListener()
	if err != nil {
		logf("[%s] could not read tor's SOCKS listener after the reset: %v", devA.name, err)
	}

	inSentAt, inAckedAt, inErr := peerTextReachesOnion(peer, devA, peerSeesA)
	if inErr != nil {
		logf("[%s->%s] the text after the reset: %v", peer.name, devA.name, inErr)
	}
	checks := []netChangeCheck{trigger}
	if osCall.name != "" {
		checks = append(checks, osCall)
	}
	lines, err := mark.lines()
	if err != nil {
		return append(checks, netChangeCheck{verdictName, err})
	}
	return append(checks, netChangeCheck{verdictName, netChangeVerdict(netChangeObs{
		lines:       lines,
		fetchMsgID:  file.msgID,
		reenabledAt: reenabledAt,
		textNextAt:  nextAt,
		textHeldAt:  heldAt,
		source:      source,
		blobPulled:  pulled,
		socksBefore: socks,
		socksAfter:  socksAfter,
		inSentAt:    inSentAt,
		inAckedAt:   inAckedAt,
	})})
}

func postNetworkChanged(api *backendapi.Client) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	res, err := api.NetworkChanged(ctx)
	cancel()
	switch {
	case err != nil:
		return err
	case res.Dropped:
		return errors.New("haomad dropped the call inside its window, so something ran the handler already")
	case res.ResetFailed:
		return errors.New("haomad answered reset_failed: its tor network reset failed")
	}
	return nil
}

func firstRunAt(lines []string, source string, since time.Time) time.Time {
	for _, l := range lines {
		at := lineTime(l)
		if hasLogMsg(l, netChangeKickedLine) && lineField(l, "source=") == source && !at.Before(since) {
			return at
		}
	}
	return time.Time{}
}

func unpinSocksPort(ctl *rigTorControl, socks string) error {
	if _, err := ctl.cmd("SETCONF SocksPort=auto"); err != nil {
		return fmt.Errorf("set SocksPort back to auto: %w", err)
	}
	conf, err := ctl.cmd("GETCONF SocksPort")
	if err != nil || len(conf) != 1 || conf[0] != "SocksPort=auto" {
		return fmt.Errorf("SocksPort after setting it to auto: %q %v", conf, err)
	}
	if got, err := ctl.socksListener(); err != nil || got != socks {
		return fmt.Errorf("SocksPort auto moved tor's SOCKS listener from %s to %q (%v)", socks, got, err)
	}
	return nil
}

func peerTextReachesOnion(peer, devA *rig, peerSeesA string) (sentAt, ackedAt time.Time, err error) {
	api, err := backendClientFor(peer)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	sentAt = time.Now()
	msgID, _, err := sendTextNoWait(peer, peerSeesA, "netchange after reset "+peer.name+"->"+devA.name)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("send: %w", err)
	}
	envIDs, err := outboundEnvelopeIDs(peer, peerSeesA, msgID, syncConvergeTimeout)
	if err != nil {
		return sentAt, time.Time{}, err
	}
	env, err := enqueuedEnvelopeFor(peer, envIDs, peerSeesA)
	if err != nil {
		return sentAt, time.Time{}, err
	}
	deadline := time.Now().Add(netChangeInboundTimeout)
	var last backendapi.OutboxRow
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		row, rerr := api.OutboxEntry(ctx, env)
		cancel()
		if rerr == nil {
			last = row
			switch row.State {
			case "sent", "delivered", "read":
				return sentAt, time.Unix(0, row.StateChangedAt), nil
			case "failed":
				return sentAt, time.Time{}, fmt.Errorf("the peer's envelope to dev-a failed: %s", row.LastError)
			}
		}
		if time.Now().After(deadline) {
			return sentAt, time.Time{}, fmt.Errorf("the peer's envelope to dev-a is %s after %s (attempts=%d, last error %q, %v)",
				last.State, netChangeInboundTimeout, last.Attempts, last.LastError, rerr)
		}
		time.Sleep(2 * time.Second)
	}
}

func networkBackOn(r *rig, ctl *rigTorControl) error {
	_, err := ctl.cmd("SETCONF DisableNetwork=0")
	if err == nil {
		return nil
	}
	logf("[%s] DisableNetwork=0 failed on the group's control connection (%v); trying a new one", r.name, err)
	fresh, derr := dialRigTor(r)
	if derr != nil {
		return fmt.Errorf("%v; a new control connection: %w", err, derr)
	}
	defer fresh.close()
	if _, rerr := fresh.cmd("SETCONF DisableNetwork=0"); rerr != nil {
		return fmt.Errorf("%v; on a new control connection: %w", err, rerr)
	}
	return nil
}

func awaitInboxEnvelope(api *backendapi.Client, envID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		res, err := api.Inbox(ctx, 0, 0)
		cancel()
		if err == nil {
			for _, e := range res.Entries {
				if e.Envelope.ID == envID {
					return nil
				}
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("envelope %s not in the inbox after %s (last error: %v)", short(envID), timeout, err)
		}
		time.Sleep(2 * time.Second)
	}
}

func awaitTextBackoff(api *backendapi.Client, envID string, left, timeout time.Duration) (next, first time.Time, err error) {
	deadline := time.Now().Add(timeout)
	var last backendapi.OutboxRow
	var lastErr error
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		row, err := api.OutboxEntry(ctx, envID)
		cancel()
		lastErr = err
		if err == nil {
			last = row
			next := time.Unix(0, row.NextAttemptAt)
			if row.State == "enqueued" && row.Attempts >= 1 && time.Until(next) >= left {
				return next, time.Unix(0, row.FirstAt), nil
			}
			if row.State != "enqueued" {
				return time.Time{}, time.Time{}, fmt.Errorf("the text's row went to %s after %d attempts (%s) before it waited %s on its backoff; a give-up window shorter than the group needs ends it so",
					row.State, row.Attempts, row.LastError, left)
			}
		}
		if time.Now().After(deadline) {
			return time.Time{}, time.Time{}, fmt.Errorf("the text's row did not wait %s on its backoff within %s (state=%s attempts=%d next in %s, last error: %v) — did the outbox attempt while the network was off?",
				left, timeout, last.State, last.Attempts, time.Until(time.Unix(0, last.NextAttemptAt)).Round(time.Second), lastErr)
		}
		time.Sleep(2 * time.Second)
	}
}

func giveUpWindow(lines []string) (time.Duration, error) {
	var found string
	for _, l := range lines {
		if hasLogMsg(l, giveUpOverrideLine) {
			found = lineField(l, "give_up_age=")
		}
	}
	if found == "" {
		return 0, fmt.Errorf("dev-a's haomad logged no %q line after the restart, so the group does not know its give-up window", giveUpOverrideLine)
	}
	d, err := time.ParseDuration(found)
	if err != nil {
		return 0, fmt.Errorf("give_up_age=%q: %w", found, err)
	}
	return d, nil
}

func giveUpLeftCheck(firstAt time.Time, giveUp time.Duration, nextAt time.Time, margin time.Duration) error {
	ends := firstAt.Add(giveUp)
	if need := nextAt.Add(margin); ends.Before(need) {
		return fmt.Errorf("the text's give-up window (%s) ends at %s, %s before the group needs it to (its next attempt %s plus %s)",
			giveUp, ends.Format(time.TimeOnly), need.Sub(ends).Round(time.Second), nextAt.Format(time.TimeOnly), margin)
	}
	return nil
}
