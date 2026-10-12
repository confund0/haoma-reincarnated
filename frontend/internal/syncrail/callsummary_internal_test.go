package syncrail

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestCallSummary_AnOlderBuildDropsTheKindWhole(t *testing.T) {
	key, err := DeriveKey([]byte("identity-private-key-A-0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(CallSummaryBody{Fingerprint: "aa", MsgID: "m1", Summary: json.RawMessage(`{"call_id":"c1"}`)})
	sealed, err := SealEnvelope(key, Envelope{Kind: KindCallSummary, Ts: 1, Body: raw})
	if err != nil {
		t.Fatal(err)
	}
	older := make(map[Kind]bool, len(knownKinds))
	for k, v := range knownKinds {
		if k != KindCallSummary {
			older[k] = v
		}
	}
	if _, err := openEnvelope(key, sealed, older); !errors.Is(err, ErrUnknownKind) {
		t.Fatalf("an older build's open = %v, want ErrUnknownKind", err)
	}
}
