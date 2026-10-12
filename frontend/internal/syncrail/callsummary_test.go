package syncrail_test

import (
	"encoding/json"
	"errors"
	"testing"

	"haoma-frontend/internal/syncrail"
)

func TestCallSummary_WireIsPinned(t *testing.T) {
	key := mustDeriveKey(t, identityA)
	const raw = `{"v":1,"kind":"call-summary","ts":1,"body":{"fingerprint":"aa11","msg_id":"m1","display_ts":1790869613,"expire_seconds":3600,"summary":{"call_id":"c1","direction":"out","outcome":"completed"}}}`
	env, err := syncrail.OpenEnvelope(key, sealRawEnvelope(t, key, raw))
	if err != nil {
		t.Fatalf("OpenEnvelope on a hand-written call summary: %v", err)
	}
	body, err := syncrail.ParseCallSummary(env)
	if err != nil {
		t.Fatalf("ParseCallSummary: %v", err)
	}
	if body.Fingerprint != "aa11" || body.MsgID != "m1" || body.DisplayTs != 1790869613 || body.ExpireSeconds != 3600 {
		t.Fatalf("parsed %+v; a tag changed", body)
	}
	if string(body.Summary) != `{"call_id":"c1","direction":"out","outcome":"completed"}` {
		t.Fatalf("summary = %s; the body must cross byte for byte", body.Summary)
	}
}

func TestCallSummary_ParseRefusesAnUnusableBody(t *testing.T) {
	env := func(body string) syncrail.Envelope {
		return syncrail.Envelope{V: syncrail.Version, Kind: syncrail.KindCallSummary, Ts: 1, Body: json.RawMessage(body)}
	}
	for name, body := range map[string]string{
		"no fingerprint": `{"msg_id":"m1","summary":{"call_id":"c1"}}`,
		"no msg id":      `{"fingerprint":"aa","summary":{"call_id":"c1"}}`,
		"no summary":     `{"fingerprint":"aa","msg_id":"m1"}`,
		"not an object":  `[1,2]`,
	} {
		if _, err := syncrail.ParseCallSummary(env(body)); err == nil {
			t.Errorf("%s: parsed, want an error", name)
		}
	}
	if _, err := syncrail.ParseCallSummary(syncrail.Envelope{V: 1, Kind: syncrail.KindContacts, Body: json.RawMessage(`{}`)}); !errors.Is(err, syncrail.ErrUnknownKind) {
		t.Fatalf("a contacts envelope parsed as a call summary: %v", err)
	}
}

func TestCallSummary_KindIsKnown(t *testing.T) {
	key := mustDeriveKey(t, identityA)
	raw, _ := json.Marshal(syncrail.CallSummaryBody{Fingerprint: "aa", MsgID: "m1", Summary: json.RawMessage(`{"call_id":"c1"}`)})
	sealed, err := syncrail.SealEnvelope(key, syncrail.Envelope{Kind: syncrail.KindCallSummary, Ts: 1, Body: raw})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := syncrail.OpenEnvelope(key, sealed); err != nil {
		t.Fatalf("the call-summary kind does not open: %v", err)
	}
}
