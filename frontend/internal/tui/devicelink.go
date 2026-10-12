package tui

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"haoma-frontend/internal/ipc"
)

const linkOfferWarning = "Linking copies this device's identity key to the other device: " +
	"it becomes you, not someone you talk to.\n\n" +
	"The joining device must be brand new — no contacts, no chats — because it takes on this identity wholesale."

const linkOfferHoldNotice = StyleLinkDim +
	"this device's rendezvous is still publishing over Tor (usually 30–60s) — " +
	"hold the words until the reachable line below" + StyleLinkBody

const (
	linkCapRefusalHeadline = "This device already has a linked device, and you can only have one."
	linkCapRefusalNext     = "Unlink it from /linked-devices, then link the new device."
)

const linkCapRefusal = linkCapRefusalHeadline + "\n\n" + linkCapRefusalNext

const linkListAnswerWait = 2 * time.Second

const (
	linkListCauseNoAnswer   = "The daemon did not answer in time."
	linkListCauseRefused    = "The daemon could not read the set."
	linkListCauseUnreadable = "The daemon's answer could not be read."
)

const linkListUnknownBody = "This device could not check whether it already has a linked device, " +
	"so it has not offered one.\n\n" +
	"%s\n\n" +
	"You can only have one linked device. Try again, or open Linked devices to see the set."

func (a *App) cmdLinkDevice(arg string) {
	if strings.TrimSpace(arg) != "" {
		a.log(StyleLinkStress + "/link-device" + StyleLinkBody + " takes no arguments — it offers to add a device to this identity.")
		return
	}

	if a.VaultCtl == nil {
		a.refuseWithoutVault("/link-device")
		return
	}
	var once sync.Once
	draw := func(outcome linkListOutcome, cause string) {
		once.Do(func() {
			a.app.QueueUpdateDraw(func() {
				switch outcome {
				case linkListFull:
					a.showLinkCapRefusal()
				case linkListUnknown:
					a.showLinkListUnknown(cause)
				default:
					a.showLinkOfferConfirm()
				}
			})
		})
	}
	a.sendRequest(ipc.FrameDeviceLinksList, ipc.DeviceLinksListRequest{}, func(f ipc.Frame) {
		var listed ipc.DeviceLinksListedResponse
		switch {
		case f.Type != ipc.FrameDeviceLinksListed:

			draw(linkListUnknown, linkListCauseRefused)
		case json.Unmarshal(f.Payload, &listed) != nil:
			draw(linkListUnknown, linkListCauseUnreadable)
		case len(listed.Devices) > 0:
			draw(linkListFull, "")
		default:
			draw(linkListClean, "")
		}
	})
	go func() {
		time.Sleep(linkListAnswerWait)
		draw(linkListUnknown, linkListCauseNoAnswer)
	}()
}

type linkListOutcome int

const (
	linkListClean linkListOutcome = iota
	linkListFull
	linkListUnknown
)

func (a *App) showLinkListUnknown(cause string) {
	const pageName = "link-list-unknown"

	back, _ := a.pages.GetFrontPage()
	dismiss := func() {
		a.pages.RemovePage(pageName)
		if back != "" {
			a.pages.SwitchToPage(back)
		}
		a.app.SetFocus(a.input)
	}

	form := tview.NewForm()
	form.SetButtonsAlign(tview.AlignCenter)

	form.AddTextView("Linking", fmt.Sprintf(linkListUnknownBody, cause), 74, 7, true, false)
	form.AddButton("Try again", func() {
		dismiss()

		go a.cmdLinkDevice("")
	})
	form.AddButton("Linked devices", func() {
		dismiss()
		a.openLinkedDevicesPage(nil)
	})
	form.AddButton("Close", dismiss)

	form.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyEscape {
			dismiss()
			return nil
		}
		return ev
	})

	form.SetBorder(true).SetTitle(" link a device — not checked ")

	grid := tview.NewGrid().
		SetColumns(0, 90, 0).
		SetRows(0, 13, 0).
		AddItem(form, 1, 1, 1, 1, 0, 0, true)

	a.pages.AddPage(pageName, grid, true, true)
	a.app.SetFocus(form)
}

func (a *App) showLinkCapRefusal() {
	const pageName = "link-cap-refusal"

	back, _ := a.pages.GetFrontPage()
	dismiss := func() {
		a.pages.RemovePage(pageName)
		if back != "" {
			a.pages.SwitchToPage(back)
		}
		a.app.SetFocus(a.input)
	}

	form := tview.NewForm()
	form.SetButtonsAlign(tview.AlignCenter)
	form.AddTextView("Linking", linkCapRefusal, 74, 4, true, false)
	form.AddButton("Linked devices", func() {
		dismiss()
		a.openLinkedDevicesPage(nil)
	})
	form.AddButton("Close", dismiss)

	form.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyEscape {
			dismiss()
			return nil
		}
		return ev
	})

	form.SetBorder(true).SetTitle(" link a device — refused ")

	grid := tview.NewGrid().
		SetColumns(0, 90, 0).
		SetRows(0, 10, 0).
		AddItem(form, 1, 1, 1, 1, 0, 0, true)

	a.pages.AddPage(pageName, grid, true, true)
	a.app.SetFocus(form)
}

func (a *App) showLinkOfferConfirm() {
	const pageName = "link-offer-confirm"

	back, _ := a.pages.GetFrontPage()
	dismiss := func() {
		a.pages.RemovePage(pageName)
		if back != "" {
			a.pages.SwitchToPage(back)
		}
		a.app.SetFocus(a.input)
	}

	form := tview.NewForm()
	form.SetButtonsAlign(tview.AlignCenter)
	form.AddTextView("Identity", linkOfferWarning, 74, 5, true, false)
	form.AddButton("Link device", func() {
		dismiss()

		a.openLinkedDevicesPage(func() { a.beginLinkFlow(flowOffer) })
	})
	form.AddButton("Cancel", dismiss)

	form.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyEscape {
			dismiss()
			return nil
		}
		return ev
	})

	form.SetBorder(true).SetTitle(" link a device — this hands over your identity ")

	grid := tview.NewGrid().
		SetColumns(0, 90, 0).
		SetRows(0, 11, 0).
		AddItem(form, 1, 1, 1, 1, 0, 0, true)

	a.pages.AddPage(pageName, grid, true, true)
	a.app.SetFocus(form)
}

func (a *App) sendLinkOffer() {
	a.log(StyleLinkDim + "starting a device-link offer…" + StyleLinkBody)
	a.sendRequest(ipc.FrameDeviceLinkOffer, ipc.DeviceLinkOfferRequest{}, func(f ipc.Frame) {
		switch f.Type {
		case ipc.FrameError:

			a.failLinkRunWithReason(f)
			return
		case ipc.FrameDeviceLinkFailed:
			a.routeDeviceLinkFailed(f)
			return
		case ipc.FrameDeviceLinkStarted:
		default:
			a.failLinkRun("The daemon answered something this run cannot use.")
			a.log(StyleLinkBad+"/link-device"+StyleLinkBody+" unexpected response: %s", f.Type)
			return
		}
		var p ipc.DeviceLinkStartedResponse
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			a.failLinkRun("The invitation could not be read.")
			a.log(StyleLinkBad+"/link-device"+StyleLinkBody+" decode: %v", err)
			return
		}
		a.winMu.Lock()
		a.lastLinkOfferHandle = p.HandleID
		a.pendingLinkOffers[p.HandleID] = pendingOnionInvite{
			words:     p.Words,
			expiresAt: p.ExpiresAt,
		}
		a.winMu.Unlock()

		a.app.QueueUpdateDraw(func() {
			a.log(StyleLinkOK + "link offer open" + StyleLinkBody + " — publishing this device's rendezvous over Tor before the words are shown")
			a.log("  "+StyleLinkDim+"handle: %s"+StyleLinkBody, p.HandleID)
			if p.ExpiresAt > 0 {
				a.log("  "+StyleLinkDim+"expires: %s"+StyleLinkBody, time.Unix(p.ExpiresAt, 0).Format("2006-01-02 15:04 MST"))
			}
			a.log("%s", linkOfferHoldNotice)
			a.log(StyleLinkDim + "/link-cancel withdraws this offer" + StyleLinkBody)
		})
	})
}

func (a *App) revealPendingLinkOffer(handleID string, notReachable bool) {
	a.winMu.Lock()
	pending, ok := a.pendingLinkOffers[handleID]
	delete(a.pendingLinkOffers, handleID)
	a.winMu.Unlock()
	if !ok {
		return
	}
	words := strings.Join(pending.words, " ")

	a.renderInviteQR(words)
	if notReachable {
		a.log(StyleLinkStress + "could not confirm this device is reachable" + StyleLinkBody +
			" — the offer is still live, so try these words anyway; if the new device cannot connect, run /link-cancel and offer again.")
	} else {
		a.log(StyleLinkOK+"reachable"+StyleLinkBody+" — carry these %d words to the device you are adding:", len(pending.words))
	}
	a.log("  "+StyleLinkWords+"%s"+StyleLinkBody, words)
	a.log(StyleLinkDim + "on the new device run /link-join with those words" + StyleLinkBody)
}

func (a *App) cmdLinkJoin(arg string) {
	words := strings.Fields(arg)
	if len(words) < 3 {
		a.log(StyleLinkBad + "/link-join" + StyleLinkBody + " needs the words shown by /link-device on your other device")
		a.log("  example: " + StyleLinkWords + "/link-join acid acorn acre acts afar affix aged" + StyleLinkBody)
		return
	}
	a.log(StyleLinkDim+"joining this device to that identity (%d words) — dialing over Tor. %s"+StyleLinkBody, len(words), joinWaitStartLine())

	run := a.linkFlow()
	if run != nil {
		run.StartDialWait()
	}
	a.sendRequest(ipc.FrameDeviceLinkAccept, ipc.DeviceLinkAcceptRequest{Words: words}, func(f ipc.Frame) {
		if run != nil {
			run.EndDialWait()
		}

		current := run != nil && a.flowIsCurrent(run)
		switch f.Type {
		case ipc.FrameError:
			var p ipc.ErrorPayload
			_ = json.Unmarshal(f.Payload, &p)

			if line, keep, ok := joinFailureLine(p.Code); ok && keep {
				slog.Info("linking: join refused; the form is kept", slog.String("reason", p.Code))
				torSlow := ipc.JoinDialFailed(p.Code)
				style := StyleLinkBad
				if torSlow {
					style = StyleTorSlow
				}
				a.log(style+"%s"+StyleLinkBody, line)
				if current {
					run.RetryLater(line, torSlow)
					a.redrawLinkedDevicesPage()
				}
				return
			}
			if current {
				a.failLinkRun("The join could not be sent.")
			}
			a.renderLinkError(f)
			return
		case ipc.FrameDeviceLinkFailed:
			if current {
				a.routeDeviceLinkFailed(f)
			}
			return
		case ipc.FrameDeviceLinkAccepted:

			a.renderLinkAccepted(f)
			a.refreshAfterLink()

			var p ipc.DeviceLinkAcceptedResponse
			if current && json.Unmarshal(f.Payload, &p) == nil {
				a.finishLinkFlow(run, p.PeerID)
			}
		default:
			if current {
				a.failLinkRun("The daemon answered something this run cannot use.")
			}
			a.log(StyleLinkBad+"/link-join"+StyleLinkBody+" unexpected response: %s", f.Type)
		}
	})
}

func (a *App) cmdLinkCancel(arg string) {
	handle := strings.TrimSpace(arg)
	if handle == "" {
		a.winMu.Lock()
		handle = a.lastLinkOfferHandle
		a.winMu.Unlock()
	}
	if handle == "" {
		a.log(StyleLinkBad + "/link-cancel" + StyleLinkBody + " needs a handle id (shown when /link-device opened the offer)")
		return
	}
	a.log(StyleLinkDim+"withdrawing device-link offer %s…"+StyleLinkBody, handle)
	a.sendRequest(ipc.FrameDeviceLinkCancel, ipc.DeviceLinkCancelRequest{HandleID: handle}, func(f ipc.Frame) {
		switch f.Type {
		case ipc.FrameError:
			a.renderLinkError(f)
			return
		case ipc.FrameDeviceLinkFailed:
			a.routeDeviceLinkFailed(f)
			return
		}
		a.forgetLinkOffer(handle)
		a.log(StyleLinkOK+"offer withdrawn"+StyleLinkBody+" %s", handle)
	})
}

func (a *App) cmdLinkedDevices() {
	if a.VaultCtl == nil {
		a.refuseWithoutVault("/linked-devices")
		return
	}
	a.openLinkedDevicesPage(nil)
}

const linkNoVaultTail = " This session has no vault open, so Settings cannot be" +
	" opened. Restart haoma-text and try again."

func (a *App) refuseWithoutVault(command string) {
	a.log(StyleLinkBad+"%s unavailable."+StyleLinkBody+linkNoVaultTail, command)
}

func (a *App) routeDeviceLinkAccepted(f ipc.Frame) {
	a.app.QueueUpdateDraw(func() { a.renderLinkAccepted(f) })
}

func (a *App) renderLinkAccepted(f ipc.Frame) {
	var p ipc.DeviceLinkAcceptedResponse
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		a.log(StyleLinkBad+"device linked, but the confirmation was unreadable"+StyleLinkBody+": %v", err)
		return
	}
	a.log(StyleLinkOK + "linked" + StyleLinkBody + " — this device is now one of your own devices")
	a.log("  "+StyleLinkDim+"reaches your other device as %s"+StyleLinkBody, p.PeerID)
	if p.AdoptedFingerprint != "" {
		a.log("  "+StyleLinkDim+"identity now %s"+StyleLinkBody+" (it should read the same on both devices)", p.AdoptedFingerprint)
	}
	if p.ContactsCleared {
		a.log("  " + StyleLinkDim + "local leftovers were cleared so this device starts clean" + StyleLinkBody)
	}
	a.log(StyleLinkDim + "no contact and no chat comes out of this — a device of yours is not a person" + StyleLinkBody)
}

func (a *App) routeDeviceLinkProbe(f ipc.Frame) {
	var p ipc.DeviceLinkProbePush
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		a.app.QueueUpdateDraw(func() {
			a.log(StyleLinkDim+"a reachability report arrived unreadable"+StyleLinkBody+": %v", err)
		})
		return
	}

	if p.Ready {

		if c := a.linkFlow(); c != nil {
			a.winMu.Lock()
			pending, ok := a.pendingLinkOffers[p.HandleID]
			a.winMu.Unlock()
			if ok {
				c.SetWords(p.HandleID, strings.Join(pending.words, " "))
			}
			c.StepOnce("OK", "waiting for your other device to join")
			a.redrawLinkedDevicesPage()
		}
		a.app.QueueUpdateDraw(func() { a.revealPendingLinkOffer(p.HandleID, p.Error != "") })
		return
	}
	line := explainLinkProbe(p.Attempt, p.Ready, p.Error)
	a.app.QueueUpdateDraw(func() { a.log("%s", line) })
}

func explainLinkProbe(attempt int, ready bool, token string) string {
	switch {
	case ready && token != "":
		return StyleLinkStress + "could not confirm this device is reachable" + StyleLinkBody +
			" — the words above are still live, so enter them on the new device; if it cannot connect, run /link-cancel and offer again."
	case ready:
		return StyleLinkOK + "reachable" + StyleLinkBody +
			" — the new device can find this one now. Enter the words there."
	default:
		return fmt.Sprintf("%sstill publishing this device's rendezvous (attempt %d) — no answer yet%s",
			StyleLinkDim, attempt, StyleLinkBody)
	}
}

func (a *App) routeDeviceLinkCompleted(f ipc.Frame) {
	var p ipc.DeviceLinkCompletedPush
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		a.app.QueueUpdateDraw(func() {
			a.log(StyleLinkBad+"a device finished linking, but the confirmation was unreadable"+StyleLinkBody+": %v", err)
		})
		return
	}
	a.forgetLinkOffer(p.HandleID)
	a.refreshAfterLink()
	if c := a.linkFlow(); c != nil {
		a.finishLinkFlow(c, p.PeerID)
	}
	a.app.QueueUpdateDraw(func() {
		a.log(StyleLinkOK + "linked" + StyleLinkBody + " — the other device joined this identity")
		a.log("  "+StyleLinkDim+"you reach it as %s"+StyleLinkBody, p.PeerID)
		a.log(StyleLinkDim + "the one-time rendezvous is gone; the device is recorded as your own" + StyleLinkBody)
	})
}

func (a *App) routeDeviceLinkCancelled(f ipc.Frame) {
	var p ipc.DeviceLinkCancelRequest
	_ = json.Unmarshal(f.Payload, &p)
	a.forgetLinkOffer(p.HandleID)
	a.app.QueueUpdateDraw(func() {
		a.log(StyleLinkDim+"device-link offer withdrawn %s"+StyleLinkBody, p.HandleID)
	})
}

func (a *App) routeDeviceLinkFailed(f ipc.Frame) {

	var p ipc.DeviceLinkFailedPush
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		if c := a.linkFlow(); c != nil {
			c.Fail("The link did not go through.")
			a.redrawLinkedDevicesPage()
		}
		a.app.QueueUpdateDraw(func() {
			a.log(StyleLinkBad+"the device link did not go through"+StyleLinkBody+" — the daemon's reason was unreadable: %v", err)
		})
		return
	}
	a.forgetLinkOffer(p.HandleID)
	headline, guidance := explainLinkFailure(a.linkSideNow(), p.Reason, p.Detail)

	if c := a.linkFlow(); c != nil && linkPushBelongsTo(c, p.HandleID) {
		c.Fail(headline)
		for _, g := range guidance {

			c.Aside(g)
		}
		a.redrawLinkedDevicesPage()
	}
	a.app.QueueUpdateDraw(func() {
		a.log(StyleLinkBad+"%s"+StyleLinkBody, headline)
		for _, g := range guidance {
			a.log("  "+StyleLinkDim+"%s"+StyleLinkBody, g)
		}
	})
}

func (a *App) renderLinkError(f ipc.Frame) {
	var p ipc.ErrorPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		a.log(StyleLinkBad+"the device link did not go through"+StyleLinkBody+" (undecodable): %v", err)
		return
	}
	headline, guidance := explainLinkFailure(a.linkSideNow(), p.Code, p.Message)
	a.log(StyleLinkBad+"%s"+StyleLinkBody, headline)
	for _, g := range guidance {
		a.log("  "+StyleLinkDim+"%s"+StyleLinkBody, g)
	}
}

func (a *App) forgetLinkOffer(handleID string) {
	a.winMu.Lock()
	defer a.winMu.Unlock()
	if handleID == "" {
		a.lastLinkOfferHandle = ""
		a.pendingLinkOffers = map[string]pendingOnionInvite{}
		return
	}
	if a.lastLinkOfferHandle == handleID {
		a.lastLinkOfferHandle = ""
	}
	delete(a.pendingLinkOffers, handleID)
}

type linkSide int

const (
	linkSideUnknown linkSide = iota
	linkSideOffering
	linkSideJoining
)

func (a *App) linkSideNow() linkSide {
	c := a.linkFlow()
	if c == nil {
		return linkSideUnknown
	}
	switch mode, _ := c.snapshot(); mode {
	case flowOffer:
		return linkSideOffering
	case flowJoin:
		return linkSideJoining
	}
	return linkSideUnknown
}

func explainLinkFailure(side linkSide, reason, detail string) (headline string, guidance []string) {
	norm := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(reason)), "-", "_")
	detail = strings.TrimSpace(detail)

	switch {
	case norm == "":
		headline = "The device link did not go through."
		if detail != "" {
			guidance = append(guidance, detail)
		}
		return headline, guidance

	case strings.Contains(norm, "no_answer"):
		return "The link may have gone through.",
			[]string{"Restart the app, then check /linked-devices on this device. If it lists the other device, this one catches up on its own. If it does not, this device kept part of a join that never finished: erase it with /wipe-device and run /link-join again."}

	case strings.Contains(norm, "not_empty"),
		strings.Contains(norm, "has_contact"),
		strings.Contains(norm, "has_chat"),
		strings.Contains(norm, "has_peer"),
		strings.Contains(norm, "nonempty"):
		return "This device already has contacts or chats, so it cannot join another device.",
			[]string{
				"A joining device takes on the other device's identity wholesale, which only works from a clean slate.",
				"Use a device you have not set up yet, or erase this one with /wipe-device and run /link-join again.",
			}

	case strings.Contains(norm, "peer_check"), strings.Contains(norm, "registry"):
		switch side {
		case linkSideOffering:
			return "This device could not read its own linked-device set, so it did not offer a link.",
				[]string{
					"An offer is only made once this device knows whether it already has a linked device, and that read failed just now.",
					"Check that the local daemon is up and reachable, then run /link-device again.",
				}
		case linkSideJoining:
			return "This device could not confirm it is a clean slate, so it did not join.",
				[]string{
					"Joining is only safe from a device with no contacts and no chats, and that could not be verified just now.",
					"Check that the local daemon is up and reachable, then run /link-join again.",
				}
		default:
			return "This device could not read its own records, so nothing was linked.",
				[]string{
					"Linking reads this device's own records before anything happens, and that read failed just now.",
					"Check that the local daemon is up and reachable, then run the same command again.",
				}
		}

	case strings.Contains(norm, "bundle"):
		return "The other device never sent its identity, so this device stayed unchanged.",
			[]string{
				"The handshake got as far as connecting, then stopped. Nothing was adopted and nothing was linked.",
				"Run /link-device again on the other device and enter the fresh words here.",
			}

	case strings.Contains(norm, "expire"), strings.Contains(norm, "timeout"), strings.Contains(norm, "timed_out"):
		return "The offer ran out before the other device joined.",
			[]string{"Run /link-device again and enter the fresh words on the other device."}

	case strings.Contains(norm, "cancel"), strings.Contains(norm, "withdraw"):
		return "The offer was withdrawn before the other device joined.", nil

	case strings.Contains(norm, "already_linked"), strings.Contains(norm, "already_a_device"):

		return linkCapRefusalHeadline, []string{linkCapRefusalNext}

	case strings.Contains(norm, "word"):
		return "Those words did not open a link offer.",
			[]string{"Re-read them from the offering device — all of them, in order — and run /link-join again."}

	case strings.Contains(norm, "unreachable"), strings.Contains(norm, "dial"), strings.Contains(norm, "connect"):
		return "This device could not reach the one that made the offer.",
			[]string{"Both devices need a working Tor connection; check the status line on each, then try again."}
	}

	headline = "The device link did not go through."
	extra := norm
	if detail != "" {
		extra = norm + ": " + detail
	}
	return headline, []string{extra}
}

func (a *App) cmdSync(arg string) {
	full := false
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "":
	case "all", "full":
		full = true
	default:
		a.log(StyleLinkBad + "/sync" + StyleLinkBody + " takes no argument, or " + StyleLinkDim + "all" + StyleLinkBody + " to ignore the sync cursor")
		return
	}

	if full {
		a.log(StyleLinkDim + "asking my devices for everything they hold…" + StyleLinkBody)
	} else {
		a.log(StyleLinkDim + "asking my devices what this one missed…" + StyleLinkBody)
	}
	a.sendRequest(ipc.FrameDeviceSync, ipc.DeviceSyncRequest{Full: full}, func(f ipc.Frame) {
		if f.Type == ipc.FrameError {
			a.renderLinkError(f)
			return
		}
		var resp ipc.DeviceSyncSentResponse
		if err := json.Unmarshal(f.Payload, &resp); err != nil {
			a.log(StyleLinkBad+"could not read the sync reply"+StyleLinkBody+" (%v)", err)
			return
		}
		if resp.Siblings == 0 {
			a.log(StyleLinkDim + "no linked devices — nothing to sync with" + StyleLinkBody)
			return
		}
		if resp.AlreadyRunning {
			a.log(StyleLinkDim+"a sync is already running"+StyleLinkBody+" with %d linked device(s); anything missing will appear in the chats as it arrives", resp.Siblings)
			return
		}
		a.syncWait.start(time.Now())
		a.log(StyleLinkOK+"asked %d linked device(s)"+StyleLinkBody+"; anything missing will appear in the chats as it arrives", resp.Siblings)
	})
}

const syncFinishedWait = 11 * time.Minute

type syncOutstanding struct {
	mu sync.Mutex
	at time.Time
}

func (s *syncOutstanding) start(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.at = now
}

func (s *syncOutstanding) take(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.at.IsZero() {
		return false
	}
	inTime := now.Sub(s.at) <= syncFinishedWait
	s.at = time.Time{}
	return inTime
}

func (a *App) routeDeviceSyncFinished(f ipc.Frame) {
	var p ipc.DeviceSyncFinishedPush
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		slog.Warn("gapfill: the sync-finished push was unreadable", slog.Any("err", err))
		return
	}
	if !a.syncWait.take(time.Now()) {
		slog.Debug("gapfill: a manual sync this client did not start has finished",
			slog.Int("answered", p.Answered),
			slog.Bool("timed_out", p.TimedOut),
		)
		return
	}
	a.log("%s", syncFinishedLine(p))
}

func syncFinishedLine(p ipc.DeviceSyncFinishedPush) string {
	name := "sync"
	if p.Full {
		name = "sync all"
	}
	total := p.RowsApplied + p.SnapshotApplied
	changed := "nothing was missing"
	if total > 0 {
		changed = fmt.Sprintf("%d change(s) here", total)
	}
	if p.Held > 0 {
		changed += fmt.Sprintf(", %d row(s) wait for a contact this device does not have yet", p.Held)
	}
	if p.Unreachable > 0 && p.Answered == 0 && !p.TimedOut {
		return fmt.Sprintf(StyleLinkBad+"%s failed"+StyleLinkBody+": the request did not reach %d linked device(s)",
			name, p.Unreachable)
	}
	if p.TimedOut || p.Unreachable > 0 {
		verb := "finished"
		if p.TimedOut {
			verb = "stopped waiting"
		}
		if total == 0 && p.Held == 0 {
			changed = "no change here"
		}
		line := fmt.Sprintf(StyleLinkBad+"%s %s"+StyleLinkBody+": %d of %d linked device(s) answered",
			name, verb, p.Answered, p.Siblings)
		if p.Unreachable > 0 {
			line += fmt.Sprintf(", %d could not be reached", p.Unreachable)
		}
		return line + "; " + changed
	}
	return fmt.Sprintf(StyleLinkOK+"%s finished"+StyleLinkBody+": %d linked device(s) answered; %s",
		name, p.Answered, changed)
}

func (a *App) refreshAfterLink() {
	a.sendRequest(ipc.FrameListPeers, nil, a.handlePeersListed)
	a.sendRequest(ipc.FrameListChats, nil, a.handleChatsListed)
}

func (a *App) failLinkRunWithReason(f ipc.Frame) {
	var p ipc.ErrorPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		a.failLinkRun("The invitation could not be published.")
		a.renderError(f)
		return
	}

	headline, guidance := explainLinkFailure(linkSideOffering, p.Code, p.Message)
	if c := a.linkFlow(); c != nil {
		c.Fail(headline)
		for _, g := range guidance {
			c.Aside(g)
		}
		a.redrawLinkedDevicesPage()
	}
	a.app.QueueUpdateDraw(func() {
		a.log(StyleLinkBad+"%s"+StyleLinkBody, headline)
		for _, g := range guidance {
			a.log("  "+StyleLinkDim+"%s"+StyleLinkBody, g)
		}
	})
}
