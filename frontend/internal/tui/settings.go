package tui

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/vault"
)

const (
	pageNameSettings = "settings"

	settingsDomainProfile  = "profile"
	settingsDomainDefaults = "defaults"
	settingsDomainFiles    = "files"
	settingsDomainSecurity = "security"
	settingsDomainTor      = "tor"
	settingsDomainLinked   = "linked-devices"
	settingsDomainNotifs   = "notifications"
	settingsDomainAdv      = "advanced"
)

var settingsDomainsOrder = []string{
	settingsDomainProfile,
	settingsDomainDefaults,
	settingsDomainFiles,
	settingsDomainSecurity,
	settingsDomainTor,
	settingsDomainLinked,
	settingsDomainNotifs,
	settingsDomainAdv,
}

var settingsDomainLabels = map[string]string{
	settingsDomainProfile:  "Profile",
	settingsDomainDefaults: "Chat defaults",
	settingsDomainFiles:    "Files",
	settingsDomainSecurity: "Security",
	settingsDomainTor:      "Tor",
	settingsDomainLinked:   "Linked devices",
	settingsDomainNotifs:   "Notifications",
	settingsDomainAdv:      "Advanced",
}

func renderAdvisory(severity, source, message string) string {
	_ = source
	switch severity {
	case "warn":
		return "[orange]⚠ " + message + "[-]"
	case "error":
		return "[red]✕ " + message + "[-]"
	default:
		return "[gray]" + message + "[-]"
	}
}

type settingsPage struct {
	grid  *tview.Grid
	list  *tview.List
	forms *tview.Pages

	initial ipc.Settings

	initialNick string

	initialRetention uint64
	initialReceipts  bool

	retentionNote string

	retentionWant    uint64
	retentionPending bool

	lockNoteTV *tview.TextView

	dirty map[string]bool

	saveHandlers map[string]func() error

	activePage string

	linked linkedDevicesState

	linkedShape string

	wipeNote *tview.TextView

	liveMu sync.Mutex
	live   linkLiveWidgets
}

func (a *App) cmdSettings() {
	if a.VaultCtl == nil {
		a.log(StyleLinkBad + "/settings unavailable" + StyleLinkBody + " — this session has no vault open, so there are no settings to read. Restart haoma-text.")
		return
	}
	a.winMu.Lock()
	if a.settings != nil {
		a.winMu.Unlock()
		a.switchTo(pageNameSettings)
		return
	}
	initial := a.VaultCtl.Settings()
	initialNick := a.selfNick
	initialRetention := a.defaultRetentionSec
	initialReceipts := a.defaultSendReceipts
	sp := newSettingsPage(a, initial, initialNick, initialRetention, initialReceipts)
	a.settings = sp
	a.winOrder = append(a.winOrder, pageNameSettings)
	a.winMu.Unlock()

	a.pages.AddPage(pageNameSettings, sp.grid, true, false)
	a.switchTo(pageNameSettings)
	a.app.SetFocus(sp.list)

	a.refreshLinkedDevicesState(sp)
}

func (a *App) closeSettings() {

	a.stopVerifyMarker()

	a.winMu.Lock()
	if a.settings == nil {
		a.winMu.Unlock()
		return
	}
	a.settings = nil
	idx := -1
	for i, name := range a.winOrder {
		if name == pageNameSettings {
			idx = i
			break
		}
	}
	if idx < 0 {
		a.winMu.Unlock()
		return
	}
	front, _ := a.pages.GetFrontPage()
	var next string
	a.winOrder, next = winOrderAfterClose(a.winOrder, idx)
	a.winMu.Unlock()

	a.pages.RemovePage(pageNameSettings)
	if front == pageNameSettings && next != "" {
		a.switchTo(next)
		return
	}
	a.winBar.SetText(a.winBarText())
}

func (a *App) settingsAnyDirty() bool {
	a.winMu.Lock()
	defer a.winMu.Unlock()
	if a.settings == nil {
		return false
	}
	for _, d := range a.settings.dirty {
		if d {
			return true
		}
	}
	return false
}

func (a *App) confirmDiscardSettings(onProceed func()) {
	const pageName = "settings-discard-confirm"
	modal := tview.NewModal().
		SetText("Settings have unsaved changes.\n\nSave them, discard them, or keep editing?").
		AddButtons([]string{"Save", "Discard", "Keep editing"}).
		SetDoneFunc(func(_ int, label string) {
			a.pages.RemovePage(pageName)
			switch label {
			case "Save":
				a.saveAllDirtySettings(onProceed)
			case "Discard":
				onProceed()
			default:

				a.app.SetFocus(a.settings.list)
			}
		})
	a.pages.AddPage(pageName, modal, true, true)
	a.app.SetFocus(modal)
}

func (a *App) saveAllDirtySettings(onProceed func()) {
	if a.settings == nil {
		onProceed()
		return
	}
	for _, key := range settingsDomainsOrder {
		if !a.settings.dirty[key] {
			continue
		}
		h := a.settings.saveHandlers[key]
		if h == nil {
			a.log("[red]settings[white] save %s: no handler registered", key)
			continue
		}
		if err := h(); err != nil {
			a.log("[red]settings[white] save %s: %v", key, err)
		}
	}
	onProceed()
}

func (a *App) pushSettingsSync() {
	if a.VaultCtl == nil {
		return
	}
	settings := a.VaultCtl.Settings()
	a.sendRequest(ipc.FrameSyncSettings, ipc.SyncSettingsRequest{Settings: settings}, nil)
}

func newSettingsPage(a *App, initial ipc.Settings, initialNick string, initialRetention uint64, initialReceipts bool) *settingsPage {
	sp := &settingsPage{
		initial:          initial,
		initialNick:      initialNick,
		initialRetention: initialRetention,
		initialReceipts:  initialReceipts,
		dirty:            map[string]bool{},
		saveHandlers:     map[string]func() error{},
		activePage:       settingsDomainsOrder[0],
	}

	for _, p := range a.peers {
		sp.linked.peerIDs = append(sp.linked.peerIDs, p.ID)
	}

	sp.list = tview.NewList().ShowSecondaryText(false)
	sp.list.SetBorder(true).SetTitle(" domains ")

	sp.list.SetBorderPadding(1, 0, 1, 1)
	for _, key := range settingsDomainsOrder {
		sp.list.AddItem(settingsDomainLabels[key], "", 0, nil)
	}

	sp.list.AddItem("[red]Close[-]", "", 0, nil)

	closeRowIndex := len(settingsDomainsOrder)

	sp.list.SetSelectedFunc(func(idx int, _ string, _ string, _ rune) {
		if idx == closeRowIndex {
			a.cmdCloseSettings()
			return
		}
		key := settingsDomainsOrder[idx]
		left := sp.activePage
		sp.activePage = key
		sp.forms.SwitchToPage(key)
		if left == settingsDomainLinked && key != settingsDomainLinked {
			a.leaveLinkedDevicesDomain(sp)
		}
		if form := sp.formForDomain(key); form != nil {
			a.app.SetFocus(form)
		}
	})
	sp.list.SetChangedFunc(func(idx int, _ string, _ string, _ rune) {
		if idx == closeRowIndex {

			return
		}
		key := settingsDomainsOrder[idx]
		left := sp.activePage
		sp.activePage = key
		sp.forms.SwitchToPage(key)
		if left == settingsDomainLinked && key != settingsDomainLinked {
			a.leaveLinkedDevicesDomain(sp)
		}
	})
	sp.list.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {

		if ev.Key() == tcell.KeyEscape {
			a.escPending = true
			return nil
		}
		return ev
	})

	sp.forms = tview.NewPages()
	sp.forms.AddPage(settingsDomainProfile, buildProfileForm(a, sp), true, true)
	sp.forms.AddPage(settingsDomainDefaults, buildDefaultsForm(a, sp), true, false)
	sp.forms.AddPage(settingsDomainFiles, buildFilesForm(a, sp), true, false)
	sp.forms.AddPage(settingsDomainSecurity, buildSecurityForm(a, sp), true, false)
	sp.forms.AddPage(settingsDomainTor, buildTorForm(a, sp), true, false)
	sp.forms.AddPage(settingsDomainLinked, buildLinkedDevicesForm(a, sp), true, false)
	sp.forms.AddPage(settingsDomainNotifs, buildNotificationsForm(a, sp), true, false)
	sp.forms.AddPage(settingsDomainAdv, buildAdvancedForm(a, sp), true, false)

	sp.grid = tview.NewGrid().
		SetColumns(28, 0).
		SetRows(0).
		AddItem(sp.list, 0, 0, 1, 1, 0, 0, true).
		AddItem(sp.forms, 0, 1, 1, 1, 0, 0, false)

	return sp
}

func (sp *settingsPage) formForDomain(key string) tview.Primitive {
	name, prim := sp.forms.GetFrontPage()
	if name == key && sp.activePage == key {
		return prim
	}
	return nil
}

func (sp *settingsPage) markDirty(key string) {
	sp.dirty[key] = true
}

func (sp *settingsPage) registerSave(key string, fn func() error) {
	if sp.saveHandlers == nil {
		sp.saveHandlers = map[string]func() error{}
	}
	sp.saveHandlers[key] = fn
}

func (sp *settingsPage) markClean(key string) {
	sp.dirty[key] = false
}

func (sp *settingsPage) rebuildForm(a *App, key string) {
	var prim tview.Primitive
	switch key {
	case settingsDomainProfile:
		prim = buildProfileForm(a, sp)
	case settingsDomainDefaults:
		prim = buildDefaultsForm(a, sp)
	case settingsDomainFiles:
		prim = buildFilesForm(a, sp)
	case settingsDomainSecurity:
		prim = buildSecurityForm(a, sp)
	case settingsDomainTor:
		prim = buildTorForm(a, sp)
	case settingsDomainLinked:
		prim = buildLinkedDevicesForm(a, sp)
	case settingsDomainNotifs:
		prim = buildNotificationsForm(a, sp)
	case settingsDomainAdv:
		prim = buildAdvancedForm(a, sp)
	default:
		return
	}

	takeFocus := sp.activePage == key && sp.forms.HasFocus()

	sp.forms.RemovePage(key)
	sp.forms.AddPage(key, prim, true, sp.activePage == key)
	sp.markClean(key)
	if takeFocus {
		a.app.SetFocus(prim)
	}
}

func (sp *settingsPage) returnToList(a *App) {
	a.app.SetFocus(sp.list)
}

func (a *App) cmdCloseSettings() {
	if a.settingsAnyDirty() {
		a.confirmDiscardSettings(func() { a.closeSettings() })
		return
	}
	a.closeSettings()
}

func buildProfileForm(a *App, sp *settingsPage) *tview.Form {
	form := tview.NewForm()
	form.SetBorder(true).SetTitle(" profile ")
	form.AddInputField("Self nick", sp.initialNick, 32, nil, func(_ string) {
		sp.markDirty(settingsDomainProfile)
	})

	save := func() error {
		nick := strings.TrimSpace(form.GetFormItemByLabel("Self nick").(*tview.InputField).GetText())
		if nick == "" {
			return fmt.Errorf("nick must not be empty")
		}
		a.sendRequest(ipc.FrameSetNick, ipc.SetNickRequest{Nick: nick}, func(f ipc.Frame) {
			if f.Type == ipc.FrameError {
				a.renderError(f)
				return
			}

			a.app.QueueUpdateDraw(func() {
				if !a.settingsPageIsOpen(sp) {
					return
				}
				sp.initialNick = nick
				sp.markClean(settingsDomainProfile)
			})
		})
		return nil
	}
	sp.registerSave(settingsDomainProfile, save)
	form.AddButton("Save", func() {
		if err := save(); err != nil {
			a.log("[red]save profile[white] %v", err)
			return
		}
		sp.returnToList(a)
	})

	form.AddButton("Cancel", func() {
		sp.rebuildForm(a, settingsDomainProfile)
		sp.returnToList(a)
	})
	form.SetCancelFunc(func() { sp.returnToList(a) })
	return form
}

func buildDefaultsForm(a *App, sp *settingsPage) *tview.Form {
	form := tview.NewForm()
	form.SetBorder(true).SetTitle(" chat defaults — applied to newly-paired chats ")
	dropdown := tview.NewDropDown().
		SetLabel("Default disappearing messages").
		SetOptions(retentionLabels(), nil)
	form.AddFormItem(dropdown)
	dropdown.SetCurrentOption(retentionOptionIndex(uint32(sp.initialRetention)))
	dropdown.SetSelectedFunc(func(_ string, _ int) { sp.markDirty(settingsDomainDefaults) })

	receiptsBox := newBracketCheckbox("Send read receipts by default", sp.initialReceipts, func(_ bool) {
		sp.markDirty(settingsDomainDefaults)
	})
	form.AddFormItem(receiptsBox)

	save := func() error {
		idx, _ := dropdown.GetCurrentOption()
		var ttl uint64
		if idx >= 0 && idx < len(retentionLevels) {
			ttl = uint64(retentionLevels[idx].seconds)
		}
		receipts := receiptsBox.Checked()
		a.sendRequest(ipc.FrameSetChatDefaults, ipc.SetChatDefaultsRequest{
			RetentionSec: ttl,
			SendReceipts: receipts,
		}, func(f ipc.Frame) {
			if f.Type == ipc.FrameError {
				a.renderError(f)
				return
			}
			a.log("[green]chat defaults saved[white]")
			a.app.QueueUpdateDraw(func() {
				if !a.settingsPageIsOpen(sp) {
					return
				}
				sp.initialRetention = ttl
				sp.initialReceipts = receipts
				sp.markClean(settingsDomainDefaults)
			})
		})
		return nil
	}
	sp.registerSave(settingsDomainDefaults, save)
	form.AddButton("Save", func() {
		if err := save(); err != nil {
			a.log("[red]save defaults[white] %v", err)
			return
		}
		sp.returnToList(a)
	})
	form.AddButton("Cancel", func() {
		sp.rebuildForm(a, settingsDomainDefaults)
		sp.returnToList(a)
	})
	form.SetCancelFunc(func() { sp.returnToList(a) })
	return form
}

func buildFilesForm(a *App, sp *settingsPage) *tview.Form {
	form := tview.NewForm()
	form.SetBorder(true).SetTitle(" files ")

	const (
		labelSave   = "Default save folder (blank = use platform default)"
		labelAttach = "Default attach folder (blank = use platform default)"
	)
	form.AddInputField(labelSave, sp.initial.DefaultSaveDir, 60, nil, func(_ string) {
		sp.markDirty(settingsDomainFiles)
	})
	form.AddInputField(labelAttach, sp.initial.DefaultAttachStartDir, 60, nil, func(_ string) {
		sp.markDirty(settingsDomainFiles)
	})
	form.AddTextView("Picker behaviour",
		renderAdvisory("info", "settings.files.empty",
			"Blank fields fall back to $HOME on Linux/macOS/Termux and Desktop on Windows."),
		0, 2, true, false)

	save := func() error {
		saveDir := strings.TrimSpace(form.GetFormItemByLabel(labelSave).(*tview.InputField).GetText())
		attachDir := strings.TrimSpace(form.GetFormItemByLabel(labelAttach).(*tview.InputField).GetText())

		if saveDir != "" {
			if info, err := os.Stat(saveDir); err != nil {
				return fmt.Errorf("save dir: %w", err)
			} else if !info.IsDir() {
				return fmt.Errorf("save dir: %s is not a directory", saveDir)
			}
		}
		if attachDir != "" {
			if info, err := os.Stat(attachDir); err != nil {
				return fmt.Errorf("attach dir: %w", err)
			} else if !info.IsDir() {
				return fmt.Errorf("attach dir: %s is not a directory", attachDir)
			}
		}
		if err := a.VaultCtl.Mutate("files", func(p *vault.Payload) error {
			p.DefaultSaveDir = saveDir
			p.DefaultAttachStartDir = attachDir
			return nil
		}); err != nil {
			return err
		}
		sp.initial.DefaultSaveDir = saveDir
		sp.initial.DefaultAttachStartDir = attachDir
		sp.markClean(settingsDomainFiles)
		a.pushSettingsSync()
		a.log("[green]files saved[white]")
		return nil
	}
	sp.registerSave(settingsDomainFiles, save)
	form.AddButton("Save", func() {
		if err := save(); err != nil {
			a.log("[red]save files[white] %v", err)
			return
		}
		sp.returnToList(a)
	})
	form.AddButton("Cancel", func() {
		sp.rebuildForm(a, settingsDomainFiles)
		sp.returnToList(a)
	})
	form.SetCancelFunc(func() { sp.returnToList(a) })
	return form
}

func buildSecurityForm(a *App, sp *settingsPage) tview.Primitive {

	statusTV := tview.NewTextView().
		SetText(threatAnswerStatusPrefix + threatModelStatusLine(sp.initial))
	bundleTV := tview.NewTextView().
		SetDynamicColors(true)

	noteTV := tview.NewTextView().SetDynamicColors(true)
	noteLines := make([]string, 0, 2)
	if driftNote := threatModelDriftNote(sp.initial); driftNote != "" {
		noteLines = append(noteLines, driftNote)
	}
	if sp.retentionNote != "" {
		noteLines = append(noteLines, sp.retentionNote)
	}
	noteTV.SetText(strings.Join(noteLines, "\n"))
	sp.lockNoteTV = noteTV

	formA := tview.NewForm()
	presetOptions := make([]string, 0, len(threatTierOrder))
	for _, t := range threatTierOrder {
		presetOptions = append(presetOptions, t.answer)
	}
	presetIdx := threatTierIndex(sp.initial.ThreatProfile)
	presetDD := tview.NewDropDown().SetLabel(threatPickerQuestion).SetOptions(presetOptions, nil)
	formA.AddFormItem(presetDD)
	presetDD.SetCurrentOption(presetIdx)

	bundleTV.SetText(threatAnswerSummary(threatTierOrder[presetIdx].token))
	presetDD.SetSelectedFunc(func(_ string, idx int) {
		sp.markDirty(settingsDomainSecurity)
		if idx >= 0 && idx < len(threatTierOrder) {
			bundleTV.SetText(threatAnswerSummary(threatTierOrder[idx].token))
		}
	})

	formB := tview.NewForm()
	idleOptions := []string{"safe-lock", "soft-lock", "hard-lock"}
	idleIdx := 0
	switch sp.initial.IdleAction {
	case "soft-lock":
		idleIdx = 1
	case "hard-lock":
		idleIdx = 2
	}
	idleDD := tview.NewDropDown().SetLabel("Idle action").SetOptions(idleOptions, nil)
	formB.AddFormItem(idleDD)
	idleDD.SetCurrentOption(idleIdx)
	idleDD.SetSelectedFunc(func(_ string, _ int) { sp.markDirty(settingsDomainSecurity) })
	formB.AddInputField("Idle timeout (seconds)", strconv.Itoa(sp.initial.IdleTimeoutSeconds), 8, nil, func(_ string) {
		sp.markDirty(settingsDomainSecurity)
	})
	formB.AddInputField("PIN validity (seconds, 0 = no escalation)", strconv.Itoa(sp.initial.PinValiditySec), 8, nil, func(_ string) {
		sp.markDirty(settingsDomainSecurity)
	})
	panicOptions := []string{"(disabled — /panic = /quit)", "safe-lock", "hard-lock", "self-destruct"}
	panicIdx := 0
	for i, p := range panicOptions {
		want := sp.initial.PanicAction
		if want == "" && i == 0 {
			panicIdx = 0
			break
		}
		if p == want {
			panicIdx = i
			break
		}
	}
	panicDD := tview.NewDropDown().SetLabel("Panic action").SetOptions(panicOptions, nil)
	formB.AddFormItem(panicDD)
	panicDD.SetCurrentOption(panicIdx)
	panicDD.SetSelectedFunc(func(_ string, _ int) { sp.markDirty(settingsDomainSecurity) })

	formD := tview.NewForm()

	formA.AddButton(threatAnswerApplyButton, func() {
		idx, label := presetDD.GetCurrentOption()
		preset := ""
		if idx >= 0 && idx < len(threatTierOrder) {
			preset = threatTierOrder[idx].token
		}
		if preset == "" {
			if err := a.VaultCtl.SetThreatProfile(""); err != nil {
				a.log("[red]apply answer[white] %v", err)
				return
			}

			sp.retentionNote = ""
			sp.retentionPending = false
			a.pushSettingsSync()
			a.log("[green]answer cleared[white] — the settings stay as they are")
			a.refreshBundleForms(sp, sp.initialRetention)
			return
		}
		a.confirmApplyThreatPreset(label, func() {
			if err := a.VaultCtl.ApplyThreatPreset(preset); err != nil {
				a.log("[red]apply answer[white] %v", err)
				return
			}
			a.pushSettingsSync()

			a.log("[green]answer applied[white] — %s. This device's settings changed.", label)

			a.sendPresetRetention(sp, preset)
			a.refreshBundleForms(sp, vault.ThreatPresetBundles[preset].DefaultRetentionSec)
		})
	})

	formB.AddButton("Change PIN…", func() { a.showSettingsChangePINModal(sp) })
	formB.AddButton("Change passphrase…", func() { a.showSettingsChangePassModal(sp) })

	save := func() error {
		if err := retiredAnswerRefusal(sp.initial.ThreatProfile); err != nil {
			return err
		}
		_, idleAction := idleDD.GetCurrentOption()
		idleTimeoutStr := formB.GetFormItemByLabel("Idle timeout (seconds)").(*tview.InputField).GetText()
		pinValidityStr := formB.GetFormItemByLabel("PIN validity (seconds, 0 = no escalation)").(*tview.InputField).GetText()
		idleTimeout, err := strconv.Atoi(strings.TrimSpace(idleTimeoutStr))
		if err != nil || idleTimeout <= 0 {
			return fmt.Errorf("idle timeout must be a positive integer")
		}
		pinValidity, err := strconv.Atoi(strings.TrimSpace(pinValidityStr))
		if err != nil || pinValidity < 0 {
			return fmt.Errorf("PIN validity must be ≥ 0")
		}
		_, panicAction := panicDD.GetCurrentOption()
		if strings.HasPrefix(panicAction, "(disabled") {
			panicAction = ""
		}
		clearPreset := false
		if presetIdx, _ := presetDD.GetCurrentOption(); presetIdx == 0 && sp.initial.ThreatProfile != "" {
			clearPreset = true
		}
		if err := a.VaultCtl.Mutate("security", func(p *vault.Payload) error {
			p.IdleAction = idleAction
			p.IdleTimeoutSeconds = idleTimeout
			p.PinValiditySec = pinValidity
			p.PanicAction = panicAction
			if clearPreset {
				p.ThreatProfile = ""
			}
			return nil
		}); err != nil {
			return err
		}
		sp.initial = a.VaultCtl.Settings()
		sp.markClean(settingsDomainSecurity)
		a.pushSettingsSync()
		a.log("[green]security saved[white]")
		return nil
	}
	sp.registerSave(settingsDomainSecurity, save)
	formD.AddButton("Save", func() {
		if err := save(); err != nil {
			a.log("[red]save security[white] %v", err)
			return
		}
		sp.rebuildForm(a, settingsDomainSecurity)
		sp.returnToList(a)
	})
	formD.AddButton("Cancel", func() {
		sp.rebuildForm(a, settingsDomainSecurity)
		sp.returnToList(a)
	})

	flex := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(statusTV, 1, 0, false).
		AddItem(formA, 5, 0, true).
		AddItem(bundleTV, 5, 0, false).
		AddItem(noteTV, len(noteLines), 0, false).
		AddItem(formB, 0, 1, false).
		AddItem(formD, 3, 0, false)
	flex.SetBorder(true).SetTitle(" security ")

	subForms := []*tview.Form{formA, formB, formD}
	order := make([]tview.Primitive, 0, 16)
	for _, f := range subForms {
		for i := 0; i < f.GetFormItemCount(); i++ {
			order = append(order, f.GetFormItem(i))
		}
		for i := 0; i < f.GetButtonCount(); i++ {
			order = append(order, f.GetButton(i))
		}
	}
	indexOf := func(p tview.Primitive) int {
		for i, x := range order {
			if x == p {
				return i
			}
		}
		return -1
	}
	flex.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Key() {
		case tcell.KeyEscape:
			sp.returnToList(a)
			return nil
		case tcell.KeyTab, tcell.KeyBacktab:
			focus := a.app.GetFocus()
			idx := indexOf(focus)
			if idx < 0 {
				return ev
			}
			if ev.Key() == tcell.KeyTab {
				idx = (idx + 1) % len(order)
			} else {
				idx = (idx - 1 + len(order)) % len(order)
			}
			a.app.SetFocus(order[idx])
			return nil
		}
		return ev
	})
	return flex
}

const threatPickerQuestion = vault.ThreatPickerQuestion

const (
	threatAnswerStatusPrefix = "Your answer: "
	threatAnswerApplyButton  = "Apply answer…"
)

var threatTierOrder = []struct {
	answer string
	line   string
	token  string
}{
	{"(not picked yet)", "", ""},
	{"No one", "nobody else uses this device", vault.PresetNobody},
	{"People at home", "somebody here picks it up and looks", vault.PresetDomestic},
	{"People with power", "they can dig, but cannot make you open it", vault.PresetPower},
	{"People with force", "they can make you unlock it", vault.PresetForce},
}

func threatTierIndex(token string) int {
	for i, t := range threatTierOrder {
		if t.token == token && token != "" {
			return i
		}
	}
	return 0
}

func threatTierAnswer(token string) string {
	return threatTierOrder[threatTierIndex(token)].answer
}

func threatModelStatusLine(s ipc.Settings) string {
	if s.ThreatProfile == "" {
		return "not picked yet"
	}
	if _, ok := vault.ThreatPresetBundles[s.ThreatProfile]; !ok {
		return s.ThreatProfile + " — a retired answer"
	}
	return threatTierAnswer(s.ThreatProfile)
}

func retiredAnswerRefusal(profile string) error {
	if profile == "" {
		return nil
	}
	if _, ok := vault.ThreatPresetBundles[profile]; ok {
		return nil
	}
	return fmt.Errorf("this device holds a retired answer (%s) — pick an answer above and press %s first",
		profile, threatAnswerApplyButton)
}

func threatModelDriftNote(s ipc.Settings) string {
	if s.ThreatProfile == "" {
		return ""
	}
	bundle, ok := vault.ThreatPresetBundles[s.ThreatProfile]
	if !ok {
		return StyleAnswerGone + "No settings can be saved until you answer again." + StyleReset
	}
	same := s.IdleAction == bundle.IdleAction &&
		s.IdleTimeoutSeconds == bundle.IdleTimeoutSeconds &&
		s.PinValiditySec == bundle.PinValiditySec &&
		s.PanicAction == bundle.PanicAction &&
		s.NotifyShellEnabled == bundle.NotifyShellEnabled &&
		s.NotifyShowSender == bundle.NotifyShowSender &&
		s.NotifyShowBody == bundle.NotifyShowBody &&
		s.NotificationsOnLock == bundle.NotificationsOnLock &&
		s.NotifyPersistUntilOpen == bundle.NotifyPersistUntilOpen &&
		s.NotifyDeepLink == bundle.NotifyDeepLink &&
		s.ShowFailedDestroyAttempts == bundle.ShowFailedDestroyAttempts
	if same {
		return ""
	}
	return "Some settings changed since this was applied."
}

var settingsBundleDomains = []string{
	settingsDomainSecurity,
	settingsDomainNotifs,
	settingsDomainDefaults,
}

func (a *App) refreshBundleForms(sp *settingsPage, retentionSec uint64) {
	sp.initial = a.VaultCtl.Settings()
	sp.initialRetention = retentionSec
	a.winMu.Lock()
	sp.initialReceipts = a.defaultSendReceipts
	a.winMu.Unlock()
	for _, key := range settingsBundleDomains {
		sp.rebuildForm(a, key)
	}

	sp.returnToList(a)
}

const (
	retentionNoteWaiting = "How long messages are kept: saving."
	retentionNoteRefused = "How long messages are kept: not saved."
	retentionNoteUnknown = "How long messages are kept: try again soon."
)

func (a *App) sendPresetRetention(sp *settingsPage, preset string) {
	bundle, ok := vault.ThreatPresetBundles[preset]
	if !ok {
		return
	}
	a.winMu.Lock()
	receipts := a.defaultSendReceipts
	known := a.chatDefaultsKnown
	a.winMu.Unlock()
	if !known {

		sp.retentionPending = false
		sp.retentionNote = retentionNoteUnknown
		slog.Warn("tui: preset retention default not sent — chat defaults not known yet",
			slog.String("preset", preset))
		a.log("[red]how long messages are kept was not changed[white] — this device does not know the current setting yet")
		return
	}
	sp.retentionWant = bundle.DefaultRetentionSec
	sp.retentionPending = true
	sp.retentionNote = retentionNoteWaiting
	slog.Debug("tui: preset retention default sent",
		slog.String("preset", preset),
		slog.Uint64("retention_sec", bundle.DefaultRetentionSec))
	a.sendRequest(ipc.FrameSetChatDefaults, ipc.SetChatDefaultsRequest{
		RetentionSec: bundle.DefaultRetentionSec,
		SendReceipts: receipts,
	}, func(f ipc.Frame) {
		if f.Type != ipc.FrameError {
			return
		}
		a.renderError(f)
		a.app.QueueUpdateDraw(func() {
			if !a.settingsPageIsOpen(sp) || !sp.retentionPending {
				return
			}
			sp.retentionPending = false
			sp.retentionNote = retentionNoteRefused

			sp.writeLockNote(a)
		})
	})
}

func (a *App) confirmPresetRetention(sec uint64) {
	a.app.QueueUpdateDraw(func() {
		a.winMu.Lock()
		sp := a.settings
		a.winMu.Unlock()
		if sp == nil || !sp.retentionPending || sp.retentionWant != sec {
			return
		}
		sp.retentionPending = false
		sp.retentionNote = "How long messages are kept: " + retentionSummaryWord(sec) + "."
		sp.writeLockNote(a)
	})
}

func (sp *settingsPage) writeLockNote(a *App) {
	if sp.lockNoteTV == nil {
		return
	}
	lines := make([]string, 0, 2)
	if driftNote := threatModelDriftNote(sp.initial); driftNote != "" {
		lines = append(lines, driftNote)
	}
	if sp.retentionNote != "" {
		lines = append(lines, sp.retentionNote)
	}
	sp.lockNoteTV.SetText(strings.Join(lines, "\n"))
}

func (a *App) settingsPageIsOpen(sp *settingsPage) bool {
	a.winMu.Lock()
	defer a.winMu.Unlock()
	return a.settings != nil && a.settings == sp
}

func buildTorForm(a *App, sp *settingsPage) *tview.Form {
	form := tview.NewForm()
	form.SetBorder(true).SetTitle(" tor ")

	status, severity, source := torPasswordStatus(a.SystemTor, sp.initial.HasTorPassword)
	form.AddTextView("Tor control-port password", renderAdvisory(severity, source, status), 0, 1, true, false)
	form.AddButton("Change Tor password…", func() { a.showSettingsTorPasswordModal(sp) })
	form.AddButton("Cancel", func() {
		sp.rebuildForm(a, settingsDomainTor)
		sp.returnToList(a)
	})
	form.SetCancelFunc(func() { sp.returnToList(a) })
	return form
}

func torPasswordStatus(systemTor, hasPassword bool) (status, severity, source string) {
	switch {
	case !systemTor && hasPassword:
		return "set, and not used: haomad runs its own tor, which needs no password. It is used with --system-tor only", "info", "tor.password.unused"
	case !systemTor:
		return "not needed: haomad runs its own tor, which needs no password. It is used with --system-tor only", "info", "tor.password.unused"
	case hasPassword:
		return "configured", "info", "tor.password.set"
	default:
		return "not set — the system tor must accept its cookie, or haomad cannot reach it", "warn", "tor.password.unset"
	}
}

func buildNotificationsForm(a *App, sp *settingsPage) *tview.Form {
	form := tview.NewForm()
	form.SetBorder(true).SetTitle(" notifications ")

	shellBox := newBracketCheckbox("Enable per-OS notifications (notify-send / termux-notification)", sp.initial.NotifyShellEnabled, func(_ bool) {
		sp.markDirty(settingsDomainNotifs)
	})
	form.AddFormItem(shellBox)
	senderBox := newBracketCheckbox("Show sender name in notifications", sp.initial.NotifyShowSender, func(_ bool) {
		sp.markDirty(settingsDomainNotifs)
	})
	form.AddFormItem(senderBox)
	bodyBox := newBracketCheckbox("Show message body in notifications", sp.initial.NotifyShowBody, func(_ bool) {
		sp.markDirty(settingsDomainNotifs)
	})
	form.AddFormItem(bodyBox)
	onLockBox := newBracketCheckbox("Allow notifications while UI is locked", sp.initial.NotificationsOnLock, func(_ bool) {
		sp.markDirty(settingsDomainNotifs)
	})
	form.AddFormItem(onLockBox)

	persistBox := newBracketCheckbox("Keep notification until I open that chat", sp.initial.NotifyPersistUntilOpen, func(_ bool) {
		sp.markDirty(settingsDomainNotifs)
	})
	form.AddFormItem(persistBox)
	deepLinkBox := newBracketCheckbox("Tap notification → open that chat (desktop: bring terminal to focus)", sp.initial.NotifyDeepLink, func(_ bool) {
		sp.markDirty(settingsDomainNotifs)
	})
	form.AddFormItem(deepLinkBox)

	destroyAttemptsBox := newBracketCheckbox("Show failed destroy attempts", sp.initial.ShowFailedDestroyAttempts, func(_ bool) {
		sp.markDirty(settingsDomainNotifs)
	})
	form.AddFormItem(destroyAttemptsBox)
	form.AddTextView("Privacy posture",
		renderAdvisory("info", "settings.notifs.privacy",
			"With both Show toggles off, banners read \"Haoma: New message\" — safest under physical inspection."),
		0, 2, true, false)

	save := func() error {
		shell := shellBox.Checked()
		showSender := senderBox.Checked()
		showBody := bodyBox.Checked()
		onLock := onLockBox.Checked()
		persist := persistBox.Checked()
		deepLink := deepLinkBox.Checked()
		destroyAttempts := destroyAttemptsBox.Checked()
		if err := a.VaultCtl.Mutate("notifications", func(p *vault.Payload) error {
			p.NotifyShellEnabled = shell
			p.NotifyShowSender = showSender
			p.NotifyShowBody = showBody
			p.NotificationsOnLock = onLock
			p.NotifyPersistUntilOpen = persist
			p.NotifyDeepLink = deepLink
			p.ShowFailedDestroyAttempts = destroyAttempts
			return nil
		}); err != nil {
			return err
		}
		sp.initial.NotifyShellEnabled = shell
		sp.initial.NotifyShowSender = showSender
		sp.initial.NotifyShowBody = showBody
		sp.initial.NotificationsOnLock = onLock
		sp.initial.NotifyPersistUntilOpen = persist
		sp.initial.NotifyDeepLink = deepLink
		sp.initial.ShowFailedDestroyAttempts = destroyAttempts
		sp.markClean(settingsDomainNotifs)
		a.pushSettingsSync()
		a.log("[green]notifications saved[white]")
		return nil
	}
	sp.registerSave(settingsDomainNotifs, save)
	form.AddButton("Save", func() {
		if err := save(); err != nil {
			a.log("[red]save notifications[white] %v", err)
			return
		}
		sp.returnToList(a)
	})
	form.AddButton("Cancel", func() {
		sp.rebuildForm(a, settingsDomainNotifs)
		sp.returnToList(a)
	})
	form.SetCancelFunc(func() { sp.returnToList(a) })
	return form
}

func buildAdvancedForm(a *App, sp *settingsPage) tview.Primitive {
	warnings := "none"
	if len(sp.initial.SecurityWarnings) > 0 {
		warnings = strings.Join(sp.initial.SecurityWarnings, "\n")
	}
	body := tview.NewTextView().
		SetDynamicColors(true).
		SetText("[gray]Security warnings:[-]\n\n" + warnings)

	flex := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(body, 0, 1, false).
		AddItem(buildWipeDeviceBlock(a, sp), wipeBlockRows, 0, true)
	flex.SetBorder(true).SetTitle(" advanced ")
	flex.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {

		if ev.Key() == tcell.KeyEscape {
			sp.returnToList(a)
			return nil
		}
		return ev
	})
	return flex
}

func threatAnswerSummary(token string) string {
	if token == "" {
		return StyleAnswerHint + "Pick an answer to see what it sets." + StyleReset
	}
	b, ok := vault.ThreatPresetBundles[token]
	if !ok {
		return StyleAnswerGone + token + " is a retired answer." + StyleReset + "\nPick one of the four below."
	}
	return fmt.Sprintf(StyleAnswerHeading+"The %q answer sets"+StyleReset+"\n  idle: %s after %ds\n  PIN valid: %s\n  panic: %s\n  messages kept: %s",
		threatTierAnswer(token),
		b.IdleAction, b.IdleTimeoutSeconds,
		pinValiditySummaryWord(b.PinValiditySec),
		b.PanicAction,
		retentionSummaryWord(b.DefaultRetentionSec))
}

func pinValiditySummaryWord(sec int) string {
	if sec == 0 {
		return "until restart"
	}
	return strconv.Itoa(sec) + "s"
}

func retentionSummaryWord(sec uint64) string {
	return retentionLabel(uint32(sec))
}

func (a *App) confirmApplyThreatPreset(label string, onApply func()) {
	const pageName = "settings-threat-preset-confirm"
	modal := tview.NewModal().
		SetText(fmt.Sprintf("Answer \"%s\"?\n\nThis writes the lock, panic, notification and message-keeping settings for that answer. You can change any of them again afterwards.", label)).
		AddButtons([]string{"Apply", "Cancel"}).
		SetDoneFunc(func(_ int, btn string) {
			a.pages.RemovePage(pageName)
			if btn == "Apply" {
				onApply()
				return
			}
			if a.settings != nil {
				a.app.SetFocus(a.settings.list)
			}
		})
	a.pages.AddPage(pageName, modal, true, true)
	a.app.SetFocus(modal)
}

func (a *App) showSettingsChangePassModal(sp *settingsPage) {
	const pageName = "settings-change-pass"
	form := tview.NewForm()
	form.SetBorder(true).SetTitle(" change passphrase ")
	form.AddPasswordField("Current passphrase", "", 32, '*', nil)
	form.AddPasswordField("New passphrase", "", 32, '*', nil)
	dismiss := func() { a.pages.RemovePage(pageName); a.app.SetFocus(sp.list) }
	form.AddButton("Save", func() {
		oldP := []byte(form.GetFormItemByLabel("Current passphrase").(*tview.InputField).GetText())
		newP := []byte(form.GetFormItemByLabel("New passphrase").(*tview.InputField).GetText())

		defer clear(oldP)
		defer clear(newP)
		if err := a.VaultCtl.ChangePassphrase(oldP, newP); err != nil {
			a.log("[red]change passphrase[white] %v", err)
			return
		}
		a.log("[green]passphrase changed[white]")
		dismiss()
	})
	form.AddButton("Cancel", dismiss)
	form.SetCancelFunc(dismiss)
	wrapInModal(a, pageName, form, 60, 11)
}

func (a *App) showSettingsChangePINModal(sp *settingsPage) {
	const pageName = "settings-change-pin"
	form := tview.NewForm()
	form.SetBorder(true).SetTitle(" change PIN ")
	form.AddPasswordField("Current PIN (blank if default)", "", 16, '*', nil)
	form.AddPasswordField("New PIN", "", 16, '*', nil)
	dismiss := func() { a.pages.RemovePage(pageName); a.app.SetFocus(sp.list) }
	form.AddButton("Save", func() {
		oldP := form.GetFormItemByLabel("Current PIN (blank if default)").(*tview.InputField).GetText()
		newP := form.GetFormItemByLabel("New PIN").(*tview.InputField).GetText()
		if err := a.VaultCtl.ChangePIN(oldP, newP); err != nil {
			a.log("[red]change PIN[white] %v", err)
			return
		}
		a.log("[green]PIN changed[white]")
		dismiss()
	})
	form.AddButton("Cancel", dismiss)
	form.SetCancelFunc(dismiss)
	wrapInModal(a, pageName, form, 60, 11)
}

func (a *App) showSettingsTorPasswordModal(sp *settingsPage) {
	const pageName = "settings-tor-password"
	form := tview.NewForm()
	form.SetBorder(true).SetTitle(" change Tor password ")

	const passwordLabel = "Password"
	form.AddTextView("", "New control-port password of the system tor (blank to clear):", 0, 1, true, false)
	form.AddPasswordField(passwordLabel, "", 40, '*', nil)
	dismiss := func() {
		a.pages.RemovePage(pageName)

		sp.initial = a.VaultCtl.Settings()
		sp.rebuildForm(a, settingsDomainTor)
		a.app.SetFocus(sp.list)
	}
	form.AddButton("Save", func() {
		pwd := form.GetFormItemByLabel(passwordLabel).(*tview.InputField).GetText()
		if err := a.VaultCtl.SetTorPassword(pwd); err != nil {
			a.log("[red]set tor password[white] %v", err)
			return
		}

		if a.SystemTor {
			a.sendRequest(ipc.FrameSetTorPassword, ipc.SetTorPasswordRequest{Password: pwd}, nil)
		}
		a.pushSettingsSync()
		if a.SystemTor {
			a.log("[green]tor password updated[white]")
		} else {
			a.log(StyleNoticeOK + "tor password saved" + StyleReset + " — used with --system-tor only; haomad runs its own tor now")
		}
		dismiss()
	})
	form.AddButton("Cancel", dismiss)
	form.SetCancelFunc(dismiss)
	wrapInModal(a, pageName, form, 70, 11)
}

func wrapInModal(a *App, pageName string, prim tview.Primitive, width, height int) {
	grid := tview.NewGrid().
		SetColumns(0, width, 0).
		SetRows(0, height, 0).
		AddItem(prim, 1, 1, 1, 1, 0, 0, true)
	a.pages.AddPage(pageName, grid, true, true)
	a.app.SetFocus(prim)
}

func (sp *settingsPage) selectDomainRow(key string) {
	for i, k := range settingsDomainsOrder {
		if k == key {
			sp.list.SetCurrentItem(i)
			return
		}
	}
}

func (a *App) leaveLinkedDevicesDomain(sp *settingsPage) {
	a.stopVerifyMarker()
	sp.linked.verifyNote = ""
	sp.linked.busy = false
	sp.linked.busyLabel = ""
	sp.linked.risksAcked = false
	if a.linkFlow() != nil {
		a.redrawLinkedDevicesPage()
		return
	}
	a.rebuildLinkedDevicesPage()
}

func (sp *settingsPage) setLive(w linkLiveWidgets) {
	sp.liveMu.Lock()
	defer sp.liveMu.Unlock()
	sp.live = w
}

func (sp *settingsPage) liveWidgets() linkLiveWidgets {
	sp.liveMu.Lock()
	defer sp.liveMu.Unlock()
	return sp.live
}
