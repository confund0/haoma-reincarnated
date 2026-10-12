package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/logid"
)

const haomaDeliveryPushSource = "../haoma/delivery_push.go"

func TestFoldDeliveryLineMatchesHaoma(t *testing.T) {
	if got := stringConst(t, haomaDeliveryPushSource, "rowFirstEnvelopePushLine"); got != rowFirstEnvelopePushLine {
		t.Errorf("haoma's rowFirstEnvelopePushLine is %q, the rig uses %q", got, rowFirstEnvelopePushLine)
	}
}

const (
	foldPeerSeesA = "a0a0a0a0000000000000000000000001"
	foldPeerSeesB = "b0b0b0b0000000000000000000000002"
	foldEnvFirst  = "e1e1e1e1000000000000000000000001"
	foldEnvSecond = "e2e2e2e2000000000000000000000002"
)

func TestFoldFirstDevice(t *testing.T) {
	folded := ipc.ChatEntry{ChatID: "c1", PeerID: foldPeerSeesA, Members: []string{foldPeerSeesA, foldPeerSeesB}}
	other := ipc.ChatEntry{ChatID: "c2", PeerID: "c0c0c0c0000000000000000000000003", Members: []string{"c0c0c0c0000000000000000000000003"}}

	firstIsA, id, err := foldFirstDevice([]ipc.ChatEntry{other, folded}, foldPeerSeesA, foldPeerSeesB)
	if err != nil || !firstIsA || id != foldPeerSeesA {
		t.Fatalf("dev-a first: firstIsA=%v id=%q err=%v", firstIsA, id, err)
	}
	folded.PeerID = foldPeerSeesB
	firstIsA, id, err = foldFirstDevice([]ipc.ChatEntry{folded}, foldPeerSeesA, foldPeerSeesB)
	if err != nil || firstIsA || id != foldPeerSeesB {
		t.Fatalf("dev-b first: firstIsA=%v id=%q err=%v", firstIsA, id, err)
	}

	split := []ipc.ChatEntry{
		{ChatID: "ca", PeerID: foldPeerSeesA, Members: []string{foldPeerSeesA}},
		{ChatID: "cb", PeerID: foldPeerSeesB, Members: []string{foldPeerSeesB}},
	}
	if _, _, err := foldFirstDevice(split, foldPeerSeesA, foldPeerSeesB); err == nil || !strings.Contains(err.Error(), "no conversation with both") {
		t.Errorf("unfolded chats: err = %v, want the no-fold refusal", err)
	}
}

func foldLine(env, rowEnv, state string) string {
	return `time=2026-10-08T00:00:00Z level=DEBUG msg="` + rowFirstEnvelopePushLine + `" service=haoma envelope_id=` +
		logid.Short(env) + " row_envelope_id=" + logid.Short(rowEnv) + " state=" + state
}

func foldPass() foldDeliverySeen {
	return foldDeliverySeen{
		sentEnvID: foldEnvFirst,
		row: events.Event{
			EnvelopeID:    foldEnvFirst,
			EnvelopeIDs:   []string{foldEnvFirst, foldEnvSecond},
			DeliveryState: events.DeliverySent,
		},
		framed: true,
		lines:  []string{foldLine(foldEnvSecond, foldEnvFirst, events.DeliverySent)},
	}
}

func TestFoldDeliveryVerdict(t *testing.T) {
	if err := foldDeliveryVerdict(foldPass()); err != nil {
		t.Fatalf("the passing round failed: %v", err)
	}

	cases := []struct {
		name string
		edit func(s *foldDeliverySeen)
		want string
	}{
		{"no frame under the first envelope", func(s *foldDeliverySeen) { s.framed = false }, "no delivery_status"},
		{"no log line: the first device acked itself", func(s *foldDeliverySeen) { s.lines = nil }, "did not come from the second device"},
		{"the line names the first envelope as the acking one", func(s *foldDeliverySeen) {
			s.lines = []string{foldLine(foldEnvFirst, foldEnvFirst, events.DeliverySent)}
		}, "did not come from the second device"},
		{"the line is for another row", func(s *foldDeliverySeen) {
			s.lines = []string{foldLine(foldEnvSecond, "f0f0f0f0000000000000000000000009", events.DeliverySent)}
		}, "did not come from the second device"},
		{"a fan-out of one", func(s *foldDeliverySeen) {
			s.row.EnvelopeIDs = []string{foldEnvFirst}
		}, "holds 1 envelope"},
		{"text_sent named the second envelope", func(s *foldDeliverySeen) { s.sentEnvID = foldEnvSecond }, "starts with"},
		{"the row is not sent", func(s *foldDeliverySeen) { s.row.DeliveryState = events.DeliveryEnqueued }, "want sent or higher"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := foldPass()
			c.edit(&s)
			err := foldDeliveryVerdict(s)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want one naming %q", err, c.want)
			}
		})
	}
}

func TestEnvelopeReportLineMatchesHaoma(t *testing.T) {
	b, err := os.ReadFile("../haoma/relay.go")
	if err != nil {
		t.Fatal(err)
	}
	want := `slog.Debug(` + strconv.Quote(envelopeReportLine) + `,
					slog.String("envelope_id", ds.EnvelopeID),
					slog.String("state", ds.State),`
	if !strings.Contains(string(b), want) {
		t.Errorf("relay.go no longer writes %s with envelope_id and state", strconv.Quote(envelopeReportLine))
	}
}

func TestAwaitFirstEnvelopeSentReadsTheReportLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "haoma.log")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	mark := markLog(path)
	line := func(env, state string) string {
		return `time=2026-10-08T00:00:00Z level=DEBUG msg="` + envelopeReportLine + `" service=haoma envelope_id=` +
			logid.Short(env) + " state=" + state + "\n"
	}
	body := line(foldEnvSecond, events.DeliverySent) + line(foldEnvFirst, events.DeliverySent)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	awaitFirstEnvelopeSent(&rig{name: "peer"}, &rig{name: "dev-a"}, mark, foldEnvFirst)
	if d := time.Since(start); d > time.Second {
		t.Errorf("the wait took %s with the line already there, want it to return at once", d)
	}
	if !(lineWant{envelopeReportLine, map[string]string{"envelope_id": logid.Short(foldEnvFirst), "state": "sent"}}).matches(strings.TrimSpace(line(foldEnvFirst, "sent"))) {
		t.Error("the report line for the first envelope does not match")
	}
	if (lineWant{envelopeReportLine, map[string]string{"envelope_id": logid.Short(foldEnvFirst), "state": "sent"}}).matches(strings.TrimSpace(line(foldEnvSecond, "sent"))) {
		t.Error("a report line for another envelope matches")
	}
}
