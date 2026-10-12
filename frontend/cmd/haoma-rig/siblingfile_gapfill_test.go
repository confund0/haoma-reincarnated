package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"haoma-frontend/internal/logid"
)

const (
	haomaServeSource     = "../haoma/gapfill_file_serve.go"
	haomaAskSource       = "../haoma/gapfill_file_ask.go"
	mirrorDispatchSource = "../../internal/mirror/dispatch.go"
)

const (
	gapMsgID   = "7d3e1c9a00000000000000000000dddd"
	otherMsgID = "7d3e1c9b00000000000000000000eeee"
)

func TestFileRequestFaultNameMatchesHaoma(t *testing.T) {
	if got := stringConst(t, haomaFaultsSource, "ignoreFileRequestEnv"); got != seedIgnoreFileRequestEnv {
		t.Fatalf("haoma reads %q, the rig sets %q", got, seedIgnoreFileRequestEnv)
	}
}

func TestSealRoutesAreWhatHaomaLogs(t *testing.T) {
	if got := stringConst(t, haomaSiblingSource, "siblingRouteSend"); got != sealRouteSend {
		t.Errorf("haoma names the route at send %q, the rig expects %q", got, sealRouteSend)
	}
	if got := stringConst(t, haomaSiblingSource, "siblingRouteGapFill"); got != sealRouteGapFill {
		t.Errorf("haoma names the route on gap-fill %q, the rig expects %q", got, sealRouteGapFill)
	}
	if sealRouteSend == sealRouteGapFill {
		t.Fatal("the two routes read the same; the route check could not tell them apart")
	}
	src, err := os.ReadFile(haomaSiblingSource)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `slog.String("route", siblingRoute(meta))`) {
		t.Errorf("the seal line in %s no longer carries route=; the route check reads that field", haomaSiblingSource)
	}
}

func TestGapFillPathLinesAreWhatHaomaLogs(t *testing.T) {
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("cannot read %s: %v", p, err)
		}
		return string(b)
	}
	serve, ask, dispatch := read(haomaServeSource), read(haomaAskSource), read(mirrorDispatchSource)
	for _, c := range []struct{ src, file, line string }{
		{serve, haomaServeSource, fileServedLine},
		{serve, haomaServeSource, fileNotServedLine},
		{read(haomaFaultsSource), haomaFaultsSource, fileRequestIgnoredLine},
		{ask, haomaAskSource, fileRequestSentLine},
		{ask, haomaAskSource, fileRequestIDsLine},
		{dispatch, mirrorDispatchSource, siblingOfferSentLine},
	} {
		if !strings.Contains(c.src, strconv.Quote(c.line)) {
			t.Errorf("%s no longer logs %q", c.file, c.line)
		}
	}
	if sib := read(haomaSiblingSource); !strings.Contains(sib, strconv.Quote(siblingFetchEventLine)) {
		t.Errorf("%s no longer logs %q", haomaSiblingSource, siblingFetchEventLine)
	}
	if !strings.Contains(ask, `"`+gapDownloadFailedPrefix) {
		t.Errorf("%s no longer logs a line starting %q", haomaAskSource, gapDownloadFailedPrefix)
	}
}

func sealLine(msgID string, fields ...string) string {
	return strings.TrimSuffix(shortIDLine(siblingSealedLine, append([]string{"msg_id", msgID, "outcome", "sealed"}, fields...)...), "\n")
}

func TestCheckSealRoute(t *testing.T) {
	r := &rig{name: "dev-b"}
	cases := []struct {
		name      string
		lines     []string
		want      string
		found     bool
		failsWith string
	}{
		{"sealed at send, the case at send", []string{sealLine(gapMsgID, "route", sealRouteSend)}, sealRouteSend, true, ""},
		{"sealed on gap-fill, the case on gap-fill", []string{sealLine(gapMsgID, "route", sealRouteGapFill)}, sealRouteGapFill, true, ""},
		{"the offer at send was ignored and gap-fill brought it", []string{sealLine(gapMsgID, "route", sealRouteGapFill)}, sealRouteSend, true, "route=gapfill, want route=send"},
		{"gap-fill won the race at send, and the text says it may be one", []string{sealLine(gapMsgID, "route", sealRouteGapFill)}, sealRouteSend, true, "this is a race, not a fault"},
		{"the held-out offer at send landed late", []string{sealLine(gapMsgID, "route", sealRouteSend)}, sealRouteGapFill, true, "route=send, want route=gapfill"},
		{"sealed twice, once by each route", []string{sealLine(gapMsgID, "route", sealRouteSend), sealLine(gapMsgID, "route", sealRouteGapFill)}, sealRouteSend, true, "route=gapfill"},
		{"a seal line with no route", []string{sealLine(gapMsgID)}, sealRouteGapFill, true, "route=(none)"},
		{"only another file was sealed", []string{sealLine(otherMsgID, "route", sealRouteGapFill)}, sealRouteGapFill, false, ""},
		{"no seal line at all", []string{shortIDLine("sibling fetch: haomad event", "msg_id", gapMsgID)}, sealRouteGapFill, false, ""},
		{"a longer message that holds the seal words", []string{strings.Replace(sealLine(gapMsgID, "route", sealRouteGapFill), siblingSealedLine, siblingSealedLine+" again", 1)}, sealRouteGapFill, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			found, err := checkSealRoute(r, c.lines, gapMsgID, c.want)
			if found != c.found {
				t.Errorf("found=%t, want %t", found, c.found)
			}
			switch {
			case c.failsWith == "" && err != nil:
				t.Errorf("want no failure, got %v", err)
			case c.failsWith != "" && err == nil:
				t.Errorf("want a failure naming %q, got a pass", c.failsWith)
			case c.failsWith != "" && !strings.Contains(err.Error(), c.failsWith):
				t.Errorf("failure %q does not name %q", err, c.failsWith)
			}
		})
	}
}

func TestAwaitSealRouteLooksOnce(t *testing.T) {
	r := writeReceiverLog(t, "dev-b", sealLine(gapMsgID, "route", sealRouteSend))
	if err := awaitSealRoute(r, gapMsgID, sealRouteSend, 0); err != nil {
		t.Errorf("want a pass, got %v", err)
	}
	if err := awaitSealRoute(r, gapMsgID, sealRouteGapFill, 0); err == nil || !strings.Contains(err.Error(), "want route=gapfill") {
		t.Errorf("want the wrong route named, got %v", err)
	}
	if err := awaitSealRoute(r, otherMsgID, sealRouteGapFill, 0); err == nil || !strings.Contains(err.Error(), "has no") {
		t.Errorf("want the missing line named, got %v", err)
	}
}

func TestFileServed(t *testing.T) {
	served := func(msgID, outcome string) string {
		return shortIDLine(fileServedLine, "sibling_peer_id", peerDevB, "msg_id", msgID, "outcome", outcome, "staging_id", "abcdef0123456789")
	}
	cases := []struct {
		name string
		log  string
		want bool
	}{
		{"served a new blob", served(gapMsgID, "served"), true},
		{"sent the kept offer again", served(gapMsgID, "resent"), true},
		{"served another file", served(otherMsgID, "served"), false},
		{"not served: no sealed copy yet", shortIDLine(fileNotServedLine, "msg_id", gapMsgID, "outcome", "no_sealed_copy"), false},
		{"ignored by the seeded fault", shortIDLine(fileRequestIgnoredLine, "sibling_peer_id", peerDevB, "outcome", "not_served"), false},
		{"a served line with another outcome", served(gapMsgID, "expired"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lines := strings.Split(strings.TrimSuffix(c.log, "\n"), "\n")
			if got := fileServed(lines, gapMsgID); got != c.want {
				t.Errorf("fileServed=%t, want %t", got, c.want)
			}
		})
	}
}

func TestAwaitFileServedFailsUnderTheSeededFault(t *testing.T) {
	devA := writeReceiverLog(t, "dev-a",
		strings.TrimSuffix(shortIDLine(fileRequestIgnoredLine, "sibling_peer_id", peerDevB, "outcome", "not_served"), "\n"),
		strings.TrimSuffix(shortIDLine(fileNotServedLine, "msg_id", gapMsgID, "outcome", "no_sealed_copy"), "\n"),
	)
	devB := writeReceiverLog(t, "dev-b",
		strings.TrimSuffix(shortIDLine(fileRequestSentLine, "sibling_peer_id", peerDevA, "msg_ids", "1"), "\n"),
		"time=2026-08-13T00:00:00Z level=DEBUG msg=\""+fileRequestIDsLine+"\" msg_ids=\"["+logid.Short(gapMsgID)+" "+logid.Short(otherMsgID)+"]\"",
	)
	err := awaitFileServed(devA, devB, gapMsgID, 0)
	if err == nil {
		t.Fatal("no served line, and the check passed")
	}
	for _, want := range []string{"no_sealed_copy", "seeded fault", "one named this file: true", "sent 1 file request"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("failure %q does not say %q", err, want)
		}
	}
}

func writeRigLogs(t *testing.T, haoma, haomad string) *rig {
	t.Helper()
	r := &rig{name: "dev-a", root: t.TempDir(), giveUp: "30s"}
	if err := os.WriteFile(filepath.Join(r.root, "haoma.log"), []byte(haoma), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.root, "haomad.log"), []byte(haomad), 0o600); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestFindSiblingOfferEnvelope(t *testing.T) {
	offerSent := func(msgID, sibling, env string) string {
		return shortIDLine(siblingOfferSentLine, "sibling_peer_id", sibling, "msg_id", msgID, "envelope_id", env)
	}
	enqueued := func(env, peer string) string {
		return shortIDLine("/send enqueuing envelope", "envelope_id", env, "peer_id", peer, "dest", "http://bbb.onion")
	}
	collides := "9f2c77b1ffffffffffffffffffffffff"

	cases := []struct {
		name          string
		haoma, haomad string
		failsWith     string
	}{
		{"one offer, enqueued for dev-b",
			offerSent(gapMsgID, peerDevB, envToDevB),
			enqueued(envToDevA, peerDevA) + enqueued(envToDevB, peerDevB), ""},
		{"the offer is for another file",
			offerSent(otherMsgID, peerDevB, envToDevB),
			enqueued(envToDevB, peerDevB), "has no"},
		{"two offers for the file",
			offerSent(gapMsgID, peerDevB, envToDevB) + offerSent(gapMsgID, peerDevA, envToDevA),
			enqueued(envToDevA, peerDevA) + enqueued(envToDevB, peerDevB), "want one"},
		{"haomad enqueued it for another peer",
			offerSent(gapMsgID, peerDevB, envToDevB),
			enqueued(envToDevB, peerDevA), "disagree"},
		{"another envelope shares the short id",
			offerSent(gapMsgID, peerDevB, envToDevB),
			enqueued(envToDevB, peerDevB) + enqueued(collides, peerDevA), "refuses to guess"},
		{"haomad logged no enqueue for it",
			offerSent(gapMsgID, peerDevB, envToDevB),
			enqueued(envToDevA, peerDevA), "cannot see"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := writeRigLogs(t, c.haoma, c.haomad)
			env, err := findSiblingOfferEnvelope(r, gapMsgID)
			if c.failsWith == "" {
				if err != nil {
					t.Fatalf("want the envelope, got %v", err)
				}
				if env != logid.Short(envToDevB) {
					t.Fatalf("got envelope %q, want %q", env, logid.Short(envToDevB))
				}
				return
			}
			if err == nil {
				t.Fatalf("want a failure naming %q, got envelope %q", c.failsWith, env)
			}
			if !strings.Contains(err.Error(), c.failsWith) {
				t.Errorf("failure %q does not name %q", err, c.failsWith)
			}
		})
	}
}

func TestLoggedEnvelopeDeadLettered(t *testing.T) {
	onDisk := logid.Short(envToDevB)
	transition := func(env, to string) string {
		return shortIDLine("outbox: transition", "envelope_id", env, "from", "enqueued", "to", to)
	}
	r := writeRigLogs(t, "", transition(envToDevA, "failed")+transition(envToDevB, "sent"))
	if dead, err := loggedEnvelopeDeadLettered(r, onDisk); err != nil || dead {
		t.Fatalf("dead=%t err=%v, want false: only another envelope failed", dead, err)
	}

	r = writeRigLogs(t, "", transition(envToDevB, "failed"))
	if dead, err := loggedEnvelopeDeadLettered(r, envToDevB[:8]); err != nil || dead {
		t.Fatalf("dead=%t err=%v, want false: a bare prefix is not the id on disk", dead, err)
	}
	if dead, err := loggedEnvelopeDeadLettered(r, onDisk); err != nil || !dead {
		t.Fatalf("dead=%t err=%v, want true", dead, err)
	}
	if err := waitForLoggedEnvelopeGiveUp(r, ""); err == nil {
		t.Error("a wait with no envelope id must refuse, not report the route closed")
	}
}

func TestNoSiblingSeal(t *testing.T) {
	r := &rig{name: "dev-b"}
	direct := []string{strings.TrimSuffix(shortIDLine("file fetch: ready", "msg_id", gapMsgID), "\n")}
	if err := noSiblingSeal(r, direct, gapMsgID); err != nil {
		t.Errorf("the contact's own transfer failed the check: %v", err)
	}
	if err := noSiblingSeal(r, []string{sealLine(otherMsgID, "route", sealRouteGapFill)}, gapMsgID); err != nil {
		t.Errorf("another file's seal failed the check: %v", err)
	}
	for _, route := range []string{sealRouteGapFill, sealRouteSend} {
		err := noSiblingSeal(r, append(direct, sealLine(gapMsgID, "route", route)), gapMsgID)
		if err == nil || !strings.Contains(err.Error(), "route="+route) {
			t.Errorf("a seal by route=%s: got %v, want a failure naming it", route, err)
		}
	}
}

func TestGapFillPathSeenCountsTransientFetchFailures(t *testing.T) {
	event := func(msgID, state string) string {
		return strings.TrimSuffix(shortIDLine(siblingFetchEventLine, "msg_id", msgID, "staging_id", "abcdef0123456789", "state", state), "\n")
	}
	devA := writeReceiverLog(t, "dev-a", strings.TrimSuffix(shortIDLine(fileServedLine, "msg_id", gapMsgID, "outcome", "served"), "\n"))
	devB := writeReceiverLog(t, "dev-b",
		event(gapMsgID, "downloading"),
		event(gapMsgID, "failed_transient"),
		event(gapMsgID, "failed_transient"),
		event(otherMsgID, "failed_transient"),
	)
	got := gapFillPathSeen(devA, devB, gapMsgID)
	if !strings.Contains(got, "failed_transient 2 time(s)") {
		t.Errorf("text %q does not count the two failed_transient events of this file", got)
	}
	quiet := writeReceiverLog(t, "dev-b", event(gapMsgID, "downloading"))
	if got := gapFillPathSeen(devA, quiet, gapMsgID); strings.Contains(got, "failed_transient") {
		t.Errorf("no transient failure, and the text names one: %q", got)
	}
}

func TestAwaitContactFetch(t *testing.T) {
	direct := writeReceiverLog(t, "dev-b", strings.TrimSuffix(shortIDLine(fetchReadyLine, "msg_id", gapMsgID), "\n"))
	if err := awaitContactFetch(direct, gapMsgID, 0); err != nil {
		t.Errorf("the contact's fetch pulled the blob, and the check failed: %v", err)
	}
	bySibling := writeReceiverLog(t, "dev-b",
		strings.TrimSuffix(shortIDLine(fetchReadyLine, "msg_id", otherMsgID), "\n"),
		sealLine(gapMsgID, "route", sealRouteGapFill),
	)
	err := awaitContactFetch(bySibling, gapMsgID, 0)
	if err == nil {
		t.Fatal("a sibling offer brought the file, and the check passed")
	}
	if !strings.Contains(err.Error(), "gapfill") {
		t.Errorf("failure %q does not name the sibling seal it saw", err)
	}
}
