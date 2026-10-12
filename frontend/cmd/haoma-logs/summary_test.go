package main

import (
	"strings"
	"testing"
)

func fileSendRun(t *testing.T) string {
	t.Helper()
	return writeRun(t, withIdentity(map[string]string{
		"eddie-vault/haoma.log": lines(
			`time=2026-08-15T10:00:00.000+01:00 level=DEBUG msg="file offer envelope prepared" service=haoma peer_id=`+short(eddieToAlice)+` recipient_peer_id=`+short(eddieToAlice)+` msg_id=17a88527ffffffffffffffffffffffff token=n5VrJqEu… sender_seq=26`,
			`time=2026-08-15T10:00:00.100+01:00 level=INFO msg="file sent" service=haoma peer_id=`+short(eddieToAlice)+` msg_id=17a88527… envelope_id=bbd889ae… size=175 recipients=1`,
		),
		"eddie-vault/haomad.log": lines(
			`time=2026-08-15T10:00:00.200+01:00 level=DEBUG msg="outbox: attempting send" service=haomad envelope_id=bbd889ae… attempt=1`,
			`time=2026-08-15T10:00:09.000+01:00 level=DEBUG msg="outbox: transition" service=haomad envelope_id=bbd889ae… from=enqueued to=sent`,
		),
		"alice-vault/haomad.log": lines(
			`time=2026-08-15T10:00:05.000+01:00 level=DEBUG msg="envelope received and queued to inbox" service=haomad envelope_id=bbd889ae… peer_id=`+short(aliceToEddie)+` from=`+onion("wvk46tbb")+` kind=text`,
			`time=2026-08-15T10:00:05.100+01:00 level=DEBUG msg="xport: sent_ack written" service=haomad envelope_id=bbd889ae… ack_id=104519fc…`,
		),
		"alice-vault/haoma.log": lines(
			`time=2026-08-15T10:00:05.200+01:00 level=INFO msg="inbound file_offer received" service=haoma envelope_id=bbd889ae… peer_id=`+short(aliceToEddie)+` msg_id=17a88527… chat_id=ca172d0e… token=n5VrJqEu… size=175`,
			`time=2026-08-15T10:00:06.000+01:00 level=DEBUG msg="file receipt shipped" service=haoma peer_id=`+short(aliceToEddie)+` token=n5VrJqEu… envelope_id=c3deb9e1… receipt_msg_id=4e173774… sender_seq=28`,
			`time=2026-08-15T10:00:07.000+01:00 level=DEBUG msg="mirror: mutation mirrored to sibling" service=haoma kind=file_offer sibling_peer_id=`+short(aliceToSibling)+` msg_id=17a88527… envelope_id=9dc2c40c…`,
			`time=2026-08-15T10:00:08.000+01:00 level=INFO msg="file ready (sealed at rest)" service=haoma msg_id=17a88527… chat_id=ca172d0e… state=ready`,
		),
		"alice-linked-vault/haoma.log": lines(
			`time=2026-08-15T10:00:07.500+01:00 level=DEBUG msg="rail: mirror applied — local state changed" service=haoma envelope_id=9dc2c40c… sibling_peer_id=` + short(linkedToAlice) + ` wire_kind=file_offer msg_id=17a88527… outcome=applied`,
		),
	}))
}

func TestSummaryGathersOneMessageAcrossEveryLog(t *testing.T) {
	root := fileSendRun(t)
	srcs := sourcesFor(t, root)
	m, err := buildIdentityMap(root, logsOnly(srcs), nil)
	if err != nil {
		t.Fatal(err)
	}

	r, err := collectSummary(pick(srcs, false, false), "17a88527ffffffffffffffffffffffff", filters{})
	if err != nil {
		t.Fatal(err)
	}

	wantAccounts := []string{"alice", "alice-linked", "eddie"}
	if strings.Join(r.accounts, ",") != strings.Join(wantAccounts, ",") {
		t.Errorf("accounts = %v, want %v", r.accounts, wantAccounts)
	}
	if len(r.lines) != 11 {
		t.Errorf("gathered %d lines, want 11", len(r.lines))
		for _, e := range r.lines {
			t.Logf("  %s %s %s", e.account, e.process, e.msg)
		}
	}

	for _, want := range []string{"bbd889ae…", "9dc2c40c…"} {
		if !r.envIDs.has(want) {
			t.Errorf("envelope %s was not harvested", want)
		}
	}

	if r.envIDs.has("e0000000…") || r.envIDs.has("e0000001…") {
		t.Error("an envelope from another message was harvested")
	}
	if !r.tokens.has("n5VrJqEu…") {
		t.Error("the file token was not harvested")
	}

	if !hasLine(r.lines, "eddie", "outbox: transition") {
		t.Error("the outbox transition, which names only the envelope, was not gathered")
	}
	if !hasLine(r.lines, "alice-linked", "rail: mirror applied — local state changed") {
		t.Error("the second device's rail line was not gathered")
	}

	var b strings.Builder
	r.write(&b, m)
	out := b.String()
	for _, want := range []string{
		"Devices named on these lines",
		"Tokens",
		"Envelopes",
		"Answers back",
		"How it ended on each device",
		"Timeline",
		"pairing with eddie",
		"sibling link with alice",
		"n5VrJqEu…",
		"file receipt shipped",
		"outcome=applied",
		"c3deb9e1…",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the summary does not carry %q", want)
		}
	}
}

func TestSummaryTimelineIsInTimeOrder(t *testing.T) {
	root := fileSendRun(t)
	srcs := sourcesFor(t, root)
	r, err := collectSummary(pick(srcs, false, false), "17a88527…", filters{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(r.lines); i++ {
		if r.lines[i].ts.Before(r.lines[i-1].ts) {
			t.Fatalf("line %d at %s comes after line %d at %s",
				i, r.lines[i].ts, i-1, r.lines[i-1].ts)
		}
	}
}

func TestSummarySaysSoWhenNothingMatches(t *testing.T) {
	root := fileSendRun(t)
	srcs := sourcesFor(t, root)
	m, err := buildIdentityMap(root, logsOnly(srcs), nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := collectSummary(pick(srcs, false, false), "deadbeefdeadbeefdeadbeefdeadbeef", filters{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.lines) != 0 {
		t.Fatalf("gathered %d lines for a message id that is not in the logs", len(r.lines))
	}
	var b strings.Builder
	r.write(&b, m)
	if !strings.Contains(b.String(), "no line in these logs names this message id") {
		t.Error("an empty answer did not say it was empty")
	}
}

func TestFilePathIsNotReadAsAToken(t *testing.T) {
	if looksLikeToken("/home/user/.haoma/token") {
		t.Error("a file path was read as a token")
	}
	if !looksLikeToken("n5VrJqEu2c5uQD1FXwWKOOIl1CFDa1rzJ2LwZWwqct0") {
		t.Error("a token was not recognised")
	}
}

func TestSweepFindsWarningsErrorsAndKnownBadSignatures(t *testing.T) {
	root := writeRun(t, map[string]string{
		"alice-vault/haomad.log": lines(
			`time=2026-08-15T10:00:00.000+01:00 level=INFO msg="all well" service=haomad`,
			`time=2026-08-15T10:00:01.000+01:00 level=WARN msg="tor-tier bring-up failed; retrying in 10s" service=haomad`,
			`time=2026-08-15T10:00:02.000+01:00 level=ERROR msg="AddOnion failed during republish" service=haomad`,
		),
		"alice-vault/haoma.log": lines(
			`time=2026-08-15T10:00:03.000+01:00 level=DEBUG msg="mirror: unclassified wire kind" service=haoma kind=reaction`,
		),
		"eddie-vault/haomad.stderr": lines(`panic: send on closed channel`),
	})
	ps, counts, err := sweep(pick(sourcesFor(t, root), false, true), filters{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 4 {
		t.Fatalf("found %d problems, want 4", len(ps))
	}
	if counts["alice warning"] != 1 || counts["alice error"] != 1 || counts["alice denylist"] != 1 {
		t.Errorf("counts for alice are wrong: %v", counts)
	}
	if counts["eddie denylist"] != 1 {
		t.Errorf("the panic in the captured standard error was not found: %v", counts)
	}

	var seen bool
	for _, p := range ps {
		if strings.HasPrefix(p.category, "DENY mirror: unclassified wire kind") {
			seen = true
		}
	}
	if !seen {
		t.Error("a DEBUG line carrying a known bad signature was not reported")
	}
}

func hasLine(lines []entry, account, msg string) bool {
	for _, e := range lines {
		if e.account == account && e.msg == msg {
			return true
		}
	}
	return false
}
