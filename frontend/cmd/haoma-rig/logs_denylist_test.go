package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	refusalLine     = `time=2026-08-17T15:05:00.100+01:00 level=DEBUG msg=ratchet.op service=haoma dir=decrypt peer=6fee9137… op_err="failed to get or create message keys: received message with old counter (index: 2, count: 1)"`
	refusalLineFull = `time=2026-08-17T15:05:00.100+01:00 level=DEBUG msg=ratchet.op service=haoma dir=decrypt peer=6fee9137002cabf79411888e48e670c4 op_err="failed to get or create message keys: received message with old counter (index: 2, count: 1)"`
	benignLine      = `time=2026-08-17T15:05:00.100+01:00 level=DEBUG msg="inbox decrypt skipped: benign old-counter re-delivery" service=haoma envelope_id=89b2a99f… peer_id=6fee9137…`
)

func scanLines(t *testing.T, lines ...string) logScan {
	t.Helper()
	dir := t.TempDir()
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "haoma.log"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return scanRigLogs(&rig{root: dir})
}

func TestARecognisedDuplicateIsNotADenylistHit(t *testing.T) {
	s := scanLines(t, refusalLine, benignLine)
	if len(s.denySamples) != 0 {
		t.Fatalf("denylist hits = %d, want 0 — a duplicate the daemon recognised and dropped fails the run: %v",
			len(s.denySamples), s.denySamples)
	}
}

func TestARecognisedDuplicateOfAnOlderLogIsNotADenylistHit(t *testing.T) {
	s := scanLines(t, refusalLineFull, benignLine)
	if len(s.denySamples) != 0 {
		t.Fatalf("denylist hits = %d, want 0 — a duplicate the daemon recognised and dropped fails the run: %v",
			len(s.denySamples), s.denySamples)
	}
}

func TestARefusalTheDaemonDidNotExplainStillFailsTheRun(t *testing.T) {
	s := scanLines(t, refusalLine)
	if len(s.denySamples) != 1 {
		t.Fatalf("denylist hits = %d, want 1 — a ratchet that came apart on its own passes the gate", len(s.denySamples))
	}
}

func TestAnotherPeersDuplicateDoesNotClearThisOne(t *testing.T) {
	other := strings.Replace(benignLine, "peer_id=6fee9137…", "peer_id=11223344…", 1)
	s := scanLines(t, refusalLine, other)
	if len(s.denySamples) != 1 {
		t.Fatalf("denylist hits = %d, want 1 — one peer's recognised duplicate cleared a different peer's refusal", len(s.denySamples))
	}
}

func TestADuplicateFarFromTheRefusalDoesNotClearIt(t *testing.T) {
	late := strings.Replace(benignLine, "T15:05:00.100", "T15:07:00.100", 1)
	s := scanLines(t, refusalLine, late)
	if len(s.denySamples) != 1 {
		t.Fatalf("denylist hits = %d, want 1 — an unrelated duplicate minutes away cleared this refusal", len(s.denySamples))
	}
}

func TestTheOtherSignaturesAreUnaffected(t *testing.T) {
	line := `time=2026-08-17T15:05:00.100+01:00 level=DEBUG msg="rail" peer=6fee9137002cabf79411888e48e670c4 err="no valid session"`
	s := scanLines(t, line, benignLine)
	if len(s.denySamples) != 1 {
		t.Fatalf("denylist hits = %d, want 1 — the pairing cleared a signature it has no business clearing", len(s.denySamples))
	}
}
