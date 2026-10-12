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
			settingsDomainProfile:  true,
			settingsDomainSecurity: true,
			settingsDomainTor:      false,
		},
		saveHandlers: map[string]func() error{
			settingsDomainProfile:  func() error { calls = append(calls, "profile"); return nil },
			settingsDomainSecurity: func() error { calls = append(calls, "lock"); return nil },
			settingsDomainTor:      func() error { calls = append(calls, "tor"); return nil },
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
	sp.markDirty(settingsDomainSecurity)
	if !sp.dirty[settingsDomainSecurity] {
		t.Error("markDirty failed to flip the bit")
	}
	if sp.dirty[settingsDomainProfile] {
		t.Error("markDirty leaked to other domains")
	}
	sp.markClean(settingsDomainSecurity)
	if sp.dirty[settingsDomainSecurity] {
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
		initialRetention: 3600,
		initialReceipts:  false,
		dirty:            map[string]bool{},
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
			ThreatProfile:      vault.PresetPower,
		},
		dirty: map[string]bool{},
	}
	prim := buildSecurityForm(&App{}, sp)
	flex, ok := prim.(*tview.Flex)
	if !ok {
		t.Fatalf("buildSecurityForm should return *tview.Flex, got %T", prim)
	}

	if flex.GetItemCount() != 6 {
		t.Fatalf("Flex item count = %d, want 6 (status TV + formA + summary TV + note TV + formB + formD)", flex.GetItemCount())
	}

	for _, idx := range []int{0, 2, 3} {
		if _, isTV := flex.GetItem(idx).(*tview.TextView); !isTV {
			t.Errorf("Flex item %d should be a standalone TextView, got %T", idx, flex.GetItem(idx))
		}
	}

	formA, ok := flex.GetItem(1).(*tview.Form)
	if !ok {
		t.Fatalf("Flex item 1 is %T, want *tview.Form", flex.GetItem(1))
	}
	itemsA := inspectForm(formA)
	if len(itemsA) != 1 {
		t.Fatalf("Form A items = %d, want 1 (preset dropdown only)", len(itemsA))
	}
	if !strings.HasPrefix(itemsA[0], "dropdown:"+threatPickerQuestion) ||
		!strings.Contains(itemsA[0], "People with power") {
		t.Errorf("Form A[0] should be the tier picker seeded to \"People with power\": %v", itemsA[0])
	}

	formB, ok := flex.GetItem(4).(*tview.Form)
	if !ok {
		t.Fatalf("Flex item 4 is %T, want *tview.Form", flex.GetItem(4))
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

	formD, ok := flex.GetItem(5).(*tview.Form)
	if !ok {
		t.Fatalf("Flex item 5 is %T, want *tview.Form", flex.GetItem(5))
	}
	if formD.GetButtonCount() != 2 {
		t.Errorf("Form D should have 2 buttons (Save, Cancel), got %d", formD.GetButtonCount())
	}
}

func TestThreatModelStatusLine(t *testing.T) {
	dom := vault.ThreatPresetBundles[vault.PresetDomestic]
	clean := func(preset string, b vault.ThreatPresetBundle) ipc.Settings {
		return ipc.Settings{
			ThreatProfile:             preset,
			IdleAction:                b.IdleAction,
			IdleTimeoutSeconds:        b.IdleTimeoutSeconds,
			PinValiditySec:            b.PinValiditySec,
			PanicAction:               b.PanicAction,
			NotifyShellEnabled:        b.NotifyShellEnabled,
			NotifyShowSender:          b.NotifyShowSender,
			NotifyShowBody:            b.NotifyShowBody,
			NotificationsOnLock:       b.NotificationsOnLock,
			NotifyPersistUntilOpen:    b.NotifyPersistUntilOpen,
			NotifyDeepLink:            b.NotifyDeepLink,
			ShowFailedDestroyAttempts: b.ShowFailedDestroyAttempts,
		}
	}
	driftedTimeout := clean(vault.PresetDomestic, dom)
	driftedTimeout.IdleTimeoutSeconds++
	driftedNotify := clean(vault.PresetDomestic, dom)
	driftedNotify.NotifyShowSender = !dom.NotifyShowSender

	cases := []struct {
		name string
		s    ipc.Settings
		want string
	}{
		{"empty means the picker has not run", ipc.Settings{}, "not picked yet"},
		{"nobody clean", clean(vault.PresetNobody, vault.ThreatPresetBundles[vault.PresetNobody]), "No one"},
		{"domestic clean", clean(vault.PresetDomestic, dom), "People at home"},
		{"power clean", clean(vault.PresetPower, vault.ThreatPresetBundles[vault.PresetPower]), "People with power"},
		{"force clean", clean(vault.PresetForce, vault.ThreatPresetBundles[vault.PresetForce]), "People with force"},
		{"a retired name is named", ipc.Settings{ThreatProfile: "privacy"}, "retired answer"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := threatModelStatusLine(c.s)
			if !strings.Contains(got, c.want) {
				t.Errorf("threatModelStatusLine = %q, want substring %q", got, c.want)
			}
		})
	}

	driftCases := []struct {
		name string
		s    ipc.Settings
		want string
	}{
		{"an unanswered picker has nothing to compare", ipc.Settings{}, ""},
		{"a clean tier reads clean", clean(vault.PresetDomestic, dom), ""},
		{"a changed timeout is seen", driftedTimeout, "Some settings changed"},
		{"a changed notification row is seen", driftedNotify, "Some settings changed"},
		{"a retired name says why nothing saves", ipc.Settings{ThreatProfile: "privacy"}, "No settings can be saved"},
	}
	for _, c := range driftCases {
		t.Run("drift/"+c.name, func(t *testing.T) {
			got := threatModelDriftNote(c.s)
			if c.want == "" {
				if got != "" {
					t.Errorf("threatModelDriftNote = %q, want empty", got)
				}
				return
			}
			if !strings.Contains(got, c.want) {
				t.Errorf("threatModelDriftNote = %q, want substring %q", got, c.want)
			}
		})
	}
}

type countingVault struct {
	stubVaultInPackage
	mutations int
}

func (v *countingVault) Mutate(_ string, transform func(*vault.Payload) error) error {
	v.mutations++
	return transform(&vault.Payload{})
}

type stubVaultInPackage struct{}

func (stubVaultInPackage) ChangePassphrase(_, _ []byte) error              { return nil }
func (stubVaultInPackage) ChangePIN(_, _ string) error                     { return nil }
func (stubVaultInPackage) SetIdleAction(string) error                      { return nil }
func (stubVaultInPackage) SetIdleTimeoutSeconds(int) error                 { return nil }
func (stubVaultInPackage) SetPinValiditySec(int) error                     { return nil }
func (stubVaultInPackage) SetTorPassword(string) error                     { return nil }
func (stubVaultInPackage) IsInsecureDefaultPassphrase() bool               { return false }
func (stubVaultInPackage) IsInsecureDefaultPIN() bool                      { return false }
func (stubVaultInPackage) SetNotifyShellEnabled(bool) error                { return nil }
func (stubVaultInPackage) SetNotifyShowSender(bool) error                  { return nil }
func (stubVaultInPackage) SetNotifyShowBody(bool) error                    { return nil }
func (stubVaultInPackage) SetNotificationsOnLock(bool) error               { return nil }
func (stubVaultInPackage) SetNotifyPersistUntilOpen(bool) error            { return nil }
func (stubVaultInPackage) SetNotifyDeepLink(bool) error                    { return nil }
func (stubVaultInPackage) SetThreatProfile(string) error                   { return nil }
func (stubVaultInPackage) SetPanicAction(string) error                     { return nil }
func (stubVaultInPackage) ApplyThreatPreset(string) error                  { return nil }
func (stubVaultInPackage) SetDefaultSaveDir(string) error                  { return nil }
func (stubVaultInPackage) SetDefaultAttachStartDir(string) error           { return nil }
func (stubVaultInPackage) Settings() ipc.Settings                          { return ipc.Settings{} }
func (stubVaultInPackage) Mutate(string, func(*vault.Payload) error) error { return nil }
func (stubVaultInPackage) CheckPIN(string) bool                            { return false }

func TestBuildLockForm_SaveRefusesWhileTheStoredAnswerIsRetired(t *testing.T) {
	build := func(profile string) (*App, *countingVault) {
		vc := &countingVault{}
		a := &App{VaultCtl: vc}
		sp := &settingsPage{
			initial: ipc.Settings{
				ThreatProfile:      profile,
				IdleAction:         "soft-lock",
				IdleTimeoutSeconds: 600,
				PinValiditySec:     120,
				PanicAction:        "hard-lock",
			},
			dirty:        map[string]bool{},
			saveHandlers: map[string]func() error{},
		}
		buildSecurityForm(a, sp)
		a.settings = sp
		return a, vc
	}

	a, vc := build("privacy")
	err := a.settings.saveHandlers[settingsDomainSecurity]()
	if err == nil {
		t.Fatal("the save went through on a retired answer — it deletes the stored answer and says nothing")
	}
	for _, want := range []string{"retired", "privacy", threatAnswerApplyButton} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal is %q, it has to name %q so a person knows what to do", err, want)
		}
	}
	if vc.mutations != 0 {
		t.Errorf("the refused save still wrote to the vault %d times", vc.mutations)
	}

	for _, live := range []string{"", vault.PresetNobody, vault.PresetDomestic, vault.PresetPower, vault.PresetForce} {
		if err := retiredAnswerRefusal(live); err != nil {
			t.Errorf("answer %q is live and was refused: %v", live, err)
		}
	}
}

func TestThreatModelDriftNote_CleanTierIsNotReportedAsChanged(t *testing.T) {
	for _, preset := range []string{
		vault.PresetNobody, vault.PresetDomestic, vault.PresetPower, vault.PresetForce,
	} {
		b := vault.ThreatPresetBundles[preset]
		s := ipc.Settings{
			ThreatProfile:             preset,
			IdleAction:                b.IdleAction,
			IdleTimeoutSeconds:        b.IdleTimeoutSeconds,
			PinValiditySec:            b.PinValiditySec,
			PanicAction:               b.PanicAction,
			NotifyShellEnabled:        b.NotifyShellEnabled,
			NotifyShowSender:          b.NotifyShowSender,
			NotifyShowBody:            b.NotifyShowBody,
			NotificationsOnLock:       b.NotificationsOnLock,
			NotifyPersistUntilOpen:    b.NotifyPersistUntilOpen,
			NotifyDeepLink:            b.NotifyDeepLink,
			ShowFailedDestroyAttempts: b.ShowFailedDestroyAttempts,
		}
		if got := threatModelDriftNote(s); got != "" {
			t.Errorf("%s reads as changed straight after an apply: %q", preset, got)
		}
	}
}

func TestThreatTierTable_MatchesTheBundles(t *testing.T) {
	tiers := 0
	for _, row := range threatTierOrder {
		if row.token == "" {
			if row.answer != "(not picked yet)" {
				t.Errorf("the first row is %q, want \"(not picked yet)\"", row.answer)
			}
			continue
		}
		tiers++
		if _, ok := vault.ThreatPresetBundles[row.token]; !ok {
			t.Errorf("tier %q is on the picker with no bundle", row.token)
		}
		if row.line == "" {
			t.Errorf("tier %q has no line under its answer", row.token)
		}
		if got := threatTierIndex(row.token); threatTierOrder[got].token != row.token {
			t.Errorf("threatTierIndex(%q) landed on %q", row.token, threatTierOrder[got].token)
		}
	}
	if tiers != len(vault.ThreatPresetBundles) {
		t.Errorf("picker holds %d tiers, the bundle map holds %d", tiers, len(vault.ThreatPresetBundles))
	}
	if threatTierIndex("") != 0 || threatTierIndex("privacy") != 0 {
		t.Error("an empty or retired token must land on the not-picked-yet row")
	}
}

func TestThreatAnswerSummary_SaysWhatEachAnswerSets(t *testing.T) {
	cases := map[string][]string{
		vault.PresetNobody:   {"No one", "soft-lock after 900s", "until restart", "safe-lock", "4w"},
		vault.PresetDomestic: {"People at home", "soft-lock after 300s", "86400s", "safe-lock", "1w"},
		vault.PresetPower:    {"People with power", "safe-lock after 60s", "300s", "hard-lock", "1d"},
		vault.PresetForce:    {"People with force", "hard-lock after 30s", "until restart", "self-destruct", "1h"},
	}
	for token, wants := range cases {
		got := threatAnswerSummary(token)
		for _, want := range wants {
			if !strings.Contains(got, want) {
				t.Errorf("%s summary is missing %q:\n%s", token, want, got)
			}
		}
	}
	if !strings.Contains(threatAnswerSummary(""), "Pick an answer") {
		t.Error("the not-picked-yet row must say to pick an answer, not print a bundle")
	}
	if !strings.Contains(threatAnswerSummary("privacy"), "retired") {
		t.Error("a retired token must be named as retired")
	}
}

const summaryPaneWidth = 80 - 28 - 2

func TestThreatAnswerSummary_FitsAnEightyColumnTerminal(t *testing.T) {
	tokens := []string{"", "privacy", vault.PresetNobody, vault.PresetDomestic, vault.PresetPower, vault.PresetForce}
	for _, token := range tokens {
		for _, line := range strings.Split(threatAnswerSummary(token), "\n") {

			plain := stripStyleTags(line)
			if n := len([]rune(plain)); n > summaryPaneWidth {
				t.Errorf("token %q line is %d columns, the pane is %d:\n%s", token, n, summaryPaneWidth, plain)
			}
		}
	}
}

func TestLockPageNotes_FitAnEightyColumnTerminal(t *testing.T) {
	notes := []string{
		retentionNoteWaiting,
		retentionNoteRefused,
		retentionNoteUnknown,
		"How long messages are kept: " + retentionSummaryWord(vault.ThreatPresetBundles[vault.PresetNobody].DefaultRetentionSec) + ".",
		threatModelDriftNote(ipc.Settings{ThreatProfile: vault.PresetForce}),
		threatModelDriftNote(ipc.Settings{ThreatProfile: "privacy"}),
	}
	for _, tier := range []string{"", vault.PresetNobody, vault.PresetDomestic, vault.PresetPower, vault.PresetForce, "privacy"} {
		notes = append(notes, threatAnswerStatusPrefix+threatModelStatusLine(ipc.Settings{ThreatProfile: tier}))
	}
	for _, note := range notes {
		plain := stripStyleTags(note)
		if n := len([]rune(plain)); n > summaryPaneWidth {
			t.Errorf("note is %d columns, the pane is %d: %s", n, summaryPaneWidth, plain)
		}
	}
}

func stripStyleTags(s string) string {
	var out strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '[':
			depth++
		case r == ']' && depth > 0:
			depth--
		case depth == 0:
			out.WriteRune(r)
		}
	}
	return out.String()
}

func TestBuildNotificationsForm_SeedsEveryToggle(t *testing.T) {
	sp := &settingsPage{
		initial: ipc.Settings{
			NotifyShellEnabled:     true,
			NotifyShowSender:       true,
			NotifyShowBody:         false,
			NotificationsOnLock:    true,
			NotifyPersistUntilOpen: true,
			NotifyDeepLink:         false,

			ShowFailedDestroyAttempts: true,
		},
		dirty: map[string]bool{},
	}
	form := buildNotificationsForm(&App{}, sp)
	items := inspectForm(form)

	if len(items) != 8 {
		t.Fatalf("Notifications form items = %d, want 8", len(items))
	}
	wantStates := []string{"true", "true", "false", "true", "true", "false", "true"}
	for i, w := range wantStates {
		if !strings.HasSuffix(items[i], ":"+w) {
			t.Errorf("checkbox %d state = %v, want suffix %q", i, items[i], w)
		}
	}
	if !strings.HasPrefix(items[7], "textview:Privacy posture") {
		t.Errorf("last item should be the privacy banner: %v", items[7])
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
	if flex.GetItemCount() != 2 {
		t.Fatalf("Advanced flex items = %d, want 2", flex.GetItemCount())
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
		name   string
		system bool
		set    bool
		want   string
	}{
		{"system tor, set", true, true, "configured"},
		{"system tor, unset", true, false, "not set"},

		{"own tor, set", false, true, "not used"},
		{"own tor, unset", false, false, "not needed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sp := &settingsPage{
				initial: ipc.Settings{HasTorPassword: c.set},
				dirty:   map[string]bool{},
			}
			form := buildTorForm(&App{SystemTor: c.system}, sp)

			if form.GetFormItemCount() != 1 {
				t.Fatalf("Tor form items = %d, want 1", form.GetFormItemCount())
			}
			tv, ok := form.GetFormItem(0).(*tview.TextView)
			if !ok {
				t.Fatalf("Tor form item 0 is not a TextView")
			}
			text := tv.GetText(false)
			if !strings.Contains(text, c.want) {
				t.Errorf("SystemTor=%v HasTorPassword=%v: textview = %q, want substring %q", c.system, c.set, text, c.want)
			}

			if warned := strings.Contains(text, "⚠"); warned != (c.system && !c.set) {
				t.Errorf("SystemTor=%v HasTorPassword=%v: warning shown = %v", c.system, c.set, warned)
			}
		})
	}
}

func TestNewApp_ChatDefaultsMirrorStartsOnTheDaemonFallbacks(t *testing.T) {
	a := New(nil)
	if !a.defaultSendReceipts {
		t.Error("the receipts mirror starts OFF; the daemon's shipped fallback is ON")
	}
	if a.defaultRetentionSec != 28*24*3600 {
		t.Errorf("the retention mirror starts at %d; the daemon's shipped fallback is 4 weeks", a.defaultRetentionSec)
	}
	if a.chatDefaultsKnown {
		t.Error("the mirror claims the daemon has spoken before any frame arrived")
	}
}

func TestSendPresetRetention_RefusesBeforeTheDaemonHasSpoken(t *testing.T) {
	a := New(nil)
	a.chatDefaultsKnown = false
	sp := &settingsPage{dirty: map[string]bool{}}
	a.sendPresetRetention(sp, vault.PresetForce)
	if sp.retentionPending {
		t.Error("a frame went out before the daemon said what its chat defaults are")
	}
	if sp.retentionNote != retentionNoteUnknown {
		t.Errorf("the page says %q, want %q", sp.retentionNote, retentionNoteUnknown)
	}
}
