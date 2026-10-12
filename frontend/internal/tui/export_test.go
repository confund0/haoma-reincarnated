package tui

import "time"

func SetSaltWaits(total, interval, poll time.Duration) func() {
	pTotal, pInterval, pPoll := saltWaitTimeout, saltPollInterval, saltPollTimeout
	saltWaitTimeout, saltPollInterval, saltPollTimeout = total, interval, poll
	return func() {
		saltWaitTimeout, saltPollInterval, saltPollTimeout = pTotal, pInterval, pPoll
	}
}

func SpinnerGlyphs() string { return string(spinnerFrames) }

const (
	RetentionNoteWaiting     = retentionNoteWaiting
	ThreatAnswerStatusPrefix = threatAnswerStatusPrefix
)

func (a *App) OnDrawGoroutine(fn func()) { a.app.QueueUpdate(fn) }

const RiskLabel = riskLabel

func (a *App) LinkedDevicesShape() string {
	var out string
	a.app.QueueUpdate(func() {
		if sp := a.openSettingsPage(); sp != nil {
			out = sp.linkedShape
		}
	})
	return out
}

func (a *App) LinkedDevicesButtons() []string {
	out := []string{}
	a.app.QueueUpdate(func() {
		sp := a.openSettingsPage()
		if sp == nil {
			return
		}
		f := sp.liveWidgets().buttons
		if f == nil {
			return
		}
		for i := 0; i < f.GetButtonCount(); i++ {
			out = append(out, f.GetButton(i).GetLabel())
		}
	})
	return out
}

func (a *App) LinkedButtonFocused(label string) bool {
	focused := false
	a.app.QueueUpdate(func() {
		sp := a.openSettingsPage()
		if sp == nil {
			return
		}
		f := sp.liveWidgets().buttons
		if f == nil {
			return
		}
		if i := f.GetButtonIndex(label); i >= 0 {
			focused = f.GetButton(i).HasFocus()
		}
	})
	return focused
}
