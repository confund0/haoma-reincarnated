package tui

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/rivo/tview"

	"haoma-frontend/internal/ipc"
)

func newStatusView() *tview.TextView {
	v := tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true)
	v.SetBorder(true).SetTitle(" status ")
	return v
}

func appendStatus(v *tview.TextView, format string, args ...any) {
	stamp := time.Now().Format("15:04:05")
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(v, "[gray]%s[white] %s\n", stamp, msg)
	v.ScrollToEnd()
}

func renderToStatus(v *tview.TextView, f ipc.Frame) {
	switch f.Type {
	case ipc.FrameStatusEvent:
		var p ipc.StatusEventPayload
		_ = json.Unmarshal(f.Payload, &p)
		var shim struct {
			Kind       string `json:"kind"`
			PeerID     string `json:"peer_id"`
			SourceAddr string `json:"source_addr"`
			SlotIdx    int    `json:"slot_idx"`
		}
		_ = json.Unmarshal(p.Event, &shim)
		appendStatus(v, "[yellow]ids[white] %s slot=%d peer=%s source=%s",
			shim.Kind, shim.SlotIdx, shim.PeerID, shim.SourceAddr)
	case ipc.FrameDeliveryStatus:
		var p ipc.DeliveryStatusPayload
		_ = json.Unmarshal(f.Payload, &p)
		color := "[green]"
		if p.State == "failed" {
			color = "[red]"
		}
		if p.LastError != "" {
			appendStatus(v, "%s%s[white] env=%s err=%s", color, p.State, shortID(p.EnvelopeID), p.LastError)
		} else {
			appendStatus(v, "%s%s[white] env=%s", color, p.State, shortID(p.EnvelopeID))
		}
	case ipc.FrameWelcome:
		var p ipc.WelcomePayload
		_ = json.Unmarshal(f.Payload, &p)
		appendStatus(v, "[green]connected[white] — daemon %s (protocol v%d)", p.DaemonVersion, p.ProtocolVersion)
	case ipc.FrameError:
		var p ipc.ErrorPayload
		_ = json.Unmarshal(f.Payload, &p)
		appendStatus(v, "[red]error[white] %s: %s", p.Code, p.Message)
	default:
		appendStatus(v, "frame: %s", f.Type)
	}
}
