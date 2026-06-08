package calls

import (
	"encoding/json"

	"haoma-frontend/internal/events"
)

func init() {
	events.RegisterCleanup(events.KindCallSummary, callSummaryCleanup)
}

func callSummaryCleanup(ev events.Event) [][]byte {
	if len(ev.Body) == 0 || ev.ChatID == "" {
		return nil
	}
	var body events.CallSummaryBody
	if err := json.Unmarshal(ev.Body, &body); err != nil {
		return nil
	}
	if body.CallID == "" {
		return nil
	}
	return [][]byte{
		stateKey(body.CallID),
		indexKey(ev.ChatID, body.CallID),
	}
}
