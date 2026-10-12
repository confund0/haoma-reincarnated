package gapfill_test

import (
	"testing"

	"haoma-frontend/internal/gapfill"
	"haoma-frontend/internal/store"
)

func init() {
	store.DefaultKDFParams = store.KDFParams{
		Time: 1, Memory: 8 * 1024, Threads: 2, KeyLen: 32, SaltLen: 16,
	}
}

func newWatermarks(t *testing.T) *gapfill.Watermarks {
	t.Helper()
	st, err := store.Unlock(t.TempDir(), "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Lock() })
	return gapfill.NewWatermarks(st)
}

func TestWatermarks_UnknownSiblingIsZero(t *testing.T) {
	w := newWatermarks(t)
	got, err := w.Get("sib-1")
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Fatalf("got %d, want 0", got)
	}
}

func TestWatermarks_AdvanceRoundTrips(t *testing.T) {
	w := newWatermarks(t)

	moved, err := w.Advance("sib-1", 42)
	if err != nil {
		t.Fatal(err)
	}
	if !moved {
		t.Fatal("first advance reported no movement")
	}
	got, err := w.Get("sib-1")
	if err != nil {
		t.Fatal(err)
	}
	if got != 42 {
		t.Fatalf("got %d, want 42", got)
	}
}

func TestWatermarks_NeverRewinds(t *testing.T) {
	w := newWatermarks(t)

	if _, err := w.Advance("sib-1", 100); err != nil {
		t.Fatal(err)
	}
	moved, err := w.Advance("sib-1", 50)
	if err != nil {
		t.Fatal(err)
	}
	if moved {
		t.Fatal("a lower value was accepted; cursors must only move forward")
	}
	if got, _ := w.Get("sib-1"); got != 100 {
		t.Fatalf("cursor rewound to %d", got)
	}

	if moved, _ := w.Advance("sib-1", 100); moved {
		t.Fatal("re-advancing to the current value reported movement")
	}
}

func TestWatermarks_PerSibling(t *testing.T) {
	w := newWatermarks(t)

	if _, err := w.Advance("sib-1", 10); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Advance("sib-2", 20); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.Get("sib-1"); got != 10 {
		t.Fatalf("sib-1 = %d, want 10", got)
	}
	if got, _ := w.Get("sib-2"); got != 20 {
		t.Fatalf("sib-2 = %d, want 20", got)
	}

}

func TestWatermarks_HeldAndAckedAreSeparate(t *testing.T) {
	w := newWatermarks(t)

	if _, err := w.Advance("sib-1", 500); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.AckedBy("sib-1"); got != 0 {
		t.Fatalf("acknowledgement reads %d after only a held-cursor write", got)
	}
	if _, err := w.RecordAck("sib-1", 7); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.Get("sib-1"); got != 500 {
		t.Fatalf("held cursor = %d after an acknowledgement write, want 500", got)
	}
	if got, _ := w.AckedBy("sib-1"); got != 7 {
		t.Fatalf("acknowledgement = %d, want 7", got)
	}
}

func TestWatermarks_RecordAckNeverRewinds(t *testing.T) {
	w := newWatermarks(t)

	if _, err := w.RecordAck("sib-1", 100); err != nil {
		t.Fatal(err)
	}
	moved, err := w.RecordAck("sib-1", 50)
	if err != nil {
		t.Fatal(err)
	}
	if moved {
		t.Fatal("a stale request rewound the acknowledgement; pruned entries would be re-owed")
	}
	if got, _ := w.AckedBy("sib-1"); got != 100 {
		t.Fatalf("acknowledgement = %d, want 100", got)
	}
}

func TestWatermarks_AllAcksEnumeratesEverySibling(t *testing.T) {
	w := newWatermarks(t)

	if _, err := w.RecordAck("sib-1", 10); err != nil {
		t.Fatal(err)
	}
	if _, err := w.RecordAck("sib-2", 20); err != nil {
		t.Fatal(err)
	}

	if _, err := w.Advance("sib-3", 999); err != nil {
		t.Fatal(err)
	}

	all, err := w.AllAcks()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all["sib-1"] != 10 || all["sib-2"] != 20 {
		t.Fatalf("AllAcks = %v", all)
	}
}

func TestWatermarks_Forget(t *testing.T) {
	w := newWatermarks(t)

	if _, err := w.Advance("sib-1", 10); err != nil {
		t.Fatal(err)
	}
	if _, err := w.RecordAck("sib-1", 5); err != nil {
		t.Fatal(err)
	}
	if err := w.Forget("sib-1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.Get("sib-1"); got != 0 {
		t.Fatalf("forgotten cursor still reads %d", got)
	}

	if got, _ := w.AckedBy("sib-1"); got != 0 {
		t.Fatalf("forgotten acknowledgement still reads %d", got)
	}

	if err := w.Forget("sib-never"); err != nil {
		t.Fatalf("forgetting an absent cursor errored: %v", err)
	}
}

func TestWatermarks_RejectsEmptyPeerID(t *testing.T) {
	w := newWatermarks(t)
	if _, err := w.Get(""); err == nil {
		t.Fatal("empty peer id accepted on Get")
	}
	if _, err := w.Advance("", 1); err == nil {
		t.Fatal("empty peer id accepted on Advance")
	}
}
