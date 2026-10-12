package presence_test

import (
	"testing"

	"haoma-frontend/internal/presence"
)

func TestBest(t *testing.T) {
	unknown := presence.Snapshot{Effective: presence.EffectiveUnknown}
	accepting := presence.Snapshot{Accepting: true, Effective: presence.EffectiveAccepting}
	human := func(state string, acc bool) presence.Snapshot {
		return presence.Snapshot{Accepting: acc, Chatty: state, Effective: state}
	}
	cases := []struct {
		name  string
		snaps []presence.Snapshot
		want  presence.Snapshot
	}{
		{"no pairing", nil, unknown},
		{"both unknown", []presence.Snapshot{unknown, unknown}, unknown},
		{"green on the second pairing only", []presence.Snapshot{unknown, human("available", false)}, human("available", false)},
		{"blue on the second pairing only", []presence.Snapshot{unknown, accepting}, accepting},
		{"green over blue", []presence.Snapshot{accepting, human("available", false)}, human("available", true)},
		{"a chosen busy over the default available", []presence.Snapshot{human("available", false), human("busy", false)}, human("busy", false)},
		{"busy over available, either order", []presence.Snapshot{human("busy", false), human("available", false)}, human("busy", false)},
		{"busy over away", []presence.Snapshot{human("away", false), human("busy", false)}, human("busy", false)},
		{"away on the desktop, the chat opened on the phone: available", []presence.Snapshot{human("away", false), human("available", true)}, human("available", true)},
		{"available over away, either order", []presence.Snapshot{human("available", true), human("away", false)}, human("available", true)},
		{"away over a state this build does not know", []presence.Snapshot{human("later", false), human("away", false)}, human("away", false)},
		{"away over blue", []presence.Snapshot{accepting, human("away", false)}, human("away", true)},
		{"a state this build does not know is no state: blue", []presence.Snapshot{accepting, human("later", false)}, presence.Snapshot{Accepting: true, Effective: presence.EffectiveAccepting}},
		{"a state this build does not know, alone, is unknown", []presence.Snapshot{human("later", false)}, unknown},
		{"one pairing is itself", []presence.Snapshot{human("away", true)}, human("away", true)},
	}
	for _, c := range cases {
		if got := presence.Best(c.snaps); got != c.want {
			t.Errorf("%s: Best = %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestBest_Arrows(t *testing.T) {
	unknown := presence.Snapshot{Effective: presence.EffectiveUnknown}
	sending := presence.Snapshot{Sending: true, Effective: presence.EffectiveUnknown}
	accepting := presence.Snapshot{Accepting: true, Effective: presence.EffectiveAccepting}
	cases := []struct {
		name  string
		snaps []presence.Snapshot
		want  presence.Snapshot
	}{
		{"down on the first pairing only", []presence.Snapshot{sending, unknown}, sending},
		{"down on the second pairing only", []presence.Snapshot{unknown, sending}, sending},
		{"down on the third pairing only", []presence.Snapshot{unknown, unknown, sending}, sending},
		{"up on one device, down on the other", []presence.Snapshot{accepting, sending},
			presence.Snapshot{Accepting: true, Sending: true, Effective: presence.EffectiveAccepting}},
		{"down on the other device than busy", []presence.Snapshot{
			{Chatty: "busy", Effective: "busy"}, sending},
			presence.Snapshot{Sending: true, Chatty: "busy", Effective: "busy"}},
		{"busy on the second device, both arrows on the first", []presence.Snapshot{
			{Accepting: true, Sending: true, Chatty: "available", Effective: "available"},
			{Chatty: "busy", Effective: "busy"}},
			presence.Snapshot{Accepting: true, Sending: true, Chatty: "busy", Effective: "busy"}},
		{"no arrow anywhere", []presence.Snapshot{unknown, {Chatty: "away", Effective: "away"}},
			presence.Snapshot{Chatty: "away", Effective: "away"}},
	}
	for _, c := range cases {
		if got := presence.Best(c.snaps); got != c.want {
			t.Errorf("%s: Best = %+v, want %+v", c.name, got, c.want)
		}
	}
}
