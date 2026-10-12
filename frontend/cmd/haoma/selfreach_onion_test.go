package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/ipc"
)

func TestSelfReach_NoOnionReachesAFrame(t *testing.T) {
	for _, v := range []any{backendapi.PeerSelfReach{}, backendapi.PeerSelfReachEvent{}, ipc.PeerSelfReachPayload{}} {
		ty := reflect.TypeOf(v)
		for i := range ty.NumField() {
			f := ty.Field(i)
			if strings.Contains(strings.ToLower(f.Name+" "+f.Tag.Get("json")), "onion") {
				t.Errorf("%s has field %s %q", ty, f.Name, f.Tag.Get("json"))
			}
		}
	}

	const onion = "abcdefghijabcdefghijabcdefghijabcdefghijabcdefghijabcdef"
	wire := `{"peer_id":"p1","onion":"` + onion + `","ok":true,"at":7}`
	var res backendapi.PeerSelfReach
	if err := json.Unmarshal([]byte(wire), &res); err != nil {
		t.Fatal(err)
	}
	var ev backendapi.PeerSelfReachEvent
	if err := json.Unmarshal([]byte(wire), &ev); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]ipc.PeerSelfReachPayload{
		"probe reply": {PeerID: res.PeerID, Ok: res.Ok, At: res.At},
		"bus event":   {PeerID: ev.PeerID, Ok: ev.Ok, At: ev.At},
	} {
		f, err := ipc.NewFrame(ipc.FramePeerSelfReachChanged, "", p)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), onion) || strings.Contains(string(raw), "onion") {
			t.Errorf("%s: frame holds an onion: %s", name, raw)
		}
	}
}
