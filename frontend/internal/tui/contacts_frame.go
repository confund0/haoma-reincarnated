package tui

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

const inheritedFrameWidth = 31

const inheritedFramePrefix = 2

const inheritedFrameEmpty = "synchronized"

const (
	inheritedFrameAll    = "pair all"
	inheritedFrameAllRow = "[ " + inheritedFrameAll + " ]"
)

type inheritedRow struct {
	fingerprint string

	state string

	all bool
}

func buildInheritedFrame(contacts []inheritedFrameEntry, heading string) (tview.Primitive, *tview.Table, []inheritedRow) {
	dimmed := tcell.StyleDefault.Foreground(ColorContactsSection)

	if len(contacts) == 0 {
		readout := tview.NewTextView().
			SetText("\n" + inheritedFrameEmpty).
			SetTextAlign(tview.AlignCenter).
			SetTextColor(ColorContactsSection)
		readout.SetBorder(true).SetTitle(" " + heading + " ")
		return readout, nil, nil
	}

	t := tview.NewTable().
		SetBorders(false).
		SetSelectable(true, false)
	t.SetBorder(true).SetTitle(" " + heading + " ")

	var rows []inheritedRow
	line := 0

	put := func(marked bool, text string, style tcell.Style, align int, selectable bool) {
		prefix := "  "
		if marked {
			prefix = GlyphContactMarked + " "
		}
		t.SetCell(line, 0, tview.NewTableCell(prefix).
			SetMaxWidth(inheritedFramePrefix).
			SetStyle(tcell.StyleDefault.Foreground(ColorContactsMarked)).
			SetSelectable(selectable))
		t.SetCell(line, 1, tview.NewTableCell(text).
			SetExpansion(1).
			SetAlign(align).
			SetStyle(style).
			SetSelectable(selectable))
		line++
	}

	put(false, "", tcell.StyleDefault, tview.AlignLeft, false)
	rows = append(rows, inheritedRow{})

	firstName := line

	for _, c := range contacts {
		name := c.label
		if name == "" {
			name = shortID(c.fingerprint)
		}
		put(c.marked, name, tcell.StyleDefault.Foreground(ColorContactsName), tview.AlignLeft, true)
		rows = append(rows, inheritedRow{fingerprint: c.fingerprint})

		if colour, running := pairStateColour(c.state); running {

			for _, sentence := range pairStateLines(c.state) {
				put(false, " "+sentence, tcell.StyleDefault.Foreground(colour), tview.AlignLeft, false)
				rows = append(rows, inheritedRow{fingerprint: c.fingerprint, state: c.state})
			}
			continue
		}

		if info := inheritedRowInfo(c, name); info != "" {
			for _, line := range wrapFrameLine(info, inheritedFrameTextWidth) {
				put(false, " "+line, dimmed, tview.AlignLeft, false)
				rows = append(rows, inheritedRow{fingerprint: c.fingerprint, state: info})
			}
		}
	}

	put(false, "", tcell.StyleDefault, tview.AlignLeft, false)
	rows = append(rows, inheritedRow{})
	put(false, centreInFrame(inheritedFrameAllRow), dimmed, tview.AlignLeft, true)
	rows = append(rows, inheritedRow{all: true})

	t.Select(firstName, 0)
	paintInheritedCursor(t, rows)
	t.SetSelectionChangedFunc(func(int, int) { paintInheritedCursor(t, rows) })

	return t, t, rows
}

const inheritedFrameTextWidth = inheritedFrameWidth - 2 - inheritedFramePrefix - 1 - 1

func inheritedRowInfo(c inheritedFrameEntry, name string) string {
	if c.invited {
		return inheritedInvitedLine(name)
	}
	if c.waiting {
		return inheritedWaitingLine(name)
	}
	if c.paired > 0 && c.devices > 0 {
		return inheritedDevicesLine(c.paired, c.devices)
	}
	return ""
}

func inheritedWaitingLine(name string) string {
	return "waiting for " + name + "'s device to offer"
}

func inheritedInvitedLine(name string) string {
	return "accept " + name + "'s invitation"
}

func inheritedDevicesLine(paired, devices int) string {
	return fmt.Sprintf("%d of %d devices", paired, devices)
}

func wrapFrameLine(text string, width int) []string {
	if width <= 0 {
		return []string{text}
	}
	var out []string
	line := ""
	for _, w := range strings.Fields(text) {
		for len([]rune(w)) > width {
			if line != "" {
				out = append(out, line)
				line = ""
			}
			r := []rune(w)
			out = append(out, string(r[:width]))
			w = string(r[width:])
		}
		switch {
		case line == "":
			line = w
		case len([]rune(line))+1+len([]rune(w)) <= width:
			line += " " + w
		default:
			out = append(out, line)
			line = w
		}
	}
	if line != "" {
		out = append(out, line)
	}
	return out
}

func pairStateLines(state string) []string {
	var out []string
	for _, s := range strings.SplitAfter(state, ". ") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func centreInFrame(text string) string {
	const inner = inheritedFrameWidth - 2
	const bodyStart = inheritedFramePrefix + 1
	pad := (inner-len([]rune(text)))/2 - bodyStart
	if pad < 0 {
		pad = 0
	}
	return strings.Repeat(" ", pad) + text
}

func paintInheritedCursor(t *tview.Table, rows []inheritedRow) {

	focused := t.HasFocus()
	if focused {
		t.SetSelectedStyle(tcell.StyleDefault.Foreground(ColorContactsCursor))
	} else {
		t.SetSelectedStyle(tcell.StyleDefault.Foreground(ColorContactsName))
	}

	selected, _ := t.GetSelection()
	if !focused {
		selected = -1
	}
	for i, r := range rows {
		prefix := t.GetCell(i, 0)
		body := t.GetCell(i, 1)
		if prefix == nil || body == nil {
			continue
		}
		marked := strings.HasPrefix(prefix.Text, GlyphContactMarked)
		cursor := " "
		if i == selected {
			cursor = GlyphContactCursor
		}
		mark := " "
		if marked {
			mark = GlyphContactMarked
		}

		prefix.SetText(mark + cursor)

		if (r.fingerprint == "" && !r.all) || r.state != "" {
			continue
		}
		colour := ColorContactsName
		if r.all {
			colour = ColorContactsSection
		}
		if i == selected {
			colour = ColorContactsCursor
		}
		body.SetStyle(tcell.StyleDefault.Foreground(colour))
	}
}

type inheritedFrameEntry struct {
	fingerprint string
	label       string
	marked      bool
	state       string

	devices, paired int
	waiting         bool

	invited bool
}

func (a *App) inheritedFrameEntries() []inheritedFrameEntry {
	a.winMu.Lock()
	defer a.winMu.Unlock()
	out := make([]inheritedFrameEntry, 0, len(a.inherited))
	for i := range a.inherited {
		c := &a.inherited[i]
		out = append(out, inheritedFrameEntry{
			fingerprint: c.Fingerprint,
			label:       c.Label,
			marked:      a.contactMarked[c.Fingerprint],
			state:       a.pairState[c.Fingerprint],
			devices:     c.Devices,
			paired:      c.Paired,
			waiting:     c.Waiting,
			invited:     c.Invited,
		})
	}
	return out
}

func (a *App) installInheritedFrameKeys(tbl *tview.Table) {
	pick := func() (inheritedRow, bool) {
		row, _ := tbl.GetSelection()
		a.winMu.Lock()
		defer a.winMu.Unlock()
		if row < 0 || row >= len(a.inheritedRows) {
			return inheritedRow{}, false
		}
		return a.inheritedRows[row], true
	}
	tbl.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Key() {
		case tcell.KeyEscape:
			a.escPending = true
			return nil
		case tcell.KeyTab, tcell.KeyBacktab:
			a.focusContactsTable()
			return nil
		case tcell.KeyEnter:
			r, ok := pick()
			if !ok {
				return nil
			}
			if r.all {
				a.startPairingRunForAll()
				return nil
			}
			if r.fingerprint != "" {
				a.showInheritedContactDetails(r.fingerprint)
			}
			return nil
		case tcell.KeyRune:
			switch ev.Rune() {
			case ' ':
				if r, ok := pick(); ok && r.fingerprint != "" {
					a.toggleContactMark(r.fingerprint)
				}
				return nil
			case 'p':
				r, ok := pick()
				if ok && r.all {
					a.startPairingRunForAll()
					return nil
				}
				under := ""
				if ok {
					under = r.fingerprint
				}
				a.startPairingRun(under)
				return nil
			case 'v':
				if r, ok := pick(); ok && r.fingerprint != "" {
					a.showInheritedContactDetails(r.fingerprint)
				}
				return nil
			case 'x':
				a.stopPairingRun()
				return nil
			}
		}
		return ev
	})
}

func (a *App) startPairingRunForAll() {
	a.winMu.Lock()
	var want []string
	standing := 0
	for i := range a.inherited {
		fp := a.inherited[i].Fingerprint
		if a.inherited[i].Waiting || a.inherited[i].Invited {

			continue
		}
		switch a.pairState[fp] {
		case pairStateWaiting, pairStateOffering, pairStateOffered, pairStateUnknown:
			standing++
			continue
		}
		want = append(want, fp)
	}
	a.winMu.Unlock()

	if len(want) == 0 {
		if standing > 0 {
			a.log(StyleLinkDim+"every contact here is already in the run"+StyleLinkBody+" — %d of them", standing)
		} else {
			a.log(StyleLinkDim + "there is nobody here to pair with" + StyleLinkBody)
		}
		return
	}
	if standing > 0 {
		a.log(StyleLinkDim+"leaving %d contact(s) alone"+StyleLinkBody+" — an invitation is already out for them, and a second one would retire it", standing)
	}
	a.confirmPairAll(want)
}

func (a *App) confirmPairAll(want []string) {
	const pageName = "pair-all-confirm"

	dismiss := func() {
		a.pages.RemovePage(pageName)
		a.switchTo("contacts")
		a.focusInheritedFrame()
	}

	form := tview.NewForm()
	form.SetButtonsAlign(tview.AlignCenter)
	form.AddTextView("Contacts", fmt.Sprintf("%d", len(want)), 0, 1, false, false)
	form.AddTextView("What happens", "one invitation for each of their devices this device is missing, one at a time. Each one mints an onion.", 0, 2, false, false)
	form.AddButton("Pair all", func() {
		dismiss()
		a.queuePairings(want, false)
	})
	form.AddButton("Cancel", dismiss)
	form.SetBorder(true).SetTitle(" " + inheritedFrameAll + " ")

	const modalWidth = 96
	grid := tview.NewGrid().
		SetColumns(0, modalWidth, 0).
		SetRows(0, 11, 0).
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

func (a *App) stopPairingRun() {
	a.winMu.Lock()
	dropped := len(a.pairQueue)
	for _, fp := range a.pairQueue {

		if a.pairState[fp] == pairStateWaiting {
			delete(a.pairState, fp)
		}

		delete(a.pairOffered, fp)
		delete(a.pairOpenOnly, fp)
	}
	a.pairQueue = nil
	a.winMu.Unlock()

	if dropped == 0 {
		a.log(StyleLinkDim + "no contact is waiting to be offered" + StyleLinkBody)
		return
	}
	slog.Debug("contacts: the pairing run was stopped",
		slog.Int("dropped", dropped),
	)
	a.log(StyleLinkDim+"pairing stopped"+StyleLinkBody+" — %d contact(s) dropped from the run. Any invitation already sent still stands.", dropped)
	a.rebuildContactsPage()
}
