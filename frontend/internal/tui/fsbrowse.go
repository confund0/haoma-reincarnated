package tui

import (
	"os"
	"strings"

	"haoma-frontend/internal/tui/haomafiledialog"
)

func (a *App) cmdFsBrowse(rest string) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "/"
	}

	mode := haomafiledialog.ModeDirSelect
	label := "directory"
	if strings.HasPrefix(strings.TrimSpace(rest), "file") {
		mode = haomafiledialog.ModeFileSelect
		label = "file"
	}

	front, _ := a.pages.GetFrontPage()

	restore := func() {
		if front != "" {
			a.pages.SwitchToPage(front)
		}
		a.app.SetFocus(a.input)
	}

	dlg := haomafiledialog.New(haomafiledialog.Options{
		Mode:     mode,
		StartDir: home,
		Title:    "fsbrowse — pick a " + label,
		OnPick: func(path string) {
			restore()
			a.log("[green]/fsbrowse[white] picked %s: %s", label, path)
		},
		OnCancel: func() {
			restore()
			a.log("[yellow]/fsbrowse[white] cancelled")
		},
	})
	dlg.Show(a.app, a.pages, "fsbrowse")
}
