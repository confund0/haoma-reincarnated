package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestParseTagShapes(t *testing.T) {
	cases := []struct {
		tag        string
		wantOK     bool
		wantStable bool
		wantVer    string
	}{
		{"tor-0.4.9.16", true, true, "0.4.9.16"},
		{"tor-0.5.0.1-alpha", true, false, "0.5.0.1"},
		{"openssl-3.5.9", true, true, "3.5.9"},
		{"openssl-3.6.0-alpha1", true, false, "3.6.0"},
		{"OpenSSL_1_1_1w", false, false, ""},
		{"release-2.1.12-stable", true, true, "2.1.12"},
		{"release-2.2.1-alpha", true, false, "2.2.1"},
		{"v1.3.2", true, true, "1.3.2"},
		{"v1.4.0", true, true, "1.4.0"},
		{"1.0.20-RELEASE", true, true, "1.0.20"},
		{"v1.5.2", true, true, "1.5.2"},
		{"v1.14.1", true, true, "1.14.1"},
	}
	for _, c := range cases {
		v, stable, ok := parseTag(c.tag)
		if ok != c.wantOK {
			t.Errorf("parseTag(%q) ok = %v, want %v", c.tag, ok, c.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if stable != c.wantStable {
			t.Errorf("parseTag(%q) stable = %v, want %v", c.tag, stable, c.wantStable)
		}
		if v.String() != c.wantVer {
			t.Errorf("parseTag(%q) version = %q, want %q", c.tag, v.String(), c.wantVer)
		}
	}
}

func TestEvaluatePinCompareRule(t *testing.T) {
	// openssl: pinned to 3.5.9, held to the 3.5 series. A newer 3.6.0
	// stable tag exists upstream but must not count as a finding.
	pin := Pin{Name: "openssl", URL: "fake://openssl", Tag: "openssl-3.5.9"}
	list := fakeLister(map[string][]string{
		"fake://openssl": {"openssl-3.5.9", "openssl-3.5.10", "openssl-3.6.0", "openssl-3.6.0-alpha1"},
	})
	got := evaluatePin(pin, list)
	if got.Result != "update" || got.Latest != "3.5.10" {
		t.Fatalf("openssl: got %+v, want an update to 3.5.10 within the 3.5 series", got)
	}

	// zlib: pinned to 1.3.2, held to the 1.3 series. 1.4.0 exists
	// upstream but must not count.
	pin = Pin{Name: "zlib", URL: "fake://zlib", Tag: "v1.3.2"}
	list = fakeLister(map[string][]string{
		"fake://zlib": {"v1.3.2", "v1.4.0"},
	})
	got = evaluatePin(pin, list)
	if got.Result != "ok" {
		t.Fatalf("zlib: got %+v, want ok (1.4.0 is a different series)", got)
	}

	// tor: pinned to 0.4.9.16, not series-locked. A newer stable
	// 0.5.0.1 is a finding; the alpha before it is not.
	pin = Pin{Name: "tor", URL: "fake://tor", Tag: "tor-0.4.9.16"}
	list = fakeLister(map[string][]string{
		"fake://tor": {"tor-0.4.9.16", "tor-0.4.9.17", "tor-0.5.0.1-alpha", "tor-0.5.0.1"},
	})
	got = evaluatePin(pin, list)
	if got.Result != "update" || got.Latest != "0.5.0.1" {
		t.Fatalf("tor: got %+v, want an update to 0.5.0.1", got)
	}
}

func TestEvaluatePinNoFinding(t *testing.T) {
	pin := Pin{Name: "opus", URL: "fake://opus", Tag: "v1.5.2"}
	list := fakeLister(map[string][]string{
		"fake://opus": {"v1.5.2", "v1.5.1"},
	})
	got := evaluatePin(pin, list)
	if got.Result != "ok" {
		t.Fatalf("got %+v, want ok", got)
	}
}

func TestEvaluatePinBadPinTagIsSourceError(t *testing.T) {
	pin := Pin{Name: "mystery", URL: "fake://mystery", Tag: "not-a-version"}
	got := evaluatePin(pin, fakeLister(nil))
	if got.Result != "error" {
		t.Fatalf("got %+v, want a source error on the unparseable pin", got)
	}
}

func TestEvaluatePinListerError(t *testing.T) {
	pin := Pin{Name: "tor", URL: "fake://tor", Tag: "tor-0.4.9.16"}
	list := func(url string) ([]string, error) { return nil, fmt.Errorf("boom") }
	got := evaluatePin(pin, list)
	if got.Result != "error" {
		t.Fatalf("got %+v, want error", got)
	}
}

func fakeLister(tags map[string][]string) tagLister {
	return func(url string) ([]string, error) {
		return tags[url], nil
	}
}

func TestEvaluateGoNewerPatch(t *testing.T) {
	current := Version{1, 25, 9}
	releases := []goRelease{
		{Version: "go1.25.10", Stable: true},
		{Version: "go1.25.9", Stable: true},
		{Version: "go1.24.20", Stable: true},
	}
	got := evaluateGo(current, releases)
	if got.Result != "update" || got.Latest != "1.25.10" {
		t.Fatalf("got %+v, want an update to 1.25.10", got)
	}
}

func TestEvaluateGoMinorDropped(t *testing.T) {
	current := Version{1, 25, 9}
	releases := []goRelease{
		{Version: "go1.26.1", Stable: true},
		{Version: "go1.25.10", Stable: false}, // no longer supported
	}
	got := evaluateGo(current, releases)
	if got.Result != "update" {
		t.Fatalf("got %+v, want an update: the 1.25 minor has no supported release left", got)
	}
}

func TestEvaluateGoOK(t *testing.T) {
	current := Version{1, 25, 9}
	releases := []goRelease{
		{Version: "go1.25.9", Stable: true},
		{Version: "go1.24.20", Stable: true},
	}
	got := evaluateGo(current, releases)
	if got.Result != "ok" {
		t.Fatalf("got %+v, want ok", got)
	}
}

func TestExitCode(t *testing.T) {
	cases := []struct {
		checks []Check
		want   int
	}{
		{[]Check{{Result: "ok"}}, 0},
		{[]Check{{Result: "ok"}, {Result: "update"}}, 1},
		{[]Check{{Result: "update"}, {Result: "error"}}, 2},
		{[]Check{{Result: "error"}, {Result: "update"}}, 2},
		{nil, 0},
	}
	for _, c := range cases {
		got := exitCode(c.checks)
		if got != c.want {
			t.Errorf("exitCode(%+v) = %d, want %d", c.checks, got, c.want)
		}
	}
}

func TestLoadPinsSkipsNdkAndTimestamp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tor-versions.json")
	content := `{
  "_comment": "ignored",
  "0.4.9.16": {
    "tor": {"url": "https://example.test/tor.git", "commit": "tor-0.4.9.16"},
    "libevent": {"url": "https://example.test/libevent.git", "commit": "release-2.1.12-stable"},
    "ndk": {"url": "https://example.test/ndk.zip", "revision": "29.0.14206865", "sha256": "abc"},
    "timestamp": "201001010000.00"
  }
}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	pins, err := loadPins(path)
	if err != nil {
		t.Fatalf("loadPins: %v", err)
	}
	if len(pins) != 2 {
		t.Fatalf("got %d pins, want 2 (ndk and timestamp skipped): %+v", len(pins), pins)
	}
	names := map[string]bool{}
	for _, p := range pins {
		names[p.Name] = true
	}
	if !names["tor"] || !names["libevent"] {
		t.Fatalf("got pins %+v, want tor and libevent", pins)
	}
}

func TestLoadPinsRealFiles(t *testing.T) {
	root := repoRoot(t)
	for _, f := range []string{
		filepath.Join(root, "mobile", "tor-build", "tor-versions.json"),
		filepath.Join(root, "mobile", "streams-build", "streams-versions.json"),
	} {
		pins, err := loadPins(f)
		if err != nil {
			t.Fatalf("loadPins(%s): %v", f, err)
		}
		if len(pins) == 0 {
			t.Fatalf("loadPins(%s): got no pins", f)
		}
		for _, p := range pins {
			if _, _, ok := parseTag(p.Tag); !ok {
				t.Errorf("loadPins(%s): pin %q has an unparseable tag %q", f, p.Name, p.Tag)
			}
		}
	}
}

// repoRoot walks up from the test's working directory to find the
// repository root (the directory holding both Go modules), the same
// way the rest of the project's tools do.
func repoRoot(t *testing.T) string {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "backend", "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "frontend", "go.mod")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repository root")
		}
		dir = parent
	}
}

func TestRunDeterministicOrder(t *testing.T) {
	root := repoRoot(t)
	list := func(url string) ([]string, error) { return nil, nil }
	fetch := func() ([]goRelease, error) {
		return []goRelease{{Version: "go1.25.9", Stable: true}}, nil
	}
	a := run(root, list, fetch)
	b := run(root, list, fetch)
	if len(a) != len(b) {
		t.Fatalf("got different lengths across runs: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Name != b[i].Name {
			t.Fatalf("check order is not deterministic: %q vs %q at index %d", a[i].Name, b[i].Name, i)
		}
	}
	if a[len(a)-1].Name != "go" {
		t.Fatalf("got last check %q, want \"go\" last", a[len(a)-1].Name)
	}
}
