package tui

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type bracketCheckbox struct {
	*tview.Box
	label    string
	checked  bool
	onChange func(bool)
	finished func(tcell.Key)

	labelWidth   int
	labelStyle   tcell.Style
	fieldStyle   tcell.Style
	focusedStyle tcell.Style
	disabled     bool
}

var _ tview.FormItem = (*bracketCheckbox)(nil)

func newBracketCheckbox(label string, checked bool, onChange func(bool)) *bracketCheckbox {
	return &bracketCheckbox{
		Box:          tview.NewBox(),
		label:        label,
		checked:      checked,
		onChange:     onChange,
		labelStyle:   tcell.StyleDefault,
		fieldStyle:   tcell.StyleDefault,
		focusedStyle: tcell.StyleDefault.Reverse(true),
	}
}

func (c *bracketCheckbox) GetLabel() string { return c.label }

func (c *bracketCheckbox) SetFormAttributes(labelWidth int, labelColor, bgColor, fieldColor, fieldBgColor tcell.Color) tview.FormItem {
	c.labelWidth = labelWidth
	c.labelStyle = tcell.StyleDefault.Foreground(labelColor).Background(bgColor)
	c.fieldStyle = tcell.StyleDefault.Foreground(fieldColor).Background(fieldBgColor)
	c.focusedStyle = tcell.StyleDefault.Foreground(fieldBgColor).Background(fieldColor)
	c.SetBackgroundColor(bgColor)
	return c
}

func (c *bracketCheckbox) GetFieldWidth() int { return 3 }

func (c *bracketCheckbox) GetFieldHeight() int { return 1 }

func (c *bracketCheckbox) SetFinishedFunc(f func(key tcell.Key)) tview.FormItem {
	c.finished = f
	return c
}

func (c *bracketCheckbox) SetDisabled(disabled bool) tview.FormItem {
	c.disabled = disabled
	return c
}

func (c *bracketCheckbox) Checked() bool { return c.checked }

func (c *bracketCheckbox) Draw(screen tcell.Screen) {
	c.Box.DrawForSubclass(screen, c)
	x, y, width, height := c.GetInnerRect()
	if height < 1 || width <= 0 {
		return
	}

	fieldStyle := c.fieldStyle
	if c.HasFocus() {
		fieldStyle = c.focusedStyle
	}

	mark := ' '
	if c.checked {
		mark = 'X'
	}

	if x+0 < x+width {
		screen.SetContent(x+0, y, '[', nil, fieldStyle)
	}
	if x+1 < x+width {
		screen.SetContent(x+1, y, mark, nil, fieldStyle)
	}
	if x+2 < x+width {
		screen.SetContent(x+2, y, ']', nil, fieldStyle)
	}

	lx := x + 4
	for _, r := range c.label {
		if lx >= x+width {
			break
		}
		screen.SetContent(lx, y, r, nil, c.labelStyle)
		lx++
	}
}

func (c *bracketCheckbox) InputHandler() func(event *tcell.EventKey, setFocus func(p tview.Primitive)) {
	return c.WrapInputHandler(func(ev *tcell.EventKey, setFocus func(p tview.Primitive)) {
		switch ev.Key() {
		case tcell.KeyRune:
			switch ev.Rune() {
			case ' ', 'x', 'X':
				c.toggle()
			}
		case tcell.KeyEnter:
			c.toggle()
		case tcell.KeyTab, tcell.KeyBacktab, tcell.KeyEscape:
			if c.finished != nil {
				c.finished(ev.Key())
			}
		}
	})
}

func (c *bracketCheckbox) MouseHandler() func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(p tview.Primitive)) (consumed bool, capture tview.Primitive) {
	return c.WrapMouseHandler(func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(p tview.Primitive)) (consumed bool, capture tview.Primitive) {
		x, y := event.Position()
		if !c.InRect(x, y) {
			return false, nil
		}
		if action == tview.MouseLeftClick {
			setFocus(c)
			c.toggle()
			return true, nil
		}
		return false, nil
	})
}

func (c *bracketCheckbox) toggle() {
	c.checked = !c.checked
	if c.onChange != nil {
		c.onChange(c.checked)
	}
}
