package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type kindCounts struct {
	onion, hex, token, name, status, ip int
}

type leftoverCounts struct {
	onion, hex, hform, ip int
}

func (l leftoverCounts) total() int { return l.onion + l.hex + l.hform + l.ip }

type nameSet struct {
	seen map[string]bool
}

func newNameSet() *nameSet { return &nameSet{seen: map[string]bool{}} }

func (n *nameSet) add(name string) {
	if name == "" {
		return
	}
	n.seen[name] = true
}

func (n *nameSet) names() []string {
	out := make([]string, 0, len(n.seen))
	for k := range n.seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

const (
	redactKeyDirMode  = 0o700
	redactKeyFileMode = 0o600
	redactKeySize     = 32
)

func defaultKeyPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "haoma-logs", "redact.key"), nil
}

func loadOrMakeKey(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		if len(b) != redactKeySize {
			return nil, fmt.Errorf("the key at %s is %d bytes, want %d; move it aside and run again to make a new one", path, len(b), redactKeySize)
		}
		return b, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), redactKeyDirMode); err != nil {
		return nil, err
	}
	key := make([]byte, redactKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, key, redactKeyFileMode); err != nil {
		return nil, err
	}
	return key, nil
}

func nameTag(key []byte, name string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(name))
	sum := mac.Sum(nil)
	return "nick:" + hex.EncodeToString(sum[:4])
}

var (
	onionAnyRe = regexp.MustCompile(`[a-z2-7]{56}(\.onion)?`)

	hexRunRe    = regexp.MustCompile(`[0-9a-fA-F]{16,}`)
	hexLetterRe = regexp.MustCompile(`[a-fA-F]`)

	tokenRunRe  = regexp.MustCompile(`[A-Za-z0-9_-]{12,}`)
	hasDigitRe  = regexp.MustCompile(`[0-9]`)
	hasLetterRe = regexp.MustCompile(`[A-Za-z]`)

	tokenKeyRe = regexp.MustCompile(`\b((?:\w+_)?token)=(\S+)`)

	urlRe = regexp.MustCompile(`https?://\S+`)

	hFormRe = regexp.MustCompile(`h:[0-9a-f]{8}`)

	labelRe = regexp.MustCompile(`label=(.+?)(?: ([A-Za-z_]+=)|$)`)

	callEndedRe = regexp.MustCompile(`^(.*)call from (.+?) ended(?: \((.*)\))?$`)

	callingRe = regexp.MustCompile(`^calling (.+?)(?: \((.*)\))?$`)

	callBareRe = regexp.MustCompile(`^(.*)call (from|with) (.+)$`)

	notifyRe = regexp.MustCompile(`^notify \((.+)\): (.*)$`)

	attachRe = regexp.MustCompile(`^attach \((\d+(?:\.\d)? (?:B|KB|MB|GB))\)(?: label=(.+))?$`)

	alreadyTagRe = regexp.MustCompile(`^nick:[0-9a-f]{8}$`)

	alreadyBlankedRe = regexp.MustCompile(`^<text len=\d+>$`)

	timestampPrefixRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}$`)

	guiStatusLineRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z\s+(?:DEBUG|INFO|WARN|ERROR)\s+status `)

	ipv4CandidateRe = regexp.MustCompile(`\d{1,3}(?:\.\d{1,3}){3}`)
	ipv6CandidateRe = regexp.MustCompile(`[0-9A-Fa-f:]{2,}`)

	portSuffixRe = regexp.MustCompile(`^:\d{1,5}`)

	mappedHexRe = regexp.MustCompile(`(?i)::ffff:[0-9a-f]{1,4}:[0-9a-f]{1,4}`)
)

var redactableSuffixes = []string{".log", ".log.prev", ".stderr", ".stderr.prev", ".txt"}

func hasRedactableSuffix(name string) bool {
	for _, s := range redactableSuffixes {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	return false
}

func tagName(key []byte, name string, names *nameSet, c *kindCounts) string {
	if alreadyTagRe.MatchString(name) {
		return name
	}
	names.add(name)
	c.name++
	return nameTag(key, name)
}

func redactAttachShape(text string, key []byte, names *nameSet, c *kindCounts) (string, bool) {
	m := attachRe.FindStringSubmatch(text)
	if m == nil {
		return text, false
	}
	if m[2] == "" {
		return text, true
	}
	return fmt.Sprintf("attach (%s) label=%s", m[1], tagName(key, m[2], names, c)), true
}

func redactKnownNameShapes(text string, key []byte, names *nameSet, c *kindCounts) (string, bool) {
	if m := notifyRe.FindStringSubmatch(text); m != nil {
		tag := tagName(key, m[1], names, c)
		rest := m[2]
		if !alreadyBlankedRe.MatchString(rest) {
			c.status++
			rest = fmt.Sprintf("<text len=%d>", len(rest))
		}
		return fmt.Sprintf("notify (%s): %s", tag, rest), true
	}
	if m := callEndedRe.FindStringSubmatch(text); m != nil {
		tag := tagName(key, m[2], names, c)
		if m[3] == "" {
			return fmt.Sprintf("%scall from %s ended", m[1], tag), true
		}
		return fmt.Sprintf("%scall from %s ended (%s)", m[1], tag, m[3]), true
	}
	if m := callingRe.FindStringSubmatch(text); m != nil {
		tag := tagName(key, m[1], names, c)
		if m[2] == "" {
			return fmt.Sprintf("calling %s", tag), true
		}
		return fmt.Sprintf("calling %s (%s)", tag, m[2]), true
	}
	if m := callBareRe.FindStringSubmatch(text); m != nil {
		tag := tagName(key, m[3], names, c)
		return fmt.Sprintf("%scall %s %s", m[1], m[2], tag), true
	}
	return text, false
}

func redactStatusText(line string, key []byte, names *nameSet, c *kindCounts) string {
	loc := guiStatusLineRe.FindStringIndex(line)
	if loc == nil {
		return line
	}
	head := line[:loc[1]]
	text := line[loc[1]:]

	if out, known := redactAttachShape(text, key, names, c); known {
		return head + out
	}
	if out, known := redactKnownNameShapes(text, key, names, c); known {
		return head + out
	}

	if strings.HasPrefix(text, "tor:") || alreadyBlankedRe.MatchString(text) {
		return head + text
	}
	c.status++
	return head + fmt.Sprintf("<text len=%d>", len(text))
}

func redactLabel(line string, key []byte, names *nameSet, c *kindCounts) string {
	return labelRe.ReplaceAllStringFunc(line, func(m string) string {
		sub := labelRe.FindStringSubmatch(m)
		tag := "label=" + tagName(key, sub[1], names, c)
		if sub[2] != "" {
			tag += " " + sub[2]
		}
		return tag
	})
}

func redactHForms(line string, key []byte, names *nameSet, c *kindCounts) string {
	return hFormRe.ReplaceAllStringFunc(line, func(m string) string {
		hexPart := strings.TrimPrefix(m, "h:")
		for _, n := range names.names() {
			sum := sha256.Sum256([]byte(n))
			if hex.EncodeToString(sum[:4]) == hexPart {
				c.name++
				return nameTag(key, n)
			}
		}
		c.name++
		return nameTag(key, m)
	})
}

func isAlnumByte(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func notStandalone(line string, start, end int, extra byte) bool {
	if start > 0 {
		p := line[start-1]
		if p == extra || isAlnumByte(p) {
			return true
		}
	}
	if end < len(line) {
		n := line[end]
		if n == extra || isAlnumByte(n) {
			return true
		}
	}
	return false
}

func ipv4Candidates(line string) [][2]int {
	var out [][2]int
	for _, loc := range ipv4CandidateRe.FindAllStringIndex(line, -1) {
		start, end := loc[0], loc[1]
		if notStandalone(line, start, end, '.') {
			continue
		}
		candidate := line[start:end]
		ip := net.ParseIP(candidate)
		if ip == nil || ip.To4() == nil {
			continue
		}
		if isLoopback(candidate) {
			continue
		}
		out = append(out, [2]int{start, end})
	}
	return out
}

func ipv6Candidates(line string) [][2]int {
	var out [][2]int
	for _, loc := range ipv6CandidateRe.FindAllStringIndex(line, -1) {
		start, end := loc[0], loc[1]
		candidate := line[start:end]
		if strings.Count(candidate, ":") < 2 {
			continue
		}
		if notStandalone(line, start, end, ':') {
			continue
		}

		if end < len(line) && line[end] == '.' {
			continue
		}
		ip := net.ParseIP(candidate)
		if ip == nil || ip.To4() != nil {
			continue
		}
		if candidate == "::1" {
			continue
		}
		out = append(out, [2]int{start, end})
	}
	return out
}

func mappedHexCandidates(line string) [][2]int {
	var out [][2]int
	for _, loc := range mappedHexRe.FindAllStringIndex(line, -1) {
		start, end := loc[0], loc[1]
		if notStandalone(line, start, end, ':') {
			continue
		}
		candidate := line[start:end]
		ip := net.ParseIP(candidate)
		if ip == nil {
			continue
		}
		v4 := ip.To4()
		if v4 == nil || v4[0] == 127 {
			continue
		}
		out = append(out, [2]int{start, end})
	}
	return out
}

func redactMappedHex(line string, c *kindCounts) string {
	matches := mappedHexCandidates(line)
	for i := len(matches) - 1; i >= 0; i-- {
		start, end := matches[i][0], matches[i][1]
		line = line[:start] + "<ip>" + line[end:]
		c.ip++
	}
	return line
}

func redactIPv4(line string, c *kindCounts) string {
	matches := ipv4Candidates(line)
	for i := len(matches) - 1; i >= 0; i-- {
		start, end := matches[i][0], matches[i][1]
		repl := "<ip>"
		newEnd := end
		if m := portSuffixRe.FindString(line[end:]); m != "" {
			repl += m
			newEnd = end + len(m)
		}
		line = line[:start] + repl + line[newEnd:]
		c.ip++
	}
	return line
}

func redactIPv6(line string, c *kindCounts) string {
	matches := ipv6Candidates(line)
	for i := len(matches) - 1; i >= 0; i-- {
		start, end := matches[i][0], matches[i][1]
		line = line[:start] + "<ip>" + line[end:]
		c.ip++
	}
	return line
}

func redactOnions(line string, c *kindCounts) string {
	return onionAnyRe.ReplaceAllStringFunc(line, func(m string) string {
		c.onion++
		base := strings.TrimSuffix(m, ".onion")
		if base != m {
			return base[:8] + "…" + ".onion"
		}
		return base[:8] + "…"
	})
}

func redactHexRuns(line string, c *kindCounts) string {
	return hexRunRe.ReplaceAllStringFunc(line, func(m string) string {
		if !hexLetterRe.MatchString(m) {
			return m
		}
		c.hex++
		return m[:8] + "…"
	})
}

func redactTokens(line string, c *kindCounts) string {
	return tokenRunRe.ReplaceAllStringFunc(line, func(m string) string {
		if timestampPrefixRe.MatchString(m) {
			return m
		}
		if !hasDigitRe.MatchString(m) || !hasLetterRe.MatchString(m) {
			return m
		}
		c.token++
		return m[:8] + "…"
	})
}

func shortenToken(s string) string {
	if len(s) <= 8 {
		return s
	}
	return s[:8] + "…"
}

func redactTokenKeys(line string, c *kindCounts) string {
	return tokenKeyRe.ReplaceAllStringFunc(line, func(m string) string {
		sub := tokenKeyRe.FindStringSubmatch(m)
		short := shortenToken(sub[2])
		if short != sub[2] {
			c.token++
		}
		return sub[1] + "=" + short
	})
}

func redactURLPathSegments(line string, c *kindCounts) string {
	return urlRe.ReplaceAllStringFunc(line, func(u string) string {
		parts := strings.Split(u, "/")
		changed := false
		for i, p := range parts {
			if i < 3 || len(p) < 12 {
				continue
			}
			parts[i] = shortenToken(p)
			changed = true
		}
		if changed {
			c.token++
		}
		return strings.Join(parts, "/")
	})
}

func processLine(line string, key []byte, names *nameSet, c *kindCounts) string {
	line = redactStatusText(line, key, names, c)
	line = redactLabel(line, key, names, c)
	line = redactHForms(line, key, names, c)
	line = redactTokenKeys(line, c)
	line = redactURLPathSegments(line, c)
	line = redactMappedHex(line, c)
	line = redactIPv4(line, c)
	line = redactIPv6(line, c)
	line = redactOnions(line, c)
	line = redactHexRuns(line, c)
	line = redactTokens(line, c)
	return line
}

func discoverNames(line string, key []byte, names *nameSet) {
	var discard kindCounts
	if loc := guiStatusLineRe.FindStringIndex(line); loc != nil {
		text := line[loc[1]:]
		if _, known := redactAttachShape(text, key, names, &discard); !known {
			redactKnownNameShapes(text, key, names, &discard)
		}
	}
	redactLabel(line, key, names, &discard)
}

func isLoopback(ip string) bool {
	return ip == "127.0.0.1" || strings.HasPrefix(ip, "127.")
}

func scanLeftovers(content string) leftoverCounts {
	var lo leftoverCounts
	for _, line := range strings.Split(content, "\n") {
		lo.onion += len(onionAnyRe.FindAllString(line, -1))
		for _, m := range hexRunRe.FindAllString(line, -1) {
			if hexLetterRe.MatchString(m) {
				lo.hex++
			}
		}
		lo.hform += len(hFormRe.FindAllString(line, -1))
		lo.ip += len(ipv4Candidates(line))
		lo.ip += len(ipv6Candidates(line))
		lo.ip += len(mappedHexCandidates(line))
	}
	return lo
}

func findLogFiles(dir, keyPath string) (files, other []string, err error) {
	absKey := ""
	if keyPath != "" {
		if a, absErr := filepath.Abs(keyPath); absErr == nil {
			absKey = a
		}
	}
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if absKey != "" {
			if abs, absErr := filepath.Abs(path); absErr == nil && abs == absKey {
				return nil
			}
		}
		if hasRedactableSuffix(d.Name()) {
			files = append(files, path)
			return nil
		}

		if d.Type().IsRegular() {
			if info, infoErr := d.Info(); infoErr == nil && info.Size() == 0 {
				return nil
			}
		}
		other = append(other, path)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	sort.Strings(files)
	sort.Strings(other)
	return files, other, nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".redact-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func redactFile(path string, key []byte, names *nameSet) (kindCounts, leftoverCounts, error) {
	info, err := os.Stat(path)
	if err != nil {
		return kindCounts{}, leftoverCounts{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return kindCounts{}, leftoverCounts{}, err
	}

	var c kindCounts
	lines := strings.Split(string(raw), "\n")
	for i, line := range lines {
		lines[i] = processLine(line, key, names, &c)
	}
	out := strings.Join(lines, "\n")

	if out != string(raw) {
		if err := atomicWrite(path, []byte(out), info.Mode()); err != nil {
			return c, leftoverCounts{}, err
		}
	}
	return c, scanLeftovers(out), nil
}

func redactDir(dir string, key []byte, keyPath string, errOut io.Writer) (bool, error) {
	files, other, err := findLogFiles(dir, keyPath)
	if err != nil {
		return false, err
	}
	if len(files) == 0 && len(other) == 0 {
		return false, fmt.Errorf("no file under %s", dir)
	}

	clean := true
	for _, f := range other {
		fmt.Fprintf(errOut, "not redacted: %s\n", filepath.Base(f))
		clean = false
	}

	names := newNameSet()
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return false, fmt.Errorf("%s: %w", f, err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			discoverNames(line, key, names)
		}
	}

	for _, f := range files {
		c, lo, err := redactFile(f, key, names)
		if err != nil {
			return false, fmt.Errorf("%s: %w", f, err)
		}
		rel, err := filepath.Rel(dir, f)
		if err != nil {
			rel = f
		}
		fmt.Fprintf(errOut,
			"%s: replaced %d onion, %d hex, %d token, %d name, %d status, %d ip; left over %d onion, %d hex, %d h:, %d ip\n",
			rel, c.onion, c.hex, c.token, c.name, c.status, c.ip,
			lo.onion, lo.hex, lo.hform, lo.ip)
		if lo.total() > 0 {
			clean = false
		}
	}
	return clean, nil
}

func redactMain(args []string) int {
	fs := flag.NewFlagSet("haoma-logs redact", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	keyPath := fs.String("key", "", "the HMAC key file to use, instead of the one in the user's config directory")
	tagNick := fs.String("tag", "", "print the tag this name would get, then stop; touches no file")
	fs.Usage = func() { redactUsage(fs) }

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	path := *keyPath
	if path == "" {
		p, err := defaultKeyPath()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		path = p
	}
	key, err := loadOrMakeKey(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	if *tagNick != "" {
		fmt.Println(nameTag(key, *tagNick))
		return 0
	}

	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "give one directory to redact, or -tag with a name")
		redactUsage(fs)
		return 2
	}

	clean, err := redactDir(fs.Arg(0), key, path, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !clean {
		return 1
	}
	return 0
}

func redactUsage(fs *flag.FlagSet) {
	w := fs.Output()
	fmt.Fprintln(w, `haoma-logs redact rewrites the logs in a directory in place, so an
exported run can be handed to an agent without carrying a real onion
address, a real token or a real name. Run it once on the export,
before any path under it is given to an agent.

Usage:
  haoma-logs redact [flags] <dir>
  haoma-logs redact -tag <nick>

The first form rewrites every file under <dir> whose name ends in
.log, .log.prev, .stderr, .stderr.prev or .txt, in place, at any
depth. A temp file is written next to the original and renamed over
it, so an interrupted run never leaves a half-written log behind. A
second run over the same files changes nothing.

Any other regular file under <dir> is left alone and named on
standard error as "not redacted", and the command exits 1: a file of
a shape this tool does not parse must be looked at by hand, not
shipped by mistake.

The second form prints the tag a name would get, without touching any
file, so a name can be looked up in a log already redacted.

Flags:`)
	fs.PrintDefaults()
	fmt.Fprintln(w, `
Names are replaced by a tag: "nick:" plus 8 hex characters of
HMAC-SHA256 under a key that stays on this machine, at the user's
config directory (haoma-logs/redact.key by default, made 0600 the
first time this runs, under a directory made 0700). Give -key a
different path, for example in a test. The key is never printed, and
no tag this tool prints can be turned back into a name without it.

A line is printed on standard error for each file, counting what was
replaced and what, if anything, still looks like it could identify
someone. The command exits 1 when anything is left over.`)
}
