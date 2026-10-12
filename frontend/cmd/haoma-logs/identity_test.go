package main

import (
	"strings"
	"testing"
)

const (
	aliceToEddie   = "1b3f1f840846bcef88679e20dfce7bd2"
	aliceToSibling = "7ae43d8911a28784ec71ea9697d1e1ff"
	linkedToAlice  = "78f3a91e4799f583245a32dc3145f2d6"
	linkedToEddie  = "9138e5dcd3e57b12664e89da094bedea"
	eddieToAlice   = "7aab232ab8ba5359dc0facdb20c9d65a"
	eddieToLinked  = "3936e164b6fa84925459439eab8d986c"
)

var (
	aliceEddieOnion   = onion("no6l3kai")
	aliceSiblingOnion = onion("z4vuvu3c")
	linkedAliceOnion  = onion("cpwdrywf")
	linkedEddieOnion  = onion("ndd4ncbf")
	eddieAliceOnion   = onion("wvk46tbb")
	eddieLinkedOnion  = onion("xc5v34bp")
)

func publishLine(ts, peer, own string) string {
	return "time=" + ts + ` level=INFO msg="peer onion published" service=haomad ` +
		"peer_id=" + short(peer) + " service_id=" + short(own)
}

func receivedLine(ts, peer, far string) string {
	return receivedLineFrom(ts, peer, short(far))
}

func receivedLineFrom(ts, peer, from string) string {
	return "time=" + ts + ` level=DEBUG msg="envelope received and queued to inbox" service=haomad ` +
		"envelope_id=e0000000… peer_id=" + short(peer) + " from=" + from + " kind=text"
}

func identityFiles() map[string]string {
	return map[string]string{
		"alice-vault/haomad.log": lines(
			publishLine("2026-08-15T09:59:10.000+01:00", aliceToEddie, aliceEddieOnion),
			publishLine("2026-08-15T09:59:10.001+01:00", aliceToSibling, aliceSiblingOnion),
			receivedLine("2026-08-15T09:59:20.000+01:00", aliceToEddie, eddieAliceOnion),
			receivedLine("2026-08-15T09:59:21.000+01:00", aliceToSibling, linkedAliceOnion),
		),
		"alice-vault/haoma.log": lines(
			`time=2026-08-15T09:59:00.000+01:00 level=INFO msg="signal: identity bootstrapped" service=haoma fingerprint=05546133… registration_id=5d2c9a1f opk_count=100`,
			`time=2026-08-15T09:59:22.000+01:00 level=DEBUG msg="mirror: mutation mirrored to sibling" service=haoma kind=text sibling_peer_id=`+short(aliceToSibling)+` msg_id=27a1474d… envelope_id=e0000001…`,
		),

		"alice-linked-vault/haomad.log": lines(
			publishLine("2026-08-15T09:59:11.000+01:00", linkedToAlice, linkedAliceOnion),
			publishLine("2026-08-15T09:59:11.001+01:00", linkedToEddie, linkedEddieOnion),
			receivedLine("2026-08-15T09:59:23.000+01:00", linkedToAlice, aliceSiblingOnion),
			receivedLine("2026-08-15T09:59:24.000+01:00", linkedToEddie, eddieLinkedOnion),
		),
		"alice-linked-vault/haoma.log": lines(
			`time=2026-08-15T09:59:01.000+01:00 level=INFO msg="signal: identity bootstrapped" service=haoma fingerprint=0584046b… registration_id=a07e33b2 opk_count=100`,
		),
		"eddie-vault/haomad.log": lines(
			publishLine("2026-08-15T09:59:12.000+01:00", eddieToAlice, eddieAliceOnion),
			publishLine("2026-08-15T09:59:12.001+01:00", eddieToLinked, eddieLinkedOnion),
			receivedLine("2026-08-15T09:59:25.000+01:00", eddieToAlice, aliceEddieOnion),
			receivedLine("2026-08-15T09:59:26.000+01:00", eddieToLinked, linkedEddieOnion),
		),
		"eddie-vault/haoma.log": lines(
			`time=2026-08-15T09:59:02.000+01:00 level=INFO msg="signal: identity bootstrapped" service=haoma fingerprint=05b04aee… registration_id=19f0c4de opk_count=100`,
		),
	}
}

func withIdentity(files map[string]string) map[string]string {
	out := identityFiles()
	for k, v := range files {
		out[k] = out[k] + v
	}
	return out
}

func threeDeviceRun(t *testing.T) string {
	t.Helper()
	return writeRun(t, identityFiles())
}

func TestIdentityMapJoinsBothSidesOfEveryPairing(t *testing.T) {
	root := threeDeviceRun(t)
	m, err := buildIdentityMap(root, logsOnly(sourcesFor(t, root)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.accounts) != 3 {
		t.Fatalf("found %d accounts, want 3", len(m.accounts))
	}

	cases := []struct {
		account string
		peerID  string
		want    string
	}{
		{"alice", aliceToEddie, "pairing with eddie, which calls it " + short(eddieToAlice)},
		{"alice", aliceToSibling, "sibling link with alice-linked, which calls it " + short(linkedToAlice)},
		{"alice-linked", linkedToAlice, "sibling link with alice, which calls it " + short(aliceToSibling)},
		{"alice-linked", linkedToEddie, "pairing with eddie, which calls it " + short(eddieToLinked)},
		{"eddie", eddieToAlice, "pairing with alice, which calls it " + short(aliceToEddie)},
		{"eddie", eddieToLinked, "pairing with alice-linked, which calls it " + short(linkedToEddie)},
	}
	for _, c := range cases {
		if got := m.describe(c.account, c.peerID); got != c.want {
			t.Errorf("%s peer %s:\n got %q\nwant %q", c.account, short(c.peerID), got, c.want)
		}

		if got := m.describe(c.account, short(c.peerID)); got != c.want {
			t.Errorf("%s peer %s, shortened form: got %q, want %q", c.account, short(c.peerID), got, c.want)
		}
	}
}

func TestIdentityMapKeepsEachAccountsFingerprint(t *testing.T) {
	root := threeDeviceRun(t)
	m, err := buildIdentityMap(root, logsOnly(sourcesFor(t, root)), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"alice":        "05546133…",
		"alice-linked": "0584046b…",
		"eddie":        "05b04aee…",
	}
	for acct, fp := range want {
		id, ok := m.byName[acct]
		if !ok {
			t.Fatalf("account %s missing", acct)
		}
		if id.fingerprint != fp {
			t.Errorf("%s fingerprint = %q, want %q", acct, id.fingerprint, fp)
		}
	}
}

func TestIdentityMapPrintsBothSides(t *testing.T) {
	root := threeDeviceRun(t)
	m, err := buildIdentityMap(root, logsOnly(sourcesFor(t, root)), nil)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	m.write(&b)
	out := b.String()
	for _, want := range []string{
		short(aliceToEddie), short(aliceToSibling),
		short(linkedToAlice), short(linkedToEddie),
		short(eddieToAlice), short(eddieToLinked),
		"sibling link with alice-linked",
		"pairing with eddie",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the printed map does not carry %q", want)
		}
	}
}

func TestStateWordIsNotReadAsAnOnion(t *testing.T) {
	if looksLikeOnion("enqueued") {
		t.Error("enqueued was read as an onion address")
	}
	if !looksLikeOnion(short(onion("no6l3kai"))) {
		t.Error("a shortened v3 onion address was not recognised")
	}
	if !looksLikeOnion(short(onion("no6l3kai")) + ".onion") {
		t.Error("a shortened v3 onion address with its suffix was not recognised")
	}
	if looksLikeOnion("no6l3ka8" + shortMark) {
		t.Error("a shortened value outside the base32 alphabet was read as an onion address")
	}
	if looksLikeOnion("no6l3k" + shortMark) {
		t.Error("a shortened value of the wrong length was read as an onion address")
	}
	if looksLikeOnion("") {
		t.Error("an empty value was read as an onion address")
	}
	if !looksLikeOnion(onion("no6l3kai")) {
		t.Error("a v3 onion address was not recognised")
	}
	if !looksLikeOnion(onion("no6l3kai") + ".onion") {
		t.Error("a v3 onion address with its suffix was not recognised")
	}
	if looksLikeOnion(onion("no6l3ka") + "8") {
		t.Error("a value outside the base32 alphabet was read as an onion address")
	}
}

func TestFarSideNotInTheRunIsSaidPlainly(t *testing.T) {
	root := writeRun(t, map[string]string{
		"alice-vault/haomad.log": lines(
			`time=2026-08-15T10:00:00.000+01:00 level=INFO msg="peer onion published" service=haomad peer_id=` +
				short(aliceToEddie) + " service_id=" + short(onion("no6l3kai")),
		),
	})
	m, err := buildIdentityMap(root, logsOnly(sourcesFor(t, root)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.describe("alice", aliceToEddie); got != "far side not seen in this run" {
		t.Errorf("got %q", got)
	}
	if got := m.describe("alice", "0000000000000000"); got != "not in this account's logs" {
		t.Errorf("got %q", got)
	}
}

func TestIdentityMapJoinsOnTheFullFromOfOlderLogs(t *testing.T) {
	root := writeRun(t, map[string]string{
		"alice-vault/haomad.log": lines(
			publishLine("2026-08-15T09:59:10.000+01:00", aliceToEddie, aliceEddieOnion),
			receivedLineFrom("2026-08-15T09:59:20.000+01:00", aliceToEddie, eddieAliceOnion),
		),
		"eddie-vault/haomad.log": lines(
			publishLine("2026-08-15T09:59:12.000+01:00", eddieToAlice, eddieAliceOnion),
			receivedLineFrom("2026-08-15T09:59:25.000+01:00", eddieToAlice, aliceEddieOnion+".onion"),
		),
	})
	m, err := buildIdentityMap(root, logsOnly(sourcesFor(t, root)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := m.describe("alice", aliceToEddie), "pairing with eddie, which calls it "+short(eddieToAlice); got != want {
		t.Errorf("alice: got %q, want %q", got, want)
	}
	if got, want := m.describe("eddie", eddieToAlice), "pairing with alice, which calls it "+short(aliceToEddie); got != want {
		t.Errorf("eddie: got %q, want %q", got, want)
	}
}
