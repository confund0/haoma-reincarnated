package tui

import (
	"fmt"
	"os"
	"strconv"
	"strings"

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
	settingsDomainLock     = "lock"
	settingsDomainTor      = "tor"
	settingsDomainNotifs   = "notifications"
	settingsDomainAdv      = "advanced"
)

var settingsDomainsOrder = []string{
	settingsDomainProfile,
	settingsDomainDefaults,
	settingsDomainFiles,
	settingsDomainLock,
	settingsDomainTor,
	settingsDomainNotifs,
	settingsDomainAdv,
}

var settingsDomainLabels = map[string]string{
	settingsDomainProfile:  "Profile",
	settingsDomainDefaults: "Chat defaults",
	settingsDomainFiles:    "Files",
	settingsDomainLock:     "Lock",
	settingsDomainTor:      "Tor",
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

	dirty map[string]bool

	saveHandlers map[string]func() error

	activePage string
}

func (a *App) cmdSettings() {
	if a.VaultCtl == nil {
		a.log("[red]/settings unavailable[white] — no vault controller (legacy --addr flow)")
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
	sp := newSettingsPage(a, initial, initialNick)
	a.settings = sp
	a.winOrder = append(a.winOrder, pageNameSettings)
	a.winMu.Unlock()

	a.pages.AddPage(pageNameSettings, sp.grid, true, false)
	a.switchTo(pageNameSettings)
	a.app.SetFocus(sp.list)
}

func (a *App) closeSettings() {
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

func newSettingsPage(a *App, initial ipc.Settings, initialNick string) *settingsPage {
	sp := &settingsPage{
		initial:      initial,
		initialNick:  initialNick,
		dirty:        map[string]bool{},
		saveHandlers: map[string]func() error{},
		activePage:   settingsDomainsOrder[0],
	}

	sp.list = tview.NewList().ShowSecondaryText(false)
	sp.list.SetBorder(true).SetTitle(" domains ")
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
		sp.activePage = key
		sp.forms.SwitchToPage(key)
		if form := sp.formForDomain(key); form != nil {
			a.app.SetFocus(form)
		}
	})
	sp.list.SetChangedFunc(func(idx int, _ string, _ string, _ rune) {
		if idx == closeRowIndex {

			return
		}
		key := settingsDomainsOrder[idx]
		sp.activePage = key
		sp.forms.SwitchToPage(key)
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
	sp.forms.AddPage(settingsDomainLock, buildLockForm(a, sp), true, false)
	sp.forms.AddPage(settingsDomainTor, buildTorForm(a, sp), true, false)
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
	_, prim := sp.forms.GetFrontPage()
	if sp.activePage == key {
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
	case settingsDomainLock:
		prim = buildLockForm(a, sp)
	case settingsDomainTor:
		prim = buildTorForm(a, sp)
	case settingsDomainNotifs:
		prim = buildNotificationsForm(a, sp)
	case settingsDomainAdv:
		prim = buildAdvancedForm(a, sp)
	default:
		return
	}
	sp.forms.RemovePage(key)
	sp.forms.AddPage(key, prim, true, sp.activePage == key)
	sp.markClean(key)
	a.app.SetFocus(sp.list)
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
			a.log("[green]nick[white] → %s", nick)
		})
		sp.initialNick = nick
		sp.markClean(settingsDomainProfile)
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
	form.AddButton("Cancel", func() { sp.rebuildForm(a, settingsDomainProfile) })
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
	dropdown.SetCurrentOption(retentionOptionIndex(uint32(sp.initial.DefaultRetentionSec)))
	dropdown.SetSelectedFunc(func(_ string, _ int) { sp.markDirty(settingsDomainDefaults) })

	receiptsBox := newBracketCheckbox("Send read receipts by default", sp.initial.DefaultSendReceipts, func(_ bool) {
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
		if err := a.VaultCtl.Mutate("defaults", func(p *vault.Payload) error {
			p.DefaultRetentionSec = ttl
			p.DefaultSendReceipts = receipts
			return nil
		}); err != nil {
			return err
		}
		sp.initial.DefaultRetentionSec = ttl
		sp.initial.DefaultSendReceipts = receipts
		sp.markClean(settingsDomainDefaults)
		a.pushSettingsSync()
		a.log("[green]chat defaults saved[white]")
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
	form.AddButton("Cancel", func() { sp.rebuildForm(a, settingsDomainDefaults) })
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
	form.AddButton("Cancel", func() { sp.rebuildForm(a, settingsDomainFiles) })
	form.SetCancelFunc(func() { sp.returnToList(a) })
	return form
}

func buildLockForm(a *App, sp *settingsPage) tview.Primitive {
	const (
		labelUnset    = "(unset)"
		labelDomestic = "Domestic"
		labelPrivacy  = "Privacy"
	)

	statusTV := tview.NewTextView().
		SetText("Threat model: " + threatModelStatusLine(sp.initial))
	bundleTV := tview.NewTextView().
		SetDynamicColors(true).
		SetText(threatPresetBundleSummary())

	formA := tview.NewForm()
	presetOptions := []string{labelUnset, labelDomestic, labelPrivacy}
	presetIdx := 0
	switch sp.initial.ThreatProfile {
	case vault.PresetDomestic:
		presetIdx = 1
	case vault.PresetPrivacy:
		presetIdx = 2
	}
	presetDD := tview.NewDropDown().SetLabel("Preset").SetOptions(presetOptions, nil)
	formA.AddFormItem(presetDD)
	presetDD.SetCurrentOption(presetIdx)
	presetDD.SetSelectedFunc(func(_ string, _ int) { sp.markDirty(settingsDomainLock) })

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
	idleDD.SetSelectedFunc(func(_ string, _ int) { sp.markDirty(settingsDomainLock) })
	formB.AddInputField("Idle timeout (seconds)", strconv.Itoa(sp.initial.IdleTimeoutSeconds), 8, nil, func(_ string) {
		sp.markDirty(settingsDomainLock)
	})
	formB.AddInputField("PIN validity (seconds, 0 = no escalation)", strconv.Itoa(sp.initial.PinValiditySec), 8, nil, func(_ string) {
		sp.markDirty(settingsDomainLock)
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
	panicDD.SetSelectedFunc(func(_ string, _ int) { sp.markDirty(settingsDomainLock) })

	formD := tview.NewForm()

	formA.AddButton("Apply preset…", func() {
		idx, label := presetDD.GetCurrentOption()
		var preset string
		switch idx {
		case 1:
			preset = vault.PresetDomestic
		case 2:
			preset = vault.PresetPrivacy
		default:
			preset = ""
		}
		if preset == "" {
			if err := a.VaultCtl.SetThreatProfile(""); err != nil {
				a.log("[red]apply preset[white] %v", err)
				return
			}
			sp.initial = a.VaultCtl.Settings()
			sp.markClean(settingsDomainLock)
			a.pushSettingsSync()
			a.log("[green]threat preset cleared[white]")
			sp.rebuildForm(a, settingsDomainLock)
			return
		}
		a.confirmApplyThreatPreset(label, func() {
			if err := a.VaultCtl.ApplyThreatPreset(preset); err != nil {
				a.log("[red]apply preset[white] %v", err)
				return
			}
			sp.initial = a.VaultCtl.Settings()
			sp.markClean(settingsDomainLock)
			a.pushSettingsSync()
			a.log("[green]threat preset[white] → %s", label)
			sp.rebuildForm(a, settingsDomainLock)
		})
	})

	formB.AddButton("Change PIN…", func() { a.showSettingsChangePINModal(sp) })
	formB.AddButton("Change passphrase…", func() { a.showSettingsChangePassModal(sp) })

	save := func() error {
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
		if err := a.VaultCtl.Mutate("lock", func(p *vault.Payload) error {
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
		sp.markClean(settingsDomainLock)
		a.pushSettingsSync()
		a.log("[green]lock saved[white]")
		return nil
	}
	sp.registerSave(settingsDomainLock, save)
	formD.AddButton("Save", func() {
		if err := save(); err != nil {
			a.log("[red]save lock[white] %v", err)
			return
		}
		sp.rebuildForm(a, settingsDomainLock)
	})
	formD.AddButton("Cancel", func() { sp.rebuildForm(a, settingsDomainLock) })

	flex := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(statusTV, 1, 0, false).
		AddItem(formA, 5, 0, true).
		AddItem(bundleTV, 4, 0, false).
		AddItem(formB, 11, 0, false).
		AddItem(formD, 3, 0, false)
	flex.SetBorder(true).SetTitle(" lock + threat model ")

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

func threatModelStatusLine(s ipc.Settings) string {
	switch s.ThreatProfile {
	case "":
		return "Custom (no preset selected)"
	case vault.PresetActivist:
		return "Activist (preset not yet wired)"
	}
	bundle, ok := vault.ThreatPresetBundles[s.ThreatProfile]
	if !ok {
		return s.ThreatProfile + " (no bundle)"
	}
	label := strings.ToUpper(s.ThreatProfile[:1]) + s.ThreatProfile[1:]
	if s.IdleAction == bundle.IdleAction &&
		s.IdleTimeoutSeconds == bundle.IdleTimeoutSeconds &&
		s.PinValiditySec == bundle.PinValiditySec &&
		s.PanicAction == bundle.PanicAction {
		return label
	}
	return label + "-modified"
}

func buildTorForm(a *App, sp *settingsPage) *tview.Form {
	form := tview.NewForm()
	form.SetBorder(true).SetTitle(" tor ")

	status := "not set — pairing will fail until configured"
	severity := "warn"
	source := "tor.password.unset"
	if sp.initial.HasTorPassword {
		status = "configured"
		severity = "info"
		source = "tor.password.set"
	}
	form.AddTextView("Tor control-port password", renderAdvisory(severity, source, status), 0, 1, true, false)
	form.AddButton("Change Tor password…", func() { a.showSettingsTorPasswordModal(sp) })
	form.AddButton("Cancel", func() { sp.rebuildForm(a, settingsDomainTor) })
	form.SetCancelFunc(func() { sp.returnToList(a) })
	return form
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
	form.AddTextView("Privacy posture",
		renderAdvisory("info", "settings.notifs.privacy",
			"With both Show toggles off, banners read \"Haoma: New message\" — safest under physical inspection."),
		0, 2, true, false)

	save := func() error {
		shell := shellBox.Checked()
		showSender := senderBox.Checked()
		showBody := bodyBox.Checked()
		onLock := onLockBox.Checked()
		if err := a.VaultCtl.Mutate("notifications", func(p *vault.Payload) error {
			p.NotifyShellEnabled = shell
			p.NotifyShowSender = showSender
			p.NotifyShowBody = showBody
			p.NotificationsOnLock = onLock
			return nil
		}); err != nil {
			return err
		}
		sp.initial.NotifyShellEnabled = shell
		sp.initial.NotifyShowSender = showSender
		sp.initial.NotifyShowBody = showBody
		sp.initial.NotificationsOnLock = onLock
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
	form.AddButton("Cancel", func() { sp.rebuildForm(a, settingsDomainNotifs) })
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
		AddItem(body, 0, 1, true)
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

func threatPresetBundleSummary() string {
	d := vault.ThreatPresetBundles[vault.PresetDomestic]
	p := vault.ThreatPresetBundles[vault.PresetPrivacy]
	return fmt.Sprintf(
		"[yellow]Bundled defaults[-]\n"+
			"  Domestic — idle %s @ %ds, PIN valid %ds, panic %s\n"+
			"  Privacy  — idle %s @ %ds, PIN valid %ds, panic %s\n"+
			"  [gray]Activist — coming when the data-destruction primitives ship[-]",
		d.IdleAction, d.IdleTimeoutSeconds, d.PinValiditySec, d.PanicAction,
		p.IdleAction, p.IdleTimeoutSeconds, p.PinValiditySec, p.PanicAction,
	)
}

func (a *App) confirmApplyThreatPreset(label string, onApply func()) {
	const pageName = "settings-threat-preset-confirm"
	modal := tview.NewModal().
		SetText(fmt.Sprintf("Apply %s preset?\n\nYour current Lock + Panic settings will be overwritten.", label)).
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
		oldP := form.GetFormItemByLabel("Current passphrase").(*tview.InputField).GetText()
		newP := form.GetFormItemByLabel("New passphrase").(*tview.InputField).GetText()
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
	form.AddTextView("", "New Tor control-port password (blank to clear):", 0, 1, true, false)
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

		a.sendRequest(ipc.FrameSetTorPassword, ipc.SetTorPasswordRequest{Password: pwd}, nil)
		a.pushSettingsSync()
		a.log("[green]tor password updated[white]")
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
