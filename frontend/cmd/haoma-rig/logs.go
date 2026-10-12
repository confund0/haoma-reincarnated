package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var denylist = []string{
	"no valid session",
	"no session record",
	"old counter",
	"panic",
	"decrypt failed",
	"corrupt",

	"mirror: unclassified wire kind",
	"mirror: rail not wired",
	"mirror: no plaintext wrapper",
	"mirror: plaintext wrapper did not parse",
	"mirror: rail key unavailable",
	"mirror: mirror body marshal failed",
	"mirror: peer meta read failed",
	"mirror: seal for sibling failed",
	"mirror: sibling send failed",

	"mirror: file offer has no sibling offer to send",
	"mirror: sibling offer could not be built",
	"mirror: sibling offer marshal failed",
	"mirror: sibling offer send failed",
	"sibling offer: drop",
	"sibling offer: refused",
	"sibling offer: not taken",
	"sibling offer: fetch not enqueued",
	"sibling fetch: the held key does not open the blob",
	"sibling fetch: pull or seal failed",

	"sibling offer on gap-fill: refused",

	"file request: no serving worker",
	"file request: row read failed",
	"file request: metadata read failed",
	"file request: the sealed copy does not open",
	"file request: staging failed",
	"file request: staging id not recorded",
	"file request: sibling offer not sent",
	"file request: sibling offer send unconfirmed",
	"file request: sibling check failed",
	"file request: kept offer read failed",
	"file request: kept offer not sent again",
	"file request: kept offers not read",
	"file request: kept offer's blob not dropped after its hour",
	"file request: kept offer not removed after its hour",
	"file request: blob of an unsent offer not dropped",
	"file request: staging id of an unsent offer not removed",

	"file request: wanted file not recorded",
	"wanted file not removed",
	"file request: wanted file check failed",
	"file request: wanted files not read",
	"file request: wanted set scan",
	"file request: downloads in flight not read",
	"file request: send failed",
	"file request: turn not recorded",
	"file request: ask counts not reset",
	"file request: failed gap-fill downloads not read",
	"sibling offer on gap-fill: not taken — ",
	"sibling offer on gap-fill: download failed; final",
	"sibling offer on gap-fill: download failed; the ask limit is reached",
	"sibling offer on gap-fill: not asked for again — ",
	"sibling offer on gap-fill: failure reason not recorded",
	"sibling fetch: metadata read failed; the failure is final",

	"rail: read linked-device set failed",
	"rail: sibling check failed",
	"rail: drop — no rail key",
	"rail: drop — envelope did not open",
	"rail: drop — mirrored wire kind is not applicable here",
	"rail: drop — no local conversation for this fingerprint",
	"rail: drop — mirrored wrapper will not parse",
	"rail: drop — mirror body unusable",
	"rail: drop — file request unusable",
	"rail: drop — sibling offer on gap-fill unusable",
	"rail: drop — applied-ledger read failed",
	"rail: mirrored mutation refused by the event log",
	"rail: persist mirrored",

	"call_summary msg id could not be made",
	"rail: drop — call summary unusable",
	"rail: drop — no local conversation for this call summary",
	"rail: call summary not written",
}

const benignOldCounter = "benign old-counter re-delivery"

const oldCounterPairWindow = 2 * time.Second

type deniedLine struct {
	sig    string
	tagged string
	at     time.Time
	peer   string
}

type logScan struct {
	errors      int
	warns       int
	errSamples  []string
	denySamples []string
	warnLines   []string
}

func (s logScan) fatal() bool { return s.errors > 0 || len(s.denySamples) > 0 }

func scanRigLogs(r *rig) logScan {
	var s logScan
	var held, cleared []deniedLine
	for _, name := range []string{"haomad.log", "haoma.log", "haomad.stderr", "haoma.stderr"} {
		f, err := os.Open(filepath.Join(r.root, name))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			tagged := name + ": " + trimTime(line)
			switch {
			case strings.Contains(line, "level=ERROR"):
				s.errors++
				if len(s.errSamples) < 8 {
					s.errSamples = append(s.errSamples, tagged)
				}
			case strings.Contains(line, "level=WARN"):
				s.warns++
				if len(s.warnLines) < 5 {
					s.warnLines = append(s.warnLines, tagged)
				}
			}
			low := strings.ToLower(line)
			if strings.Contains(line, benignOldCounter) {
				cleared = append(cleared, deniedLine{
					at:   lineTime(line),
					peer: lineField(line, "peer_id="),
				})
				continue
			}
			for _, sig := range denylist {
				if strings.Contains(low, sig) {
					held = append(held, deniedLine{
						sig:    sig,
						tagged: tagged,
						at:     lineTime(line),
						peer:   lineField(line, "peer="),
					})
					break
				}
			}
		}
		f.Close()
	}
	for _, d := range held {
		if d.sig == "old counter" && pairedWithBenign(d, cleared) {
			continue
		}
		if len(s.denySamples) < 8 {
			s.denySamples = append(s.denySamples, "["+d.sig+"] "+d.tagged)
		}
	}
	return s
}

func pairedWithBenign(d deniedLine, cleared []deniedLine) bool {
	if d.peer == "" || d.at.IsZero() {
		return false
	}
	for _, c := range cleared {
		if c.peer == "" || c.at.IsZero() {
			continue
		}
		if !strings.HasPrefix(d.peer, c.peer) {
			continue
		}
		gap := c.at.Sub(d.at)
		if gap < 0 {
			gap = -gap
		}
		if gap <= oldCounterPairWindow {
			return true
		}
	}
	return false
}

func lineField(line, key string) string {
	i := strings.Index(line, key)
	if i < 0 {
		return ""
	}
	v := line[i+len(key):]
	if j := strings.IndexAny(v, " \t"); j >= 0 {
		v = v[:j]
	}
	return strings.TrimSuffix(v, "…")
}

func lineTime(line string) time.Time {
	v := lineField(line, "time=")
	if v == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}
	}
	return t
}

func reviewLogs(rigs []*rig) (checks []check) {
	for _, r := range rigs {
		s := scanRigLogs(r)
		c := check{name: "logs:" + r.name, ok: !s.fatal()}
		switch {
		case s.fatal():
			c.err = fmt.Sprintf("%d ERROR, %d denylist hit — %s", s.errors, len(s.denySamples), firstSample(s))
		case s.warns > 0:
			c.err = fmt.Sprintf("%d WARN (non-fatal)", s.warns)
		}
		checks = append(checks, c)
		for _, w := range s.warnLines {
			logf("[%s] WARN: %s", r.name, w)
		}
		for _, e := range s.errSamples {
			logf("[%s] ERROR: %s", r.name, e)
		}
		for _, d := range s.denySamples {
			logf("[%s] DENYLIST: %s", r.name, d)
		}
	}
	return checks
}

func writeMergedTrace(rigs []*rig, path string) error {
	type entry struct{ ts, text string }
	var entries []entry
	for _, r := range rigs {
		for _, name := range []string{"haomad.log", "haoma.log"} {
			f, err := os.Open(filepath.Join(r.root, name))
			if err != nil {
				continue
			}
			src := strings.TrimSuffix(name, ".log")
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for sc.Scan() {
				t := sc.Text()
				entries = append(entries, entry{
					ts:   timeToken(t),
					text: fmt.Sprintf("%-12s %s", r.name+"/"+src, t),
				})
			}
			f.Close()
		}
	}

	sort.SliceStable(entries, func(i, j int) bool { return entries[i].ts < entries[j].ts })
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.text)
		b.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

var rigLogFiles = []string{
	"haomad.log", "haomad.log.prev",
	"haoma.log", "haoma.log.prev",
	"haomad.stderr", "haoma.stderr",
}

const (
	keepPassingRuns = 10
	keepFailingRuns = 30
	keepAbortedRuns = 30

	keepFilteredPassingRuns = 10
	keepFilteredFailingRuns = 30
	keepFilteredAbortedRuns = 30
)

type runOutcome int

const (
	runPassed runOutcome = iota
	runFailed
	runAborted
)

var runOutcomes = []runOutcome{runPassed, runFailed, runAborted}

func (o runOutcome) prefix() string {
	switch o {
	case runPassed:
		return "pass-"
	case runAborted:
		return "abort-"
	default:
		return "fail-"
	}
}

func runDirPrefix(o runOutcome, filtered bool) string {
	if filtered {
		return "filtered-" + o.prefix()
	}
	return o.prefix()
}

func keepRuns(o runOutcome, filtered bool) int {
	switch {
	case o == runPassed && filtered:
		return keepFilteredPassingRuns
	case o == runPassed:
		return keepPassingRuns
	case o == runAborted && filtered:
		return keepFilteredAbortedRuns
	case o == runAborted:
		return keepAbortedRuns
	case filtered:
		return keepFilteredFailingRuns
	default:
		return keepFailingRuns
	}
}

func (c config) filteredRun() bool {
	return c.scenario == scenarioSibling && c.siblingPlan.filtered()
}

func runOutcomeFor(ok bool) runOutcome {
	if ok {
		return runPassed
	}
	return runFailed
}

func runScope(cfg config) string {
	if cfg.scenario == scenarioSibling {
		return cfg.siblingPlan.describe()
	}
	return fmt.Sprintf("scenario %s: the whole scenario (it has no groups)", cfg.scenario)
}

const scopeFile = "scope.txt"

func finalLine(cfg config, o runOutcome) string {
	switch {
	case o != runPassed:
		return ""
	case cfg.filteredRun():
		return "ASKED GROUPS PASSED + LOGS CLEAN — " + cfg.siblingPlan.describe()
	default:
		return "ALL FLOWS PASSED + LOGS CLEAN"
	}
}

func keepRunLogs(cfg config, outcome runOutcome, rigs []*rig) string {
	dst := preserveRun(cfg.base, cfg.stamp, runDirPrefix(outcome, cfg.filteredRun()), rigs)
	scope := runScope(cfg) + "\noutcome: " + strings.TrimSuffix(outcome.prefix(), "-") + "\n"
	if err := os.MkdirAll(dst, 0o700); err != nil {
		logf("could not write %s: %v", filepath.Join(dst, scopeFile), err)
	} else if err := os.WriteFile(filepath.Join(dst, scopeFile), []byte(scope), 0o600); err != nil {
		logf("could not write %s: %v", filepath.Join(dst, scopeFile), err)
	}
	if !cfg.keepAllLogs {
		for _, o := range runOutcomes {
			for _, filtered := range []bool{false, true} {
				pruneRuns(cfg.base, runDirPrefix(o, filtered), keepRuns(o, filtered))
			}
		}
	}
	logf("run logs: %s", dst)
	return dst
}

type runEvidence struct {
	cfg  config
	rigs *[]*rig
	kept bool
}

func (e *runEvidence) keep(outcome runOutcome) string {
	if e.kept {
		return ""
	}
	e.kept = true
	return keepRunLogs(e.cfg, outcome, *e.rigs)
}

func (e *runEvidence) keepIfAborted() {
	if e.kept || len(*e.rigs) == 0 {
		return
	}
	e.keep(runAborted)
}

func preserveRun(base, stamp, prefix string, rigs []*rig) string {
	dst := filepath.Join(base, prefix+stamp)
	for _, r := range rigs {
		rd := filepath.Join(dst, r.name)
		if err := os.MkdirAll(rd, 0o700); err != nil {
			continue
		}
		for _, name := range rigLogFiles {
			copyFile(filepath.Join(r.root, name), filepath.Join(rd, name))
		}
		keepTorLog(r, rd)
	}

	if err := os.MkdirAll(dst, 0o700); err == nil {
		if err := writeMergedTrace(rigs, filepath.Join(dst, "trace.log")); err != nil {
			logf("merged trace write failed: %v", err)
		}
	}
	return dst
}

func pruneRuns(base, prefix string, keep int) {
	if keep < 1 {
		return
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	var dirs []string
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		if _, err := time.Parse("20060102-150405", strings.TrimPrefix(e.Name(), prefix)); err != nil {
			continue
		}
		dirs = append(dirs, e.Name())
	}
	if len(dirs) <= keep {
		return
	}
	sort.Strings(dirs)
	for _, name := range dirs[:len(dirs)-keep] {
		if err := os.RemoveAll(filepath.Join(base, name)); err != nil {
			logf("pruning %s failed: %v", name, err)
			continue
		}
		logf("pruned old run logs: %s", name)
	}
}

const keptTorLog = "tor-log.txt"

func keepTorLog(r *rig, rd string) {
	if !r.ownTor() || !r.torLogFromSet {
		return
	}
	in, err := openFrom(r.torLogPath(), r.torLogFrom)
	if err != nil {
		return
	}
	defer in.Close()
	out, err := os.Create(filepath.Join(rd, keptTorLog))
	if err != nil {
		return
	}
	defer out.Close()
	io.Copy(out, in)
}

func copyFile(src, dst string) {
	in, err := os.Open(src)
	if err != nil {
		return
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return
	}
	defer out.Close()
	io.Copy(out, in)
}

func trimTime(line string) string {
	if strings.HasPrefix(line, "time=") {
		if sp := strings.IndexByte(line, ' '); sp > 0 {
			return line[sp+1:]
		}
	}
	return line
}

func timeToken(line string) string {
	if strings.HasPrefix(line, "time=") {
		if sp := strings.IndexByte(line, ' '); sp > 5 {
			return line[5:sp]
		}
	}
	return ""
}

func firstSample(s logScan) string {
	if len(s.denySamples) > 0 {
		return s.denySamples[0]
	}
	if len(s.errSamples) > 0 {
		return s.errSamples[0]
	}
	return ""
}
