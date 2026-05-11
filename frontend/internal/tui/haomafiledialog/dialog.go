package haomafiledialog

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type Mode int

const (
	ModeDirSelect Mode = iota

	ModeFileSelect
)

type Options struct {
	Mode     Mode
	StartDir string
	Title    string
	FS       FS

	OnPick func(path string)

	OnCancel func()

	PageWidth  int
	PageHeight int
}

type Dialog struct {
	model *Model
	mode  Mode
	title string

	onPick   func(path string)
	onCancel func()

	pathBar   *tview.TextView
	filter    *tview.InputField
	hiddenChk *tview.Checkbox
	table     *tview.Table
	saveBtn   *tview.Button
	cancelBtn *tview.Button

	root *tview.Grid

	pageW, pageH int

	pendingSelectName string

	app   *tview.Application
	pages *tview.Pages
	page  string

	dismissed bool
}

func New(opts Options) *Dialog {
	if opts.StartDir == "" {
		opts.StartDir = "/"
	}
	if opts.Title == "" {
		switch opts.Mode {
		case ModeFileSelect:
			opts.Title = "select file"
		default:
			opts.Title = "select directory"
		}
	}
	if opts.PageWidth == 0 {
		opts.PageWidth = 90
	}
	if opts.PageHeight == 0 {
		opts.PageHeight = 24
	}
	d := &Dialog{
		model:    NewModel(opts.FS, opts.StartDir),
		mode:     opts.Mode,
		title:    opts.Title,
		onPick:   opts.OnPick,
		onCancel: opts.OnCancel,
		pageW:    opts.PageWidth,
		pageH:    opts.PageHeight,
	}
	d.build()
	return d
}

func (d *Dialog) Model() *Model { return d.model }

func (d *Dialog) Show(app *tview.Application, pages *tview.Pages, pageName string) {
	if app == nil || pages == nil {
		return
	}
	d.app = app
	d.pages = pages
	d.page = pageName
	pages.AddPage(pageName, d.root, true, true)
	app.SetFocus(d.filter)
}

func (d *Dialog) dismiss() {
	if d.dismissed || d.pages == nil {
		return
	}
	d.dismissed = true
	d.pages.RemovePage(d.page)
}

func (d *Dialog) build() {
	d.pathBar = tview.NewTextView().
		SetDynamicColors(true).
		SetRegions(false).
		SetWrap(false)

	d.filter = tview.NewInputField().
		SetLabel("filter ").
		SetFieldWidth(0)
	d.filter.SetChangedFunc(func(s string) {
		d.model.SetFilter(strings.TrimSpace(s))
		d.refillTable()
	})

	d.hiddenChk = tview.NewCheckbox().
		SetLabel("show hidden ").
		SetCheckedString("[X]").
		SetUncheckedString("[ ]")
	d.hiddenChk.SetChangedFunc(func(checked bool) {
		d.model.SetShowHidden(checked)
		d.refillTable()
	})

	d.table = tview.NewTable().
		SetBorders(false).
		SetSelectable(true, false).
		SetFixed(1, 0)

	d.cancelBtn = tview.NewButton("Cancel").SetSelectedFunc(func() {
		d.dismiss()
		if d.onCancel != nil {
			d.onCancel()
		}
	})

	if d.mode == ModeDirSelect {
		d.saveBtn = tview.NewButton("Save here").SetSelectedFunc(func() {
			path := d.model.CurrentDir()
			d.dismiss()
			if d.onPick != nil {
				d.onPick(path)
			}
		})
	}

	d.installInputCaptures()
	d.refillTable()

	controlsRow := tview.NewFlex().SetDirection(tview.FlexColumn).
		AddItem(d.filter, 0, 4, true).
		AddItem(d.hiddenChk, 16, 0, false)
	controlsBox := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(controlsRow, 1, 0, true)
	controlsBox.SetBorder(true).SetTitle(" filter ")

	entriesBox := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(d.table, 0, 1, true)
	entriesBox.SetBorder(true).SetTitle(" entries ")

	buttonBar := tview.NewFlex().SetDirection(tview.FlexColumn)
	if d.saveBtn != nil {
		buttonBar.AddItem(nil, 0, 1, false)
		buttonBar.AddItem(d.saveBtn, 12, 0, false)
		buttonBar.AddItem(nil, 2, 0, false)
		buttonBar.AddItem(d.cancelBtn, 10, 0, false)
		buttonBar.AddItem(nil, 0, 1, false)
	} else {
		buttonBar.AddItem(nil, 0, 1, false)
		buttonBar.AddItem(d.cancelBtn, 10, 0, false)
		buttonBar.AddItem(nil, 0, 1, false)
	}

	body := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(d.pathBar, 1, 0, false).
		AddItem(controlsBox, 3, 0, true).
		AddItem(entriesBox, 0, 1, false).
		AddItem(buttonBar, 1, 0, false)
	body.SetBorder(true).SetTitle(" " + d.title + " ")

	d.root = tview.NewGrid().
		SetColumns(0, d.pageW, 0).
		SetRows(0, d.pageH, 0).
		AddItem(body, 1, 1, 1, 1, 0, 0, true)
	d.root.SetInputCapture(d.gridInputCapture())
}

func (d *Dialog) installInputCaptures() {
	d.filter.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Key() {
		case tcell.KeyUp, tcell.KeyDown, tcell.KeyPgUp, tcell.KeyPgDn, tcell.KeyHome, tcell.KeyEnd:

			if h := d.table.InputHandler(); h != nil {
				h(ev, func(p tview.Primitive) {})
			}
			return nil
		case tcell.KeyEnter:
			row, _ := d.table.GetSelection()
			d.activateRow(row)
			return nil
		}
		return ev
	})

	d.table.SetSelectedFunc(func(row, _ int) { d.activateRow(row) })
}

func (d *Dialog) activateRow(row int) {
	entries := d.model.Entries()
	if row < 1 || row-1 >= len(entries) {
		return
	}
	e := entries[row-1]
	if e.IsDir {
		if e.Name == ".." {

			d.pendingSelectName = filepath.Base(d.model.CurrentDir())
		}
		d.model.NavigateInto(e.Name)
		d.refillTable()
		return
	}
	if d.mode == ModeFileSelect {
		path := d.model.ResolvePath(e.Name)
		d.dismiss()
		if d.onPick != nil {
			d.onPick(path)
		}
	}

}

func (d *Dialog) gridInputCapture() func(ev *tcell.EventKey) *tcell.EventKey {
	return func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Key() {
		case tcell.KeyEscape:
			if d.app != nil && d.app.GetFocus() == d.filter && d.filter.GetText() != "" {

				d.filter.SetText("")
				return nil
			}
			d.dismiss()
			if d.onCancel != nil {
				d.onCancel()
			}
			return nil
		case tcell.KeyTab, tcell.KeyBacktab:
			order := d.focusOrder()
			if d.app == nil || len(order) == 0 {
				return ev
			}
			focus := d.app.GetFocus()
			idx := -1
			for i, p := range order {
				if p == focus {
					idx = i
					break
				}
			}
			if idx < 0 {
				return ev
			}
			if ev.Key() == tcell.KeyTab {
				idx = (idx + 1) % len(order)
			} else {
				idx = (idx - 1 + len(order)) % len(order)
			}
			d.app.SetFocus(order[idx])
			return nil
		}
		return ev
	}
}

func (d *Dialog) focusOrder() []tview.Primitive {
	out := []tview.Primitive{d.filter, d.hiddenChk}
	if d.saveBtn != nil {
		out = append(out, d.saveBtn)
	}
	out = append(out, d.cancelBtn)
	return out
}

func (d *Dialog) refillTable() {
	d.pathBar.Clear()
	dir := d.model.CurrentDir()
	if err := d.model.Err(); err != nil {
		fmt.Fprintf(d.pathBar, "[red]%s[white]  (%v)", dir, err)
	} else {
		fmt.Fprintf(d.pathBar, "%s", dir)
	}

	d.table.Clear()

	headerStyle := tcell.StyleDefault.Bold(true)
	for col, label := range []string{"Name", "Size", "Modified"} {
		cell := tview.NewTableCell(label).SetStyle(headerStyle)
		if col == 0 {
			cell.SetExpansion(5)
		} else {
			cell.SetExpansion(1)
		}
		d.table.SetCell(0, col, cell)
	}

	entries := d.model.Entries()
	for r, e := range entries {
		nameCell := tview.NewTableCell(displayName(e)).SetExpansion(5)
		sizeCell := tview.NewTableCell(displaySize(e)).SetExpansion(1)
		modCell := tview.NewTableCell(displayModTime(e)).SetExpansion(1)
		if e.IsDir {
			nameCell.SetTextColor(tcell.ColorAqua)
		}
		d.table.SetCell(r+1, 0, nameCell)
		d.table.SetCell(r+1, 1, sizeCell)
		d.table.SetCell(r+1, 2, modCell)
	}

	if len(entries) == 0 {
		d.pendingSelectName = ""
		return
	}

	selectRow := 1
	if d.pendingSelectName != "" {
		for i, e := range entries {
			if e.Name == d.pendingSelectName {
				selectRow = i + 1
				break
			}
		}
		d.pendingSelectName = ""
	}
	d.table.Select(selectRow, 0)
}

func displayName(e Entry) string {
	if e.Name == ".." {
		return ".."
	}
	if e.IsDir {
		return e.Name + "/"
	}
	return e.Name
}

func displaySize(e Entry) string {
	if e.IsDir {
		return "—"
	}
	const (
		kib = 1024
		mib = 1024 * kib
		gib = 1024 * mib
	)
	switch {
	case e.Size >= gib:
		return fmt.Sprintf("%.1f GiB", float64(e.Size)/gib)
	case e.Size >= mib:
		return fmt.Sprintf("%.1f MiB", float64(e.Size)/mib)
	case e.Size >= kib:
		return fmt.Sprintf("%.1f KiB", float64(e.Size)/kib)
	default:
		return fmt.Sprintf("%d B", e.Size)
	}
}

func displayModTime(e Entry) string {
	if e.ModTime.IsZero() {
		return "—"
	}
	return e.ModTime.Format("2006-01-02 15:04")
}
