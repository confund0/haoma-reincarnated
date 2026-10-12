//go:build windows

package tui

import "haoma-frontend/internal/ipc"

const videoCallsSupported = false

func (a *App) routeVideoRawTransport(_ ipc.Frame) {
	a.log("[red]vid sink[white] " + videoUnavailableMessage)
}
