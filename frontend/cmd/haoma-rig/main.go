package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var rigNames = []string{"alice", "bob", "charlie", "dave", "erin", "frank", "grace", "heidi", "ivan", "judy"}

type config struct {
	n        int
	base     string
	bins     string
	build    bool
	wipe     bool
	keep     bool
	logLevel string
	torPass  string
	repoRoot string
	stamp    string
}

func main() {
	var cfg config
	flag.IntVar(&cfg.n, "n", 2, "number of rigs to launch (>=2)")
	flag.StringVar(&cfg.base, "base", "", "base directory for rig trees (default <repo>/tmp/rig)")
	flag.StringVar(&cfg.bins, "bins", "", "directory for built binaries (default <repo>/tmp/bins)")
	flag.BoolVar(&cfg.build, "build", true, "build haomad + haoma before launching")
	flag.BoolVar(&cfg.wipe, "wipe", true, "wipe each rig tree before provisioning")
	flag.BoolVar(&cfg.keep, "keep", false, "leave rigs running after asserts (until SIGINT) instead of tearing down")
	flag.StringVar(&cfg.logLevel, "log-level", "debug", "daemon log level: debug|info|warn|error")
	flag.Parse()

	if err := run(cfg); err != nil {
		logf("FATAL: %v", err)
		os.Exit(2)
	}
}

func run(cfg config) error {
	if cfg.n < 2 {
		return fmt.Errorf("-n must be >= 2 (got %d)", cfg.n)
	}
	cfg.torPass = os.Getenv("HAOMA_TOR_PASSWORD")
	if cfg.torPass == "" {
		return errors.New("$HAOMA_TOR_PASSWORD is unset — the harness needs the system Tor control password (see reference_dev_tor_password)")
	}

	root, err := findRepoRoot()
	if err != nil {
		return err
	}
	cfg.repoRoot = root
	cfg.stamp = time.Now().Format("20060102-150405")
	if cfg.base == "" {
		cfg.base = filepath.Join(root, "tmp", "rig")
	}
	if cfg.bins == "" {
		cfg.bins = filepath.Join(root, "tmp", "bins")
	}
	logf("repo root: %s", root)

	if cfg.build {
		if err := buildBinaries(cfg); err != nil {
			return err
		}
	}
	haomadBin := filepath.Join(cfg.bins, "haomad")
	haomaBin := filepath.Join(cfg.bins, "haoma")
	for _, b := range []string{haomadBin, haomaBin} {
		if _, err := os.Stat(b); err != nil {
			return fmt.Errorf("binary missing: %s (run with -build)", b)
		}
	}

	rigs := make([]*rig, 0, cfg.n)
	defer func() {
		if cfg.keep {
			return
		}
		for _, r := range rigs {
			r.teardown()
		}
	}()

	for i := 0; i < cfg.n; i++ {
		name := rigName(i)
		r := &rig{
			name:      name,
			root:      filepath.Join(cfg.base, name),
			bePass:    name + "-be-pw",
			fePass:    name + "-fe-pw",
			torPass:   cfg.torPass,
			logLevel:  cfg.logLevel,
			haomadBin: haomadBin,
			haomaBin:  haomaBin,
		}
		logf("[%s] provisioning %s", name, r.root)
		if err := r.provision(cfg.wipe); err != nil {
			return fmt.Errorf("provision %s: %w", name, err)
		}
		logf("[%s] launching haomad …", name)
		if err := r.launchHaomad(); err != nil {
			return fmt.Errorf("launch haomad %s: %w", name, err)
		}
		logf("[%s] haomad ready on %s", name, r.haomadAddr)
		logf("[%s] launching haoma …", name)
		if err := r.launchHaoma(); err != nil {
			return fmt.Errorf("launch haoma %s: %w", name, err)
		}
		logf("[%s] haoma ready on %s", name, r.haomaAddr)
		if err := r.connect(); err != nil {
			return fmt.Errorf("connect %s: %w", name, err)
		}
		logf("[%s] IPC connected", name)
		rigs = append(rigs, r)
	}

	hub := rigs[0]
	results := make([]*edgeResult, 0, cfg.n-1)
	for i := 1; i < len(rigs); i++ {
		results = append(results, runEdge(hub, rigs[i]))
	}

	if cfg.keep {
		logf("rigs left running (-keep). Ctrl-C to tear down + review logs.")
		printAttach(rigs)
		waitForSignal()
	}
	for _, r := range rigs {
		r.teardown()
	}

	logChecks, trace := reviewLogs(cfg.base, cfg.stamp, rigs)
	ok := report(results, logChecks)
	if trace != "" {
		logf("merged debug trace: %s", trace)
	}

	if !ok {
		dst := preserveLogs(cfg.base, cfg.stamp, rigs)
		logf("logs preserved: %s", dst)
		return errors.New("one or more asserted flows or log checks failed")
	}
	logf("ALL FLOWS PASSED + LOGS CLEAN")
	return nil
}

func runEdge(hub, spoke *rig) *edgeResult {
	res := &edgeResult{a: hub.name, b: spoke.name}
	logf("[%s<->%s] pairing …", hub.name, spoke.name)
	hubSeesSpoke, spokeSeesHub, err := pairOnion(hub, spoke)
	res.add("pair", err)
	if err != nil {
		logf("[%s<->%s] PAIR FAILED: %v", hub.name, spoke.name, err)
		return res
	}
	logf("[%s<->%s] paired (hub sees spoke=%s, spoke sees hub=%s)", hub.name, spoke.name, short(hubSeesSpoke), short(spokeSeesHub))

	time.Sleep(settleDelay)

	d, err := deliverText(hub, spoke, hubSeesSpoke, fmt.Sprintf("ping %s->%s", hub.name, spoke.name))
	res.add(edge(hub, spoke)+" text", err)

	_, errBA := deliverText(spoke, hub, spokeSeesHub, fmt.Sprintf("pong %s->%s", spoke.name, hub.name))
	res.add(edge(spoke, hub)+" text", errBA)

	if err == nil {
		res.addBool("delivery", d.delivered)
		res.add("read", assertRead(hub, spoke, d))
		res.add("edit", assertEdit(hub, spoke, hubSeesSpoke, d.msgID, "edited "+d.msgID))
		res.add("reaction", assertReaction(hub, spoke, hubSeesSpoke, d.msgID, "👍"))

		dd, derr := deliverText(hub, spoke, hubSeesSpoke, fmt.Sprintf("delete-me %s->%s", hub.name, spoke.name))
		if derr != nil {
			res.add("delete", fmt.Errorf("setup text: %w", derr))
		} else {
			res.add("delete", assertDelete(hub, spoke, hubSeesSpoke, dd.msgID))
		}

		_, ferr := sendFile(hub, spoke, hubSeesSpoke, "")
		res.add("file", ferr)

		const wantCaption = "rig caption ☕"
		fc, fcErr := sendFile(hub, spoke, hubSeesSpoke, wantCaption)
		if fcErr == nil && fc.caption != wantCaption {
			fcErr = fmt.Errorf("caption mismatch: got %q want %q", fc.caption, wantCaption)
		}
		res.add("file+caption", fcErr)

		quoteText := fmt.Sprintf("quote-me %s->%s", hub.name, spoke.name)
		qt, qErr := deliverText(hub, spoke, hubSeesSpoke, quoteText)
		if qErr != nil {
			res.add("reply", fmt.Errorf("setup text: %w", qErr))
		} else {
			res.add("reply", assertReply(hub, spoke, hubSeesSpoke, qt.msgID, "re: "+qt.msgID, quoteText))
		}
	}
	for _, c := range res.checks {
		if !c.ok {
			logf("[%s<->%s] %s FAILED: %s", hub.name, spoke.name, c.name, c.err)
		}
	}
	return res
}

func edge(from, to *rig) string { return from.name + "->" + to.name }

type check struct {
	name string
	ok   bool
	err  string
}

type edgeResult struct {
	a, b   string
	checks []check
}

func (r *edgeResult) add(name string, err error) {
	c := check{name: name, ok: err == nil}
	if err != nil {
		c.err = err.Error()
	}
	r.checks = append(r.checks, c)
}

func (r *edgeResult) addBool(name string, ok bool) {
	c := check{name: name, ok: ok}
	if !ok {
		c.err = "not observed"
	}
	r.checks = append(r.checks, c)
}

func report(results []*edgeResult, logChecks []check) bool {
	logf("──────── RESULTS ────────")
	allOK := true
	printCheck := func(edge string, c check) {
		allOK = allOK && c.ok
		mark := "PASS"
		if !c.ok {
			mark = "FAIL"
		}
		detail := ""
		if c.err != "" {
			detail = "  " + c.err
		}
		logf("  %-4s %-11s %-16s%s", mark, edge, c.name, detail)
	}
	for _, r := range results {
		for _, c := range r.checks {
			printCheck(r.a+"<->"+r.b, c)
		}
	}
	for _, c := range logChecks {
		printCheck("logs", c)
	}
	logf("─────────────────────────")
	return allOK
}

func printAttach(rigs []*rig) {
	logf("rig endpoints (note: haoma-text can't attach to these — its manual mode was retired; inspect via logs / dumpstore):")
	for _, r := range rigs {
		logf("  %-8s haoma-ipc=%s  haomad-api=%s  dir=%s", r.name, r.haomaAddr, r.haomadAddr, r.root)
	}
}

func waitForSignal() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
}

func buildBinaries(cfg config) error {
	if err := os.MkdirAll(cfg.bins, 0o700); err != nil {
		return err
	}
	steps := []struct {
		dir, out, pkg string
	}{
		{filepath.Join(cfg.repoRoot, "backend"), filepath.Join(cfg.bins, "haomad"), "./cmd/haomad"},
		{filepath.Join(cfg.repoRoot, "frontend"), filepath.Join(cfg.bins, "haoma"), "./cmd/haoma"},
	}
	for _, s := range steps {
		logf("building %s → %s", s.pkg, s.out)
		cmd := exec.Command("go", "build", "-o", s.out, s.pkg)
		cmd.Dir = s.dir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("build %s: %w\n%s", s.pkg, err, out)
		}
	}
	return nil
}

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		_, e1 := os.Stat(filepath.Join(dir, "backend", "go.mod"))
		_, e2 := os.Stat(filepath.Join(dir, "frontend", "go.mod"))
		if e1 == nil && e2 == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("repo root not found (need backend/go.mod + frontend/go.mod above CWD)")
		}
		dir = parent
	}
}

func rigName(i int) string {
	if i < len(rigNames) {
		return rigNames[i]
	}
	return fmt.Sprintf("rig%d", i)
}

func short(peerID string) string {
	if len(peerID) <= 8 {
		return peerID
	}
	return peerID[:8]
}

var startWall = time.Now()

func logf(format string, args ...any) {
	el := time.Since(startWall).Truncate(time.Millisecond)
	fmt.Fprintf(os.Stdout, "[%8s] %s\n", el, fmt.Sprintf(format, args...))
}

func readReadyLine(r io.Reader, timeout time.Duration) (string, error) {
	type res struct {
		addr string
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			var rl struct {
				Status  string `json:"status"`
				APIAddr string `json:"api_addr"`
			}
			if json.Unmarshal(sc.Bytes(), &rl) == nil && rl.Status == "ready" {
				ch <- res{rl.APIAddr, nil}
				io.Copy(io.Discard, r)
				return
			}
		}
		if err := sc.Err(); err != nil {
			ch <- res{"", err}
			return
		}
		ch <- res{"", errors.New("stdout closed before ready line (daemon exited early — check its log)")}
	}()
	select {
	case r := <-ch:
		return r.addr, r.err
	case <-time.After(timeout):
		return "", fmt.Errorf("no ready line within %s", timeout)
	}
}

func childEnv(overrides map[string]string) []string {
	strip := map[string]bool{}
	for k := range overrides {
		strip[k] = true
	}
	out := make([]string, 0, len(os.Environ())+len(overrides))
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i >= 0 && strip[kv[:i]] {
			continue
		}
		out = append(out, kv)
	}
	for k, v := range overrides {
		out = append(out, k+"="+v)
	}
	return out
}
