package pair_test

import (
	"encoding/hex"
	"testing"

	"haoma-frontend/internal/pair"
	"haoma-frontend/internal/store"
)

const (
	tagPersonA = "aa11bb22cc33dd44ee55ff6600778899"
	tagPersonB = "bb22cc33dd44ee55ff6600778899aa11"
)

func newTagStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Unlock(t.TempDir(), "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Lock() })
	return st
}

func offeringTag(t *testing.T, st *store.Store, fingerprint string) string {
	t.Helper()
	tag, err := pair.OfferingDeviceTag(st, fingerprint)
	if err != nil {
		t.Fatalf("OfferingDeviceTag(%s): %v", fingerprint, err)
	}
	return tag
}

func TestOfferingDeviceTag_SamePersonIsStableAcrossRestarts(t *testing.T) {
	dir := t.TempDir()
	st1, err := store.Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	first := offeringTag(t, st1, tagPersonA)
	if second := offeringTag(t, st1, tagPersonA); second != first {
		t.Fatalf("two offers to one person named this device %q then %q", first, second)
	}
	if err := st1.Lock(); err != nil {
		t.Fatal(err)
	}

	st2, err := store.Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st2.Lock() })
	if after := offeringTag(t, st2, tagPersonA); after != first {
		t.Fatalf("after a restart this device names itself %q to the same person, was %q", after, first)
	}
}

func TestOfferingDeviceTag_DifferentPeopleGetUnlinkableTags(t *testing.T) {
	device := newTagStore(t)
	toA := offeringTag(t, device, tagPersonA)
	toB := offeringTag(t, device, tagPersonB)
	if toA == toB {
		t.Fatalf("one device named itself %q to two different people — comparing notes identifies the device", toA)
	}

	other := newTagStore(t)
	if otherToB := offeringTag(t, other, tagPersonB); otherToB == toB {
		t.Fatal("the tag for a person is derivable from the person alone")
	}
}

func TestOfferingDeviceTag_TwoOfMyDevicesNameThemselvesApart(t *testing.T) {
	first := offeringTag(t, newTagStore(t), tagPersonA)
	second := offeringTag(t, newTagStore(t), tagPersonA)
	if first == second {
		t.Fatalf("two devices both named themselves %q to one person", first)
	}
}

func TestOfferingDeviceTag_WireShape(t *testing.T) {
	tag := offeringTag(t, newTagStore(t), tagPersonA)
	raw, err := hex.DecodeString(tag)
	if err != nil {
		t.Fatalf("tag %q is not hex: %v", tag, err)
	}
	if len(raw) != pair.OfferingTagLen {
		t.Fatalf("tag is %d bytes, want %d", len(raw), pair.OfferingTagLen)
	}
}

func TestOfferingDeviceTag_RefusesAnEmptyFingerprint(t *testing.T) {
	if tag, err := pair.OfferingDeviceTag(newTagStore(t), ""); err == nil {
		t.Fatalf("an empty fingerprint produced the tag %q", tag)
	}
	if tag, err := pair.OfferingDeviceTag(nil, tagPersonA); err == nil {
		t.Fatalf("a nil store produced the tag %q", tag)
	}
}
