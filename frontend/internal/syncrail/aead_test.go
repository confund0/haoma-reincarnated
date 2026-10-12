package syncrail_test

import (
	"bytes"
	"errors"
	"testing"

	"haoma-frontend/internal/syncrail"
)

const tagOverhead = 16

var (
	identityA = []byte("identity-private-key-A-0123456789")
	identityB = []byte("identity-private-key-B-0123456789")
)

func mustDeriveKey(t *testing.T, identityPriv []byte) []byte {
	t.Helper()
	key, err := syncrail.DeriveKey(identityPriv)
	if err != nil {
		t.Fatalf("DeriveKey(%q) failed: %v", identityPriv, err)
	}
	return key
}

func mustSeal(t *testing.T, key, plaintext []byte) []byte {
	t.Helper()
	ct, err := syncrail.Seal(key, plaintext)
	if err != nil {
		t.Fatalf("Seal of %d bytes failed: %v", len(plaintext), err)
	}
	return ct
}

func corrupt(t *testing.T, b []byte, i int) []byte {
	t.Helper()
	if i < 0 || i >= len(b) {
		t.Fatalf("test bug: corrupt index %d out of range for %d bytes", i, len(b))
	}
	out := append([]byte(nil), b...)
	out[i] ^= 0x01
	return out
}

func filler(n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(i*31 + i/251)
	}
	return out
}

func TestDeriveKeyIsDeterministicSoSiblingsAgreeWithoutKeyAgreement(t *testing.T) {
	first := mustDeriveKey(t, identityA)
	second := mustDeriveKey(t, append([]byte(nil), identityA...))

	if !bytes.Equal(first, second) {
		t.Fatalf("DeriveKey returned different keys for the same identity private half — " +
			"the rail has no key agreement at all; both siblings derive independently from " +
			"the copied identity keypair, so a non-deterministic derivation means device B " +
			"can never open anything device A sealed")
	}
}

func TestDeriveKeyIsDomainSeparatedPerIdentity(t *testing.T) {
	keyA := mustDeriveKey(t, identityA)
	keyB := mustDeriveKey(t, identityB)

	if bytes.Equal(keyA, keyB) {
		t.Fatalf("two unrelated identity private halves derived the same rail key — " +
			"every install on earth would share a rail key and any stranger could open " +
			"my mirrored messages")
	}
}

func TestDeriveKeyIsSensitiveToASingleBitOfIdentity(t *testing.T) {
	keyA := mustDeriveKey(t, identityA)
	flipped := mustDeriveKey(t, corrupt(t, identityA, 3))

	if bytes.Equal(keyA, flipped) {
		t.Fatalf("flipping one bit of the identity private half left the rail key " +
			"unchanged — the derivation is not consuming the whole input, so most of " +
			"the identity secret is contributing no entropy to the rail key")
	}
}

func TestDeriveKeyRejectsEmptyIdentity(t *testing.T) {
	cases := []struct {
		name  string
		input []byte
	}{
		{"nil", nil},
		{"zero-length slice", []byte{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, err := syncrail.DeriveKey(tc.input)
			if !errors.Is(err, syncrail.ErrEmptyIdentity) {
				t.Fatalf("DeriveKey with no identity material returned err=%v, want ErrEmptyIdentity — "+
					"deriving from nothing would hand every caller the same fixed key and the "+
					"caller would never learn the vault handed it an empty secret", err)
			}
			if key != nil {
				t.Fatalf("DeriveKey returned a %d-byte key alongside its error — a caller that "+
					"checks the key before the error would seal under an all-installs-identical key",
					len(key))
			}
		})
	}
}

func TestDeriveKeyReturnsExactlyKeySize(t *testing.T) {
	key := mustDeriveKey(t, identityA)
	if len(key) != syncrail.KeySize {
		t.Fatalf("DeriveKey returned %d bytes, want KeySize=%d — XChaCha20-Poly1305 refuses "+
			"any other length, so the whole rail would fail to initialise at send time",
			len(key), syncrail.KeySize)
	}

	if _, err := syncrail.Seal(key, []byte("probe")); err != nil {
		t.Fatalf("a KeySize-long DeriveKey output was rejected by Seal: %v", err)
	}
}

func TestSealAndOpenRejectAKeyThatIsNotKeySize(t *testing.T) {
	sealed := mustSeal(t, mustDeriveKey(t, identityA), []byte("body"))

	cases := []struct {
		name string
		key  []byte
	}{
		{"nil key", nil},
		{"too short", bytes.Repeat([]byte{0x5a}, syncrail.KeySize-1)},
		{"too long", bytes.Repeat([]byte{0x5a}, syncrail.KeySize+1)},
		{"raw identity material passed straight through", identityA},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if out, err := syncrail.Seal(tc.key, []byte("body")); err == nil {
				t.Fatalf("Seal accepted a %d-byte key and produced %d bytes — a caller that "+
					"hands over un-derived identity material must be told, not quietly "+
					"given a seal under something that is not the rail key",
					len(tc.key), len(out))
			}
			if out, err := syncrail.Open(tc.key, sealed); err == nil {
				t.Fatalf("Open accepted a %d-byte key and returned %d bytes", len(tc.key), len(out))
			}
		})
	}
}

func TestSealOpenRoundTripAcrossPayloadSizes(t *testing.T) {
	key := mustDeriveKey(t, identityA)

	cases := []struct {
		name      string
		plaintext []byte
	}{
		{"empty plaintext", []byte{}},
		{"single byte", []byte{0x00}},
		{"short text", []byte("mirror this edit to my other device")},
		{"one mebibyte", filler(1 << 20)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ct := mustSeal(t, key, tc.plaintext)
			got, err := syncrail.Open(key, ct)
			if err != nil {
				t.Fatalf("Open of a %d-byte payload we just sealed failed: %v — "+
					"a sibling would drop a legitimate mirror", len(tc.plaintext), err)
			}
			if !bytes.Equal(got, tc.plaintext) {
				t.Fatalf("Open returned %d bytes that differ from the %d sealed — "+
					"a mirrored mutation would be applied with corrupted content",
					len(got), len(tc.plaintext))
			}
		})
	}
}

func TestSealDrawsAFreshNoncePerCall(t *testing.T) {
	key := mustDeriveKey(t, identityA)
	plaintext := []byte("the same mutation sealed twice")

	first := mustSeal(t, key, plaintext)
	second := mustSeal(t, key, plaintext)

	if bytes.Equal(first, second) {
		t.Fatalf("two seals of identical plaintext under the same key produced identical " +
			"bytes — a deterministic seal lets haomad match repeated payloads and count " +
			"how often the same content crosses the rail")
	}
	if bytes.Equal(first[:syncrail.NonceSize], second[:syncrail.NonceSize]) {
		t.Fatalf("two seals reused the same nonce — XChaCha20-Poly1305 has no counter on " +
			"this rail because two devices seal concurrently; a repeated nonce under a " +
			"shared key leaks the plaintext XOR and destroys the seal outright")
	}
}

func TestOpenRejectsTheWrongKey(t *testing.T) {
	sealed := mustSeal(t, mustDeriveKey(t, identityA), []byte("private mirror body"))
	stranger := mustDeriveKey(t, identityB)

	plain, err := syncrail.Open(stranger, sealed)
	if err == nil {
		t.Fatalf("Open under an unrelated identity's key succeeded and returned %q — "+
			"a message from a non-sibling must fail closed, never be treated as mine", plain)
	}
}

func TestOpenRejectsTamperingInEveryRegion(t *testing.T) {
	key := mustDeriveKey(t, identityA)
	plaintext := []byte("a mirrored wrapper long enough to have a real ciphertext region")
	sealed := mustSeal(t, key, plaintext)

	tagStart := len(sealed) - tagOverhead
	cases := []struct {
		region string
		index  int
		why    string
	}{
		{
			region: "nonce",
			index:  2,
			why:    "a flipped nonce bit changes the keystream; if Open still succeeded the nonce would not be authenticated",
		},
		{
			region: "ciphertext",
			index:  syncrail.NonceSize + 4,
			why:    "a flipped ciphertext bit is an attacker editing the mirrored mutation in flight",
		},
		{
			region: "tag",
			index:  tagStart + 5,
			why:    "a flipped tag bit means the Poly1305 check is not actually being enforced",
		},
	}
	for _, tc := range cases {
		t.Run(tc.region, func(t *testing.T) {
			plain, err := syncrail.Open(key, corrupt(t, sealed, tc.index))
			if err == nil {
				t.Fatalf("Open accepted a payload with one bit flipped in the %s region and "+
					"returned %d bytes — %s", tc.region, len(plain), tc.why)
			}
		})
	}
}

func TestOpenRejectsInputTooShortToHaveComeFromSeal(t *testing.T) {
	key := mustDeriveKey(t, identityA)
	sealed := mustSeal(t, key, []byte("body"))

	cases := []struct {
		name  string
		input []byte
	}{
		{"nil", nil},
		{"empty", []byte{}},
		{"one byte", sealed[:1]},
		{"nonce only", sealed[:syncrail.NonceSize]},
		{"one byte short of nonce+tag", sealed[:syncrail.NonceSize+tagOverhead-1]},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plain, err := syncrail.Open(key, tc.input)
			if !errors.Is(err, syncrail.ErrShortCiphertext) {
				t.Fatalf("Open of %d bytes returned err=%v, want ErrShortCiphertext — a "+
					"truncated payload must be named as malformed, not slice out of range "+
					"and take the receive loop down", len(tc.input), err)
			}
			if plain != nil {
				t.Fatalf("Open returned %d bytes alongside its length error", len(plain))
			}
		})
	}
}

func TestSealOutputIsExactlyNoncePlusPlaintextPlusTag(t *testing.T) {
	key := mustDeriveKey(t, identityA)

	for _, size := range []int{0, 1, 64, 4096} {
		sealed := mustSeal(t, key, filler(size))
		want := syncrail.NonceSize + size + tagOverhead
		if len(sealed) != want {
			t.Fatalf("Seal of %d bytes produced %d bytes, want %d (nonce %d + plaintext + tag %d) — "+
				"any extra byte is a framing header, and a framing header is exactly the "+
				"fingerprint haomad would use to tell rail traffic from peer traffic",
				size, len(sealed), want, syncrail.NonceSize, tagOverhead)
		}
	}
}

func TestSealedPayloadIsIndistinguishableFromAnyOpaqueBlob(t *testing.T) {
	key := mustDeriveKey(t, identityA)

	plaintext := []byte(`{"fingerprint":"AB12CD34","msg_id":"m-991","wire_kind":"text"}`)

	const seals = 64
	outputs := make([][]byte, 0, seals)
	seen := make(map[string]bool, seals)
	for i := 0; i < seals; i++ {
		sealed := mustSeal(t, key, plaintext)
		outputs = append(outputs, sealed)

		nonce := string(sealed[:syncrail.NonceSize])
		if seen[nonce] {
			t.Fatalf("nonce repeated within %d seals — a repeat is both a crypto break and "+
				"a correlatable constant on the wire", seals)
		}
		seen[nonce] = true

		for _, needle := range [][]byte{
			plaintext,
			[]byte("fingerprint"),
			[]byte("msg_id"),
			[]byte("wire_kind"),
			[]byte("AB12CD34"),
		} {
			if bytes.Contains(sealed, needle) {
				t.Fatalf("sealed payload contains the cleartext %q — haomad forwards these "+
					"bytes and would be able to read, or at minimum recognise, rail traffic; "+
					"a rail payload must look like any other opaque encrypted blob",
					needle)
			}
		}

		if bytes.HasPrefix(sealed, plaintext[:4]) {
			t.Fatalf("sealed payload begins with the plaintext's own JSON opening %q — "+
				"the seal is passing structure straight through to the wire", plaintext[:4])
		}
	}

	for pos := 0; pos < syncrail.NonceSize; pos++ {
		constant := true
		for _, sealed := range outputs[1:] {
			if sealed[pos] != outputs[0][pos] {
				constant = false
				break
			}
		}
		if constant {
			t.Fatalf("byte %d is identical across all %d seals — a constant at a fixed "+
				"offset is a magic number by another name, and haomad matching it would "+
				"learn which of my PeerIDs are actually my own devices", pos, seals)
		}
	}
}
