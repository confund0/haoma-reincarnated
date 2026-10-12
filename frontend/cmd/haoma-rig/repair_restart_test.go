package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/logging"
	"haoma-frontend/internal/logid"
)

func TestRepairRestart_LogMessagesMatchHaomaSource(t *testing.T) {
	cases := map[string]string{
		reattachedWaitMsg:     "../haoma/repair_reattach.go",
		invitationAnsweredMsg: "../haoma/repair.go",
	}
	for msg, path := range cases {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !strings.Contains(string(raw), "slog.Info("+strconv.Quote(msg)) {
			t.Errorf("%s no longer logs %q at INFO — the rig check that reads it would read nothing", path, msg)
		}
	}
}

func TestRepairRestart_CountsTheLineForThisInvitationOnly(t *testing.T) {
	dir := t.TempDir()
	logger, closeLog, err := logging.New(logging.Config{
		Level: "info", File: filepath.Join(dir, "haoma.log"), Service: "haoma",
	})
	if err != nil {
		t.Fatal(err)
	}
	const mine, other = "7c1e4a09d3b2458f96e0a1b7c42d5f38", "0f9a3c61e7d24b58a1c3e90b7d46f215"
	logger.Info(reattachedWaitMsg,
		slog.String("invitation_id", mine),
		slog.String("handle_id", "3b8e1f0c9a7d4e26"),
		slog.String("fingerprint", "05c1e84f27b30a95d6e1483fbc70a2d59e63817f04ab52c9d381e7f6a04b3c95e2"),
		slog.Int64("expires_at", 1755508620),
	)
	logger.Info(reattachedWaitMsg, slog.String("invitation_id", other))
	const paired = "d2b7f40c19e84a3695c0e1f7a8b35d26"
	logger.Info(invitationAnsweredMsg,
		slog.String("invitation_id", mine),
		slog.String("peer_id", paired),
		slog.String("chat_id", "aa11bb22cc33dd44ee55ff6600778890"),
	)
	logger.Info("invitation: waiting for the person to dial", slog.String("invitation_id", mine))
	if err := closeLog(); err != nil {
		t.Fatal(err)
	}

	r := &rig{name: "slave", root: dir}
	for _, c := range []struct {
		msg, id string
		want    int
	}{
		{reattachedWaitMsg, mine, 1},
		{reattachedWaitMsg, other, 1},
		{reattachedWaitMsg, "ffffffffffffffffffffffffffffffff", 0},
		{invitationAnsweredMsg, mine, 1},
	} {
		got, err := countLogLines(r, c.msg, "invitation_id", c.id)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("%q naming %s: counted %d, want %d", c.msg, c.id, got, c.want)
		}
	}

	logged, found, err := answeredPeerID(r, mine)
	if err != nil || !found {
		t.Fatalf("the completion line for this invitation was not found: %v", err)
	}
	if logged != logid.Short(paired) {
		t.Fatalf("the completion line names %q, want %q", logged, logid.Short(paired))
	}
	if _, found, _ := answeredPeerID(r, other); found {
		t.Fatal("a completion line was found for an invitation that never completed")
	}
}

func TestRepairRestart_LivePairingsExactly(t *testing.T) {
	r := &rig{name: "peer"}
	peers := []ipc.PeerEntry{
		{ID: "master-pairing"},
		{ID: "slave-pairing"},
		{ID: "my-other-device", Sibling: true},
		{ID: "old-pairing", RetiredAt: 1754900000},
	}
	if err := assertLivePairingsExactly(r, peers, "master-pairing", "slave-pairing"); err != nil {
		t.Fatalf("two live pairings, a sibling and a retired one were refused: %v", err)
	}
	if err := assertLivePairingsExactly(r, peers, "master-pairing"); err == nil {
		t.Fatal("a second live pairing passed a check that wants one")
	}
	if err := assertLivePairingsExactly(r, peers, "master-pairing", "someone-else"); err == nil {
		t.Fatal("a pairing this run never made passed")
	}
	if err := assertLivePairingsExactly(r, peers, "master-pairing", ""); err == nil {
		t.Fatal("an unnamed pairing passed")
	}
}
