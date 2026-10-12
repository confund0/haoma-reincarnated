package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"haoma-frontend/internal/backendapi"
)

const (
	defaultTorBin = "/usr/bin/tor"

	torBootstrapTimeout = 5 * time.Minute

	torPollInterval = time.Second

	torStartSlack = 10 * time.Second
)

func torDirFor(dirs, name string) string { return filepath.Join(dirs, name) }

func prepareTorDir(label, dir, rigBase string) error {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	absBase, err := filepath.Abs(rigBase)
	if err != nil {
		return err
	}
	if isWithin(absDir, absBase) {
		return fmt.Errorf("tor data dir %s is inside the rig base %s, whose rig trees are wiped each run", absDir, absBase)
	}
	if err := os.MkdirAll(absDir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(absDir, 0o700); err != nil {
		return err
	}
	return endLeftoverTors(label, filepath.Join(absDir, "torrc"), 5*time.Second)
}

func (r *rig) rigBase() string {
	if r.base != "" {
		return r.base
	}
	return filepath.Dir(r.root)
}

func (r *rig) torLogPath() string {
	if !r.ownTor() {
		return ""
	}
	return filepath.Join(r.torDir, "tor.log")
}

func (r *rig) torStartFailure(logFrom, torLogFrom int64) string {
	haomadLine := lastMatchingLine(filepath.Join(r.root, "haomad.log"), logFrom, func(l string) bool {
		return strings.Contains(l, "tor exited") || strings.Contains(l, "embedded tor") || strings.Contains(l, `msg=fatal`)
	})
	if haomadLine == "" {
		haomadLine = "(no line about tor in haomad.log since this launch)"
	}
	tail := tailLines(r.torLogPath(), torLogFrom, torLogTailLines)
	if len(tail) == 0 {
		tail = []string{"(nothing in tor.log since this launch)"}
	}
	return fmt.Sprintf("haomad.log: %s; %s, last lines: %s", trimTime(haomadLine), r.torLogPath(), strings.Join(tail, " | "))
}

const torLogTailLines = 5

func openFrom(path string, from int64) (*os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if st, err := f.Stat(); err == nil && st.Size() >= from {
		if _, err := f.Seek(from, io.SeekStart); err != nil {
			f.Close()
			return nil, err
		}
	}
	return f, nil
}

func lastMatchingLine(path string, from int64, match func(string) bool) string {
	f, err := openFrom(path, from)
	if err != nil {
		return ""
	}
	defer f.Close()
	var last string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if match(sc.Text()) {
			last = sc.Text()
		}
	}
	return last
}

func tailLines(path string, from int64, n int) []string {
	if path == "" {
		return nil
	}
	f, err := openFrom(path, from)
	if err != nil {
		return nil
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if len(lines) > n {
			lines = lines[1:]
		}
	}
	return lines
}

func isWithin(path, dir string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func (r *rig) ownTor() bool { return r.torDir != "" }

func (r *rig) haomadArgs(apiAddr string) []string {
	args := []string{
		"--cfg-dir", r.root,
		"--api-addr", apiAddr,
		"--log-level", r.logLevel,
		"--log-file", filepath.Join(r.root, "haomad.log"),
	}
	switch {
	case r.ownTor():

		args = append(args, "--manage-tor", r.torBin, "--tor-data-dir", r.torDir)
	case r.torControl != "":
		args = append(args, "--tor-control", r.torControl, "--tor-socks", r.torSocks)
	}
	return args
}

func (r *rig) haomadEnv() map[string]string {
	env := map[string]string{
		"HAOMA_PASSPHRASE":   r.bePass,
		"HAOMA_TOR_PASSWORD": r.torPass,
	}
	if r.ownTor() {
		env["HAOMA_TOR_PASSWORD"] = ""
	}
	if r.giveUp != "" {
		env["HAOMA_OUTBOX_GIVEUP"] = r.giveUp
	}
	if r.clockOffset != "" {

		env["HAOMA_TOR_CLOCK_SKEW_OFFSET"] = r.clockOffset
	}

	env[stallPeerCircuitEnv] = r.stallPeerCircuit
	return env
}

type torReader interface {
	TorInfo(ctx context.Context) (backendapi.TorInfoResponse, error)
	SystemInfo(ctx context.Context) (backendapi.SystemInfo, error)
}

type torWait struct {
	api      torReader
	ownTor   bool
	launched time.Time
	exited   <-chan struct{}
	timeout  time.Duration
	interval time.Duration

	logPath string
	logFrom int64
	torLog  string
}

type torWaitResult struct {
	version string
	took    time.Duration
}

func (w torWait) run() (torWaitResult, error) {
	deadline := w.launched.Add(w.timeout)
	var last backendapi.TorHealth
	var lastErr error
	seen := false
	for {
		health, answered, ready, err := w.poll()
		if answered {
			last, seen = health, true
		}
		lastErr = err
		if ready != nil {
			return *ready, nil
		}
		var notOwn errNotOwnTor
		if errors.As(err, &notOwn) {
			return torWaitResult{}, err
		}
		select {
		case <-w.exited:
			return torWaitResult{}, fmt.Errorf("haomad exited while its tor was bootstrapping — %s", w.lastStep())
		default:
		}
		if !time.Now().Before(deadline) {
			return torWaitResult{}, errors.New(w.timeoutText(last, seen, lastErr))
		}
		time.Sleep(w.interval)
	}
}

type errNotOwnTor struct{ started string }

func (e errNotOwnTor) Error() string {
	return fmt.Sprintf("haomad talks to a tor started at %s, before this launch — not the rig's own tor", e.started)
}

func (w torWait) poll() (h backendapi.TorHealth, answered bool, ready *torWaitResult, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := w.api.TorInfo(ctx)
	if err != nil {
		return h, false, nil, err
	}
	h = info.Health
	if !h.Ready {
		return h, true, nil, nil
	}
	sys, sysErr := w.api.SystemInfo(ctx)
	if !w.ownTor {

		return h, true, &torWaitResult{version: sys.Tor.Version, took: time.Since(w.launched)}, nil
	}
	if sysErr != nil {
		return h, true, nil, sysErr
	}
	if sys.Tor.StartedAt == "" {

		return h, true, nil, errors.New("tor at 100%, but haomad's control connection does not name its tor yet")
	}
	started, err := time.Parse(time.RFC3339, sys.Tor.StartedAt)
	if err != nil {
		return h, true, nil, fmt.Errorf("tor start time %q: %w", sys.Tor.StartedAt, err)
	}
	if started.Before(w.launched.Add(-torStartSlack)) {
		return h, true, nil, errNotOwnTor{started: sys.Tor.StartedAt}
	}
	return h, true, &torWaitResult{version: sys.Tor.Version, took: time.Since(w.launched)}, nil
}

func (w torWait) timeoutText(last backendapi.TorHealth, seen bool, lastErr error) string {
	var b strings.Builder
	fmt.Fprintf(&b, "tor did not reach 100%% within %s", w.timeout)
	switch {
	case !seen:
		b.WriteString("; haomad never answered for its tor")
	case last.Unreachable:
		b.WriteString("; haomad cannot reach its tor's control port")
	default:
		fmt.Fprintf(&b, "; haomad's last reading: %d%%", last.Bootstrap)
	}
	if lastErr != nil {
		fmt.Fprintf(&b, " (last error: %v)", lastErr)
	}
	b.WriteString("; " + w.lastStep())
	if w.torLog != "" {
		b.WriteString("; tor's own log: " + w.torLog)
	}
	return b.String()
}

func (w torWait) lastStep() string {
	step, warn := lastBootstrapStep(w.logPath, w.logFrom)
	if step.tag == "" {
		return "no bootstrap step in haomad.log since this launch (the steps are logged at info)"
	}
	s := fmt.Sprintf("last bootstrap step: %d%% %s (%s)", step.progress, step.tag, step.summary)
	if warn.warning != "" {
		s += fmt.Sprintf("; last tor warning: %q, reason %s, at %s", warn.warning, warn.reason, warn.tag)
	}
	return s
}

type bootstrapLine struct {
	progress int
	tag      string
	summary  string
	warning  string
	reason   string
}

const (
	bootstrapPhaseMsg   = `msg="tor bootstrap phase"`
	bootstrapWarningMsg = `msg="tor bootstrap warning"`
)

func lastBootstrapStep(path string, from int64) (step, warn bootstrapLine) {
	f, err := openFrom(path, from)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		isStep := strings.Contains(line, bootstrapPhaseMsg)
		isWarn := strings.Contains(line, bootstrapWarningMsg)
		if !isStep && !isWarn {
			continue
		}
		l := bootstrapLine{
			tag:     slogValue(line, "tag"),
			summary: slogValue(line, "summary"),
			warning: slogValue(line, "warning"),
			reason:  slogValue(line, "reason"),
		}
		l.progress, _ = strconv.Atoi(slogValue(line, "bootstrap"))
		if isStep {
			step = l
		} else {
			warn = l
		}
	}
	return
}

func slogValue(line, key string) string {
	needle := " " + key + "="
	i := strings.Index(line, needle)
	if i < 0 {
		return ""
	}
	v := line[i+len(needle):]
	if strings.HasPrefix(v, `"`) {
		q, err := strconv.QuotedPrefix(v)
		if err != nil {
			return ""
		}
		s, err := strconv.Unquote(q)
		if err != nil {
			return ""
		}
		return s
	}
	if j := strings.IndexByte(v, ' '); j >= 0 {
		v = v[:j]
	}
	return v
}

func fileSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}

func resolveTorPaths(cfg *config) error {
	if cfg.torDirs == "" {

		cfg.torDirs = filepath.Join(cfg.repoRoot, "tmp", "rig-tor")
		if cfg.chutney != "" {
			cfg.torDirs = filepath.Join(cfg.repoRoot, "tmp", "rig-tor-chutney")
		}
	}
	var err error
	if cfg.chutney != "" {
		if cfg.chutney, err = filepath.Abs(cfg.chutney); err != nil {
			return fmt.Errorf("-chutney: %w", err)
		}
	}
	if cfg.torDirs, err = filepath.Abs(cfg.torDirs); err != nil {
		return fmt.Errorf("-tor-dirs: %w", err)
	}
	if cfg.torBin, err = filepath.Abs(cfg.torBin); err != nil {
		return fmt.Errorf("-tor-bin: %w", err)
	}
	return nil
}

func checkTorConfig(cfg *config) (string, error) {
	if cfg.systemTor {
		if cfg.chutney != "" {
			return "", errors.New("-chutney puts each rig's own tor on a private network; it cannot be used with -system-tor")
		}
		if cfg.torPass == "" {
			return "", errors.New("$HAOMA_TOR_PASSWORD is unset — -system-tor needs the system Tor control password (see reference_dev_tor_password)")
		}

		if (cfg.torControl == "") != (cfg.torSocks == "") {
			return "", errors.New("-tor-control and -tor-socks are set together or not at all")
		}
		where := "127.0.0.1:9051 and 127.0.0.1:9050"
		if cfg.torControl != "" {
			where = cfg.torControl + " and " + cfg.torSocks
		}
		return fmt.Sprintf("tor: every rig uses the system tor at %s (-system-tor), with the control password from $HAOMA_TOR_PASSWORD", where), nil
	}

	if cfg.torControl != "" || cfg.torSocks != "" {
		return "", errors.New("-tor-control and -tor-socks name a shared tor; they need -system-tor")
	}
	st, err := os.Stat(cfg.torBin)
	if err != nil {
		return "", fmt.Errorf("tor binary: %w (set -tor-bin, or run with -system-tor)", err)
	}
	if st.IsDir() || st.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("tor binary %s is not an executable file", cfg.torBin)
	}
	return fmt.Sprintf("tor: each rig runs its own tor (%s), data dirs under %s, kept between runs; $HAOMA_TOR_PASSWORD is not needed", cfg.torBin, cfg.torDirs), nil
}

func (r *rig) awaitTor(launched time.Time, logFrom int64, exited <-chan struct{}) error {
	api, err := backendClientFor(r)
	if err != nil {
		return fmt.Errorf("tor wait: %w", err)
	}
	w := torWait{
		api:      api,
		ownTor:   r.ownTor(),
		launched: launched,
		exited:   exited,
		timeout:  torBootstrapTimeout,
		interval: torPollInterval,
		logPath:  filepath.Join(r.root, "haomad.log"),
		logFrom:  logFrom,
	}
	w.torLog = r.torLogPath()
	res, err := w.run()
	if err != nil {
		return err
	}
	which := "system tor"
	if r.ownTor() {
		which = "own tor"
	}
	logf("[%s] %s %s at 100%% after %s", r.name, which, res.version, res.took.Round(100*time.Millisecond))
	return nil
}

var _ torReader = (*backendapi.Client)(nil)
