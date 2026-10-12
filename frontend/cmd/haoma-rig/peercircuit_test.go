package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	haomadStallFaultSource = "../../../backend/cmd/haomad/circuit_stall_fault.go"
	haomadRenewSource      = "../../../backend/cmd/haomad/circuit_renew.go"
)

func TestPeerCircuitNamesAndLinesMatchHaomad(t *testing.T) {
	for _, c := range []struct{ src, name, want string }{
		{haomadStallFaultSource, "stallPeerCircuitEnv", stallPeerCircuitEnv},
		{haomadStallFaultSource, "stallFaultOnLine", stallFaultOnLine},
		{haomadStallFaultSource, "stallFaultArmedLine", stallFaultArmedLine},
		{haomadStallFaultSource, "stallFaultStalledLine", stallFaultStalledLine},
		{haomadStallFaultSource, "stallFaultThroughLine", stallFaultThroughLine},
		{haomadRenewSource, "circuitRenewedLine", peerCircuitRenewLine},
		{haomadRenewSource, "routeFailNoAnswer", routeFailNoAnswer},
		{"../../../backend/internal/outbox/outbox.go", "resendOnNewCircuitLine", resendOnNewCircuitLine},
	} {
		if got := stringConst(t, c.src, c.name); got != c.want {
			t.Errorf("haomad's %s is %q, the rig uses %q", c.name, got, c.want)
		}
	}
}

func TestHaomadEnvClearsTheStallFault(t *testing.T) {
	t.Setenv(stallPeerCircuitEnv, "frompeer")
	envOf := func(r *rig) string {
		v := ""
		for _, kv := range childEnv(r.haomadEnv()) {
			if k, val, _ := strings.Cut(kv, "="); k == stallPeerCircuitEnv {
				v = val
			}
		}
		return v
	}
	if got := envOf(&rig{name: "dev-a"}); got != "" {
		t.Errorf("off: %s=%q, want empty whatever the shell holds", stallPeerCircuitEnv, got)
	}
	if got := envOf(&rig{name: "dev-a", stallPeerCircuit: "abcdef0123456789"}); got != "abcdef0123456789" {
		t.Errorf("on: %s=%q, want the peer id", stallPeerCircuitEnv, got)
	}
}

func line(msg, peerID string) string {
	return `time=2026-10-02T10:00:00.000Z level=INFO msg="` + msg + `" service=haomad peer_id=` + peerID[:8] + "…"
}

func renewLine(peerID, reason, proof string) string {
	return `time=2026-10-02T10:00:00.000Z level=INFO msg="` + peerCircuitRenewLine + `" service=haomad peer_id=` + peerID[:8] + `… reason="` + reason + `" proof="` + proof + `" failures=1 renewals=1 tag=1`
}

func resendLine(envID string) string {
	return `time=2026-10-02T10:00:00.000Z level=DEBUG msg="` + resendOnNewCircuitLine + `" service=haomad envelope_id=` + envID[:8] + `… dest=http://abcdefgh…`
}

const (
	verdictPeer = "abcdef0123456789abcdef"
	verdictEnv  = "e9e9e9e9aaaaaaaabbbbbbbbcccccccc"
)

func passLines() []string {
	return []string{
		line(stallFaultOnLine, verdictPeer),
		line(stallFaultArmedLine, verdictPeer),
		line(stallFaultStalledLine, verdictPeer),
		renewLine(verdictPeer, routeFailNoAnswer, "inbound envelope"),
		resendLine(verdictEnv),
		line(stallFaultThroughLine, verdictPeer),
	}
}

func without(lines []string, drop string) []string {
	var out []string
	for _, l := range lines {
		if !strings.Contains(l, drop) {
			out = append(out, l)
		}
	}
	return out
}

func TestPeerCircuitVerdict(t *testing.T) {
	const other = "99999999aaaaaaaa"
	pass := passLines()
	if err := peerCircuitVerdict(pass, verdictPeer, verdictEnv, true); err != nil {
		t.Fatalf("a run where the cut renewed and the text went again at once failed: %v", err)
	}

	renewalOff := []string{
		line(stallFaultOnLine, verdictPeer),
		line(stallFaultArmedLine, verdictPeer),
		line(stallFaultStalledLine, verdictPeer),
		line(stallFaultStalledLine, verdictPeer),
		line(stallFaultStalledLine, verdictPeer),
	}
	err := peerCircuitVerdict(renewalOff, verdictPeer, verdictEnv, false)
	if err == nil {
		t.Fatal("the fault with the renewal off passed the check")
	}
	if !strings.Contains(err.Error(), "no automatic new tor circuit") {
		t.Fatalf("error %q does not name the missing renewal", err)
	}

	sixty := without(without(pass, resendOnNewCircuitLine), peerCircuitRenewLine)
	sixty = append(sixty, renewLine(verdictPeer, "timeout", "inbound envelope"))

	for name, c := range map[string]struct {
		lines []string
		held  bool
		want  string
	}{
		"never armed":               {without(pass, stallFaultArmedLine), true, "never armed"},
		"nothing stalled":           {without(pass, stallFaultStalledLine), true, "no request stalled"},
		"renewal of another":        {append(without(pass, peerCircuitRenewLine), renewLine(other, routeFailNoAnswer, "inbound envelope")), true, "no automatic new tor circuit"},
		"two renewals":              {append(passLines(), renewLine(verdictPeer, routeFailNoAnswer, "inbound envelope")), true, "2 automatic new tor circuits"},
		"text not held":             {pass, false, "did not hold"},
		"renewed, tag did not move": {without(pass, stallFaultThroughLine), false, "did not move the tag"},
		"proof was the self-probe":  {append(without(pass, peerCircuitRenewLine), renewLine(verdictPeer, routeFailNoAnswer, "self-probe")), true, "not the inbound envelope"},
		"the 60 s path":             {sixty, true, "not the 30 s cut's"},
		"no send again at once":     {without(pass, resendOnNewCircuitLine), true, "did not send the text again"},
		"send again of another row": {append(without(pass, resendOnNewCircuitLine), resendLine("ffffffff00000000")), true, "did not send the text again"},
	} {
		t.Run(name, func(t *testing.T) {
			err := peerCircuitVerdict(c.lines, verdictPeer, verdictEnv, c.held)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want one naming %q", err, c.want)
			}
		})
	}
}

func TestPeerCircuitLines_WaitsForTheThroughLine(t *testing.T) {
	const peer = "87dc75de0123456789abcdef01234567"
	at := func(ts, msg, rest string) string {
		return "time=2026-10-02T" + ts + `Z level=INFO msg="` + msg + `" service=haomad peer_id=` + peer[:8] + "…" + rest + "\n"
	}
	first := at("15:36:26.678", stallFaultOnLine, "") +
		at("15:37:30.870", stallFaultArmedLine, "") +
		at("15:37:31.440", stallFaultStalledLine, "") +
		at("15:38:31.441", peerCircuitRenewLine, ` reason="`+routeFailNoAnswer+`" proof="inbound envelope" failures=1 renewals=1 tag=1`) +
		resendLine(verdictEnv) + "\n"
	last := at("15:38:31.659", stallFaultThroughLine, "")

	path := filepath.Join(t.TempDir(), "haomad.log")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	mark := markLog(path)
	if err := os.WriteFile(path, []byte(first), 0o600); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(218 * time.Millisecond)
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			return
		}
		_, _ = f.WriteString(last)
		_ = f.Close()
	}()

	early, err := mark.lines()
	if err != nil {
		t.Fatal(err)
	}
	if err := peerCircuitVerdict(early, peer, verdictEnv, true); err == nil || !strings.Contains(err.Error(), "no request under a new tag") {
		t.Fatalf("the early read: err = %v, want the failure that run reported", err)
	}

	lines, err := peerCircuitLines(mark, peer, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 6 {
		t.Fatalf("read %d lines, want the five and the outbox's send again", len(lines))
	}
	if err := peerCircuitVerdict(lines, peer, verdictEnv, true); err != nil {
		t.Fatalf("the five lines of a run that passed failed the verdict: %v", err)
	}
}

func TestPeerCircuitLines_LineThatNeverComes(t *testing.T) {
	const peer = "87dc75de0123456789abcdef01234567"
	path := filepath.Join(t.TempDir(), "haomad.log")
	mark := markLog(path)
	body := `time=2026-10-02T15:38:31.441Z level=INFO msg="` + peerCircuitRenewLine + `" service=haomad peer_id=` + peer[:8] + `… proof="inbound envelope"` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	lines, err := peerCircuitLines(mark, peer, 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 300*time.Millisecond {
		t.Fatal("returned before the wait without the line")
	}
	if err := peerCircuitVerdict(lines, peer, verdictEnv, true); err == nil {
		t.Fatal("the verdict passed without the line")
	}
}
