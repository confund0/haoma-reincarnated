package dht

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"net/netip"
	"testing"

	"haoma/internal/bencode"
)

func TestID_XorAndBitLen(t *testing.T) {
	var a, b ID
	a[19] = 0x01
	b[19] = 0x03
	d := a.Xor(b)
	if d[19] != 0x02 {
		t.Fatalf("xor last byte: got %x want 02", d[19])
	}
	if d.BitLen() != 2 {
		t.Fatalf("bitlen of 0x02: got %d want 2", d.BitLen())
	}

	var zero ID
	if zero.BitLen() != 0 {
		t.Fatalf("bitlen zero: got %d", zero.BitLen())
	}

	var high ID
	high[0] = 0x80
	if high.BitLen() != 160 {
		t.Fatalf("bitlen high bit: got %d", high.BitLen())
	}
}

func TestLess_ConsistentWithXorMetric(t *testing.T) {
	var a, b ID
	a[0] = 0x01
	b[0] = 0x02
	if !less(a, b) {
		t.Fatal("a < b expected")
	}
	if less(b, a) {
		t.Fatal("not b < a")
	}
	if less(a, a) {
		t.Fatal("not a < a")
	}
}

func TestRoutingTable_AddAndClosest(t *testing.T) {
	var self ID
	self[0] = 0xff
	rt := newRoutingTable(self)

	n1 := self
	n1[19] ^= 0x01
	addr1 := netip.MustParseAddrPort("1.2.3.4:6881")
	if !rt.addNode(n1, addr1) {
		t.Fatal("add n1 failed")
	}

	n2 := self
	n2[0] ^= 0x80
	addr2 := netip.MustParseAddrPort("5.6.7.8:6881")
	if !rt.addNode(n2, addr2) {
		t.Fatal("add n2 failed")
	}

	if rt.addNode(self, netip.MustParseAddrPort("9.9.9.9:6881")) {
		t.Fatal("self should be rejected")
	}

	closest := rt.closestNodes(self, 2)
	if len(closest) != 2 {
		t.Fatalf("want 2 closest, got %d", len(closest))
	}
	if closest[0].id != n1 || closest[1].id != n2 {
		t.Fatalf("closest order wrong: %v then %v", closest[0].id, closest[1].id)
	}

	if rt.count() != 2 {
		t.Fatalf("count: got %d want 2", rt.count())
	}
}

func TestRoutingTable_DedupesByAddress(t *testing.T) {
	var self ID
	rt := newRoutingTable(self)

	var n1, n2 ID
	n1[19] = 0x01
	n2[19] = 0x02
	addr := netip.MustParseAddrPort("1.1.1.1:1234")

	rt.addNode(n1, addr)
	rt.addNode(n2, addr)

	if got := rt.count(); got != 1 {
		t.Fatalf("count after dedupe: got %d want 1", got)
	}
}

func TestBEP44_SignVerifyRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	item := &MutableItem{
		PrivKey: priv,
		Salt:    []byte("haoma-pair-v1"),
		Seq:     42,
		Value:   []byte("hello, world"),
	}
	copy(item.PubKey[:], pub)
	if err := item.Sign(); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := item.Verify(); err != nil {
		t.Fatalf("verify: %v", err)
	}

	tampered := *item
	tampered.Value = []byte("hello, evil")
	if err := tampered.Verify(); err == nil {
		t.Fatal("expected tamper detection")
	}
}

func TestBEP44_BufferToSign_MatchesBEPSpec(t *testing.T) {

	salt := []byte("salt")
	bv, _ := bencode.Marshal([]byte("test"))
	got := bufferToSign(salt, bv, 1)
	want := []byte("4:salt4:salt3:seqi1e1:v4:test")
	if !bytes.Equal(got, want) {
		t.Fatalf("bufferToSign mismatch:\n  got:  %q\n  want: %q", got, want)
	}
}

func TestBEP44_BufferToSign_SaltOmittedWhenEmpty(t *testing.T) {
	bv, _ := bencode.Marshal([]byte("test"))
	got := bufferToSign(nil, bv, 0)
	want := []byte("3:seqi0e1:v4:test")
	if !bytes.Equal(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestBEP44_Target(t *testing.T) {
	var pk [32]byte
	for i := range pk {
		pk[i] = byte(i)
	}
	salt := []byte("haoma")
	m := &MutableItem{PubKey: pk, Salt: salt}
	want := sha1.Sum(append(pk[:], salt...))
	got := m.Target()
	if !bytes.Equal(got[:], want[:]) {
		t.Fatalf("target mismatch: got %x want %x", got, want)
	}
}

func TestKRPC_QueryRoundTrip(t *testing.T) {
	original := &krpcMsg{
		T: "ab",
		Y: "q",
		Q: "find_node",
		A: &krpcArgs{
			ID:     ID{0xaa, 0xbb, 0xcc},
			Target: ID{0x11, 0x22, 0x33},
		},
	}
	encoded, err := bencode.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded krpcMsg
	if err := bencode.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.T != "ab" || decoded.Y != "q" || decoded.Q != "find_node" {
		t.Fatalf("envelope mismatch: %+v", decoded)
	}
	if decoded.A == nil || decoded.A.ID != original.A.ID || decoded.A.Target != original.A.Target {
		t.Fatalf("args mismatch: %+v", decoded.A)
	}
}

func TestKRPC_BEP44ResponseRoundTrip(t *testing.T) {
	seq := int64(7)
	var pk [32]byte
	pk[0] = 0xaa
	var sig [64]byte
	sig[0] = 0xbb

	value := []byte("payload")
	bv, _ := bencode.Marshal(value)

	original := &krpcMsg{
		T: "xy",
		Y: "r",
		R: &krpcReturn{
			ID:    ID{0x01},
			Token: "tok",
			V:     bv,
			K:     pk[:],
			Sig:   sig[:],
			Seq:   &seq,
		},
	}
	encoded, err := bencode.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded krpcMsg
	if err := bencode.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.R == nil || decoded.R.Token != "tok" {
		t.Fatalf("token round-trip: %+v", decoded.R)
	}
	if decoded.R.Seq == nil || *decoded.R.Seq != 7 {
		t.Fatalf("seq round-trip: %+v", decoded.R.Seq)
	}

	if !bytes.Equal(decoded.R.V, bv) {
		t.Fatalf("v round-trip: got %q want %q", decoded.R.V, bv)
	}

	var got []byte
	if err := bencode.Unmarshal(decoded.R.V, &got); err != nil {
		t.Fatalf("inner unmarshal: %v", err)
	}
	if !bytes.Equal(got, value) {
		t.Fatalf("payload mismatch: got %q want %q", got, value)
	}
}

func TestParseCompactNodes_Wellformed(t *testing.T) {

	var rec1 [26]byte
	for i := 0; i < 20; i++ {
		rec1[i] = byte(i + 1)
	}
	rec1[20], rec1[21], rec1[22], rec1[23] = 8, 8, 8, 8
	binary.BigEndian.PutUint16(rec1[24:], 6881)

	var rec2 [26]byte
	for i := 0; i < 20; i++ {
		rec2[i] = byte(i + 100)
	}
	rec2[20], rec2[21], rec2[22], rec2[23] = 1, 1, 1, 1
	binary.BigEndian.PutUint16(rec2[24:], 1234)

	blob := append(rec1[:], rec2[:]...)
	got, err := parseCompactNodes(blob)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d nodes want 2", len(got))
	}
	if got[0].Addr.String() != "8.8.8.8:6881" {
		t.Fatalf("addr[0]: %s", got[0].Addr)
	}
	if got[1].Addr.String() != "1.1.1.1:1234" {
		t.Fatalf("addr[1]: %s", got[1].Addr)
	}
}

func TestParseCompactNodes_FiltersInvalid(t *testing.T) {

	var rec1 [26]byte
	rec1[20] = 8
	binary.BigEndian.PutUint16(rec1[24:], 6881)

	var rec2 [26]byte
	rec2[20] = 1
	binary.BigEndian.PutUint16(rec2[24:], 0)

	blob := append(rec1[:], rec2[:]...)
	got, _ := parseCompactNodes(blob)
	if len(got) != 1 {
		t.Fatalf("got %d, expected zero-port filtered", len(got))
	}
}

func TestParseCompactNodes_MisalignedErrors(t *testing.T) {
	if _, err := parseCompactNodes(make([]byte, 25)); err == nil {
		t.Fatal("expected error on mis-aligned blob")
	}
}

func TestErrorFromBencode(t *testing.T) {

	b, _ := bencode.Marshal([]any{int64(203), "Protocol Error"})
	code, msg := errorFromBencode(b)
	if code != 203 || msg != "Protocol Error" {
		t.Fatalf("got (%d, %q)", code, msg)
	}
}
