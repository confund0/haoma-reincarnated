package syncrail

import (
	"encoding/json"
	"errors"
	"fmt"
)

type CallSummaryBody struct {
	Fingerprint string `json:"fingerprint"`

	MsgID string `json:"msg_id"`

	DisplayTs int64 `json:"display_ts"`

	ExpireSeconds uint32 `json:"expire_seconds,omitempty"`

	Summary json.RawMessage `json:"summary"`
}

func ParseCallSummary(env Envelope) (CallSummaryBody, error) {
	if env.Kind != KindCallSummary {
		return CallSummaryBody{}, fmt.Errorf("%w: %q", ErrUnknownKind, env.Kind)
	}
	var body CallSummaryBody
	if err := json.Unmarshal(env.Body, &body); err != nil {
		return CallSummaryBody{}, fmt.Errorf("syncrail: parse call summary: %w", err)
	}
	switch {
	case body.Fingerprint == "":
		return CallSummaryBody{}, errors.New("syncrail: call summary has no fingerprint")
	case body.MsgID == "":
		return CallSummaryBody{}, errors.New("syncrail: call summary has no msg id")
	case len(body.Summary) == 0:
		return CallSummaryBody{}, errors.New("syncrail: call summary has no summary")
	}
	return body, nil
}
