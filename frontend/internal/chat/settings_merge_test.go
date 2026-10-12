package chat_test

import (
	"testing"

	"haoma-frontend/internal/chat"
)

func newChatWithSettings(t *testing.T) (*chat.Store, chat.ChatID) {
	t.Helper()
	st := chat.NewStore(newStore(t))
	dc, err := st.CreateDirect("peer-1")
	if err != nil {
		t.Fatal(err)
	}
	return st, dc.ID
}

func settingsOf(t *testing.T, st *chat.Store, id chat.ChatID) chat.Chat {
	t.Helper()
	c, err := st.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestMergeSettingsInheritsFromASibling(t *testing.T) {
	st, id := newChatWithSettings(t)
	moved, err := st.MergeSettings(id, chat.SettingsUpdate{
		Muted:        true,
		Receipts:     true,
		NickOverride: "quiet one",
		At:           chat.SettingsStamps{Muted: 100, Receipts: 100, NickOverride: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 3 {
		t.Fatalf("moved %v, want all three fields", moved)
	}
	c := settingsOf(t, st, id)
	if !c.IsNotificationsMuted() || !c.ReadReceiptsDisabled() {
		t.Fatalf("toggles not inherited: muted=%v receipts_disabled=%v", c.IsNotificationsMuted(), c.ReadReceiptsDisabled())
	}
}

func TestMergeSettingsCarriesAClearedValue(t *testing.T) {
	st, id := newChatWithSettings(t)
	if err := st.SetNotificationsMuted(id, true); err != nil {
		t.Fatal(err)
	}
	if err := st.SetNickOverride(id, "quiet one"); err != nil {
		t.Fatal(err)
	}

	local := settingsOf(t, st, id)
	later := local.Stamps().Muted + 60
	moved, err := st.MergeSettings(id, chat.SettingsUpdate{
		Muted:        false,
		NickOverride: "",
		At:           chat.SettingsStamps{Muted: later, NickOverride: later},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 2 {
		t.Fatalf("moved %v, want muted and nick_override", moved)
	}
	c := settingsOf(t, st, id)
	if c.IsNotificationsMuted() {
		t.Fatal("un-muting did not cross — this is the defect the slice exists to fix")
	}
	if c.NickOverrideValue() != "" {
		t.Fatalf("a cleared nick did not cross: %q", c.NickOverrideValue())
	}
}

func TestMergeSettingsRefusesAnOlderChange(t *testing.T) {
	st, id := newChatWithSettings(t)
	if err := st.SetNotificationsMuted(id, true); err != nil {
		t.Fatal(err)
	}
	local := settingsOf(t, st, id)
	earlier := local.Stamps().Muted - 60

	moved, err := st.MergeSettings(id, chat.SettingsUpdate{
		Muted: false,
		At:    chat.SettingsStamps{Muted: earlier},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 0 {
		t.Fatalf("moved %v, want nothing — an older change won", moved)
	}
	if !settingsOf(t, st, id).IsNotificationsMuted() {
		t.Fatal("an older un-mute overwrote a newer mute")
	}
}

func TestMergeSettingsKeepsTwoDifferentFields(t *testing.T) {
	st, id := newChatWithSettings(t)
	if err := st.SetNickOverride(id, "mine"); err != nil {
		t.Fatal(err)
	}
	local := settingsOf(t, st, id)

	moved, err := st.MergeSettings(id, chat.SettingsUpdate{
		Muted:        true,
		NickOverride: "",
		At:           chat.SettingsStamps{Muted: local.Stamps().NickOverride - 60},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 1 || moved[0] != "muted" {
		t.Fatalf("moved %v, want muted only", moved)
	}
	c := settingsOf(t, st, id)
	if !c.IsNotificationsMuted() {
		t.Fatal("the sibling's mute was discarded along with its empty nick")
	}
	if c.NickOverrideValue() != "mine" {
		t.Fatalf("this device's newer nick was overwritten: %q", c.NickOverrideValue())
	}
}

func TestMergeSettingsZeroStampLosesToAnySetting(t *testing.T) {
	st, id := newChatWithSettings(t)
	if err := st.SetNotificationsMuted(id, true); err != nil {
		t.Fatal(err)
	}
	moved, err := st.MergeSettings(id, chat.SettingsUpdate{Muted: false})
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 0 {
		t.Fatalf("moved %v, want nothing", moved)
	}
	if !settingsOf(t, st, id).IsNotificationsMuted() {
		t.Fatal("a sibling that never set the field un-muted this one")
	}
}

func TestMergeSettingsTieConvergesFromBothSides(t *testing.T) {
	const stamp = 500

	stA, idA := newChatWithSettings(t)
	if _, err := stA.MergeSettings(idA, chat.SettingsUpdate{
		Muted: false, At: chat.SettingsStamps{Muted: stamp},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := stA.MergeSettings(idA, chat.SettingsUpdate{
		Muted: true, At: chat.SettingsStamps{Muted: stamp},
	}); err != nil {
		t.Fatal(err)
	}

	stB, idB := newChatWithSettings(t)
	if _, err := stB.MergeSettings(idB, chat.SettingsUpdate{
		Muted: true, At: chat.SettingsStamps{Muted: stamp},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := stB.MergeSettings(idB, chat.SettingsUpdate{
		Muted: false, At: chat.SettingsStamps{Muted: stamp},
	}); err != nil {
		t.Fatal(err)
	}

	a := settingsOf(t, stA, idA).IsNotificationsMuted()
	b := settingsOf(t, stB, idB).IsNotificationsMuted()
	if a != b {
		t.Fatalf("the two devices disagree after merging at an equal stamp: %v vs %v — "+
			"each reports a successful merge and no later sweep can see the difference", a, b)
	}
}

func TestLocalChangesAreStamped(t *testing.T) {
	st, id := newChatWithSettings(t)
	before := settingsOf(t, st, id).Stamps()
	if before.Muted != 0 || before.Receipts != 0 || before.NickOverride != 0 {
		t.Fatalf("a fresh chat carries stamps: %+v", before)
	}
	if err := st.SetNotificationsMuted(id, true); err != nil {
		t.Fatal(err)
	}
	if err := st.SetDisableReadReceipts(id, true); err != nil {
		t.Fatal(err)
	}
	if err := st.SetNickOverride(id, "x"); err != nil {
		t.Fatal(err)
	}
	after := settingsOf(t, st, id).Stamps()
	if after.Muted == 0 || after.Receipts == 0 || after.NickOverride == 0 {
		t.Fatalf("a local change left a field unstamped, so it can never merge: %+v", after)
	}
}

func TestMergeSettingsRefusesAnUnknownChat(t *testing.T) {
	st := chat.NewStore(newStore(t))
	if _, err := st.MergeSettings("no-such-chat", chat.SettingsUpdate{Muted: true}); err == nil {
		t.Fatal("merging into an unknown conversation succeeded")
	}
}
