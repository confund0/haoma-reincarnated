package tui

import (
	"encoding/json"
	"log/slog"
	"slices"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"haoma-frontend/internal/ipc"
)

const (
	pairStateWaiting  = "waiting"
	pairStateOffering = "Preparing the invitation. Keep this device on."
	pairStateOffered  = "invitation sent"
	pairStateFailed   = "it did not go out"
	pairStateUnknown  = "no answer came back"
)

func pairStateColour(state string) (tcell.Color, bool) {
	switch state {
	case pairStateWaiting:
		return ColorContactsSection, true
	case pairStateOffering:
		return ColorContactsPairing, true
	case pairStateOffered:
		return ColorContactsOK, true
	case pairStateFailed:
		return ColorContactsBad, true
	case pairStateUnknown:
		return ColorContactsPairing, true
	}
	return tcell.ColorDefault, false
}

func (a *App) toggleContactMark(fingerprint string) {
	if fingerprint == "" {
		return
	}
	a.winMu.Lock()
	if a.contactMarked == nil {
		a.contactMarked = map[string]bool{}
	}
	marked := !a.contactMarked[fingerprint]
	if marked {
		a.contactMarked[fingerprint] = true
	} else {
		delete(a.contactMarked, fingerprint)
	}
	a.winMu.Unlock()

	slog.Debug("contacts: a contact was marked for pairing",
		slog.String("fingerprint", fingerprint),
		slog.Bool("marked", marked),
	)
	a.rebuildContactsPage()
}

func (a *App) startPairingRun(under string) {
	a.winMu.Lock()
	var want []string
	for i := range a.inherited {
		if a.contactMarked[a.inherited[i].Fingerprint] {
			want = append(want, a.inherited[i].Fingerprint)
		}
	}
	a.winMu.Unlock()

	if len(want) == 0 && under != "" {
		want = append(want, under)
	}
	if len(want) == 0 {
		a.log(StyleLinkDim + "mark a contact with Space first, or press p on one" + StyleLinkBody)
		return
	}

	a.queuePairings(want, true)
}

func (a *App) queuePairings(want []string, manual bool) {
	a.winMu.Lock()
	if a.pairState == nil {
		a.pairState = map[string]string{}
	}
	queued := 0
	var theirs []string
	for _, fp := range want {
		state := a.pairState[fp]
		switch state {
		case pairStateWaiting, pairStateOffering:
			continue
		}

		if slices.Contains(a.pairQueue, fp) {
			continue
		}
		entry, _ := a.inheritedEntryLocked(fp)
		if entry.Invited || (entry.Waiting && !manual) {
			delete(a.contactMarked, fp)
			theirs = append(theirs, fp)
			continue
		}
		copies := entry.Offers
		if copies < 1 || state == pairStateOffered || state == pairStateUnknown {
			copies = 1
		}
		a.pairState[fp] = pairStateWaiting
		for i := 0; i < copies; i++ {
			a.pairQueue = append(a.pairQueue, fp)
		}
		delete(a.pairOpenOnly, fp)
		delete(a.contactMarked, fp)
		queued++
		slog.Debug("contacts: a contact joined the pairing run",
			slog.String("fingerprint", fp),
			slog.Int("offers", copies),
		)
	}

	start := queued > 0 && !a.pairRunning
	if start {
		a.pairRunning = true
	}
	waiting := len(a.pairQueue)
	a.winMu.Unlock()

	for _, fp := range theirs {
		a.winMu.Lock()
		entry, _ := a.inheritedEntryLocked(fp)
		a.winMu.Unlock()
		name := a.pairingContactName(fp)
		if entry.Invited {
			a.log(StyleLinkDim+"%s"+StyleLinkBody+" — an invitation from that person stands; accept it in /invitations", inheritedInvitedLine(name))
			continue
		}
		a.log(StyleLinkDim+"%s"+StyleLinkBody+" — that person's device offers this pairing; press p on the row to pair anyway", inheritedWaitingLine(name))
	}
	if queued == 0 {
		if len(theirs) == 0 {
			a.log(StyleLinkDim + "those contacts are already in the pairing run" + StyleLinkBody)
		}
		a.rebuildContactsPage()
		return
	}
	slog.Debug("contacts: pairing run asked to start",
		slog.Int("queued", queued),
		slog.Int("waiting", waiting),
		slog.Bool("started_here", start),
	)
	a.log(StyleLinkDim+"pairing with %d contact(s), one at a time…"+StyleLinkBody, queued)
	if start {
		a.runNextPairing()
	}
	a.rebuildContactsPage()
}

func (a *App) runNextPairing() {
	a.winMu.Lock()
	if len(a.pairQueue) == 0 {
		a.pairRunning = false
		a.winMu.Unlock()
		slog.Debug("contacts: pairing run finished")
		return
	}

	if !a.haomaConnected {
		left := len(a.pairQueue)
		a.pairRunning = false
		a.winMu.Unlock()
		slog.Debug("contacts: pairing run held — the daemon is not reachable",
			slog.Int("still_waiting", left),
		)
		a.log(StyleLinkDim+"pairing held"+StyleLinkBody+" — %d contact(s) still to offer. The run starts again when the daemon is back.", left)
		return
	}
	fingerprint := a.pairQueue[0]
	a.pairQueue = a.pairQueue[1:]
	a.pairState[fingerprint] = pairStateOffering
	if a.pairOffered == nil {
		a.pairOffered = map[string]bool{}
	}
	a.pairOffered[fingerprint] = true
	openOnly := a.pairOpenOnly[fingerprint]
	if !slices.Contains(a.pairQueue, fingerprint) {
		delete(a.pairOpenOnly, fingerprint)
	}
	left := len(a.pairQueue)
	a.winMu.Unlock()

	slog.Debug("contacts: offering a pairing",
		slog.String("fingerprint", fingerprint),
		slog.Int("still_waiting", left),
	)

	a.sendRequest(ipc.FrameInvitationOffer, ipc.InvitationOfferRequest{
		Fingerprint: fingerprint,
		Force:       true,
		OpenOnly:    openOnly,
	}, func(f ipc.Frame) {
		a.finishPairing(fingerprint, f)
	})
}

func (a *App) finishPairing(fingerprint string, f ipc.Frame) {
	state := pairStateOffered
	reason := ""
	siblings := 0
	switch f.Type {
	case ipc.FrameInvitationOffered:
		var p ipc.InvitationOfferedResponse
		if err := json.Unmarshal(f.Payload, &p); err != nil {

			reason = "undecodable"
		} else {
			siblings = p.Siblings
		}
	default:
		state = pairStateFailed
		var p ipc.ErrorPayload
		if err := json.Unmarshal(f.Payload, &p); err == nil {

			reason = p.Code
		}
		switch reason {
		case ipc.ReasonNoAnswer:
			state = pairStateUnknown
		case ipc.ReasonNothingOpen:

			state = pairStateOffered
		case ipc.ReasonEncodeFrame:

			state = pairStateOffered
		}
	}

	a.winMu.Lock()
	a.pairState[fingerprint] = state
	recheck := a.pushedDuringOffer[fingerprint]
	delete(a.pushedDuringOffer, fingerprint)
	a.winMu.Unlock()

	if recheck {
		a.sendRequest(ipc.FrameListInheritedContacts, nil, func(f ipc.Frame) {
			a.handleInheritedContactsListed(f)
			a.requeueOffered()
		})
	}

	slog.Debug("contacts: one pairing offer ended",
		slog.String("fingerprint", fingerprint),
		slog.String("state", state),
		slog.String("reason", reason),
		slog.Int("siblings", siblings),
	)
	a.reportPairingOutcome(fingerprint, state, reason, siblings)
	a.runNextPairing()
	a.app.QueueUpdateDraw(a.rebuildContactsPage)
}

func (a *App) resumePairingRun() {
	a.winMu.Lock()
	start := len(a.pairQueue) > 0 && !a.pairRunning
	if start {
		a.pairRunning = true
	}
	left := len(a.pairQueue)
	a.winMu.Unlock()

	if !start {
		return
	}
	slog.Debug("contacts: pairing run resumed with the connection",
		slog.Int("still_waiting", left),
	)
	a.log(StyleLinkDim+"pairing resumed"+StyleLinkBody+" — %d contact(s) left to offer", left)
	a.runNextPairing()
	a.app.QueueUpdateDraw(a.rebuildContactsPage)
}

func (a *App) reportPairingOutcome(fingerprint, state, reason string, siblings int) {
	name := a.pairingContactName(fingerprint)
	if reason == ipc.ReasonNothingOpen {
		slog.Debug("contacts: nothing left to offer to a contact; an invitation stands for each device",
			slog.String("fingerprint", fingerprint),
		)
		return
	}
	if state == pairStateOffered {
		if reason == "undecodable" {

			a.log(StyleLinkOK+"invitation offered"+StyleLinkBody+" to %s — the daemon's answer could not be read, so this device cannot say how many of your devices took it", name)
			return
		}
		if reason == ipc.ReasonEncodeFrame {

			a.log(StyleLinkOK+"invitation offered"+StyleLinkBody+" to %s — the daemon could not build its answer, so this device cannot say how many of your devices took it", name)
			return
		}
		a.log(StyleLinkOK+"invitation offered"+StyleLinkBody+" to %s — handed to %d of your devices to pass on", name, siblings)
		return
	}
	if state == pairStateUnknown {
		a.log(StyleLinkDim+"no answer"+StyleLinkBody+" for %s — the connection to the daemon went down while the invitation was out", name)
		a.log("  " + StyleLinkDim + "the invitation may be standing. Open /invitations once the daemon is back, and offer again only if it is not there." + StyleLinkBody)
		return
	}
	headline, guidance := explainInvitationOfferFailure(reason)
	a.log(refusalStyle(reason)+"no invitation went out"+StyleLinkBody+" for %s — %s", name, headline)
	for _, g := range guidance {
		a.log("  "+StyleLinkDim+"%s"+StyleLinkBody, g)
	}
}

func (a *App) inheritedEntryLocked(fingerprint string) (ipc.InheritedContactEntry, bool) {
	for _, c := range a.inherited {
		if c.Fingerprint == fingerprint {
			return c, true
		}
	}
	return ipc.InheritedContactEntry{}, false
}

func (a *App) pairingContactName(fingerprint string) string {
	a.winMu.Lock()
	defer a.winMu.Unlock()
	for _, c := range a.inherited {
		if c.Fingerprint == fingerprint && c.Label != "" {
			return c.Label
		}
	}
	return shortID(fingerprint)
}

func (a *App) handleInheritedContactsListed(f ipc.Frame) {
	if f.Type != ipc.FrameInheritedContactsListed {
		reason := ""
		var e ipc.ErrorPayload
		if err := json.Unmarshal(f.Payload, &e); err == nil {
			reason = e.Code
		}
		slog.Warn("contacts: the contacts with no pairing could not be listed",
			slog.String("reason", reason),
		)
		headline, guidance := explainInheritedContactsListFailure(reason)
		a.app.QueueUpdateDraw(func() {
			a.log(StyleLinkBad+"%s"+StyleLinkBody, headline)
			for _, g := range guidance {
				a.log("  "+StyleLinkDim+"%s"+StyleLinkBody, g)
			}
		})
		return
	}
	var p ipc.InheritedContactsListedResponse
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		slog.Warn("contacts: the list of contacts with no pairing was unreadable", slog.Any("err", err))
		return
	}
	slog.Debug("contacts: the contacts with no pairing were listed",
		slog.Int("contact_count", len(p.Contacts)),
	)
	a.applyInheritedContacts(p.Contacts, p.Linked)
}

func (a *App) routeInheritedContactsChanged(f ipc.Frame) {
	var p ipc.InheritedContactsChangedPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		slog.Warn("contacts: the pushed contacts with no pairing were unreadable", slog.Any("err", err))
		return
	}
	slog.Debug("contacts: the contacts with no pairing changed",
		slog.Int("contact_count", len(p.Contacts)),
	)
	a.applyInheritedContacts(p.Contacts, p.Linked)
	a.requeueOffered()
}

func (a *App) requeueOffered() {
	a.winMu.Lock()
	queued := 0
	for _, c := range a.inherited {
		fp := c.Fingerprint
		if !a.pairOffered[fp] || c.Open < 1 || c.Waiting || c.Invited {
			continue
		}
		switch a.pairState[fp] {
		case pairStateOffering:

			if a.pushedDuringOffer == nil {
				a.pushedDuringOffer = map[string]bool{}
			}
			a.pushedDuringOffer[fp] = true
			continue
		case pairStateWaiting:
			continue
		case pairStateFailed, pairStateUnknown:

			continue
		}
		if slices.Contains(a.pairQueue, fp) {
			continue
		}
		if a.pairState == nil {
			a.pairState = map[string]string{}
		}
		a.pairState[fp] = pairStateWaiting
		for i := 0; i < c.Open; i++ {
			a.pairQueue = append(a.pairQueue, fp)
		}
		if a.pairOpenOnly == nil {
			a.pairOpenOnly = map[string]bool{}
		}
		a.pairOpenOnly[fp] = true
		queued++
		slog.Debug("contacts: a contact offered to before shows a device still to offer; it joined the run again",
			slog.String("fingerprint", fp),
			slog.Int("offers", c.Open),
		)
	}
	start := queued > 0 && !a.pairRunning
	if start {
		a.pairRunning = true
	}
	a.winMu.Unlock()

	if queued == 0 {
		return
	}
	a.log(StyleLinkDim+"another device of %d contact(s) to pair with, one at a time…"+StyleLinkBody, queued)
	if start {
		a.runNextPairing()
	}
	a.app.QueueUpdateDraw(a.rebuildContactsPage)
}

func (a *App) showInheritedContactDetails(fingerprint string) {
	const pageName = "inherited-contact-details"

	a.winMu.Lock()
	var found *ipc.InheritedContactEntry
	for i := range a.inherited {
		if a.inherited[i].Fingerprint == fingerprint {
			found = &a.inherited[i]
			break
		}
	}
	a.winMu.Unlock()
	if found == nil {
		return
	}
	c := *found

	dismiss := func() {
		a.pages.RemovePage(pageName)
		a.switchTo("contacts")
		a.focusInheritedFrame()
	}

	unset := func(s string) string {
		if s == "" {
			return "—"
		}
		return s
	}

	form := tview.NewForm()
	form.SetButtonsAlign(tview.AlignCenter)
	form.AddTextView("Real nick", unset(c.Nick), 0, 1, false, false)
	form.AddTextView("Local alias", unset(c.Alias), 0, 1, false, false)
	form.AddTextView("Fingerprint", formatFingerprint(c.Fingerprint), 0, 2, false, false)
	switch {
	case c.Invited:

	case c.Waiting:
		form.AddButton("Pair anyway", func() {
			dismiss()
			a.queuePairings([]string{c.Fingerprint}, true)
		})
	default:
		form.AddButton("Pair", func() {
			dismiss()
			a.queuePairings([]string{c.Fingerprint}, true)
		})
	}
	form.AddButton("Close", dismiss)
	form.SetBorder(true).SetTitle(" details ")

	const modalWidth = 96
	grid := tview.NewGrid().
		SetColumns(0, modalWidth, 0).
		SetRows(0, 12, 0).
		AddItem(form, 1, 1, 1, 1, 0, 0, true)

	grid.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyEscape {
			dismiss()
			return nil
		}
		return ev
	})

	a.pages.AddPage(pageName, grid, true, true)
	a.app.SetFocus(form)
}

func (a *App) applyInheritedContacts(contacts []ipc.InheritedContactEntry, linked bool) {
	a.winMu.Lock()
	a.inherited = contacts
	a.linked = linked

	held := map[string]bool{}
	for _, c := range contacts {
		held[c.Fingerprint] = true
	}
	for fp := range a.contactMarked {
		if !held[fp] {
			delete(a.contactMarked, fp)
		}
	}
	for fp := range a.pairState {
		if !held[fp] {
			delete(a.pairState, fp)
		}
	}
	a.winMu.Unlock()

	a.app.QueueUpdateDraw(a.rebuildContactsPage)
}

func explainInheritedContactsListFailure(reason string) (string, []string) {
	norm := normaliseReason(reason)
	switch {

	case strings.Contains(norm, "no_answer"):
		return "The contacts with no pairing could not be read.",
			[]string{
				"The connection to the daemon went down before it replied. This is not an empty section.",
				"Open /contacts again once the status line shows the daemon back.",
			}

	case strings.Contains(norm, "store_read_failed"):
		return "The contacts with no pairing could not be read.",
			[]string{
				"This is not an empty section — it is a section that failed, and there may be contacts behind it.",
				"Open /contacts again; if it keeps failing, this device's storage is the thing to look at.",
			}
	case strings.Contains(norm, "encode_frame"):
		return "The daemon could not put the contacts into a frame.",
			[]string{"Nothing is wrong with the contacts themselves. Open /contacts again; a build that keeps doing this is the thing to look at."}
	}
	return "The contacts with no pairing could not be read.", []string{unworded(norm)}
}
