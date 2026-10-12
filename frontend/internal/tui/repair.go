package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
)

const invitationDeadlineLayout = "2006-01-02 15:04 MST"

type invitationLook struct {
	tag        string
	color      tcell.Color
	phrase     string
	answerable bool
}

func invitationLookFor(state string, expiresAt int64, now time.Time) invitationLook {
	deadline := ""
	if expiresAt > 0 {
		deadline = time.Unix(expiresAt, 0).Format(invitationDeadlineLayout)
	}
	switch state {
	case events.InvitationLive:
		if events.InvitationAnswerable(state, expiresAt, now.Unix()) {
			return invitationLook{
				tag:        StyleInvitationLive,
				color:      ColorInvitationLive,
				phrase:     "live - lapses " + deadline,
				answerable: true,
			}
		}
		if expiresAt <= 0 {
			return invitationLook{
				tag:    StyleInvitationDim,
				color:  ColorInvitationDim,
				phrase: "no deadline recorded - it cannot be answered",
			}
		}
		return invitationLook{
			tag:    StyleInvitationDim,
			color:  ColorInvitationDim,
			phrase: "lapsed - stood until " + deadline,
		}
	case events.InvitationSuperseded:
		return invitationLook{tag: StyleInvitationDim, color: ColorInvitationDim, phrase: "retired by a newer invitation"}
	case events.InvitationAccepted:
		return invitationLook{tag: StyleInvitationOK, color: ColorInvitationOK, phrase: "accepted - the pairing completed"}
	case events.InvitationFailed:
		return invitationLook{tag: StyleInvitationBad, color: ColorInvitationBad, phrase: "failed - no pairing came of it"}
	case "":
		return invitationLook{tag: StyleInvitationDim, color: ColorInvitationDim, phrase: "state unknown"}
	default:
		return invitationLook{tag: StyleInvitationDim, color: ColorInvitationDim, phrase: state}
	}
}

func (a *App) cmdRepair(arg string) {
	needle := strings.TrimSpace(arg)
	if needle == "" {
		a.log(StyleLinkBad + "/re-pair" + StyleLinkBody + " needs the contact you cannot reach — for example " + StyleLinkWords + "/re-pair alice" + StyleLinkBody)
		a.log(StyleLinkDim + "the name is the one /chats shows; a conversation inherited from one of your devices has no pairing yet" + StyleLinkBody)
		return
	}
	a.sendRequest(ipc.FrameListChats, nil, func(f ipc.Frame) {
		a.handleChatsListed(f)
		if f.Type != ipc.FrameChatsListed {
			return
		}
		a.winMu.Lock()
		chats := append([]ipc.ChatEntry(nil), a.chats...)
		a.winMu.Unlock()
		match, candidates := resolveInvitationContact(chats, needle)
		a.app.QueueUpdateDraw(func() { a.startInvitationOffer(needle, match, candidates) })
	})
}

func (a *App) startInvitationOffer(needle string, match ipc.ChatEntry, candidates []string) {
	switch {
	case len(candidates) > 1:
		a.log(StyleLinkBad+"more than one contact matches"+StyleLinkBody+" %q — name one of these:", needle)
		for _, c := range candidates {
			a.log("  "+StyleLinkWords+"%s"+StyleLinkBody, c)
		}
		return
	case match.ChatID == "":
		a.log(StyleLinkBad+"no contact here is called"+StyleLinkBody+" %q", needle)
		a.log(StyleLinkDim + "check /chats for the name this device knows them by" + StyleLinkBody)
		return
	case match.Fingerprint == "":
		a.log(StyleLinkBad+"this device holds no identity for"+StyleLinkBody+" %s", invitationContactName(match))
		a.log(StyleLinkDim + "an invitation is addressed to a person's identity, and that conversation carries none" + StyleLinkBody)
		return
	}

	label := invitationContactName(match)
	a.log(StyleLinkDim+"offering %s a pairing from this device…"+StyleLinkBody, label)
	a.log("  " + StyleLinkDim + pairStateOffering + StyleLinkBody)
	a.sendRequest(ipc.FrameInvitationOffer, ipc.InvitationOfferRequest{Fingerprint: match.Fingerprint}, func(f ipc.Frame) {
		if f.Type != ipc.FrameInvitationOffered {
			a.renderInvitationError(f, explainInvitationOfferFailure)
			return
		}
		var p ipc.InvitationOfferedResponse
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			a.app.QueueUpdateDraw(func() {
				a.log(StyleLinkBad+"the invitation went out, but the confirmation was unreadable"+StyleLinkBody+": %v", err)
			})
			return
		}
		a.app.QueueUpdateDraw(func() {
			a.log(StyleLinkOK+"invitation offered"+StyleLinkBody+" — handed to %d of your devices to pass on", p.Siblings)
			if p.ExpiresAt > 0 {
				a.log("  "+StyleLinkDim+"it stands until %s"+StyleLinkBody, time.Unix(p.ExpiresAt, 0).Format(invitationDeadlineLayout))
			}
			a.log("  "+StyleLinkDim+"it reaches %s in the conversation they already have with you; nothing happens until they accept"+StyleLinkBody, label)
		})
	})
}

func invitationContactName(c ipc.ChatEntry) string {
	if c.Label != "" {
		return c.Label
	}
	if c.Fingerprint != "" {
		return shortID(c.Fingerprint)
	}
	return shortID(c.ChatID)
}

func resolveInvitationContact(chats []ipc.ChatEntry, needle string) (ipc.ChatEntry, []string) {
	want := strings.ToLower(strings.TrimSpace(needle))
	if want == "" {
		return ipc.ChatEntry{}, nil
	}
	var direct []ipc.ChatEntry
	for _, c := range chats {
		if c.Kind == ipc.ChatKindDirect {
			direct = append(direct, c)
		}
	}
	tiers := []func(ipc.ChatEntry) bool{
		func(c ipc.ChatEntry) bool { return strings.ToLower(c.Label) == want },
		func(c ipc.ChatEntry) bool {
			return c.Label != "" && strings.HasPrefix(strings.ToLower(c.Label), want)
		},
		func(c ipc.ChatEntry) bool {
			return c.Fingerprint != "" && strings.HasPrefix(strings.ToLower(c.Fingerprint), want)
		},
		func(c ipc.ChatEntry) bool { return strings.ToLower(c.ChatID) == want },
	}
	for _, match := range tiers {
		var hits []ipc.ChatEntry
		for _, c := range direct {
			if match(c) {
				hits = append(hits, c)
			}
		}
		switch len(hits) {
		case 0:
			continue
		case 1:
			return hits[0], nil
		default:
			names := make([]string, 0, len(hits))
			for _, h := range hits {
				names = append(names, invitationContactName(h))
			}
			sort.Strings(names)
			return ipc.ChatEntry{}, names
		}
	}
	return ipc.ChatEntry{}, nil
}

func (a *App) cmdInvites() {
	a.showInvitationsModal()
}

const invitesPageName = "invites"

func (a *App) showInvitationsModal() {
	back, _ := a.pages.GetFrontPage()

	table := tview.NewTable().SetSelectable(true, false)
	table.SetBorder(true).SetTitle(" invitations — Enter accepts one offered to you, Esc closes ")

	var shown []ipc.InvitationEntry

	var generation uint64

	closed := false
	dismiss := func() {
		if closed {
			return
		}
		closed = true
		a.winMu.Lock()
		a.invitesRefresh = nil
		a.invitesDismiss = nil
		generation++
		a.winMu.Unlock()
		a.pages.RemovePage(invitesPageName)
		if back != "" {
			a.pages.SwitchToPage(back)
		}
		a.app.SetFocus(a.input)
	}

	table.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyEscape {
			dismiss()
			return nil
		}
		return ev
	})

	grid := tview.NewGrid().
		SetColumns(0, 96, 0).
		SetRows(0, 18, 0).
		AddItem(table, 1, 1, 1, 1, 0, 0, true)

	a.pages.AddPage(invitesPageName, grid, true, true)
	a.app.SetFocus(table)

	fill := func(entries []ipc.InvitationEntry) {

		anchor := ""
		if cur, _ := table.GetSelection(); cur >= 1 && cur-1 < len(shown) {
			anchor = shown[cur-1].MsgID
		}
		shown = entries
		buildInvitationsTable(table, entries, time.Now())

		row := 1
		if len(entries) == 0 {
			row = 0
		}
		for i, e := range entries {
			if anchor != "" && e.MsgID == anchor {
				row = i + 1
				break
			}
		}
		table.Select(row, 0)
	}
	table.SetSelectedFunc(func(row, _ int) {
		idx := row - 1
		if idx < 0 || idx >= len(shown) {
			return
		}
		entry := shown[idx]
		dismiss()
		a.acceptInvitation(entry)
	})

	load := func() {
		a.winMu.Lock()
		if a.invitesRefresh == nil {

			a.winMu.Unlock()
			return
		}
		generation++
		mine := generation
		a.winMu.Unlock()

		a.sendRequest(ipc.FrameInvitationsList, ipc.InvitationsListRequest{}, func(f ipc.Frame) {
			var entries []ipc.InvitationEntry
			refusal := ""
			ok := false
			if f.Type == ipc.FrameInvitationsListed {
				var p ipc.InvitationsListedResponse
				if err := json.Unmarshal(f.Payload, &p); err == nil {
					entries, ok = p.Invitations, true
				}
			} else {

				var p ipc.ErrorPayload
				if err := json.Unmarshal(f.Payload, &p); err == nil {
					refusal = p.Code
				}
			}
			a.app.QueueUpdateDraw(func() {
				a.winMu.Lock()
				stale := mine != generation
				a.winMu.Unlock()
				if stale || !a.pages.HasPage(invitesPageName) {
					return
				}
				if !ok {
					headline, guidance := explainInvitationsListFailure(refusal)
					buildInvitationsFailure(table, headline, guidance)
					shown = nil
					return
				}
				fill(entries)
			})
		})
	}

	a.winMu.Lock()
	a.invitesRefresh = load
	a.invitesDismiss = dismiss
	a.winMu.Unlock()
	load()
}

func (a *App) closeInvitesPage() {
	a.winMu.Lock()
	dismiss := a.invitesDismiss
	a.winMu.Unlock()
	if dismiss != nil {
		dismiss()
	}
}

func buildInvitationsFailure(table *tview.Table, headline string, guidance []string) {
	table.Clear()
	table.SetCell(0, 0, tview.NewTableCell(headline).
		SetTextColor(ColorInvitationBad).SetSelectable(false))
	for i, g := range guidance {
		table.SetCell(i+1, 0, tview.NewTableCell(g).
			SetTextColor(ColorInvitationDim).SetSelectable(false))
	}
}

func buildInvitationsTable(table *tview.Table, entries []ipc.InvitationEntry, now time.Time) {
	table.Clear()
	if len(entries) == 0 {
		for i, line := range []string{
			"No invitations.",
			"When somebody's device cannot reach you, it offers a pairing here.",
			"When you run /re-pair, what you offered is listed here too.",
		} {
			table.SetCell(i, 0, tview.NewTableCell(line).
				SetTextColor(ColorInvitationDim).SetSelectable(false))
		}
		return
	}
	for col, head := range []string{"invitation", "contact", "state", "lapses"} {
		table.SetCell(0, col, tview.NewTableCell(head).
			SetTextColor(ColorInvitationDim).SetSelectable(false).SetExpansion(1))
	}
	for i, e := range entries {
		look := invitationLookFor(e.State, e.ExpiresAt, now)
		lead := "you offered"
		if e.Direction == ipc.InvitationOfferedToMe {
			lead = "they offered"
		}
		name := e.Label
		if name == "" {
			name = "(unnamed contact)"
		}
		state := look.phrase
		if idx := strings.Index(state, " - "); idx >= 0 {
			state = state[:idx]
		}
		if invitationAnswerableHere(e, look) {
			state += " - Enter accepts"
		}
		deadline := "not recorded"
		if e.ExpiresAt > 0 {
			deadline = time.Unix(e.ExpiresAt, 0).Format(invitationDeadlineLayout)
		}
		row := i + 1
		table.SetCell(row, 0, tview.NewTableCell(lead).SetTextColor(look.color).SetExpansion(1))
		table.SetCell(row, 1, tview.NewTableCell(name).SetTextColor(look.color).SetExpansion(1))
		table.SetCell(row, 2, tview.NewTableCell(state).SetTextColor(look.color).SetExpansion(1))
		table.SetCell(row, 3, tview.NewTableCell(deadline).SetTextColor(ColorInvitationDim).SetExpansion(1))
	}
}

func invitationAnswerableHere(e ipc.InvitationEntry, look invitationLook) bool {
	return look.answerable && e.Direction == ipc.InvitationOfferedToMe
}

func (a *App) acceptInvitation(e ipc.InvitationEntry) {
	look := invitationLookFor(e.State, e.ExpiresAt, time.Now())
	name := e.Label
	if name == "" {
		name = "that contact"
	}
	a.switchTo("status")
	if e.Direction == ipc.InvitationOfferedByMe {
		a.log(StyleLinkBad + "that invitation is one you offered" + StyleLinkBody + " — only the person it was offered to can answer it")
		a.log(StyleLinkDim + "it stands until its deadline; you will be told here when they answer" + StyleLinkBody)
		return
	}
	if !look.answerable {
		a.log(StyleLinkBad+"that invitation cannot be accepted"+StyleLinkBody+" — %s", look.phrase)
		a.log(StyleLinkDim+"ask %s to offer another from the device that cannot reach you"+StyleLinkBody, name)
		return
	}

	a.claimInvitationReport(e.MsgID)
	a.log(StyleLinkDim+"accepting %s's invitation — dialling over Tor, this can take a while…"+StyleLinkBody, name)
	a.sendRequest(ipc.FrameInvitationAccept, ipc.InvitationAcceptRequest{MsgID: e.MsgID}, func(f ipc.Frame) {
		if f.Type != ipc.FrameInvitationAccepted {
			a.renderInvitationError(f, explainInvitationAcceptFailure)
			return
		}
		var p ipc.InvitationAcceptedResponse
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			a.app.QueueUpdateDraw(func() {
				a.log(StyleLinkBad+"the pairing completed, but the confirmation was unreadable"+StyleLinkBody+": %v", err)
			})
			return
		}
		a.app.QueueUpdateDraw(func() {
			a.log(StyleLinkOK+"invitation accepted"+StyleLinkBody+" — you can reach %s from this device now", name)
			a.log("  "+StyleLinkDim+"they answer as %s"+StyleLinkBody, p.PeerID)
			a.log("  " + StyleLinkDim + "it joined the conversation you already had with them; there is no second one" + StyleLinkBody)
		})
	})
}

func (a *App) claimInvitationReport(msgID string) {
	if msgID == "" {
		return
	}
	a.winMu.Lock()
	if a.invitesAnsweredHere == nil {
		a.invitesAnsweredHere = map[string]bool{}
	}
	a.invitesAnsweredHere[msgID] = true
	a.winMu.Unlock()
}

func (a *App) routeInvitationRow(ev rawEvent) {
	a.winMu.Lock()
	refresh := a.invitesRefresh
	label := ""
	for _, c := range a.chats {
		if c.ChatID == ev.ChatID {
			label = invitationContactName(c)
			break
		}
	}
	a.winMu.Unlock()
	if refresh != nil {
		refresh()
	}

	var body events.InvitationBody
	if err := json.Unmarshal(ev.Body, &body); err != nil {
		return
	}
	if !events.InvitationTerminal(body.State) {
		return
	}

	a.winMu.Lock()
	answeredHere := a.invitesAnsweredHere[ev.MsgID]
	a.winMu.Unlock()
	if answeredHere {
		return
	}

	named := label != ""
	if !named {
		label = "that contact"
	}
	offeredHere := ev.Direction == "out"
	a.app.QueueUpdateDraw(func() {
		if body.State == events.InvitationAccepted {
			a.log(StyleLinkOK+"invitation accepted"+StyleLinkBody+" — you and %s are paired now", label)
			return
		}
		a.log(StyleLinkBad+"invitation failed"+StyleLinkBody+" — no pairing with %s came of it", label)
		if offeredHere {

			if named {
				a.log("  "+StyleLinkDim+"offer another with /re-pair %s"+StyleLinkBody, label)
			} else {
				a.log("  " + StyleLinkDim + "offer another with /re-pair and the name /chats shows for them" + StyleLinkBody)
			}
			return
		}
		a.log("  " + StyleLinkDim + "ask them to offer another from the device that cannot reach you" + StyleLinkBody)
	})
}

func (a *App) renderInvitationError(f ipc.Frame, explain func(reason string) (string, []string)) {
	var p ipc.ErrorPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		a.app.QueueUpdateDraw(func() {
			a.log(StyleLinkBad+"the invitation did not go through"+StyleLinkBody+" (undecodable): %v", err)
		})
		return
	}
	headline, guidance := explain(p.Code)
	style := refusalStyle(p.Code)
	a.app.QueueUpdateDraw(func() {
		a.log(style+"%s"+StyleLinkBody, headline)
		for _, g := range guidance {
			a.log("  "+StyleLinkDim+"%s"+StyleLinkBody, g)
		}
	})
}

func refusalStyle(code string) string {
	norm := normaliseReason(code)
	if strings.Contains(norm, "rendezvous_unreachable") || strings.Contains(norm, "self_check_failed") {
		return StyleTorSlow
	}
	return StyleLinkBad
}

func explainInvitationOfferFailure(reason string) (string, []string) {
	norm := normaliseReason(reason)
	switch {
	case norm == "":
		return "The invitation was not offered.", []string{unworded(norm)}

	case strings.Contains(norm, "no_answer"):
		return "The invitation had no answer.",
			[]string{
				"The connection to the daemon went down before it replied. The invitation may have gone out.",
				"Open /invitations once the daemon is back, and offer again only if nothing stands for that person.",
			}

	case strings.Contains(norm, "self_check_failed"):
		return torSlowLine,
			[]string{"Nothing new went out."}

	case strings.Contains(norm, "offer_in_progress"):
		return "Another invitation is being offered right now.",
			[]string{"This device offers one invitation at a time, and nothing went out for this one. Run /re-pair again once the other has finished."}

	case strings.Contains(norm, "already_paired"):
		return "This device is already paired with every device of that person it knows of.",
			[]string{"An invitation is for a device of a contact this device has no pairing with; open the conversation and write to them instead."}

	case strings.Contains(norm, "contact_offers"):
		return "That person's device offers this pairing, not this one.",
			[]string{"Nothing went out. Their invitation shows in /invitations when it arrives; accept it there. Press p on the row in the linked device frame to pair anyway."}

	case strings.Contains(norm, "nothing_open"):
		return "An invitation from this device already stands for each device of that person.",
			[]string{"Nothing new went out, and nothing was taken back."}

	case strings.Contains(norm, "invitation_from_contact"):
		return "That person's device has already sent this device an invitation.",
			[]string{"Nothing went out. Accept their invitation in /invitations."}

	case strings.Contains(norm, "unknown_contact"):
		return "No conversation on this device belongs to that person.",
			[]string{"Check /chats for the name this device knows them by."}

	case strings.Contains(norm, "not_inherited"):
		return "That conversation did not come from one of your own devices.",
			[]string{"An invitation travels through a device of yours that still holds a pairing with that person, and this contact reached you some other way."}

	case strings.Contains(norm, "no_sibling"):
		return "This device has no linked device to carry the invitation.",
			[]string{
				"The invitation reaches that person through a device of yours that is still paired with them.",
				"Link this device to another of yours with /link-device, or pair with that person directly using /invite-tor.",
			}

	case strings.Contains(norm, "rail_send_failed"):
		return "None of your devices accepted the invitation to pass on.",
			[]string{"They may be offline. Check /linked-devices, then run /re-pair again once one of them is reachable."}

	case strings.Contains(norm, "tag_failed"), strings.Contains(norm, "key_stash_failed"),
		strings.Contains(norm, "persist_failed"), strings.Contains(norm, "store_read_failed"):
		return "This device could not record the invitation, so it took it back.",
			[]string{"Nothing was left standing. Try /re-pair again; if it keeps failing, this device's storage is the thing to look at."}

	case strings.Contains(norm, "invite_failed"), strings.Contains(norm, "build_failed"):
		return "This device could not mint a pairing to offer.",
			[]string{"Check that Tor is up on the status line, then run /re-pair again."}

	case strings.Contains(norm, "backend_unavailable"), strings.Contains(norm, "not_ready"):
		return "The daemon is not ready to offer an invitation.",
			[]string{"Wait for the status line to show the backend up, then run /re-pair again."}

	case strings.Contains(norm, "bad_request"):
		return "The daemon could not tell which person this was for.",
			[]string{"Run /re-pair again with the name /chats shows for them."}
	}
	return "The invitation was not offered.", []string{unworded(norm)}
}

func explainInvitationAcceptFailure(reason string) (string, []string) {
	norm := normaliseReason(reason)
	switch {
	case norm == "":
		return "The invitation was not accepted.", []string{unworded(norm)}

	case strings.Contains(norm, "no_answer"):
		return "The accept had no answer.",
			[]string{
				"The connection to the daemon went down before it replied. The invitation may already have been answered.",
				"Reopen /invites once the daemon is back; a pairing that landed shows in /contacts instead.",
			}

	case strings.Contains(norm, "unknown_invitation"):
		return "This device holds no invitation under that row.",
			[]string{"Reopen /invites — the list it was chosen from is out of date."}

	case strings.Contains(norm, "invitation_not_live"):
		return "That invitation is no longer the one to answer.",
			[]string{"A newer invitation from the same device retired it, it has already been answered, or an accept of it is still running.",
				"Reopen /invites to see what stands now."}

	case strings.Contains(norm, "invitation_lapsed"):
		return "That invitation ran out before it was accepted.",
			[]string{"Ask them to offer another from the device that cannot reach you."}

	case strings.Contains(norm, "fingerprint_mismatch"):
		return "Whoever answered is not the person that invitation was addressed to.",
			[]string{
				"Nothing was paired and nothing was saved. This is what the check exists for.",
				"Tell them their invitation was answered by somebody else, and have them offer a fresh one.",
			}

	case strings.Contains(norm, "rendezvous_refused"):
		return "The far side refused this device.",
			[]string{"That invitation is finished. Ask them to offer another."}

	case strings.Contains(norm, "rendezvous_unreachable"):
		return torSlowLine,
			[]string{"The invitation stands until its deadline; accept it again from /invites."}

	case strings.Contains(norm, "import_failed"):
		return "The pairing material would not install on this device.",
			[]string{"That invitation is spent. Ask them to offer another."}

	case strings.Contains(norm, "store_read_failed"):
		return "This device could not read the invitation.",
			[]string{"Nothing was dialled. Try again from /invites; if it keeps failing, this device's storage is the thing to look at."}

	case strings.Contains(norm, "bad_payload"):
		return "The rendezvous answered with something this device cannot use.",
			[]string{
				"That invitation is spent — a rendezvous hands its material over once, whatever came of it.",
				"Ask them to offer another from the device that cannot reach you.",
			}

	case strings.Contains(norm, "backend_unavailable"), strings.Contains(norm, "not_ready"),
		strings.Contains(norm, "build_failed"):
		return "The daemon is not ready to answer an invitation.",
			[]string{"Wait for the status line to show the backend up, then try again from /invites."}

	case strings.Contains(norm, "bad_request"):
		return "This device would not dial that invitation.",
			[]string{"It is finished here. Ask them to offer another."}
	}
	return "The invitation was not accepted.", []string{unworded(norm)}
}

func explainInvitationsListFailure(reason string) (string, []string) {
	norm := normaliseReason(reason)
	switch {

	case strings.Contains(norm, "no_answer"):
		return "The invitations could not be read.",
			[]string{
				"The connection to the daemon went down before it replied. This is not an empty list.",
				"Reopen /invites once the status line shows the daemon back.",
			}

	case strings.Contains(norm, "store_read_failed"):
		return "The invitations could not be read.",
			[]string{
				"This is not an empty list — it is a list that failed, and there may be an invitation standing behind it.",
				"Reopen /invites; if it keeps failing, this device's storage is the thing to look at.",
			}

	case strings.Contains(norm, "backend_unavailable"), strings.Contains(norm, "not_ready"):
		return "The daemon is not ready to list invitations.",
			[]string{"Wait for the status line to show the backend up, then reopen /invites."}

	case strings.Contains(norm, "encode_frame"):
		return "The daemon could not put the list into a frame.",
			[]string{"Nothing is wrong with the invitations themselves. Reopen /invites; a build that keeps doing this is the thing to look at."}
	}
	return "The invitations could not be read.", []string{unworded(norm)}
}

func normaliseReason(reason string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(reason)), "-", "_")
}

func unworded(norm string) string {
	if norm == "" {
		return "The daemon gave no reason."
	}
	return fmt.Sprintf("The daemon refused with %q, which this build has no wording for.", norm)
}
