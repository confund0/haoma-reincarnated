package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCountInboundFrom_CountsTheLogsShortForm(t *testing.T) {
	const peer = "30162203697a50dcadc6bfa5102e3068"
	const other = "aaaaaaaa97a50dcadc6bfa5102e3068b"
	root := t.TempDir()
	at := func(d time.Duration) string {
		return time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC).Add(d).Format("2006-01-02T15:04:05.000Z")
	}
	line := func(ts, id string) string {
		return "time=" + ts + ` level=INFO msg="` + inboundEnvelopeLine + `" service=haomad peer_id=` + id[:8] + "…\n"
	}
	log := line(at(0), peer) + line(at(2*time.Second), peer) + line(at(3*time.Second), other) +
		"time=" + at(4*time.Second) + ` level=INFO msg="something else" service=haomad peer_id=` + peer[:8] + "…\n"
	if err := os.WriteFile(filepath.Join(root, "haomad.log"), []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &rig{name: "dev-b", root: root}
	since := time.Date(2026, 10, 2, 10, 0, 1, 0, time.UTC)
	n, err := countInboundFrom(r, peer, since)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("countInboundFrom = %d, want 1: one envelope from the peer after the mark", n)
	}
	if n, _ := countInboundFrom(r, peer, since.Add(-time.Hour)); n != 2 {
		t.Fatalf("countInboundFrom over the whole log = %d, want 2", n)
	}
}
