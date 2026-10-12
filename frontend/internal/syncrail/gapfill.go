package syncrail

import (
	"encoding/json"
	"errors"
	"fmt"
)

type GapRequestBody struct {
	Watermark uint64 `json:"watermark"`

	Full bool `json:"full,omitempty"`

	Reciprocate bool `json:"reciprocate,omitempty"`
}

type GapResponseBody struct {
	Rows []RowSnapshot `json:"rows"`

	RangeEnd uint64 `json:"range_end"`

	More bool `json:"more,omitempty"`

	Full bool `json:"full,omitempty"`
}

type RowSnapshot struct {
	Fingerprint string `json:"fingerprint"`

	MsgID string `json:"msg_id"`

	Kind string `json:"kind"`

	Inbound bool `json:"inbound,omitempty"`

	DisplayTs     int64  `json:"display_ts"`
	SenderTs      int64  `json:"sender_ts,omitempty"`
	ExpireSeconds uint32 `json:"expire_seconds,omitempty"`

	Body json.RawMessage `json:"body,omitempty"`

	EditedAt  int64 `json:"edited_at,omitempty"`
	DeletedAt int64 `json:"deleted_at,omitempty"`

	Swept bool `json:"swept,omitempty"`

	ReadAt int64 `json:"read_at,omitempty"`

	DeliveryRank string `json:"delivery_rank,omitempty"`

	DeliveryFailed bool `json:"delivery_failed,omitempty"`

	ReceiptSentAt int64 `json:"receipt_sent_at,omitempty"`

	ReceiptGeneration uint64 `json:"receipt_generation,omitempty"`
}

func ParseGapRequest(env Envelope) (GapRequestBody, error) {
	if env.Kind != KindGapRequest {
		return GapRequestBody{}, fmt.Errorf("%w: %q", ErrUnknownKind, env.Kind)
	}
	var body GapRequestBody
	if err := json.Unmarshal(env.Body, &body); err != nil {
		return GapRequestBody{}, fmt.Errorf("syncrail: parse gap request: %w", err)
	}
	return body, nil
}

func ParseGapResponse(env Envelope) (GapResponseBody, error) {
	if env.Kind != KindGapResponse {
		return GapResponseBody{}, fmt.Errorf("%w: %q", ErrUnknownKind, env.Kind)
	}
	var body GapResponseBody
	if err := json.Unmarshal(env.Body, &body); err != nil {
		return GapResponseBody{}, fmt.Errorf("syncrail: parse gap response: %w", err)
	}
	if body.RangeEnd == 0 && len(body.Rows) > 0 {
		return GapResponseBody{}, errors.New("syncrail: gap response carries rows but no range end")
	}
	return body, nil
}
