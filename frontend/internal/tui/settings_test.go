package tui

import (
	"strings"
	"testing"

	"github.com/rivo/tview"

	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/vault"
)

func TestSaveAllDirtySettings_CallsRegisteredHandlers(t *testing.T) {
	a := &App{}
	calls := []string{}
	a.settings = &settingsPage{
		dirty: map[string]bool{
			settingsDomainProfile: true,
			settingsDomainLock:    true,
			settingsDomainTor:     false,
		},
		saveHandlers: map[string]func() error{
			settingsDomainProfile: func() error { calls = append(calls, "profile"); return nil },
			settingsDomainLock:    func() error { calls = append(calls, "lock"); return nil },
			settingsDomainTor:     func() error { calls = append(calls, "tor"); return nil },
		},
	}
	proceeded := false
	a.saveAllDirtySettings(func() { proceeded = true })
	if !proceeded {
		t.Error("onProceed was not called")
	}

	wantPrefixes := []string{"profile", "lock"}
	if len(calls) != len(wantPrefixes) {
		t.Fatalf("calls = %v, want %v (only dirty domains)", calls, wantPrefixes)
	}
	for i, want := range wantPrefixes {
		if calls[i] != want {
			t.Errorf("call[%d] = %q, want %q", i, calls[i], want)
		}
	}
}

func TestRenderAdvisory_SeverityBranches(t *testing.T) {
	cases := []struct {
		severity   string
		message    string
		wantPrefix string
		wantBody   string
	}{
		{"info", "all good", "[gray]", "all good"},
		{"warn", "stale tor address", "[orange]⚠ ", "stale tor address"},
		{"error", "auth failed", "[red]✕ ", "auth failed"},
		{"unknown-severity-falls-to-info", "fallback", "[gray]", "fallback"},
	}
	for _, c := range cases {
		got := renderAdvisory(c.severity, "test.source", c.message)
		if !strings.HasPrefix(got, c.wantPrefix) {
			t.Errorf("severity %q: prefix = %q, want prefix %q", c.severity, got, c.wantPrefix)
		}
		if !strings.Contains(got, c.wantBody) {
			t.Errorf("severity %q: body missing %q in %q", c.severity, c.wantBody, got)
		}
		if !strings.HasSuffix(got, "[-]") {
			t.Errorf("severity %q: missing closing tag in %q", c.severity, got)
		}
	}
}

func TestSettingsPage_MarkDirtyClean(t *testing.T) {
	sp := &settingsPage{dirty: map[string]bool{}}
	sp.markDirty(settingsDomainLock)
	if !sp.dirty[settingsDomainLock] {
		t.Error("markDirty failed to flip the bit")
	}
	if sp.dirty[settingsDomainProfile] {
		t.Error("markDirty leaked to other domains")
	}
	sp.markClean(settingsDomainLock)
	if sp.dirty[settingsDomainLock] {
		t.Error("markClean failed to clear the bit")
	}
}

func TestSettingsPage_AnyDirtyHelper(t *testing.T) {
	a := &App{}
	if a.settingsAnyDirty() {
		t.Error("nil settings should report no dirty")
	}
	a.settings = &settingsPage{dirty: map[string]bool{}}
	if a.settingsAnyDirty() {
		t.Error("empty dirty map should report no dirty")
	}
	a.settings.dirty[settingsDomainNotifs] = true
	if !a.settingsAnyDirty() {
		t.Error("expected aggregate dirty when one domain is dirty")
	}
	a.settings.dirty[settingsDomainNotifs] = false
	if a.settingsAnyDirty() {
		t.Error("expected aggregate clean when all domains cleared")
	}
}

func inspectForm(form *tview.Form) []string {
	out := []string{}
	for i := 0; i < form.GetFormItemCount(); i++ {
		item := form.GetFormItem(i)
		switch v := item.(type) {
		case *tview.InputField:
			out = append(out, "input:"+v.GetLabel()+":"+v.GetText())
		case *tview.Checkbox:
			s := "checkbox:" + v.GetLabel() + ":"
			if v.IsChecked() {
				s += "true"
			} else {
				s += "false"
			}
			out = append(out, s)
		case *bracketCheckbox:
			s := "checkbox:" + v.GetLabel() + ":"
			if v.Checked() {
				s += "true"
			} else {
				s += "false"
			}
			out = append(out, s)
		case *tview.DropDown:
			_, sel := v.GetCurrentOption()
			out = append(out, "dropdown:"+v.GetLabel()+":"+sel)
		case *tview.TextView:
			out = append(out, "textview:"+v.GetLabel())
		default:
			out = append(out, "?:"+v.GetLabel())
		}
	}
	return out
}

func TestBuildProfileForm_SeedsNick(t *testing.T) {
	sp := &settingsPage{initialNick: "alice", dirty: map[string]bool{}}
	form := buildProfileForm(&App{}, sp)
	items := inspectForm(form)
	if len(items) != 1 || !strings.HasPrefix(items[0], "input:Self nick:alice") {
		t.Errorf("Profile form items = %v, want one input seeded with alice", items)
	}
}

func TestBuildDefaultsForm_SeedsRetentionAndReceipts(t *testing.T) {
	sp := &settingsPage{
		initial: ipc.Settings{
			DefaultRetentionSec: 3600,
			DefaultSendReceipts: false,
		},
		dirty: map[string]bool{},
	}
	form := buildDefaultsForm(&App{}, sp)
	items := inspectForm(form)
	if len(items) != 2 {
		t.Fatalf("Defaults form items = %d, want 2", len(items))
	}
	if !strings.HasPrefix(items[0], "dropdown:Default disappearing messages:") {
		t.Errorf("first item not the retention dropdown: %v", items[0])
	}
	if !strings.Contains(items[0], "1h") {
		t.Errorf("retention dropdown didn't seed to 1h preset: %v", items[0])
	}
	if !strings.HasPrefix(items[1], "checkbox:Send read receipts by default:false") {
		t.Errorf("receipts checkbox should seed to false: %v", items[1])
	}
}

func TestBuildFilesForm_SeedsPickerDefaults(t *testing.T) {
	sp := &settingsPage{
		initial: ipc.Settings{
			DefaultSaveDir:        "/home/alice/Downloads",
			DefaultAttachStartDir: "/home/alice",
		},
		dirty: map[string]bool{},
	}
	form := buildFilesForm(&App{}, sp)
	items := inspectForm(form)

	if len(items) != 3 {
		t.Fatalf("Files form items = %d, want 3", len(items))
	}
	if !strings.Contains(items[0], "/home/alice/Downloads") {
		t.Errorf("save-dir input should seed to /home/alice/Downloads: %v", items[0])
	}
	if !strings.Contains(items[1], "/home/alice") {
		t.Errorf("attach-dir input should seed to /home/alice: %v", items[1])
	}
	if !strings.HasPrefix(items[2], "textview:Picker behaviour") {
		t.Errorf("third item should be the picker-behaviour hint: %v", items[2])
	}
}

func TestBuildFilesForm_BlankFieldsAccepted(t *testing.T) {
	sp := &settingsPage{
		initial: ipc.Settings{},
		dirty:   map[string]bool{},
	}
	form := buildFilesForm(&App{}, sp)
	items := inspectForm(form)
	if len(items) != 3 {
		t.Fatalf("Files form items = %d, want 3", len(items))
	}

	for i, want := range []string{"input:Default save folder", "input:Default attach folder"} {
		if !strings.HasPrefix(items[i], want) {
			t.Errorf("item %d should start with %q, got %v", i, want, items[i])
		}
		if !strings.HasSuffix(items[i], ":") {
			t.Errorf("item %d should have empty seed value, got %v", i, items[i])
		}
	}
}

func TestBuildLockForm_SeedsFields(t *testing.T) {

	sp := &settingsPage{
		initial: ipc.Settings{
			IdleAction:         "soft-lock",
			IdleTimeoutSeconds: 600,
			PinValiditySec:     120,
			PanicAction:        "hard-lock",
			ThreatProfile:      vault.PresetPrivacy,
		},
		dirty: map[string]bool{},
	}
	prim := buildLockForm(&App{}, sp)
	flex, ok := prim.(*tview.Flex)
	if !ok {
		t.Fatalf("buildLockForm should return *tview.Flex, got %T", prim)
	}

	if flex.GetItemCount() != 5 {
		t.Fatalf("Flex item count = %d, want 5 (status TV + formA + bundle TV + formB + formD)", flex.GetItemCount())
	}

	if _, isTV := flex.GetItem(0).(*tview.TextView); !isTV {
		t.Errorf("Flex item 0 should be the standalone status TextView, got %T", flex.GetItem(0))
	}
	if _, isTV := flex.GetItem(2).(*tview.TextView); !isTV {
		t.Errorf("Flex item 2 should be the standalone bundle TextView, got %T", flex.GetItem(2))
	}

	formA, ok := flex.GetItem(1).(*tview.Form)
	if !ok {
		t.Fatalf("Flex item 1 is %T, want *tview.Form", flex.GetItem(1))
	}
	itemsA := inspectForm(formA)
	if len(itemsA) != 1 {
		t.Fatalf("Form A items = %d, want 1 (preset dropdown only)", len(itemsA))
	}
	if !strings.HasPrefix(itemsA[0], "dropdown:Preset") || !strings.Contains(itemsA[0], "Privacy") {
		t.Errorf("Form A[0] should be preset dropdown seeded to Privacy: %v", itemsA[0])
	}

	formB, ok := flex.GetItem(3).(*tview.Form)
	if !ok {
		t.Fatalf("Flex item 3 is %T, want *tview.Form", flex.GetItem(3))
	}
	itemsB := inspectForm(formB)
	if len(itemsB) != 4 {
		t.Fatalf("Form B items = %d, want 4 (idle dropdown + 2 inputs + panic dropdown)", len(itemsB))
	}
	if !strings.Contains(itemsB[0], "soft-lock") {
		t.Errorf("idle dropdown should seed to soft-lock: %v", itemsB[0])
	}
	if !strings.Contains(itemsB[1], "600") {
		t.Errorf("idle timeout should seed to 600: %v", itemsB[1])
	}
	if !strings.Contains(itemsB[2], "120") {
		t.Errorf("pin validity should seed to 120: %v", itemsB[2])
	}
	if !strings.Contains(itemsB[3], "hard-lock") {
		t.Errorf("panic action should seed to hard-lock: %v", itemsB[3])
	}

	formD, ok := flex.GetItem(4).(*tview.Form)
	if !ok {
		t.Fatalf("Flex item 4 is %T, want *tview.Form", flex.GetItem(4))
	}
	if formD.GetButtonCount() != 2 {
		t.Errorf("Form D should have 2 buttons (Save, Cancel), got %d", formD.GetButtonCount())
	}
}

func TestThreatModelStatusLine(t *testing.T) {
	dom := vault.ThreatPresetBundles[vault.PresetDomestic]
	cases := []struct {
		name string
		s    ipc.Settings
		want string
	}{
		{
			name: "unset is Custom",
			s:    ipc.Settings{},
			want: "Custom",
		},
		{
			name: "domestic clean",
			s: ipc.Settings{
				ThreatProfile:      vault.PresetDomestic,
				IdleAction:         dom.IdleAction,
				IdleTimeoutSeconds: dom.IdleTimeoutSeconds,
				PinValiditySec:     dom.PinValiditySec,
				PanicAction:        dom.PanicAction,
			},
			want: "Domestic",
		},
		{
			name: "domestic-modified when timeout drifts",
			s: ipc.Settings{
				ThreatProfile:      vault.PresetDomestic,
				IdleAction:         dom.IdleAction,
				IdleTimeoutSeconds: dom.IdleTimeoutSeconds + 1,
				PinValiditySec:     dom.PinValiditySec,
				PanicAction:        dom.PanicAction,
			},
			want: "Domestic-modified",
		},
		{
			name: "activist surfaces unwired notice",
			s:    ipc.Settings{ThreatProfile: vault.PresetActivist},
			want: "not yet wired",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := threatModelStatusLine(c.s)
			if !strings.Contains(got, c.want) {
				t.Errorf("threatModelStatusLine = %q, want substring %q", got, c.want)
			}
		})
	}
}

func TestBuildNotificationsForm_SeedsAllFour(t *testing.T) {
	sp := &settingsPage{
		initial: ipc.Settings{
			NotifyShellEnabled:  true,
			NotifyShowSender:    true,
			NotifyShowBody:      false,
			NotificationsOnLock: true,
		},
		dirty: map[string]bool{},
	}
	form := buildNotificationsForm(&App{}, sp)
	items := inspectForm(form)

	if len(items) != 5 {
		t.Fatalf("Notifications form items = %d, want 5", len(items))
	}
	wantStates := []string{"true", "true", "false", "true"}
	for i, w := range wantStates {
		if !strings.HasSuffix(items[i], ":"+w) {
			t.Errorf("checkbox %d state = %v, want suffix %q", i, items[i], w)
		}
	}
	if !strings.HasPrefix(items[4], "textview:Privacy posture") {
		t.Errorf("last item should be the privacy banner: %v", items[4])
	}
}

func TestBuildAdvancedForm_SeedsSecurityWarnings(t *testing.T) {

	sp := &settingsPage{
		initial: ipc.Settings{
			SecurityWarnings: []string{"pin_validity_exceeds_recommended"},
		},
		dirty: map[string]bool{},
	}
	prim := buildAdvancedForm(&App{}, sp)
	flex, ok := prim.(*tview.Flex)
	if !ok {
		t.Fatalf("Advanced root = %T, want *tview.Flex", prim)
	}
	if flex.GetItemCount() != 1 {
		t.Fatalf("Advanced flex items = %d, want 1", flex.GetItemCount())
	}
	tv, ok := flex.GetItem(0).(*tview.TextView)
	if !ok {
		t.Fatalf("Advanced child = %T, want *tview.TextView", flex.GetItem(0))
	}
	if !strings.Contains(tv.GetText(true), "pin_validity_exceeds_recommended") {
		t.Errorf("Advanced body missing warning: %q", tv.GetText(true))
	}
}

func TestBuildTorForm_PasswordSetIndicator(t *testing.T) {
	cases := []struct {
		name string
		set  bool
		want string
	}{
		{"set", true, "configured"},
		{"unset", false, "not set"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sp := &settingsPage{
				initial: ipc.Settings{HasTorPassword: c.set},
				dirty:   map[string]bool{},
			}
			form := buildTorForm(&App{}, sp)

			if form.GetFormItemCount() != 1 {
				t.Fatalf("Tor form items = %d, want 1", form.GetFormItemCount())
			}
			tv, ok := form.GetFormItem(0).(*tview.TextView)
			if !ok {
				t.Fatalf("Tor form item 0 is not a TextView")
			}
			text := tv.GetText(false)
			if !strings.Contains(text, c.want) {
				t.Errorf("HasTorPassword=%v: textview = %q, want substring %q", c.set, text, c.want)
			}
		})
	}
}
