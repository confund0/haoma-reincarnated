package tui_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gdamore/tcell/v2"

	"haoma-frontend/internal/ipc"
)

const downgradeEnvID = "env-downgrade-1"

func deliveryDowngradeScript(release <-chan struct{}) func(context.Context, *ipc.Session) {
	answer := rowScript(map[string]any{
		"kind":        "file",
		"body":        map[string]any{"name": "marker under test", "size": 1024, "state": "ready"},
		"envelope_id": downgradeEnvID,
	}, "delivered", false)

	fence, _ := json.Marshal(map[string]any{
		"recv_seq":   uint64(2),
		"chat_id":    simChatID,
		"direction":  "in",
		"kind":       "text",
		"display_ts": int64(1_700_000_001),
		"msg_id":     "m-sim-fence",
		"body":       map[string]any{"text": "the fence row"},
	})

	return func(_ context.Context, s *ipc.Session) {
		for {
			f, err := s.Recv()
			if err != nil {
				return
			}
			typ, payload, ok := answer(f.Type)
			if !ok {
				continue
			}
			out, err := ipc.NewFrame(typ, f.ID, payload)
			if err != nil || s.Send(out) != nil {
				return
			}
			if f.Type != ipc.FrameListTimeline {
				continue
			}
			<-release

			late, err := ipc.NewFrame(ipc.FrameDeliveryStatus, "", ipc.DeliveryStatusPayload{
				EnvelopeID: downgradeEnvID,
				State:      "sent",
			})
			if err != nil || s.Send(late) != nil {
				return
			}
			push, err := ipc.NewFrame(ipc.FrameTimelineEvent, "", ipc.TimelineEventPayload{Event: fence})
			if err != nil || s.Send(push) != nil {
				return
			}
		}
	}
}

func TestDeliveryMarker_LateReportDoesNotStepBackward(t *testing.T) {
	release := make(chan struct{})
	dir, addr, cancel := spinScriptedIPCServer(t, deliveryDowngradeScript(release))
	defer cancel()

	_, sim := launchApp(t, dir, addr)
	typeCommand(sim, "/chats")
	waitForScreen(t, sim, "sim-peer")
	sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
	waitForScreen(t, sim, "marker under test")

	head, _, glyphs := findMarkerCell(t, sim, "marker under test")
	if glyphs != "<-" {
		t.Fatalf("seeded marker = %q, want %q — the row is not where this gate needs it", glyphs, "<-")
	}
	if got := fgOf(head); got != tcell.ColorGreen {
		t.Fatalf("seeded first glyph colour = %v, want %v — the row was never painted at delivered", got, tcell.ColorGreen)
	}

	close(release)
	waitForScreen(t, sim, "the fence row")

	head, _, glyphs = findMarkerCell(t, sim, "marker under test")
	if glyphs != "<-" {
		t.Errorf("painted marker = %q, want %q", glyphs, "<-")
	}
	if got := fgOf(head); got != tcell.ColorGreen {
		t.Errorf("first glyph colour = %v, want %v — a late \"sent\" report pulled the marker back to \"the offer reached their daemon\" after a receipt had already said their haoma holds the content", got, tcell.ColorGreen)
	}
}
