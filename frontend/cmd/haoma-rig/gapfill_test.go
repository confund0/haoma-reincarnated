package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/logid"
)

func TestObserveRows(t *testing.T) {
	body, err := json.Marshal(events.TextBody{Text: "after the rewrite"})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	rows := map[string]events.Event{
		"aaaaaaaabbbbbbbb": {
			MsgID:               "aaaaaaaabbbbbbbb",
			ChatID:              chat.ChatID("ccccccccdddddddd"),
			Body:                body,
			EditedAt:            1755000000,
			MirroredFromSibling: true,
		},
	}

	for _, tc := range []struct {
		name  string
		rows  map[string]events.Event
		err   error
		watch []string
		want  []string
	}{
		{
			name:  "read failed names the error and nothing else",
			err:   errors.New("await timeline_page: deadline"),
			watch: []string{"aaaaaaaabbbbbbbb"},
			want:  []string{"read failed", "deadline"},
		},
		{
			name:  "a row that never arrived says so",
			rows:  rows,
			watch: []string{"eeeeeeeeffffffff"},
			want:  []string{"1 row(s)", "absent"},
		},
		{
			name:  "a row that did arrive carries the fields the assertion reads",
			rows:  rows,
			watch: []string{"aaaaaaaabbbbbbbb"},
			want: []string{
				"chat=cccccccc", "edited_at=1755000000", "deleted_at=0",
				"mirrored=true", "after the rewrite",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := observeRows(tc.rows, tc.err, tc.watch)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("observation %q is missing %q", got, want)
				}
			}
		})
	}
}

func TestCountOutboxFailed(t *testing.T) {
	dir := t.TempDir()
	daemonLog := "" +
		"time=2026-08-13T00:00:00Z level=DEBUG msg=\"outbox: rescheduling\" envelope_id=e1 attempts=1\n" +
		"time=2026-08-13T00:00:01Z level=DEBUG msg=\"outbox: transition\" envelope_id=e1 from=enqueued to=sent\n" +
		"time=2026-08-13T00:00:02Z level=DEBUG msg=\"outbox: transition\" envelope_id=e2 from=enqueued to=failed\n" +
		"time=2026-08-13T00:00:03Z level=DEBUG msg=\"outbox: transition\" envelope_id=e3 from=enqueued to=failed\n"
	if err := os.WriteFile(filepath.Join(dir, "haomad.log"), []byte(daemonLog), 0o600); err != nil {
		t.Fatalf("write haomad.log: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "haoma.log"), []byte(
		"time=2026-08-13T00:00:04Z level=DEBUG msg=\"outbox: transition\" envelope_id=e4 from=enqueued to=failed\n",
	), 0o600); err != nil {
		t.Fatalf("write haoma.log: %v", err)
	}

	r := &rig{name: "peer", root: dir}
	got, err := countOutboxFailed(r)
	if err != nil {
		t.Fatalf("countOutboxFailed: %v", err)
	}
	if got != 2 {
		t.Errorf("counted %d dead-letter(s), want 2 (the sent transition and the frontend-log entry must not count)", got)
	}
}

func TestCountOutboxFailedMissingLog(t *testing.T) {
	r := &rig{name: "peer", root: t.TempDir()}
	if _, err := countOutboxFailed(r); err == nil {
		t.Error("a rig directory with no haomad.log must error, not report zero as if the daemon simply had nothing to say")
	}
}

func TestTruncateMarksWhatItCut(t *testing.T) {
	if got := truncate("short", 40); got != "short" {
		t.Errorf("a string within the limit must survive whole, got %q", got)
	}
	got := truncate(strings.Repeat("x", 50), 40)
	if len([]rune(got)) != 41 || !strings.HasSuffix(got, "…") {
		t.Errorf("an over-long string must be cut to the limit and marked, got %q", got)
	}
}

func shortIDLine(msg string, kv ...string) string {
	line := "time=2026-08-13T00:00:00Z level=DEBUG msg=\"" + msg + "\""
	for i := 0; i+1 < len(kv); i += 2 {
		v := kv[i+1]
		if strings.HasSuffix(kv[i], "_id") {
			v = logid.Short(v)
		}
		line += " " + kv[i] + "=" + v
	}
	return line + "\n"
}

const (
	envToDevA = "1401f10c00000000000000000000aaaa"
	envToDevB = "9f2c77b100000000000000000000bbbb"
	peerDevA  = "5c7b289700000000000000000000aaaa"
	peerDevB  = "e3a10dd400000000000000000000bbbb"
)

func TestEnqueuedEnvelopeForPicksTheRightDevice(t *testing.T) {
	dir := t.TempDir()
	daemonLog := shortIDLine("/send enqueuing envelope", "envelope_id", envToDevA, "peer_id", peerDevA, "dest", "http://aaa.onion") +
		shortIDLine("/send enqueuing envelope", "envelope_id", envToDevB, "peer_id", peerDevB, "dest", "http://bbb.onion")
	if err := os.WriteFile(filepath.Join(dir, "haomad.log"), []byte(daemonLog), 0o600); err != nil {
		t.Fatalf("write haomad.log: %v", err)
	}
	r := &rig{name: "peer", root: dir}
	send := []string{envToDevA, envToDevB}

	got, err := enqueuedEnvelopeFor(r, send, peerDevB)
	if err != nil {
		t.Fatalf("enqueuedEnvelopeFor: %v", err)
	}
	if got != envToDevB {
		t.Fatalf("picked %q for dev-b, want dev-b's own envelope", got)
	}

	got, err = enqueuedEnvelopeFor(r, send, peerDevA)
	if err != nil {
		t.Fatalf("enqueuedEnvelopeFor: %v", err)
	}
	if got != envToDevA {
		t.Fatalf("picked %q for dev-a, want dev-a's own envelope", got)
	}
}

func TestEnqueuedEnvelopeForSeparatesWhatItCannotSeeFromWhatTheSendDid(t *testing.T) {
	send := []string{envToDevA, envToDevB}

	blind := t.TempDir()
	if err := os.WriteFile(filepath.Join(blind, "haomad.log"), []byte(
		shortIDLine("/send enqueuing envelope", "envelope_id", "deadbeef00000000000000000000cccc", "peer_id", peerDevA),
	), 0o600); err != nil {
		t.Fatalf("write haomad.log: %v", err)
	}
	_, err := enqueuedEnvelopeFor(&rig{name: "peer", root: blind}, send, peerDevB)
	if err == nil {
		t.Fatal("a log holding nothing about this send must refuse")
	}
	if !strings.Contains(err.Error(), "harness cannot see") {
		t.Errorf("verdict must not blame the send when the harness could not read it: %v", err)
	}

	pinned := t.TempDir()
	if err := os.WriteFile(filepath.Join(pinned, "haomad.log"), []byte(
		shortIDLine("/send enqueuing envelope", "envelope_id", envToDevA, "peer_id", peerDevA),
	), 0o600); err != nil {
		t.Fatalf("write haomad.log: %v", err)
	}
	_, err = enqueuedEnvelopeFor(&rig{name: "peer", root: pinned}, send, peerDevB)
	if err == nil {
		t.Fatal("a send that addressed no envelope to dev-b must refuse")
	}
	if !strings.Contains(err.Error(), "did not address that device") {
		t.Errorf("verdict must name the send: %v", err)
	}
}

func TestEnqueuedEnvelopeForRefusesASharedPrefix(t *testing.T) {
	dir := t.TempDir()
	collider := envToDevB[:8] + "ffffffffffffffffffffffff"
	if collider == envToDevB {
		t.Fatal("fixture error: the collider must be a different id sharing the prefix")
	}
	daemonLog := shortIDLine("/send enqueuing envelope", "envelope_id", envToDevB, "peer_id", peerDevB) +
		shortIDLine("/send enqueuing envelope", "envelope_id", collider, "peer_id", peerDevA)
	if err := os.WriteFile(filepath.Join(dir, "haomad.log"), []byte(daemonLog), 0o600); err != nil {
		t.Fatalf("write haomad.log: %v", err)
	}
	r := &rig{name: "peer", root: dir}
	_, err := enqueuedEnvelopeFor(r, []string{envToDevA, envToDevB}, peerDevB)
	if err == nil {
		t.Fatal("a prefix carried by two envelopes must refuse — waiting on it could answer for the wrong send")
	}
	if !strings.Contains(err.Error(), "shares that prefix") {
		t.Errorf("verdict must name the collision: %v", err)
	}
}

func TestEnvelopeDeadLetteredReadsIdentityNotCount(t *testing.T) {
	dir := t.TempDir()
	daemonLog := shortIDLine("outbox: transition", "envelope_id", "deadbeef00000000000000000000cccc", "from", "enqueued", "to", "failed") +
		shortIDLine("outbox: transition", "envelope_id", envToDevB, "from", "enqueued", "to", "sent")
	if err := os.WriteFile(filepath.Join(dir, "haomad.log"), []byte(daemonLog), 0o600); err != nil {
		t.Fatalf("write haomad.log: %v", err)
	}
	r := &rig{name: "peer", root: dir}

	dead, err := envelopeDeadLettered(r, envToDevB)
	if err != nil {
		t.Fatalf("envelopeDeadLettered: %v", err)
	}
	if dead {
		t.Fatal("reported dev-b's route closed — the only failed line belongs to another send")
	}

	daemonLog += shortIDLine("outbox: transition", "envelope_id", envToDevB, "from", "enqueued", "to", "failed")
	if err := os.WriteFile(filepath.Join(dir, "haomad.log"), []byte(daemonLog), 0o600); err != nil {
		t.Fatalf("rewrite haomad.log: %v", err)
	}
	dead, err = envelopeDeadLettered(r, envToDevB)
	if err != nil {
		t.Fatalf("envelopeDeadLettered: %v", err)
	}
	if !dead {
		t.Fatal("dev-b's envelope dead-lettered and the wait did not see it")
	}
}

func TestEnvelopeDeadLetteredFindsALineWrittenBeforeItLooked(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "haomad.log"), []byte(
		shortIDLine("outbox: transition", "envelope_id", envToDevB, "from", "enqueued", "to", "failed"),
	), 0o600); err != nil {
		t.Fatalf("write haomad.log: %v", err)
	}
	r := &rig{name: "peer", root: dir}
	dead, err := envelopeDeadLettered(r, envToDevB)
	if err != nil {
		t.Fatalf("envelopeDeadLettered: %v", err)
	}
	if !dead {
		t.Fatal("a line written before the first look is still the right answer")
	}
}

func TestWaitForEnvelopeGiveUpRefusesWithNothingToWaitOn(t *testing.T) {
	r := &rig{name: "peer", root: t.TempDir(), giveUp: "30s"}
	if err := waitForEnvelopeGiveUp(r, ""); err == nil {
		t.Error("a wait with no envelope id must refuse, not report the route closed")
	}
}
