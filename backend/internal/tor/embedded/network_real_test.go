package embedded

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"haoma/internal/tor/control"
)

const (
	realTorBinEnv = "HAOMA_TOR_BIN"
	realTorNetEnv = "HAOMA_TOR_NETWORK"
)

func waitBootstrapped(t *testing.T, conn *control.Conn, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		v, err := conn.GetInfo("status/bootstrap-phase")
		if err != nil {
			t.Fatal(err)
		}
		if last = v; strings.Contains(v, "PROGRESS=100 ") {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("tor did not reach bootstrap 100 in %s; last %q", timeout, last)
}

func TestRealTor_NetworkResetKeepsSocksAndOnions(t *testing.T) {
	bin := os.Getenv(realTorBinEnv)
	if bin == "" {
		t.Skipf("%s not set — skipping live tor test", realTorBinEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	inst, err := Bootstrap(ctx, Config{BinPath: bin, DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = inst.Stop(5 * time.Second) }()
	conn, err := control.Dial(ctx, inst.ControlAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Authenticate(""); err != nil {
		t.Fatal(err)
	}
	onion, err := conn.AddOnionNew([]control.OnionPort{{VirtPort: 80, Target: "127.0.0.1:9"}})
	if err != nil {
		t.Fatal(err)
	}
	withNet := os.Getenv(realTorNetEnv) == "1"
	if withNet {
		waitBootstrapped(t, conn, 3*time.Minute)
	} else {
		t.Logf("%s not set: bootstrap after the toggles is NOT checked", realTorNetEnv)
	}
	onions := func() string {
		t.Helper()
		v, err := conn.GetInfo("onions/current")
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	listeners := func() []string {
		t.Helper()
		l, err := conn.SocksListeners()
		if err != nil {
			t.Fatal(err)
		}
		return l
	}

	before := listeners()
	if _, err := conn.ToggleNetwork(); err != nil {
		t.Fatal(err)
	}
	moved := listeners()
	t.Logf("no pin: SOCKS %v -> %v", before, moved)
	if len(moved) != 1 || moved[0] == before[0] {
		t.Fatalf("without the pin the SOCKS listener stayed at %v; this tor does not move an auto port, so the pin is not tested", moved)
	}

	if err := conn.SetConf("SocksPort", "auto IsolateDestAddr"); err != nil {
		t.Fatal(err)
	}
	if got := listeners(); len(got) != 1 || got[0] != moved[0] {
		t.Fatalf("SocksPort back to auto moved the listener: %v -> %v", moved, got)
	}
	_, line, err := conn.PinSocksPort()
	if err != nil {
		t.Fatal(err)
	}
	if want := moved[0] + " IsolateDestAddr"; line != want {
		t.Errorf("pinned line %q, want %q", line, want)
	}
	if off, err := conn.ToggleNetwork(); err != nil || off {
		t.Fatalf("toggle: off=%v err=%v", off, err)
	}
	if got := listeners(); len(got) != 1 || got[0] != moved[0] {
		t.Errorf("with the pin the SOCKS listener moved: %v -> %v", moved, got)
	}
	if conf, err := conn.GetConf("SocksPort"); err != nil || len(conf) != 1 || !strings.HasSuffix(conf[0], " IsolateDestAddr") {
		t.Errorf("SocksPort after the pin = %q %v, want the flag kept", conf, err)
	}
	if got := onions(); !strings.Contains(got, onion.ServiceID) {
		t.Errorf("onions/current after the toggles = %q, want %s in it", got, onion.ServiceID)
	}
	if withNet {
		waitBootstrapped(t, conn, 3*time.Minute)
	}

	if _, line, err := conn.PinSocksPort(); err != nil || line != "" {
		t.Errorf("second pin = %q %v, want nothing to do", line, err)
	}

	c2, err := control.Dial(ctx, inst.ControlAddr)
	if err != nil {
		t.Fatalf("a new control connection after the toggles: %v", err)
	}
	defer c2.Close()
	if _, err := c2.Authenticate(""); err != nil {
		t.Fatal(err)
	}
}
