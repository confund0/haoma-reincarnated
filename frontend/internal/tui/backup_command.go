package tui

import (
	"strings"
)

func (a *App) cmdBackup(rest string) {
	if a.BackupCtl == nil {
		a.log("[red]/backup requires the vault flow[white] (run with --cfg-dir, not --addr)")
		return
	}
	dest := strings.TrimSpace(rest)
	a.log("[yellow]backing up[white] — stopping haomad + haoma, archiving cfg-dir…")
	resolved, files, byteCount, err := a.BackupCtl.Backup(dest)
	if err != nil {
		a.log("[red]/backup failed:[white] %v", err)
		a.log("[yellow]daemons are stopped[white] — type /quit and re-launch haoma-text to recover")
		return
	}
	a.log("[green]backup written[white] files=%d bytes=%d", files, byteCount)
	a.log("  [gray]%s[white]", resolved)
	a.log("[yellow]restart haoma-text[white] to continue (this session will exit shortly)")
	go a.app.Stop()
}
