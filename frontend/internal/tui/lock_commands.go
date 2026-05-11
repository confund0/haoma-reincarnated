package tui

import (
	"strconv"
	"strings"

	"haoma-frontend/internal/ipc"
)

func (a *App) cmdChangePass(rest string) {
	if a.VaultCtl == nil {
		a.log("[red]/change-pass requires the vault flow[white] (run with --cfg-dir, not --addr)")
		return
	}
	parts := strings.Fields(rest)
	if len(parts) != 2 {
		a.log("[yellow]usage:[white] /change-pass <current-passphrase> <new-passphrase>")
		a.log("  [gray]passphrases must not contain spaces in v0[white]")
		return
	}
	if err := a.VaultCtl.ChangePassphrase(parts[0], parts[1]); err != nil {
		a.log("[red]/change-pass failed:[white] %v", err)
		return
	}
	a.log("[green]vault re-sealed with new passphrase[white] — keep the new one safe; there is no recovery")
}

func (a *App) cmdChangePIN(rest string) {
	if a.VaultCtl == nil {
		a.log("[red]/change-pin requires the vault flow[white] (run with --cfg-dir)")
		return
	}
	parts := strings.Fields(rest)
	if len(parts) != 2 {
		a.log("[yellow]usage:[white] /change-pin <current-pin> <new-pin>")
		a.log("  [gray]if the current PIN is the insecure default (0000), <current-pin> is not validated[white]")
		return
	}
	if err := a.VaultCtl.ChangePIN(parts[0], parts[1]); err != nil {
		a.log("[red]/change-pin failed:[white] %v", err)
		return
	}
	a.log("[green]PIN updated[white] — soft-lock will accept the new value on next idle")
}

func (a *App) cmdSetIdleAction(rest string) {
	if a.VaultCtl == nil {
		a.log("[red]/set-idle-action requires the vault flow[white]")
		return
	}
	v := strings.TrimSpace(rest)
	if v == "" {
		a.log("[yellow]usage:[white] /set-idle-action <soft-lock|safe-lock>")
		return
	}
	if err := a.VaultCtl.SetIdleAction(v); err != nil {
		a.log("[red]/set-idle-action failed:[white] %v", err)
		return
	}
	a.log("[green]idle action[white] = %q (takes effect on next idle window)", v)
}

func (a *App) cmdSetIdleTimeout(rest string) {
	if a.VaultCtl == nil {
		a.log("[red]/set-idle-timeout requires the vault flow[white]")
		return
	}
	n, err := strconv.Atoi(strings.TrimSpace(rest))
	if err != nil {
		a.log("[yellow]usage:[white] /set-idle-timeout <seconds>")
		return
	}
	if err := a.VaultCtl.SetIdleTimeoutSeconds(n); err != nil {
		a.log("[red]/set-idle-timeout failed:[white] %v", err)
		return
	}
	a.log("[green]idle timeout[white] = %ds (live; next watcher tick picks it up)", n)
}

func (a *App) cmdSetPinValidity(rest string) {
	if a.VaultCtl == nil {
		a.log("[red]/set-pin-validity requires the vault flow[white]")
		return
	}
	n, err := strconv.Atoi(strings.TrimSpace(rest))
	if err != nil {
		a.log("[yellow]usage:[white] /set-pin-validity <seconds>  (0 = PIN valid until process restart)")
		return
	}
	if err := a.VaultCtl.SetPinValiditySec(n); err != nil {
		a.log("[red]/set-pin-validity failed:[white] %v", err)
		return
	}
	if n == 0 {
		a.log("[green]pin validity[white] = 0 (no escalation; PIN valid until process restart)")
	} else {
		a.log("[green]pin validity[white] = %ds (soft-lock escalates to hard-lock after this window)", n)
	}
}

func (a *App) cmdSetTorPassword(rest string) {
	if a.VaultCtl == nil {
		a.log("[red]/set-tor-password requires the vault flow[white]")
		return
	}
	v := strings.TrimSpace(rest)
	if v == "" {
		a.log("[yellow]usage:[white] /set-tor-password <password>  (cleartext value tor expects on AUTHENTICATE)")
		return
	}
	if err := a.VaultCtl.SetTorPassword(v); err != nil {
		a.log("[red]/set-tor-password failed:[white] %v", err)
		return
	}
	a.winMu.Lock()
	haomaUp := a.haomaConnected
	a.winMu.Unlock()
	if !haomaUp {
		a.log("[green]vault re-sealed[white] — haoma offline; live update skipped, next haomad start picks up the new password")
		return
	}
	a.log("[green]vault re-sealed[white] — propagating to haomad…")
	a.sendRequest(ipc.FrameSetTorPassword, ipc.SetTorPasswordRequest{Password: v}, func(f ipc.Frame) {
		if f.Type == ipc.FrameError {
			a.renderError(f)
			a.log("[yellow]vault re-sealed but live update failed[white] — next haomad restart will pick up the new password")
			return
		}
		if f.Type != ipc.FrameTorPasswordAccepted {
			a.log("[red]/set-tor-password[white] unexpected response type: %s", f.Type)
			return
		}
		a.log("[green]tor password applied live[white] — watchdog reconnecting; tor chip will flip within ~10s")
	})
}
