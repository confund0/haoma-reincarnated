package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const chutneyAuths = `DirAuthority test000a no-v2 orport=5100 v3ident=E7515369160D97D48ED16699D58716FF247A86EB 127.0.0.1:7100 3C9AF069CCA993990A10A1D63C84B0548616A53F
DirAuthority test001a no-v2 orport=5101 v3ident=BFC4970362C78A86CA1BD8E8100708F7887855C5 127.0.0.1:7101 1FABB09F8FC1512164BA5EEC7747EB1274EFD38E
`

const chutneyAuthorityTorrc = `TestingTorNetwork 1

## Options that we always want to test ##
DataDirectory /n/000a
Nickname test000a
ControlSocket 0
SocksPort 0
ControlPort 127.0.0.1:8000
Address 127.0.0.1
OrPort 127.0.0.1:5100
DirPort 7100
AuthoritativeDirectory 1
V3AuthoritativeDirectory 1
TestingV3AuthInitialVotingInterval 20
TestingDirAuthVoteHSDir *
` + chutneyAuths

const chutneyRelayTorrc = `TestingTorNetwork 1
DataDirectory /n/004r
Nickname test004r
Address 127.0.0.1
OrPort 127.0.0.1:5104
DirPort 7104
` + chutneyAuths

const chutneyClientTorrc = `TestingTorNetwork 1

## Options that we always want to test ##
DataDirectory /n/007c
Nickname test007c
# Use ControlSocket rather than ControlPort unix: to support older tors
ControlSocket 0
Sandbox 1
UseMicrodescriptors 1
SocksPort 127.0.0.1:9007
DnsPort 0
ControlPort 127.0.0.1:8007
RunAsDaemon 0
` + chutneyAuths

const chutneyOnionTorrc = `TestingTorNetwork 1
DataDirectory /n/008h
Nickname test008h
SocksPort 0
ControlPort 127.0.0.1:8008
` + chutneyAuths + `Address 127.0.0.1
HiddenServiceDir /n/008h/hidden_service
HiddenServicePort 5858 127.0.0.1:4747
HiddenServiceVersion 3
`

func writeChutneyNet(t *testing.T, torrcs map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range torrcs {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name, "torrc"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "network.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestChutneyTorrc(t *testing.T) {
	dir := writeChutneyNet(t, map[string]string{
		"000a": chutneyAuthorityTorrc,
		"004r": chutneyRelayTorrc,
		"007c": chutneyClientTorrc,
		"008h": chutneyOnionTorrc,
	})
	body, err := chutneyTorrc(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := chutneyTorrcMarker + " from " + filepath.Join(dir, "007c", "torrc") + "; do not edit\n" +
		"TestingTorNetwork 1\n" + chutneyAuths + "UseDefaultFallbackDirs 0\n"
	if body != want {
		t.Fatalf("torrc.local body:\n%s\nwant:\n%s", body, want)
	}
}

func TestChutneyTorrcSkipsRelayLines(t *testing.T) {
	dir := writeChutneyNet(t, map[string]string{
		"000a": chutneyAuthorityTorrc,
		"007c": chutneyClientTorrc,
	})
	body, err := chutneyTorrc(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range []string{"TestingV3AuthInitialVotingInterval", "TestingDirAuthVoteHSDir", "OrPort", "SocksPort", "DataDirectory"} {
		if strings.Contains(body, l) {
			t.Errorf("body holds %s:\n%s", l, body)
		}
	}
}

func TestChutneyTorrcRefuses(t *testing.T) {
	noAuths := strings.ReplaceAll(chutneyClientTorrc, chutneyAuths, "")
	cases := []struct {
		name    string
		torrcs  map[string]string
		wantErr string
	}{
		{"relays only", map[string]string{"000a": chutneyAuthorityTorrc, "004r": chutneyRelayTorrc}, "no client or onion service torrc"},
		{"no authorities", map[string]string{"007c": noAuths}, "no DirAuthority line"},
		{"not a testing network", map[string]string{"007c": strings.Replace(chutneyClientTorrc, "TestingTorNetwork 1", "TestingTorNetwork 0", 1)}, "does not set TestingTorNetwork 1"},
		{"clients disagree", map[string]string{"007c": chutneyClientTorrc, "008h": strings.Replace(chutneyOnionTorrc, "test001a", "test009a", 1)}, "give different network lines"},
	}
	for _, c := range cases {
		dir := writeChutneyNet(t, c.torrcs)
		if _, err := chutneyTorrc(dir); err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: err %v, want %q", c.name, err, c.wantErr)
		}
	}
	if _, err := chutneyTorrc(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("a missing nodes dir was taken")
	}
}

func TestSyncChutneyTorrc(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "torrc.local")
	body := chutneyTorrcMarker + " from x; do not edit\nTestingTorNetwork 1\n"

	if err := syncChutneyTorrc(dir, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("public run made %s: %v", path, err)
	}

	if err := syncChutneyTorrc(dir, body); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != body {
		t.Fatalf("torrc.local %q (%v), want %q", got, err, body)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("torrc.local mode %v", st.Mode().Perm())
	}

	newer := body + "DirAuthority new\n"
	if err := syncChutneyTorrc(dir, newer); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != newer {
		t.Fatalf("torrc.local not rewritten: %q", got)
	}

	if err := syncChutneyTorrc(dir, ""); err == nil || !strings.Contains(err.Error(), "written by -chutney") {
		t.Fatalf("public run on a -chutney dir: %v", err)
	}

	own := "Socks5Proxy 127.0.0.1:1080\n"
	if err := os.WriteFile(path, []byte(own), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syncChutneyTorrc(dir, body); err == nil || !strings.Contains(err.Error(), "not one -chutney wrote") {
		t.Fatalf("own torrc.local: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != own {
		t.Fatalf("own torrc.local changed: %q", got)
	}
	if err := syncChutneyTorrc(dir, ""); err != nil {
		t.Fatalf("public run with an own torrc.local: %v", err)
	}
}

const testingNetworkWarn = "Sep 30 13:47:00.627 [warn] TestingTorNetwork is set. This will make your node almost unusable in the public Tor network, and is therefore only advised if you are building a testing Tor network!\n"

func TestCheckTorNetwork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tor.log")
	earlier := "Sep 30 13:40:00.000 [notice] Bootstrapped 100% (done): Done\n"
	if err := os.WriteFile(path, []byte(earlier), 0o600); err != nil {
		t.Fatal(err)
	}
	from := fileSize(path)

	if err := checkTorNetwork(path, from, false); err != nil {
		t.Fatalf("public: %v", err)
	}
	if err := checkTorNetwork(path, from, true); err == nil || !strings.Contains(err.Error(), "not on the private network") {
		t.Fatalf("-chutney without the line: %v", err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(testingNetworkWarn); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := checkTorNetwork(path, from, true); err != nil {
		t.Fatalf("-chutney: %v", err)
	}
	if err := checkTorNetwork(path, from, false); err == nil || !strings.Contains(err.Error(), "not on the public network") {
		t.Fatalf("public run on a testing network: %v", err)
	}

	if err := checkTorNetwork(path, fileSize(path), true); err == nil {
		t.Fatal("-chutney passed on an earlier launch's line")
	}
}

func TestResolveTorPathsChutney(t *testing.T) {
	cfg := config{repoRoot: "/repo", torBin: "/usr/bin/tor", chutney: "/repo/tmp/chutney-net/nodes"}
	if err := resolveTorPaths(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.torDirs != "/repo/tmp/rig-tor-chutney" {
		t.Fatalf("-chutney default torDirs %q", cfg.torDirs)
	}
	cfg = config{repoRoot: "/repo", torBin: "/usr/bin/tor", chutney: "net/nodes", torDirs: "/t"}
	if err := resolveTorPaths(&cfg); err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	if cfg.torDirs != "/t" || cfg.chutney != filepath.Join(wd, "net", "nodes") {
		t.Fatalf("torDirs %q, chutney %q", cfg.torDirs, cfg.chutney)
	}
}

func TestCheckTorConfigChutneyNeedsOwnTor(t *testing.T) {
	cfg := config{systemTor: true, torPass: "pw", chutney: "/n"}
	if _, err := checkTorConfig(&cfg); err == nil || !strings.Contains(err.Error(), "-system-tor") {
		t.Fatalf("-chutney with -system-tor: %v", err)
	}
}
