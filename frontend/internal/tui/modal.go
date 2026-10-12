package tui

import (
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func (a *App) twoSectionModal(title string, top, bottom *tview.Form, topRows, bottomRows int, onCancel func()) tview.Primitive {
	return a.twoSectionModalWithHeader(title, nil, 0, top, bottom, topRows, bottomRows, onCancel)
}

func (a *App) twoSectionModalWithHeader(title string, header *tview.TextView, headerRows int, top, bottom *tview.Form, topRows, bottomRows int, onCancel func()) tview.Primitive {
	sep := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter).
		SetText("[gray]" + strings.Repeat("─", 56) + "[white]")

	grid := tview.NewGrid().SetColumns(0)
	row := 0
	if header != nil && headerRows > 0 {
		grid.SetRows(headerRows, topRows, 1, bottomRows)
		grid.AddItem(header, 0, 0, 1, 1, 0, 0, false)
		row = 1
	} else {
		grid.SetRows(topRows, 1, bottomRows)
	}
	grid.AddItem(top, row, 0, 1, 1, 0, 0, true).
		AddItem(sep, row+1, 0, 1, 1, 0, 0, false).
		AddItem(bottom, row+2, 0, 1, 1, 0, 0, false)
	grid.SetBorder(true).SetTitle(" " + title + " ")

	order := func() []tview.Primitive {
		out := make([]tview.Primitive, 0, 8)
		for i := 0; i < top.GetFormItemCount(); i++ {
			out = append(out, top.GetFormItem(i))
		}
		for i := 0; i < top.GetButtonCount(); i++ {
			out = append(out, top.GetButton(i))
		}
		for i := 0; i < bottom.GetFormItemCount(); i++ {
			out = append(out, bottom.GetFormItem(i))
		}
		for i := 0; i < bottom.GetButtonCount(); i++ {
			out = append(out, bottom.GetButton(i))
		}
		return out
	}()

	indexOf := func(p tview.Primitive) int {
		for i, x := range order {
			if x == p {
				return i
			}
		}
		return -1
	}

	grid.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Key() {
		case tcell.KeyEscape:
			if onCancel != nil {
				onCancel()
			}
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

	return grid
}
