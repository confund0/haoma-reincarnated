package gapfill_test

import (
	"encoding/json"
	"testing"

	"haoma-frontend/internal/gapfill"
)

func body(s string) json.RawMessage { return json.RawMessage(`{"text":"` + s + `"}`) }

func TestDecide(t *testing.T) {
	cases := []struct {
		name  string
		local gapfill.LocalRow
		in    gapfill.Incoming
		want  gapfill.Action
	}{
		{
			name: "absent row is inserted",
			in:   gapfill.Incoming{Body: body("hi")},
			want: gapfill.ActionInsert,
		},
		{
			name:  "identical row is a no-op",
			local: gapfill.LocalRow{Present: true, Body: body("hi")},
			in:    gapfill.Incoming{Body: body("hi")},
			want:  gapfill.ActionNone,
		},
		{
			name:  "newer incoming edit wins",
			local: gapfill.LocalRow{Present: true, Body: body("v1"), EditedAt: 100},
			in:    gapfill.Incoming{Body: body("v2"), EditedAt: 200},
			want:  gapfill.ActionEdit,
		},
		{
			name:  "older incoming edit loses",
			local: gapfill.LocalRow{Present: true, Body: body("v2"), EditedAt: 200},
			in:    gapfill.Incoming{Body: body("v1"), EditedAt: 100},
			want:  gapfill.ActionNone,
		},
		{
			name:  "a first edit against an unedited local row wins",
			local: gapfill.LocalRow{Present: true, Body: body("v0")},
			in:    gapfill.Incoming{Body: body("v1"), EditedAt: 100},
			want:  gapfill.ActionEdit,
		},
		{
			name:  "incoming delete beats a newer local edit",
			local: gapfill.LocalRow{Present: true, Body: body("v9"), EditedAt: 9000},
			in:    gapfill.Incoming{DeletedAt: 100},
			want:  gapfill.ActionDelete,
		},
		{
			name:  "local delete is never resurrected by a newer edit",
			local: gapfill.LocalRow{Present: true, DeletedAt: 100},
			in:    gapfill.Incoming{Body: body("back"), EditedAt: 9000},
			want:  gapfill.ActionNone,
		},
		{
			name:  "delete on both sides is a no-op",
			local: gapfill.LocalRow{Present: true, DeletedAt: 100},
			in:    gapfill.Incoming{DeletedAt: 200},
			want:  gapfill.ActionNone,
		},
		{
			name: "a tombstone for a row we never held inserts nothing",
			in:   gapfill.Incoming{DeletedAt: 100},
			want: gapfill.ActionNone,
		},
		{
			name:  "a swept row tombstones the local copy",
			local: gapfill.LocalRow{Present: true, Body: body("old")},
			in:    gapfill.Incoming{Swept: true},
			want:  gapfill.ActionDelete,
		},
		{
			name: "a swept row we never held is a no-op",
			in:   gapfill.Incoming{Swept: true},
			want: gapfill.ActionNone,
		},
		{
			name:  "a swept row already tombstoned locally is a no-op",
			local: gapfill.LocalRow{Present: true, DeletedAt: 5},
			in:    gapfill.Incoming{Swept: true},
			want:  gapfill.ActionNone,
		},
		{
			name:  "same edit arriving twice is not a tie",
			local: gapfill.LocalRow{Present: true, Body: body("v1"), EditedAt: 100},
			in:    gapfill.Incoming{Body: body("v1"), EditedAt: 100},
			want:  gapfill.ActionNone,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := gapfill.Decide(tc.local, tc.in); got != tc.want {
				t.Fatalf("Decide = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestDecide_TieConvergesFromBothSides(t *testing.T) {
	const at = 1742643890
	a := body("apple")
	b := body("banana")

	onA := gapfill.Decide(
		gapfill.LocalRow{Present: true, Body: a, EditedAt: at},
		gapfill.Incoming{Body: b, EditedAt: at},
	)

	onB := gapfill.Decide(
		gapfill.LocalRow{Present: true, Body: b, EditedAt: at},
		gapfill.Incoming{Body: a, EditedAt: at},
	)

	moved := 0
	if onA == gapfill.ActionEdit {
		moved++
	}
	if onB == gapfill.ActionEdit {
		moved++
	}
	if moved != 1 {
		t.Fatalf("tie did not converge: A=%s B=%s (%d sides moved, want exactly 1)", onA, onB, moved)
	}

	if onA != gapfill.ActionNone {
		t.Fatalf("expected the lexicographically smaller body to be the winner; A moved to %s", onA)
	}
	if onB != gapfill.ActionEdit {
		t.Fatalf("expected B to adopt the winner; B decided %s", onB)
	}
}

func TestDecide_IsIdempotentAfterApplying(t *testing.T) {
	in := gapfill.Incoming{Body: body("v2"), EditedAt: 200}
	local := gapfill.LocalRow{Present: true, Body: body("v1"), EditedAt: 100}

	if got := gapfill.Decide(local, in); got != gapfill.ActionEdit {
		t.Fatalf("first pass = %s, want edit", got)
	}

	local = gapfill.LocalRow{Present: true, Body: in.Body, EditedAt: in.EditedAt}
	if got := gapfill.Decide(local, in); got != gapfill.ActionNone {
		t.Fatalf("second pass = %s, want none", got)
	}
}
