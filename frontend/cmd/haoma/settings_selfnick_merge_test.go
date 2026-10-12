package main

import (
	"log/slog"
	"testing"
	"time"

	"haoma-frontend/internal/store"
	"haoma-frontend/internal/syncrail"
)

func newNickDaemon(t *testing.T) *daemon {
	t.Helper()
	st, err := store.Unlock(t.TempDir(), "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Lock() })
	d := &daemon{store: st}
	if err := d.loadSelfNickInto(); err != nil {
		t.Fatal(err)
	}
	return d
}

func heldNick(t *testing.T, d *daemon) selfNickState {
	t.Helper()
	cur, err := loadSelfNickState(d.store)
	if err != nil {
		t.Fatalf("read the held nick: %v", err)
	}
	return cur
}

func mergeNick(t *testing.T, d *daemon, nick string, at int64) bool {
	t.Helper()
	moved, err := d.mergeSelfNick(slog.Default(), nick, at)
	if err != nil {
		t.Fatalf("merge %q@%d: %v", nick, at, err)
	}
	return moved
}

func TestSelfNickMerge_NewerNameWins(t *testing.T) {
	d := newNickDaemon(t)
	if err := writeSelfNick(d.store, "mara", 1000); err != nil {
		t.Fatal(err)
	}

	if !mergeNick(t, d, "mara-phone", 2000) {
		t.Fatal("the merge reported no change; a newer name must take the field")
	}
	cur := heldNick(t, d)
	if cur.nick != "mara-phone" {
		t.Errorf("the device holds %q, want %q", cur.nick, "mara-phone")
	}
	if cur.at != 2000 {
		t.Errorf("the stamp is %d, want %d — a name stored under the old stamp loses the next merge", cur.at, 2000)
	}
	if got := d.selfNick(); got != "mara-phone" {
		t.Errorf("the cached nick is %q, want %q", got, "mara-phone")
	}
}

func TestSelfNickMerge_OlderNameLoses(t *testing.T) {
	d := newNickDaemon(t)
	if err := writeSelfNick(d.store, "mara", 2000); err != nil {
		t.Fatal(err)
	}

	if mergeNick(t, d, "mara-old", 1000) {
		t.Fatal("the merge reported a change; an older name must lose")
	}
	cur := heldNick(t, d)
	if cur.nick != "mara" {
		t.Errorf("the device holds %q, want %q", cur.nick, "mara")
	}
	if cur.at != 2000 {
		t.Errorf("the stamp is %d, want %d", cur.at, 2000)
	}
}

func TestSelfNickMerge_EqualStampsSettleOnTheValue(t *testing.T) {
	const at int64 = 3000

	alpha := newNickDaemon(t)
	if err := writeSelfNick(alpha.store, "alpha", at); err != nil {
		t.Fatal(err)
	}
	beta := newNickDaemon(t)
	if err := writeSelfNick(beta.store, "beta", at); err != nil {
		t.Fatal(err)
	}

	mergeNick(t, alpha, "beta", at)
	mergeNick(t, beta, "alpha", at)

	a, b := heldNick(t, alpha), heldNick(t, beta)
	if a.nick != b.nick {
		t.Fatalf("the two devices settled on %q and %q — one identity now holds two names for ever", a.nick, b.nick)
	}
	if a.at != at || b.at != at {
		t.Errorf("the stamps moved to %d and %d, want %d on both", a.at, b.at, at)
	}
}

func TestSelfNickMerge_TakenWhereNoNameIsHeld(t *testing.T) {
	d := newNickDaemon(t)
	if cur := heldNick(t, d); !cur.isDefault {
		t.Fatalf("a fresh device holds %q and does not read as the placeholder", cur.nick)
	}

	if !mergeNick(t, d, "mara", 0) {
		t.Fatal("the merge reported no change; a name must be taken where none is held")
	}
	cur := heldNick(t, d)
	if cur.nick != "mara" {
		t.Errorf("the device holds %q, want %q", cur.nick, "mara")
	}
	if cur.isDefault {
		t.Error("the device still reads as being on the placeholder")
	}
	if cur.at != 0 {
		t.Errorf("the stamp is %d, want 0 — an unstamped name must not be stamped on arrival", cur.at)
	}
}

func TestSelfNickMerge_LocalWriteBeatsAClockAhead(t *testing.T) {
	d := newNickDaemon(t)

	ahead := time.Now().Unix() + 3600
	if !mergeNick(t, d, "from-sibling", ahead) {
		t.Fatal("the seeding merge changed nothing")
	}

	if _, err := d.setSelfNick("typed-here"); err != nil {
		t.Fatal(err)
	}
	cur := heldNick(t, d)
	if cur.nick != "typed-here" {
		t.Fatalf("the device holds %q, want %q", cur.nick, "typed-here")
	}
	if cur.at <= ahead {
		t.Fatalf("the rename was stamped %d against a held %d — it loses the next merge", cur.at, ahead)
	}

	if mergeNick(t, d, "from-sibling", ahead) {
		t.Fatal("the sibling's older name took the field back")
	}
	if got := heldNick(t, d).nick; got != "typed-here" {
		t.Fatalf("the device holds %q, want %q", got, "typed-here")
	}
}

func TestSelfNickSnapshot_ThePlaceholderIsNotSent(t *testing.T) {
	d := newNickDaemon(t)
	if snap, ok := selfNickSnapshot(d); ok {
		t.Fatalf("a device on the placeholder offered %q to its siblings", snap.Nick)
	}

	if _, err := d.setSelfNick("mara"); err != nil {
		t.Fatal(err)
	}
	snap, ok := selfNickSnapshot(d)
	if !ok {
		t.Fatal("a device with a name offered nothing")
	}
	if snap.Nick != "mara" {
		t.Errorf("it offered %q, want %q", snap.Nick, "mara")
	}
	if snap.NickAt == 0 {
		t.Error("it offered no stamp; an unstamped name loses to every stamped one")
	}
}

func TestApplySelfNick_MergesTheSiblingsName(t *testing.T) {
	d := newNickDaemon(t)
	applySelfNick(d, slog.Default(), syncrail.SelfSnapshot{Nick: "mara", NickAt: 4000})
	cur := heldNick(t, d)
	if cur.nick != "mara" || cur.at != 4000 {
		t.Fatalf("the device holds %q@%d, want %q@%d", cur.nick, cur.at, "mara", 4000)
	}
}
