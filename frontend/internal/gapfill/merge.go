package gapfill

import (
	"bytes"
	"encoding/json"
)

type Action int

const (
	ActionNone Action = iota

	ActionInsert

	ActionEdit

	ActionDelete
)

func (a Action) String() string {
	switch a {
	case ActionInsert:
		return "insert"
	case ActionEdit:
		return "edit"
	case ActionDelete:
		return "delete"
	default:
		return "none"
	}
}

type LocalRow struct {
	Present   bool
	Body      json.RawMessage
	EditedAt  int64
	DeletedAt int64
}

type Incoming struct {
	Body      json.RawMessage
	EditedAt  int64
	DeletedAt int64
	Swept     bool
}

func Decide(local LocalRow, in Incoming) Action {

	if in.Swept {
		if !local.Present || local.DeletedAt > 0 {
			return ActionNone
		}
		return ActionDelete
	}

	if !local.Present {

		if in.DeletedAt > 0 {
			return ActionNone
		}
		return ActionInsert
	}

	if local.DeletedAt > 0 {
		return ActionNone
	}
	if in.DeletedAt > 0 {
		return ActionDelete
	}

	if in.EditedAt > local.EditedAt {
		return ActionEdit
	}
	if in.EditedAt < local.EditedAt {
		return ActionNone
	}

	if in.EditedAt == 0 {
		return ActionNone
	}

	if bytes.Equal(local.Body, in.Body) {
		return ActionNone
	}

	if bytes.Compare(in.Body, local.Body) < 0 {
		return ActionEdit
	}
	return ActionNone
}
