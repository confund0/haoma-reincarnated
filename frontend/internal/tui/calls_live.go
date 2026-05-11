package tui

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"haoma-frontend/internal/ipc"
)

type liveCallPage struct {
	callID    string
	chatID    string
	peerID    string
	startedAt time.Time

	body *tview.TextView
	form *tview.Form

	mu      sync.Mutex
	muted   bool
	dropped uint64

	mic streamSampleHistory
	spk streamSampleHistory

	stop     chan struct{}
	stopOnce sync.Once
}

type streamSampleHistory struct {
	cur     streamSample
	prev    streamSample
	hasCur  bool
	hasPrev bool
}

type streamSample struct {
	at        time.Time
	bytesIn   uint64
	bytesOut  uint64
	framesIn  uint64
	framesOut uint64
	jitterMs  float64
}

func liveCallPageName(callID string) string {
	return "call-live:" + callID
}

func (a *App) openLiveCallWindow(call ipc.CallEntry) {
	a.winMu.Lock()
	page, ok := a.liveCalls[call.CallID]
	if !ok {
		page = a.newLiveCallPage(call)
		a.liveCalls[call.CallID] = page
		a.pages.AddPage(liveCallPageName(call.CallID), buildLiveCallLayout(page), true, false)
		go a.runLiveCallTicker(page)
	}
	a.winMu.Unlock()

	a.pages.SwitchToPage(liveCallPageName(call.CallID))

	a.app.SetFocus(page.form)
	page.refresh()
}

func (a *App) dismissLiveCallWindow(callID string) {
	a.winMu.Lock()
	page, ok := a.liveCalls[callID]
	if ok {
		delete(a.liveCalls, callID)
	}
	a.winMu.Unlock()
	if !ok {
		return
	}
	page.stopOnce.Do(func() { close(page.stop) })
	a.pages.RemovePage(liveCallPageName(callID))

	a.app.SetFocus(a.input)
}

func (a *App) newLiveCallPage(call ipc.CallEntry) *liveCallPage {
	body := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignLeft).
		SetWrap(false)
	body.SetChangedFunc(func() { a.app.Draw() })

	page := &liveCallPage{
		callID:    call.CallID,
		chatID:    call.ChatID,
		peerID:    call.PeerID,
		startedAt: time.Unix(call.StartedAt, 0),
		body:      body,
		stop:      make(chan struct{}),
	}

	form := tview.NewForm().
		AddButton("mute", func() { a.cmdLiveCallMute(page) }).
		AddButton("hangup", func() { a.cmdLiveCallHangup(page) }).
		AddButton("close", func() { a.dismissLiveCallWindow(page.callID) })
	form.SetButtonsAlign(tview.AlignCenter).
		SetCancelFunc(func() { a.dismissLiveCallWindow(page.callID) })
	form.SetBackgroundColor(tcell.ColorDefault)
	page.form = form

	return page
}

func buildLiveCallLayout(page *liveCallPage) tview.Primitive {
	const bodyRows = 6
	const formRows = 3

	flex := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(page.body, bodyRows, 0, false).
		AddItem(page.form, formRows, 0, true)

	frame := tview.NewFrame(flex).SetBorders(0, 0, 0, 0, 0, 0)
	frame.SetBorder(true).SetTitle(" call ")

	grid := tview.NewGrid().
		SetColumns(0, 56, 0).
		SetRows(0, bodyRows+formRows+2, 0).
		AddItem(frame, 1, 1, 1, 1, 0, 0, true)
	return grid
}

func (a *App) runLiveCallTicker(page *liveCallPage) {
	t := time.NewTicker(1 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-page.stop:
			return
		case <-t.C:
			a.app.QueueUpdateDraw(page.refresh)
		}
	}
}

func (lp *liveCallPage) refresh() {
	lp.mu.Lock()
	mic := lp.mic
	spk := lp.spk
	dropped := lp.dropped
	muted := lp.muted
	lp.mu.Unlock()

	dur := time.Since(lp.startedAt)
	header := fmt.Sprintf("%s   [green]connected[white]", formatCallDuration(dur))
	if muted {
		header = fmt.Sprintf("%s   [green]connected[white]   [yellow]muted[white]", formatCallDuration(dur))
	}

	micLine := formatStreamLine("↑ mic", mic, true)
	spkLine := formatStreamLine("↓ spk", spk, false)
	dropsLine := fmt.Sprintf("drops: %d", dropped)

	const pad = "  "
	lp.body.SetText(fmt.Sprintf("%s%s\n\n%s%s\n%s%s\n%s%s",
		pad, header,
		pad, micLine,
		pad, spkLine,
		pad, dropsLine,
	))
}

func formatCallDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d.Seconds())
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	if h > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

func formatStreamLine(label string, h streamSampleHistory, isMic bool) string {
	if !h.hasCur {
		return fmt.Sprintf("%s   waiting for first sample…", label)
	}
	var frames uint64
	if isMic {
		frames = h.cur.framesOut
	} else {
		frames = h.cur.framesIn
	}
	kbps := "—"
	if h.hasPrev {
		dt := h.cur.at.Sub(h.prev.at).Seconds()
		if dt > 0 {
			var dBytes uint64
			if isMic {
				dBytes = h.cur.bytesOut - h.prev.bytesOut
			} else {
				dBytes = h.cur.bytesIn - h.prev.bytesIn
			}
			kbpsF := float64(dBytes) * 8.0 / dt / 1000.0
			kbps = fmt.Sprintf("%.0f kbps", kbpsF)
		}
	}
	if isMic {
		return fmt.Sprintf("%s   %d fr   %s   — ms", label, frames, kbps)
	}
	return fmt.Sprintf("%s   %d fr   %s   %.0f ms", label, frames, kbps, h.cur.jitterMs)
}

func (a *App) cmdLiveCallMute(page *liveCallPage) {
	page.mu.Lock()
	action := ipc.CallControlMute
	if page.muted {
		action = ipc.CallControlUnmute
	}
	page.mu.Unlock()
	a.sendRequest(ipc.FrameCallControl, ipc.CallControlRequest{
		CallID: page.callID,
		Action: action,
	}, func(f ipc.Frame) {
		if f.Type == ipc.FrameError {
			a.renderError(f)
			return
		}
		if f.Type != ipc.FrameCallControlled {
			a.log("[red]call_control[white] unexpected response: %s", f.Type)
			return
		}
		var p ipc.CallControlledResponse
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			a.log("[red]call_control[white] decode failed: %v", err)
			return
		}
		a.app.QueueUpdateDraw(func() {
			page.mu.Lock()
			page.muted = p.Action == ipc.CallControlMute
			page.mu.Unlock()

			label := "mute"
			if page.muted {
				label = "unmute"
			}
			if btn := page.form.GetButton(0); btn != nil {
				btn.SetLabel(label)
			}
			page.refresh()
		})
	})
}

func (a *App) cmdLiveCallHangup(page *liveCallPage) {
	a.respondCall(page.callID, ipc.CallActionEnd, "")
}

func (a *App) routeCallStreamEvent(f ipc.Frame) {
	var p ipc.CallStreamEventPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		a.log("[red]call.stream-event[white] decode failed: %v", err)
		return
	}
	a.winMu.Lock()
	page := a.liveCalls[p.CallID]
	a.winMu.Unlock()

	switch p.Type {
	case "stats":
		if page == nil {
			return
		}
		applyStreamSample(page, p)
		a.app.QueueUpdateDraw(page.refresh)
	case "warn":
		a.app.QueueUpdateDraw(func() {
			a.log("[yellow]call streamer warn[white] (%s/%s): %s", shortCallID(p.CallID), p.Side, p.Reason)
		})
	case "error":
		a.app.QueueUpdateDraw(func() {
			a.log("[red]call streamer error[white] (%s/%s): %s", shortCallID(p.CallID), p.Side, p.Reason)
		})
	default:

	}
}

func applyStreamSample(page *liveCallPage, p ipc.CallStreamEventPayload) {
	page.mu.Lock()
	defer page.mu.Unlock()
	sample := streamSample{
		at:        time.Now(),
		bytesIn:   p.BytesIn,
		bytesOut:  p.BytesOut,
		framesIn:  p.FramesIn,
		framesOut: p.FramesOut,
		jitterMs:  p.JitterMs,
	}
	switch p.Side {
	case "mic":
		if page.mic.hasCur {
			page.mic.prev = page.mic.cur
			page.mic.hasPrev = true
		}
		page.mic.cur = sample
		page.mic.hasCur = true
	case "spk":
		if page.spk.hasCur {
			page.spk.prev = page.spk.cur
			page.spk.hasPrev = true
		}
		page.spk.cur = sample
		page.spk.hasCur = true
	}

	if p.FramesDropped > page.dropped {
		page.dropped = p.FramesDropped
	}
}
