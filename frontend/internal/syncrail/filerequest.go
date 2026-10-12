package syncrail

import (
	"encoding/json"
	"errors"
	"fmt"
)

const MaxFileRequestIDs = 50

type FileRequestBody struct {
	MsgIDs []string `json:"msg_ids"`
}

func (b FileRequestBody) Check() error {
	switch {
	case len(b.MsgIDs) == 0:
		return errors.New("syncrail: file request names no msg id")
	case len(b.MsgIDs) > MaxFileRequestIDs:
		return fmt.Errorf("syncrail: file request names %d msg ids, over the %d limit", len(b.MsgIDs), MaxFileRequestIDs)
	}
	seen := make(map[string]bool, len(b.MsgIDs))
	for i, id := range b.MsgIDs {
		if id == "" {
			return fmt.Errorf("syncrail: file request msg id %d is empty", i)
		}
		if seen[id] {

			return fmt.Errorf("syncrail: file request names msg id %d twice", i)
		}
		seen[id] = true
	}
	return nil
}

func ParseFileRequest(env Envelope) (FileRequestBody, error) {
	if env.Kind != KindFileRequest {
		return FileRequestBody{}, fmt.Errorf("%w: %q", ErrUnknownKind, env.Kind)
	}
	var body FileRequestBody
	if err := json.Unmarshal(env.Body, &body); err != nil {
		return FileRequestBody{}, fmt.Errorf("syncrail: parse file request: %w", err)
	}
	if err := body.Check(); err != nil {
		return FileRequestBody{}, err
	}
	return body, nil
}
