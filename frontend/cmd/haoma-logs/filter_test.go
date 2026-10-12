package main

import (
	"testing"
	"time"
)

func TestFiltersCompose(t *testing.T) {
	e := parseLine(`time=2026-08-15T10:12:35.893+01:00 level=DEBUG msg="mirror: mutation mirrored to sibling" ` +
		`service=haoma kind=edit sibling_peer_id=7ae43d89… msg_id=967dccd2ffffffffffffffffffffffff envelope_id=5fd21fa5…`)
	e.account = "alice"
	e.process = "haoma"

	day := e.ts

	mustParse := func(s string) time.Time {
		t.Helper()
		got, err := parseWhen(s, day)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	cases := []struct {
		name string
		f    filters
		want bool
	}{
		{"nothing set", filters{}, true},
		{"message id in full", filters{msg: "967dccd2ffffffffffffffffffffffff"}, true},
		{"message id as the log holds it", filters{msg: "967dccd2…"}, true},
		{"a different message id", filters{msg: "27a1474dffffffffffffffffffffffff"}, false},
		{"envelope id", filters{envelope: "5fd21fa5…"}, true},
		{"wire kind", filters{kind: "edit"}, true},
		{"a different wire kind", filters{kind: "text"}, false},
		{"account", filters{accounts: []string{"alice"}}, true},
		{"another account", filters{accounts: []string{"eddie"}}, false},
		{"one of several accounts", filters{accounts: []string{"eddie", "alice"}}, true},
		{"all five together", filters{
			msg: "967dccd2…", envelope: "5fd21fa5…", kind: "edit",
			accounts: []string{"alice"},
			from:     mustParse("10:12:00"), hasFrom: true,
			to: mustParse("10:13:00"), hasTo: true,
		}, true},
		{"all five, one of them wrong", filters{
			msg: "967dccd2…", envelope: "5fd21fa5…", kind: "text",
			accounts: []string{"alice"},
			from:     mustParse("10:12:00"), hasFrom: true,
			to: mustParse("10:13:00"), hasTo: true,
		}, false},
		{"window starts too late", filters{from: mustParse("10:13:00"), hasFrom: true}, false},
		{"window ends too early", filters{to: mustParse("10:12:00"), hasTo: true}, false},
	}
	for _, c := range cases {
		if got := c.f.match(e); got != c.want {
			t.Errorf("%s: match = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestParseWhenReadsSeveralWays(t *testing.T) {
	day := time.Date(2026, 8, 15, 0, 0, 0, 0, time.Local)
	for _, s := range []string{
		"2026-08-15T10:12:35+01:00",
		"2026-08-15T10:12:35",
		"2026-08-15 10:12",
		"2026-08-15",
		"10:12:35",
		"10:12",
	} {
		if _, err := parseWhen(s, day); err != nil {
			t.Errorf("cannot read %q: %v", s, err)
		}
	}
	got, err := parseWhen("10:12:35", day)
	if err != nil {
		t.Fatal(err)
	}
	if got.Year() != 2026 || got.Month() != time.August || got.Day() != 15 {
		t.Errorf("a clock time on its own got the date %v, want the day of the run", got)
	}
	if _, err := parseWhen("half past ten", day); err == nil {
		t.Error("a value that is not a time was accepted")
	}
}

func TestMatchesAnyLooksAtEveryKeyThatHoldsAMessageID(t *testing.T) {
	e := parseLine(`time=2026-08-15T10:12:56.003+01:00 level=DEBUG msg="fan-out prepared a device" ` +
		`service=haoma peer_id=1b3f1f84… edit_msg_id=967dccd2ffffffffffffffffffffffff payload_bytes=423`)
	if !matchesAny(e, msgIDKeys, "967dccd2…") {
		t.Error("a message id written under edit_msg_id was not found")
	}
	if matchesAny(e, msgIDKeys, "1b3f1f84…") {
		t.Error("a peer id was matched as a message id")
	}
}

func TestTheRecordedDeviceIsAKnownPeerIDKey(t *testing.T) {
	e := parseLine(`time=2026-08-17T10:12:56.003+01:00 level=DEBUG ` +
		`msg="read receipt addressed to the chat's stored device" ` +
		`service=haoma msg_id=967dccd2… peer_id=1b3f1f84… recorded_peer_id=aa11bb22…`)

	if !matchesAny(e, peerIDKeys, "aa11bb22…") {
		t.Error("the device recorded on the row was not found under recorded_peer_id")
	}
	u := newUnknownKeys()
	u.note(e)
	if got := u.list(); len(got) != 0 {
		t.Errorf("the tool warned about a key it should know: %v", got)
	}
}

func TestAnAcknowledgedMessageIsFoundByItsOwnID(t *testing.T) {
	e := parseLine(`time=2026-08-17T10:12:56.003+01:00 level=DEBUG ` +
		`msg="read receipt acknowledges this message" ` +
		`service=haoma msg_id=967dccd2… chat_id=cc33… peer_id=1b3f1f84… ` +
		`receipt_msg_id=dd44ee55… envelope_id=ff66aa77…`)

	if !matchesAny(e, msgIDKeys, "967dccd2…") {
		t.Error("the acknowledged message id was not found")
	}
	if !matchesAny(e, ackKeys, "dd44ee55…") {
		t.Error("the receipt id was not recognised as an answer coming back")
	}
	u := newUnknownKeys()
	u.note(e)
	if got := u.list(); len(got) != 0 {
		t.Errorf("the tool warned about a key it should know: %v", got)
	}
}

func TestAReceiptIsFoundByTheMessageItAcknowledged(t *testing.T) {
	const acked = "967dccd2ffffffffffffffffffffffff"
	root := writeRun(t, map[string]string{
		"peer-vault/haoma.log": lines(
			`time=2026-08-17T10:12:35.000+01:00 level=DEBUG msg="read receipt addressed to the device that sent the message" `+
				`service=haoma msg_id=967dccd2… peer_id=1b3f1f84… source=recorded_at_ingest`,
			`time=2026-08-17T10:12:36.000+01:00 level=DEBUG msg="read receipt shipped" `+
				`service=haoma chat_id=cc330000… peer_id=1b3f1f84… envelope_id=5fd21fa5… receipt_msg_id=dd440000… sender_seq=4 targets=1 acked_msg_ids=967dccd2…`,
			`time=2026-08-17T10:12:36.100+01:00 level=DEBUG msg="read receipt acknowledges this message" `+
				`service=haoma msg_id=967dccd2… chat_id=cc330000… peer_id=1b3f1f84… receipt_msg_id=dd440000… envelope_id=5fd21fa5…`,
			`time=2026-08-17T10:12:40.000+01:00 level=DEBUG msg="something else entirely" service=haoma msg_id=27a1474d…`,
		),
	})
	srcs := pick(sourcesFor(t, root), false, false)

	var found []string
	f := filters{msg: acked}
	for _, e := range collect(t, srcs) {
		if f.match(e) {
			found = append(found, e.msg)
		}
	}
	for _, want := range []string{
		"read receipt addressed to the device that sent the message",
		"read receipt acknowledges this message",
	} {
		hit := false
		for _, got := range found {
			if got == want {
				hit = true
			}
		}
		if !hit {
			t.Errorf("filtering by the acknowledged message id did not return %q; it returned %v", want, found)
		}
	}
	for _, got := range found {
		if got == "something else entirely" {
			t.Error("the filter returned a line about a different message")
		}
	}

	res, err := collectSummary(srcs, acked, filters{})
	if err != nil {
		t.Fatal(err)
	}
	named := false
	for _, e := range res.lines {
		for _, a := range e.attrs {
			if has(peerIDKeys, a.key) && a.val == "1b3f1f84…" {
				named = true
			}
		}
	}
	if !named {
		t.Errorf("the summary of %s names no device; it read %d line(s)", short(acked), len(res.lines))
	}
}
