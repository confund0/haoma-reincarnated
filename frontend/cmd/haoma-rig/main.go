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

const (
	scenarioStar    = "star"
	scenarioSibling = "sibling"
	scenarioRestart = "restart"

	scenarioRendezvous = "rendezvous"

	scenarioRepair = "repair"

	scenarioRepairRestart = "repair-restart"

	scenarioRepairDevices = "repair-devices"

	scenarioRepairDevicesOld = "repair-devices-old"

	scenarioRepairDevicesBetween = "repair-devices-between"

	scenarioDestroy = "destroy"

	scenarioSelfErase = "self-erase"

	scenarioSelfEraseRefused = "self-erase-refused"

	scenarioCall = "call"

	scenarioNetProbe = "netprobe"
)

type config struct {
	n           int
	scenario    string
	base        string
	bins        string
	build       bool
	wipe        bool
	keep        bool
	keepAllLogs bool
	logLevel    string
	giveUp      string
	torPass     string
	torControl  string
	torSocks    string
	systemTor   bool
	torBin      string
	torDirs     string
	repoRoot    string
	stamp       string

	cases       string
	siblingPlan siblingPlan

	warmUp bool

	chutney      string
	chutneyTorrc string

	ignoreSiblingOffer bool

	skipEarlyReceipt bool

	ignoreEarlyReceipt bool

	ignoreFileRequest bool
}

const seedIgnoreSiblingOfferEnv = "HAOMA_FAULT_IGNORE_SIBLING_OFFER"

const seedSkipEarlyReceiptEnv = "HAOMA_FAULT_SKIP_EARLY_RECEIPT"

const seedIgnoreEarlyReceiptEnv = "HAOMA_FAULT_IGNORE_EARLY_RECEIPT"

const seedIgnoreFileRequestEnv = "HAOMA_FAULT_IGNORE_FILE_REQUEST"

const holdGapFillFetchEnv = "HAOMA_FAULT_HOLD_GAPFILL_FETCH"

const dropFirstDestroySaltEnv = "HAOMA_FAULT_DROP_FIRST_DESTROY_SALT"

const dropContactDeletePushEnv = "HAOMA_FAULT_DROP_CONTACT_DELETE_PUSH"

const gapOfferTTLEnv = "HAOMA_GAPFILL_OFFER_TTL"

func main() {
	var cfg config
	flag.IntVar(&cfg.n, "n", 2, "number of rigs to launch (>=2); ignored by -scenario sibling, which always uses 3")
	flag.StringVar(&cfg.scenario, "scenario", scenarioStar, "scenario: star (hub + spokes, peer flows) | sibling (two of my own devices + one peer, linked-device rail; -cases picks its groups) | restart (harness lifecycle self-check) | rendezvous (a long-lived invitation, dialled after its host restarted) | repair (an inherited contact is offered a pairing invitation) | repair-restart (the same, with the offering device's haoma stopped while the person answers) | repair-devices (a new device of mine pairs with both devices of a contact) | repair-devices-between (the same, the first invitation accepted before the second is offered) | repair-devices-old (old devices close the pairings their linked device shows missing) | destroy (one device erases another that is listening) | self-erase (a device removed while it was switched off erases itself on its return) | self-erase-refused (the same removal with a mistyped PIN, where nothing is erased) | call (calls between one device and a contact's two linked devices, on fake streamers) | netprobe (no rig: one tor behind a cut proxy; does tor send NETWORK_LIVENESS DOWN on the cut and UP after it)")
	flag.StringVar(&cfg.base, "base", "", "base directory for rig trees (default <repo>/tmp/rig)")
	flag.StringVar(&cfg.bins, "bins", "", "directory for built binaries (default <repo>/tmp/rig-bins)")
	flag.BoolVar(&cfg.build, "build", true, "build haomad + haoma before launching")
	flag.BoolVar(&cfg.wipe, "wipe", true, "wipe each rig tree before provisioning")
	flag.BoolVar(&cfg.keep, "keep", false, "leave rigs running after asserts (until SIGINT) instead of tearing down")
	flag.BoolVar(&cfg.keepAllLogs, "keep-all-logs", false, fmt.Sprintf("keep every run's log directory instead of pruning to the newest %d passing, %d failing and %d aborted runs (%d, %d and %d on their own counts for runs filtered with -cases)", keepPassingRuns, keepFailingRuns, keepAbortedRuns, keepFilteredPassingRuns, keepFilteredFailingRuns, keepFilteredAbortedRuns))
	flag.StringVar(&cfg.logLevel, "log-level", "debug", "daemon log level: debug|info|warn|error")
	flag.StringVar(&cfg.giveUp, "outbox-giveup", "90s", "shorten each rig's outbox dead-letter window (Go duration; empty = shipped default). The receipt-retry gate needs a real give-up, and the shipped window is two days.")
	flag.BoolVar(&cfg.systemTor, "system-tor", false, "every rig uses the shared system tor (9051/9050, password from $HAOMA_TOR_PASSWORD) instead of a tor of its own")
	flag.StringVar(&cfg.torBin, "tor-bin", defaultTorBin, "tor binary each rig's haomad starts (ignored with -system-tor)")
	flag.StringVar(&cfg.torDirs, "tor-dirs", "", "directory holding one tor data dir per rig, kept between runs (default <repo>/tmp/rig-tor, or <repo>/tmp/rig-tor-chutney with -chutney; ignored with -system-tor)")
	flag.StringVar(&cfg.chutney, "chutney", "", "nodes dir of a running Chutney network (e.g. <repo>/tmp/chutney-net/nodes): every rig's own tor joins that private Tor network instead of the public one")
	flag.StringVar(&cfg.torControl, "tor-control", "", "with -system-tor: tor control-port address for every rig (empty = the daemon's own default). Set both this and -tor-socks to run against a tor other than the system one.")
	flag.StringVar(&cfg.torSocks, "tor-socks", "", "with -system-tor: tor SOCKS address for every rig (empty = the daemon's own default). Set both this and -tor-control to run against a tor other than the system one.")
	flag.StringVar(&cfg.cases, "cases", "", "with -scenario sibling only: comma-separated groups to run after the setup, e.g. text,gapfill; the groups they need are added and the order is always the full run's (empty = every group; list or help = print the groups and what each needs, then exit; list works with any -scenario)")
	flag.BoolVar(&cfg.warmUp, "warmup", true, "with -cases: end the setup with a warm-up (a text each way on every pairing, a sync on each of my devices, then a wait until no sync sweep is armed); false turns it off, for debugging the setup itself. Ignored by the full run, which never warms up")
	flag.Parse()

	if isSiblingCasesList(cfg.cases) {
		printSiblingGroups(os.Stdout, siblingGroups)
		return
	}

	if err := run(cfg); err != nil {
		logf("FATAL: %v", err)
		os.Exit(2)
	}
}

func run(cfg config) error {
	switch cfg.scenario {
	case scenarioStar:
	case scenarioSibling:

		if cfg.n != siblingRigCount {
			logf("scenario %s uses exactly %d rigs — ignoring -n %d", cfg.scenario, siblingRigCount, cfg.n)
		}
		cfg.n = siblingRigCount
	case scenarioRestart:

		if cfg.n != restartRigCount {
			logf("scenario %s uses exactly %d rigs — ignoring -n %d", cfg.scenario, restartRigCount, cfg.n)
		}
		cfg.n = restartRigCount
	case scenarioRendezvous:

		if cfg.n != rendezvousRigCount {
			logf("scenario %s uses exactly %d rigs — ignoring -n %d", cfg.scenario, rendezvousRigCount, cfg.n)
		}
		cfg.n = rendezvousRigCount
	case scenarioRepair, scenarioRepairRestart:

		if cfg.n != repairRigCount {
			logf("scenario %s uses exactly %d rigs — ignoring -n %d", cfg.scenario, repairRigCount, cfg.n)
		}
		cfg.n = repairRigCount
	case scenarioRepairDevices, scenarioRepairDevicesOld, scenarioRepairDevicesBetween:

		if cfg.n != repairDevicesRigCount {
			logf("scenario %s uses exactly %d rigs — ignoring -n %d", cfg.scenario, repairDevicesRigCount, cfg.n)
		}
		cfg.n = repairDevicesRigCount
	case scenarioDestroy:

		if cfg.n != destroyRigCount {
			logf("scenario %s uses exactly %d rigs — ignoring -n %d", cfg.scenario, destroyRigCount, cfg.n)
		}
		cfg.n = destroyRigCount
	case scenarioSelfErase, scenarioSelfEraseRefused:

		if cfg.n != selfEraseRigCount {
			logf("scenario %s uses exactly %d rigs — ignoring -n %d", cfg.scenario, selfEraseRigCount, cfg.n)
		}
		cfg.n = selfEraseRigCount
	case scenarioCall:

		if cfg.n != callRigCount {
			logf("scenario %s uses exactly %d rigs — ignoring -n %d", cfg.scenario, callRigCount, cfg.n)
		}
		cfg.n = callRigCount
	case scenarioNetProbe:

		if cfg.systemTor {
			return fmt.Errorf("-scenario %s starts a tor of its own; it cannot be used with -system-tor", scenarioNetProbe)
		}
	default:
		return fmt.Errorf("unknown -scenario %q (want %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s or %s)",
			cfg.scenario, scenarioStar, scenarioSibling, scenarioRestart,
			scenarioRendezvous, scenarioRepair, scenarioRepairRestart,
			scenarioRepairDevices, scenarioRepairDevicesBetween, scenarioRepairDevicesOld, scenarioDestroy,
			scenarioSelfErase, scenarioSelfEraseRefused, scenarioCall, scenarioNetProbe)
	}
	if cfg.n < 2 {
		return fmt.Errorf("-n must be >= 2 (got %d)", cfg.n)
	}

	if err := resolveRunCases(&cfg); err != nil {
		return err
	}
	if cfg.scenario == scenarioSibling {
		logf("%s", cfg.siblingPlan.describe())
	}
	cfg.ignoreSiblingOffer = os.Getenv(seedIgnoreSiblingOfferEnv) == "1"
	if cfg.ignoreSiblingOffer {
		logf("SEEDED FAULT ON (%s=1): every rig ignores sibling offers — the sibling file gates for a file I sent MUST fail on this run", seedIgnoreSiblingOfferEnv)
	}
	cfg.skipEarlyReceipt = os.Getenv(seedSkipEarlyReceiptEnv) == "1"
	if cfg.skipEarlyReceipt {
		logf("SEEDED FAULT ON (%s=1): no receipt when a contact's offer arrives — the \"file key with the download\" checks MUST fail on this run, and the files must still arrive", seedSkipEarlyReceiptEnv)
	}
	cfg.ignoreEarlyReceipt = os.Getenv(seedIgnoreEarlyReceiptEnv) == "1"
	if cfg.ignoreEarlyReceipt {
		logf("SEEDED FAULT ON (%s=1): a sender gives the first receipt per file no key — the \"file key with the download\" checks MUST fail on this run, and the files must still arrive", seedIgnoreEarlyReceiptEnv)
	}
	cfg.ignoreFileRequest = os.Getenv(seedIgnoreFileRequestEnv) == "1"
	if cfg.ignoreFileRequest {
		logf("SEEDED FAULT ON (%s=1): every rig serves no file request — the sibling file gates on gap-fill MUST fail on this run", seedIgnoreFileRequestEnv)
	}
	if cfg.systemTor {
		cfg.torPass = os.Getenv("HAOMA_TOR_PASSWORD")
	}

	root, err := findRepoRoot()
	if err != nil {
		return err
	}
	cfg.repoRoot = root
	if err := resolveTorPaths(&cfg); err != nil {
		return err
	}
	torLine, err := checkTorConfig(&cfg)
	if err != nil {
		return err
	}
	logf("%s", torLine)
	if cfg.chutney != "" {
		if cfg.chutneyTorrc, err = chutneyTorrc(cfg.chutney); err != nil {
			return err
		}
		logf("tor: every rig's tor joins the private Chutney network in %s, not the public Tor network", cfg.chutney)
	}
	cfg.stamp = time.Now().Format("20060102-150405")
	if cfg.base == "" {
		cfg.base = filepath.Join(root, "tmp", "rig")
	}
	if cfg.bins == "" {

		cfg.bins = filepath.Join(root, "tmp", "rig-bins")
	}
	logf("repo root: %s", root)

	if cfg.scenario == scenarioNetProbe {

		return runNetProbe(cfg)
	}

	if cfg.build {
		if err := timePhase("build binaries", func() error { return buildBinaries(cfg) }); err != nil {
			return err
		}
	}
	haomadBin := filepath.Join(cfg.bins, "haomad")
	haomaBin := filepath.Join(cfg.bins, "haoma")
	for _, b := range []string{haomadBin, haomaBin} {
		if _, err := os.Stat(b); err != nil {
			return fmt.Errorf("binary missing: %s (run with -build)", b)
		}
		if err := checkFaultsBuilt(b); err != nil {
			return err
		}
	}

	streamerDir := ""
	if cfg.scenario == scenarioCall {
		streamerDir = filepath.Join(cfg.bins, fakeStreamerDir)
		if cfg.build {
			if err := timePhase("build binaries", func() error { return buildFakeStreamers(root, streamerDir) }); err != nil {
				return err
			}
		}
		if err := checkFakeStreamers(streamerDir); err != nil {
			return err
		}
	}

	rigs := make([]*rig, 0, cfg.n)

	evidence := &runEvidence{cfg: cfg, rigs: &rigs}
	defer evidence.keepIfAborted()

	defer func() {
		if cfg.keep {
			return
		}
		for _, r := range rigs {
			r.teardown()
		}
	}()

	for i := 0; i < cfg.n; i++ {
		name := rigNameFor(cfg, i)
		r := &rig{
			name:       name,
			root:       filepath.Join(cfg.base, name),
			bePass:     name + "-be-pw",
			fePass:     name + "-fe-pw",
			torPass:    cfg.torPass,
			torControl: cfg.torControl,
			torSocks:   cfg.torSocks,
			torBin:     cfg.torBin,
			torDir:     rigTorDir(cfg, name),
			base:       cfg.base,
			keep:       cfg.keep,
			logLevel:   cfg.logLevel,
			haomadBin:  haomadBin,
			haomaBin:   haomaBin,
			giveUp:     cfg.giveUp,

			chutneyTorrc: cfg.chutneyTorrc,
			streamerDir:  streamerDir,

			ignoreSiblingOffer: cfg.ignoreSiblingOffer,
			skipEarlyReceipt:   cfg.skipEarlyReceipt,
			ignoreEarlyReceipt: cfg.ignoreEarlyReceipt,
			ignoreFileRequest:  cfg.ignoreFileRequest,
		}
		logf("[%s] provisioning %s", name, r.root)
		if err := timePhase("provision a rig", func() error { return r.provision(cfg.wipe) }); err != nil {
			return fmt.Errorf("provision %s: %w", name, err)
		}

		rigs = append(rigs, r)
		logf("[%s] launching haomad …", name)

		if err := timePhase("launch haomad (tor bootstrap)", r.launchHaomad); err != nil {
			return fmt.Errorf("launch haomad %s: %w", name, err)
		}
		logf("[%s] haomad ready on %s", name, r.haomadAddr)
		logf("[%s] launching haoma …", name)
		if err := timePhase("launch haoma", r.launchHaoma); err != nil {
			return fmt.Errorf("launch haoma %s: %w", name, err)
		}
		logf("[%s] haoma ready on %s", name, r.haomaAddr)
		if err := timePhase("connect IPC", r.connect); err != nil {
			return fmt.Errorf("connect %s: %w", name, err)
		}
		logf("[%s] IPC connected", name)
	}

	var results []*pairingResult
	scenarioStart := time.Now()
	switch cfg.scenario {
	case scenarioSibling:

		results = append(results, runSiblingFleet(rigs[0], rigs[1], rigs[2], cfg.siblingPlan))
	case scenarioRestart:

		results = append(results, runRestartFleet(rigs[0], rigs[1]))
	case scenarioRendezvous:

		results = append(results, runRendezvousFleet(rigs[0], rigs[1]))
	case scenarioRepair:

		results = append(results, runRepairFleet(rigs[0], rigs[1], rigs[2], false))
	case scenarioRepairRestart:

		results = append(results, runRepairFleet(rigs[0], rigs[1], rigs[2], true))
	case scenarioRepairDevices:

		results = append(results, runRepairDevicesFleet(rigs[0], rigs[1], rigs[2], rigs[3], false))
	case scenarioRepairDevicesBetween:

		results = append(results, runRepairDevicesFleet(rigs[0], rigs[1], rigs[2], rigs[3], true))
	case scenarioRepairDevicesOld:

		results = append(results, runRepairDevicesOldFleet(rigs[0], rigs[1], rigs[2], rigs[3]))
	case scenarioDestroy:

		results = append(results, runDestroyFleet(rigs[0], rigs[1]))
	case scenarioSelfErase:

		results = append(results, runSelfEraseFleet(rigs[0], rigs[1]))
	case scenarioSelfEraseRefused:

		results = append(results, runSelfEraseRefusedFleet(rigs[0], rigs[1]))
	case scenarioCall:

		results = append(results, runCallFleet(rigs[0], rigs[1], rigs[2]))
	default:

		hub := rigs[0]
		results = make([]*pairingResult, 0, cfg.n-1)
		for i := 1; i < len(rigs); i++ {
			results = append(results, runPairing(hub, rigs[i]))
		}
	}

	markPhase("scenario "+cfg.scenario, scenarioStart)

	if cfg.keep {
		logf("rigs left running (-keep). Ctrl-C to tear down + review logs.")
		printAttach(rigs)
		waitForSignal()
	}
	teardownStart := time.Now()
	for _, r := range rigs {
		r.teardown()
	}
	markPhase("teardown", teardownStart)

	logChecks := reviewLogs(rigs)
	ok := report(results, logChecks)
	reportPhases(time.Since(startWall))
	outcome := runOutcomeFor(ok)
	if line := finalLine(cfg, outcome); line != "" {
		logf("%s", line)
	}

	evidence.keep(outcome)
	if !ok {
		return errors.New("one or more asserted flows or log checks failed")
	}
	return nil
}

func runPairing(hub, spoke *rig) *pairingResult {
	res := &pairingResult{a: hub.name, b: spoke.name}
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
	res.add(direction(hub, spoke)+" text", err)

	_, errBA := deliverText(spoke, hub, spokeSeesHub, fmt.Sprintf("pong %s->%s", spoke.name, hub.name))
	res.add(direction(spoke, hub)+" text", errBA)

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

		if fcErr == nil {
			res.add("file key with the download", fc.keyWithDownload)
		} else {
			res.add("file key with the download", fmt.Errorf("not asserted: %w", fcErr))
		}

		quoteText := fmt.Sprintf("quote-me %s->%s", hub.name, spoke.name)
		qt, qErr := deliverText(hub, spoke, hubSeesSpoke, quoteText)
		if qErr != nil {
			res.add("reply", fmt.Errorf("setup text: %w", qErr))
		} else {
			res.add("reply", assertReply(hub, spoke, hubSeesSpoke, qt.msgID, "re: "+qt.msgID, quoteText))
		}

		res.add("receipt retry", assertReceiptRetry(hub, spoke, hubSeesSpoke))
	}
	for _, c := range res.checks {
		if !c.ok {
			logf("[%s<->%s] %s FAILED: %s", hub.name, spoke.name, c.name, c.err)
		}
	}
	return res
}

func direction(from, to *rig) string { return from.name + "->" + to.name }

type check struct {
	name string
	ok   bool
	err  string
}

type pairingResult struct {
	a, b   string
	checks []check

	scope string
}

func (r *pairingResult) add(name string, err error) {
	c := check{name: name, ok: err == nil}
	if err != nil {
		c.err = err.Error()
	}
	r.checks = append(r.checks, c)
}

func (r *pairingResult) addBool(name string, ok bool) {
	c := check{name: name, ok: ok}
	if !ok {
		c.err = "not observed"
	}
	r.checks = append(r.checks, c)
}

func report(results []*pairingResult, logChecks []check) bool {
	logf("──────── RESULTS ────────")
	allOK := true
	printCheck := func(pairing string, c check) {
		allOK = allOK && c.ok
		mark := "PASS"
		if !c.ok {
			mark = "FAIL"
		}
		detail := ""
		if c.err != "" {
			detail = "  " + c.err
		}
		logf("  %-4s %-11s %-16s%s", mark, pairing, c.name, detail)
	}
	for _, r := range results {
		if r.scope != "" {
			logf("  %-4s %-11s %s", "RAN", r.a+"<->"+r.b, r.scope)
		}
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
		cmd := exec.Command("go", "build", "-tags", faultsBuildTag, "-o", s.out, s.pkg)
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

func rigTorDir(cfg config, name string) string {
	if cfg.systemTor {
		return ""
	}
	return torDirFor(cfg.torDirs, name)
}

func rigName(i int) string {
	if i < len(rigNames) {
		return rigNames[i]
	}
	return fmt.Sprintf("rig%d", i)
}

func rigNameFor(cfg config, i int) string {
	if cfg.scenario == scenarioSibling && i < len(siblingRigNames) {
		return siblingRigNames[i]
	}
	if (cfg.scenario == scenarioRepair || cfg.scenario == scenarioRepairRestart) && i < len(repairRigNames) {
		return repairRigNames[i]
	}
	if (cfg.scenario == scenarioRepairDevices || cfg.scenario == scenarioRepairDevicesOld ||
		cfg.scenario == scenarioRepairDevicesBetween) && i < len(repairDevicesRigNames) {
		return repairDevicesRigNames[i]
	}
	if cfg.scenario == scenarioDestroy && i < len(destroyRigNames) {
		return destroyRigNames[i]
	}
	if (cfg.scenario == scenarioSelfErase || cfg.scenario == scenarioSelfEraseRefused) &&
		i < len(selfEraseRigNames) {
		return selfEraseRigNames[i]
	}
	if cfg.scenario == scenarioCall && i < len(callRigNames) {
		return callRigNames[i]
	}
	return rigName(i)
}

func short(peerID string) string {
	if len(peerID) <= 8 {
		return peerID
	}
	return peerID[:8]
}

var startWall = time.Now()

var logOut io.Writer = os.Stdout

func logf(format string, args ...any) {
	el := time.Since(startWall).Truncate(time.Millisecond)
	fmt.Fprintf(logOut, "[%8s] %s\n", el, fmt.Sprintf(format, args...))
}

func readReadyLine(r io.Reader, timeout time.Duration) (string, error) {
	addr, _, err := readReadyLineWatch(r, timeout)
	return addr, err
}

func readReadyLineWatch(r io.Reader, timeout time.Duration) (string, <-chan struct{}, error) {
	type res struct {
		addr string
		err  error
	}
	ch := make(chan res, 1)
	ended := make(chan struct{})
	go func() {
		defer close(ended)
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
		return r.addr, ended, r.err
	case <-time.After(timeout):
		return "", ended, fmt.Errorf("no ready line within %s", timeout)
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
