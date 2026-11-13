package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

// Check is one line of the report.
type Check struct {
	Name   string
	Pin    string
	Latest string
	Result string // "ok", "update" or "error"
	Err    string
}

// Line formats the check the way the report prints it, one line per
// check, key=value.
func (c Check) Line() string {
	switch c.Result {
	case "error":
		return fmt.Sprintf("check=%s result=error err=%q", c.Name, c.Err)
	case "update":
		return fmt.Sprintf("check=%s pin=%s latest=%s result=update", c.Name, c.Pin, c.Latest)
	default:
		return fmt.Sprintf("check=%s pin=%s result=ok", c.Name, c.Pin)
	}
}

// exitCode picks the process exit code for a set of checks: 2 if any
// check could not be read at all, else 1 if any check found a newer
// release, else 0.
func exitCode(checks []Check) int {
	code := 0
	for _, c := range checks {
		switch c.Result {
		case "error":
			code = 2
		case "update":
			if code < 1 {
				code = 1
			}
		}
	}
	return code
}

// seriesLocked names the pins held to their current major.minor series
// rather than compared against any newer stable release.
var seriesLocked = map[string]bool{
	"openssl": true,
	"zlib":    true,
}

// tagLister lists the tags of a git remote. The real implementation
// runs "git ls-remote --tags"; tests use a fake with no network.
type tagLister func(url string) ([]string, error)

// evaluatePin checks one pinned library against its upstream tags.
func evaluatePin(pin Pin, list tagLister) Check {
	pinVer, _, ok := parseTag(pin.Tag)
	if !ok {
		return Check{Name: pin.Name, Result: "error", Err: fmt.Sprintf("pinned tag %q does not parse", pin.Tag)}
	}
	tags, err := list(pin.URL)
	if err != nil {
		return Check{Name: pin.Name, Result: "error", Err: err.Error()}
	}
	var best Version
	haveBest := false
	for _, tag := range tags {
		v, stable, ok := parseTag(tag)
		if !ok || !stable {
			continue
		}
		if seriesLocked[pin.Name] && !sameSeries(v, pinVer) {
			continue
		}
		if !haveBest || compareVersions(v, best) > 0 {
			best = v
			haveBest = true
		}
	}
	if haveBest && compareVersions(best, pinVer) > 0 {
		return Check{Name: pin.Name, Pin: pinVer.String(), Latest: best.String(), Result: "update"}
	}
	return Check{Name: pin.Name, Pin: pinVer.String(), Result: "ok"}
}

// goRelease is one entry of https://go.dev/dl/?mode=json.
type goRelease struct {
	Version string `json:"version"`
	Stable  bool   `json:"stable"`
}

// goReleaseFetcher fetches the supported Go release list. The real
// implementation does the HTTP GET; tests use a fake with no network.
type goReleaseFetcher func() ([]goRelease, error)

var goDirectiveRe = regexp.MustCompile(`(?m)^go (\d+\.\d+(?:\.\d+)?)\s*$`)

// parseGoDirective reads the "go" line of a go.mod file.
func parseGoDirective(path string) (Version, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m := goDirectiveRe.FindStringSubmatch(string(data))
	if m == nil {
		return nil, fmt.Errorf("%s: no \"go\" directive found", path)
	}
	return parseDotted(m[1])
}

func parseDotted(s string) (Version, error) {
	v, _, ok := parseTag(s)
	if !ok {
		return nil, fmt.Errorf("%q does not parse as a version", s)
	}
	return v, nil
}

func parseGoVersionString(s string) (Version, bool) {
	const prefix = "go"
	if len(s) <= len(prefix) || s[:len(prefix)] != prefix {
		return nil, false
	}
	v, _, ok := parseTag(s[len(prefix):])
	return v, ok
}

// evaluateGo checks the current Go toolchain version against the
// supported release list: a finding is a newer patch of the same
// minor, or that minor no longer being in the list at all.
func evaluateGo(current Version, releases []goRelease) Check {
	var sameMinorMax Version
	haveSameMinor := false
	var overallMax Version
	haveOverall := false
	for _, r := range releases {
		if !r.Stable {
			continue
		}
		v, ok := parseGoVersionString(r.Version)
		if !ok {
			continue
		}
		if !haveOverall || compareVersions(v, overallMax) > 0 {
			overallMax = v
			haveOverall = true
		}
		if sameSeries(v, current) {
			if !haveSameMinor || compareVersions(v, sameMinorMax) > 0 {
				sameMinorMax = v
				haveSameMinor = true
			}
		}
	}
	if !haveSameMinor {
		latest := "none"
		if haveOverall {
			latest = overallMax.String()
		}
		return Check{Name: "go", Pin: current.String(), Latest: latest, Result: "update"}
	}
	if compareVersions(sameMinorMax, current) > 0 {
		return Check{Name: "go", Pin: current.String(), Latest: sameMinorMax.String(), Result: "update"}
	}
	return Check{Name: "go", Pin: current.String(), Result: "ok"}
}

// goCheck builds the "go" check: it reads both modules' go.mod files,
// requires them to agree, fetches the release list and evaluates it.
func goCheck(root string, fetch goReleaseFetcher) Check {
	backendVer, err := parseGoDirective(filepath.Join(root, "backend", "go.mod"))
	if err != nil {
		return Check{Name: "go", Result: "error", Err: err.Error()}
	}
	frontendVer, err := parseGoDirective(filepath.Join(root, "frontend", "go.mod"))
	if err != nil {
		return Check{Name: "go", Result: "error", Err: err.Error()}
	}
	if backendVer.String() != frontendVer.String() {
		return Check{Name: "go", Result: "error", Err: fmt.Sprintf("backend go.mod (%s) and frontend go.mod (%s) disagree", backendVer, frontendVer)}
	}
	releases, err := fetch()
	if err != nil {
		return Check{Name: "go", Result: "error", Err: err.Error()}
	}
	return evaluateGo(backendVer, releases)
}

// pinChecks loads both versions JSON files under root and evaluates
// every pin they hold, sorted by name so the report is deterministic.
// A file that cannot be read or parsed becomes one error check named
// for the file.
func pinChecks(root string, list tagLister) []Check {
	var checks []Check
	files := []string{
		filepath.Join(root, "mobile", "tor-build", "tor-versions.json"),
		filepath.Join(root, "mobile", "streams-build", "streams-versions.json"),
	}
	var pins []Pin
	for _, f := range files {
		p, err := loadPins(f)
		if err != nil {
			checks = append(checks, Check{Name: filepath.Base(f), Result: "error", Err: err.Error()})
			continue
		}
		pins = append(pins, p...)
	}
	sort.Slice(pins, func(i, j int) bool { return pins[i].Name < pins[j].Name })
	for _, p := range pins {
		checks = append(checks, evaluatePin(p, list))
	}
	return checks
}

// run builds every check, in deterministic order: the library pins
// sorted by name, then the Go toolchain check last.
func run(root string, list tagLister, fetch goReleaseFetcher) []Check {
	checks := pinChecks(root, list)
	checks = append(checks, goCheck(root, fetch))
	return checks
}
