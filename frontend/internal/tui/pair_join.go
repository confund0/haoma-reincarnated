package tui

import (
	"strings"
	"sync"
	"time"
)

type pairJoinWait struct {
	mu    sync.Mutex
	gen   uint64
	since time.Time
}

func (a *App) startPairJoinWait() uint64 {
	w := &a.pairJoin
	w.mu.Lock()
	w.gen++
	gen := w.gen
	w.since = time.Now()
	w.mu.Unlock()
	go a.tickPairJoinWait(gen)
	return gen
}

func (a *App) endPairJoinWait(gen uint64) {
	w := &a.pairJoin
	w.mu.Lock()
	if w.gen == gen {
		w.since = time.Time{}
	}
	w.mu.Unlock()
	a.refreshSysBar()
}

func (a *App) tickPairJoinWait(gen uint64) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		a.refreshSysBar()
		<-t.C
		w := &a.pairJoin
		w.mu.Lock()
		mine := w.gen == gen && !w.since.IsZero()
		left := joinSecondsLeft(time.Since(w.since))
		w.mu.Unlock()
		if !mine {
			return
		}
		if left == 0 {
			a.refreshSysBar()
			return
		}
	}
}

func (a *App) pairJoinCountdown() string {
	w := &a.pairJoin
	w.mu.Lock()
	since := w.since
	w.mu.Unlock()
	if since.IsZero() {
		return ""
	}
	return joinWaitLine(time.Since(since))
}

func joinBarWidth(text string) int {
	if text == "" {
		return 0
	}
	return len([]rune(text)) + 1
}

func (a *App) setJoinBar() {
	text := a.pairJoinCountdown()
	if text == "" {
		a.joinBar.SetText("")
	} else {
		a.joinBar.SetText(StyleTorSlow + text + StyleReset + " ")
	}
	if a.sysRow != nil {
		a.sysRow.ResizeItem(a.joinBar, joinBarWidth(text), 0)
	}
}

func (a *App) refreshSysBar() {
	go a.app.QueueUpdateDraw(func() {
		a.sysBar.SetText(a.sysBarText())
		a.setJoinBar()
	})
}

func (a *App) keepPairJoinWords(words []string) {
	line := "/accept-tor " + strings.Join(words, " ")
	go a.app.QueueUpdateDraw(func() {
		if a.input.GetText() == "" {
			a.input.SetText(line)
		}
	})
}
