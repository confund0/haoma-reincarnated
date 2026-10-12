package syncrail_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"haoma-frontend/internal/syncrail"
)

func sampleMirrorBody() syncrail.MirrorBody {
	return syncrail.MirrorBody{
		Fingerprint:  "9F3A-11BC-77DE-0042",
		Wrapper:      []byte{0x02, 0x00, 0xff, 0x10, 'h', 'i'},
		WireKind:     "text",
		MsgID:        "msg-7f31a0",
		DisplayTs:    1753600000000,
		OriginChatID: "chat-local-4",
		OriginPeerID: "peer-local-9",
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("test bug: marshalling %T failed: %v", v, err)
	}
	return raw
}

func mirrorEnvelope(t *testing.T, body syncrail.MirrorBody) syncrail.Envelope {
	t.Helper()
	return syncrail.Envelope{
		V:    syncrail.Version,
		Kind: syncrail.KindMirror,
		Ts:   1753600000123,
		Body: mustMarshal(t, body),
	}
}

func TestSealEnvelopeOpenEnvelopeRoundTripsAMirrorBody(t *testing.T) {
	key := mustDeriveKey(t, identityA)
	want := sampleMirrorBody()

	sealed, err := syncrail.SealEnvelope(key, mirrorEnvelope(t, want))
	if err != nil {
		t.Fatalf("SealEnvelope failed: %v", err)
	}

	env, err := syncrail.OpenEnvelope(key, sealed)
	if err != nil {
		t.Fatalf("OpenEnvelope of an envelope this build just sealed failed: %v — "+
			"the sibling would drop its own family's mirrors", err)
	}
	if env.Kind != syncrail.KindMirror {
		t.Fatalf("Kind survived the round trip as %q, want %q — the apply path dispatches "+
			"on Kind and would route the body to the wrong handler", env.Kind, syncrail.KindMirror)
	}
	if env.Ts != 1753600000123 {
		t.Fatalf("Ts survived the round trip as %d, want 1753600000123", env.Ts)
	}

	got, err := syncrail.ParseMirrorBody(env)
	if err != nil {
		t.Fatalf("ParseMirrorBody rejected a body we just built: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mirror body changed across seal/open:\n got %+v\nwant %+v\n"+
			"the sibling applies these fields verbatim, so any drift here is a mutation "+
			"applied differently on my two devices", got, want)
	}
}

func TestSealEnvelopeStampsTheCurrentVersionWhenCallerLeavesItZero(t *testing.T) {
	key := mustDeriveKey(t, identityA)

	sealed, err := syncrail.SealEnvelope(key, syncrail.Envelope{
		Kind: syncrail.KindMirror,
		Body: mustMarshal(t, sampleMirrorBody()),
	})
	if err != nil {
		t.Fatalf("SealEnvelope failed: %v", err)
	}

	env, err := syncrail.OpenEnvelope(key, sealed)
	if err != nil {
		t.Fatalf("OpenEnvelope rejected an envelope sealed with V left at zero: %v — "+
			"every caller would have to remember to stamp the version by hand, and the "+
			"one that forgets ships an envelope its own sibling refuses", err)
	}
	if env.V != syncrail.Version {
		t.Fatalf("V came back %d, want Version=%d", env.V, syncrail.Version)
	}
}

func TestSealEnvelopeKeepsAnExplicitVersion(t *testing.T) {
	key := mustDeriveKey(t, identityA)

	sealed, err := syncrail.SealEnvelope(key, mirrorEnvelope(t, sampleMirrorBody()))
	if err != nil {
		t.Fatalf("SealEnvelope failed: %v", err)
	}
	env, err := syncrail.OpenEnvelope(key, sealed)
	if err != nil {
		t.Fatalf("OpenEnvelope failed: %v", err)
	}
	if env.V != syncrail.Version {
		t.Fatalf("explicitly-set V came back %d, want %d", env.V, syncrail.Version)
	}
}

func TestSealEnvelopeRefusesAnUnmarshalableBody(t *testing.T) {
	key := mustDeriveKey(t, identityA)

	sealed, err := syncrail.SealEnvelope(key, syncrail.Envelope{
		Kind: syncrail.KindMirror,
		Body: json.RawMessage("{not json"),
	})
	if err == nil {
		t.Fatalf("SealEnvelope sealed an envelope whose body is not valid JSON and returned "+
			"%d bytes — the send path would ship bytes no sibling can ever parse, and the "+
			"failure would surface as a mystery drop on the far device", len(sealed))
	}
	if sealed != nil {
		t.Fatalf("SealEnvelope returned %d bytes alongside its marshal error", len(sealed))
	}
}

func sealRawEnvelope(t *testing.T, key []byte, raw string) []byte {
	t.Helper()
	if !json.Valid([]byte(raw)) {
		t.Fatalf("test bug: hand-made envelope is not valid JSON: %s", raw)
	}
	return mustSeal(t, key, []byte(raw))
}

func TestOpenEnvelopeRejectsAnIncompatibleRailVersion(t *testing.T) {
	key := mustDeriveKey(t, identityA)
	sealed := sealRawEnvelope(t, key, `{"v":99,"kind":"mirror","ts":1,"body":{}}`)

	env, err := syncrail.OpenEnvelope(key, sealed)
	if !errors.Is(err, syncrail.ErrVersionMismatch) {
		t.Fatalf("OpenEnvelope on a v99 envelope returned err=%v, want ErrVersionMismatch — "+
			"a future schema must be dropped whole, never half-applied by a build that "+
			"only understands some of its fields", err)
	}
	if !reflect.DeepEqual(env, syncrail.Envelope{}) {
		t.Fatalf("OpenEnvelope returned a populated envelope %+v alongside its version "+
			"error — a caller that reads the envelope before the error applies the "+
			"very mutation we just refused", env)
	}
}

func TestOpenEnvelopeRejectsARailKindThisBuildDoesNotImplement(t *testing.T) {
	key := mustDeriveKey(t, identityA)

	cases := []struct {
		name string
		raw  string
	}{

		{"kind from a newer build", `{"v":1,"kind":"not-a-kind-this-build-implements","ts":1,"body":{}}`},
		{"kind absent entirely", `{"v":1,"ts":1,"body":{}}`},
		{"kind empty string", `{"v":1,"kind":"","ts":1,"body":{}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, err := syncrail.OpenEnvelope(key, sealRawEnvelope(t, key, tc.raw))
			if !errors.Is(err, syncrail.ErrUnknownKind) {
				t.Fatalf("OpenEnvelope returned err=%v, want ErrUnknownKind — an envelope "+
					"whose kind this build cannot name must be dropped, not guessed at", err)
			}
			if !reflect.DeepEqual(env, syncrail.Envelope{}) {
				t.Fatalf("OpenEnvelope returned %+v alongside its unknown-kind error", env)
			}
		})
	}
}

func TestOpenEnvelopeRejectsSealedBytesThatAreNotAnEnvelope(t *testing.T) {
	key := mustDeriveKey(t, identityA)

	sealed := mustSeal(t, key, []byte("this is not an envelope"))

	if _, err := syncrail.OpenEnvelope(key, sealed); err == nil {
		t.Fatalf("OpenEnvelope accepted sealed bytes that are not envelope JSON — " +
			"a valid tag is not a valid envelope, and the apply path must not run on a " +
			"zero-valued struct")
	}
}

func TestOpenEnvelopeRejectsTheWrongKeyBeforeParsing(t *testing.T) {
	sealed, err := syncrail.SealEnvelope(mustDeriveKey(t, identityA), mirrorEnvelope(t, sampleMirrorBody()))
	if err != nil {
		t.Fatalf("SealEnvelope failed: %v", err)
	}

	if _, err := syncrail.OpenEnvelope(mustDeriveKey(t, identityB), sealed); err == nil {
		t.Fatalf("OpenEnvelope succeeded under an unrelated identity's key — a peer " +
			"message misrouted here by a stale linked-device set must fail closed")
	}
}

func TestParseMirrorBodyAcceptsAFullyPopulatedBody(t *testing.T) {
	want := sampleMirrorBody()

	got, err := syncrail.ParseMirrorBody(mirrorEnvelope(t, want))
	if err != nil {
		t.Fatalf("ParseMirrorBody rejected a complete mirror body: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseMirrorBody returned %+v, want %+v", got, want)
	}
}

func TestParseMirrorBodyRejectsBodiesTheApplyPathCannotUse(t *testing.T) {
	full := sampleMirrorBody()

	noFingerprint := full
	noFingerprint.Fingerprint = ""

	noMsgID := full
	noMsgID.MsgID = ""

	noWrapper := full
	noWrapper.Wrapper = nil

	emptyWrapper := full
	emptyWrapper.Wrapper = []byte{}

	cases := []struct {
		name string
		body syncrail.MirrorBody
		why  string
	}{
		{
			name: "no fingerprint",
			body: noFingerprint,
			why:  "fingerprint is the only routing key a sibling has — chat ids and peer ids are local to the sending device and mean nothing here",
		},
		{
			name: "no msg id",
			body: noMsgID,
			why:  "msg id is what stops the same mutation being applied twice when a mirror arrives again",
		},
		{
			name: "nil wrapper",
			body: noWrapper,
			why:  "the wrapper IS the mutation; without it there is nothing to apply",
		},
		{
			name: "zero-length wrapper",
			body: emptyWrapper,
			why:  "an empty wrapper is as unusable as a missing one and must not slip past a nil-only check",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := syncrail.ParseMirrorBody(mirrorEnvelope(t, tc.body))
			if err == nil {
				t.Fatalf("ParseMirrorBody accepted a body with %s and returned %+v — %s",
					tc.name, got, tc.why)
			}
			if !reflect.DeepEqual(got, syncrail.MirrorBody{}) {
				t.Fatalf("ParseMirrorBody returned a populated body %+v alongside its error", got)
			}
		})
	}
}

func TestParseMirrorBodyRejectsAnEnvelopeOfAnotherKind(t *testing.T) {
	env := mirrorEnvelope(t, sampleMirrorBody())
	env.Kind = "contacts"

	if _, err := syncrail.ParseMirrorBody(env); !errors.Is(err, syncrail.ErrUnknownKind) {
		t.Fatalf("ParseMirrorBody on a %q envelope returned err=%v, want ErrUnknownKind — "+
			"parsing a foreign kind's body as a mirror is how a future rail message gets "+
			"applied as a content mutation", env.Kind, err)
	}
}

func TestParseMirrorBodyRejectsAMalformedBody(t *testing.T) {
	cases := []struct {
		name string
		body json.RawMessage
	}{
		{"absent body", nil},
		{"not json", json.RawMessage("not json at all")},
		{"json null", json.RawMessage("null")},
		{"wrong json type", json.RawMessage(`["fingerprint"]`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := syncrail.Envelope{V: syncrail.Version, Kind: syncrail.KindMirror, Body: tc.body}
			if got, err := syncrail.ParseMirrorBody(env); err == nil {
				t.Fatalf("ParseMirrorBody accepted a %s body and returned %+v — a malformed "+
					"body must be reported, not silently decoded to a zero mirror", tc.name, got)
			}
		})
	}
}

func TestVersionAndKindLiveInsideTheSealNotOnTheWire(t *testing.T) {
	key := mustDeriveKey(t, identityA)

	sealed, err := syncrail.SealEnvelope(key, mirrorEnvelope(t, sampleMirrorBody()))
	if err != nil {
		t.Fatalf("SealEnvelope failed: %v", err)
	}

	for _, needle := range [][]byte{
		[]byte(`"kind"`),
		[]byte(`"v"`),
		[]byte(string(syncrail.KindMirror)),
		[]byte("fingerprint"),
	} {
		if bytes.Contains(sealed, needle) {
			t.Fatalf("sealed rail payload contains %q in the clear — haomad forwards these "+
				"bytes and would be able to tell a rail envelope from an ordinary peer "+
				"message, which is exactly the sibling correlation the whole architecture "+
				"exists to deny it", needle)
		}
	}
}
