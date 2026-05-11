package xport

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func sampleSecret() []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

func sampleEnvelope() Envelope {
	return Envelope{
		ID:        "env-1",
		Timestamp: 1_700_000_000,
		From:      "sender-onion",
		Payload:   []byte("opaque-ciphertext"),
	}
}

func TestSign_SetsMac(t *testing.T) {
	env := Sign(sampleEnvelope(), sampleSecret())
	if len(env.Mac) != 32 {
		t.Errorf("Mac length = %d, want 32 (HMAC-SHA256)", len(env.Mac))
	}
}

func TestSign_Deterministic(t *testing.T) {
	env := sampleEnvelope()
	secret := sampleSecret()
	a := Sign(env, secret)
	b := Sign(env, secret)
	if !bytes.Equal(a.Mac, b.Mac) {
		t.Errorf("Sign not deterministic: %x vs %x", a.Mac, b.Mac)
	}
}

func TestVerify_OK(t *testing.T) {
	env := Sign(sampleEnvelope(), sampleSecret())
	if err := Verify(env, sampleSecret()); err != nil {
		t.Errorf("Verify on freshly-signed envelope: %v", err)
	}
}

func TestVerify_WrongSecret(t *testing.T) {
	env := Sign(sampleEnvelope(), sampleSecret())
	other := make([]byte, 32)
	for i := range other {
		other[i] = byte(255 - i)
	}
	if err := Verify(env, other); !errors.Is(err, ErrMacMismatch) {
		t.Errorf("Verify with wrong secret: err = %v, want ErrMacMismatch", err)
	}
}

func TestVerify_TamperedPayload(t *testing.T) {
	env := Sign(sampleEnvelope(), sampleSecret())
	env.Payload = []byte("tampered")
	if err := Verify(env, sampleSecret()); !errors.Is(err, ErrMacMismatch) {
		t.Errorf("Verify on tampered payload: err = %v, want ErrMacMismatch", err)
	}
}

func TestVerify_TamperedFrom(t *testing.T) {
	env := Sign(sampleEnvelope(), sampleSecret())
	env.From = "spoofed"
	if err := Verify(env, sampleSecret()); !errors.Is(err, ErrMacMismatch) {
		t.Errorf("Verify on tampered From: err = %v, want ErrMacMismatch", err)
	}
}

func TestVerify_TamperedTimestamp(t *testing.T) {
	env := Sign(sampleEnvelope(), sampleSecret())
	env.Timestamp++
	if err := Verify(env, sampleSecret()); !errors.Is(err, ErrMacMismatch) {
		t.Errorf("Verify on tampered Timestamp: err = %v, want ErrMacMismatch", err)
	}
}

func TestVerify_TamperedID(t *testing.T) {
	env := Sign(sampleEnvelope(), sampleSecret())
	env.ID = "different-id"
	if err := Verify(env, sampleSecret()); !errors.Is(err, ErrMacMismatch) {
		t.Errorf("Verify on tampered ID: err = %v, want ErrMacMismatch", err)
	}
}

func TestVerify_TamperedKind(t *testing.T) {
	env := sampleEnvelope()
	env.Kind = KindText
	env = Sign(env, sampleSecret())
	env.Kind = KindStatus
	if err := Verify(env, sampleSecret()); !errors.Is(err, ErrMacMismatch) {
		t.Errorf("Verify on tampered Kind: err = %v, want ErrMacMismatch", err)
	}
}

func TestVerify_TamperedPresenceSource(t *testing.T) {
	env := sampleEnvelope()
	env.PresenceSource = PresenceSourceHaoma
	env = Sign(env, sampleSecret())
	env.PresenceSource = PresenceSourceHaomad
	if err := Verify(env, sampleSecret()); !errors.Is(err, ErrMacMismatch) {
		t.Errorf("Verify on tampered PresenceSource: err = %v, want ErrMacMismatch", err)
	}

	env = sampleEnvelope()
	env.PresenceSource = PresenceSourceHaoma
	env = Sign(env, sampleSecret())
	env.PresenceSource = ""
	if err := Verify(env, sampleSecret()); !errors.Is(err, ErrMacMismatch) {
		t.Errorf("Verify on stripped PresenceSource: err = %v, want ErrMacMismatch", err)
	}
}

func TestVerify_TamperedPadding(t *testing.T) {
	env := sampleEnvelope()
	env.Padding = []byte("original-pad")
	env = Sign(env, sampleSecret())
	env.Padding = []byte("forged-pad")
	if err := Verify(env, sampleSecret()); !errors.Is(err, ErrMacMismatch) {
		t.Errorf("Verify on tampered Padding: err = %v, want ErrMacMismatch", err)
	}

	env = sampleEnvelope()
	env.Padding = []byte("original-pad")
	env = Sign(env, sampleSecret())
	env.Padding = nil
	if err := Verify(env, sampleSecret()); !errors.Is(err, ErrMacMismatch) {
		t.Errorf("Verify on stripped Padding: err = %v, want ErrMacMismatch", err)
	}
}

func TestSign_PreV11_5_EmptyKindRoundTrips(t *testing.T) {

	env := Sign(sampleEnvelope(), sampleSecret())
	if env.Kind != "" {
		t.Fatalf("test setup: Kind = %q, want empty", env.Kind)
	}
	if err := Verify(env, sampleSecret()); err != nil {
		t.Errorf("Verify on empty-Kind envelope: %v", err)
	}
	if got := env.EffectiveKind(); got != KindText {
		t.Errorf("EffectiveKind on empty Kind = %q, want %q", got, KindText)
	}
}

func TestEffectiveKind(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", KindText},
		{KindText, KindText},
		{KindStatus, KindStatus},
		{"custom", "custom"},
	}
	for _, c := range cases {
		got := Envelope{Kind: c.in}.EffectiveKind()
		if got != c.want {
			t.Errorf("EffectiveKind(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRandomPadding_WithinBounds(t *testing.T) {
	var seenZero, seenNonZero bool
	for i := 0; i < 200; i++ {
		var env Envelope
		if err := RandomPadding(&env); err != nil {
			t.Fatalf("RandomPadding: %v", err)
		}
		if len(env.Padding) > MaxPaddingBytes {
			t.Errorf("iter %d: len(Padding) = %d, exceeds MaxPaddingBytes=%d", i, len(env.Padding), MaxPaddingBytes)
		}
		if len(env.Padding) == 0 {
			seenZero = true
		} else {
			seenNonZero = true
		}
	}

	if !seenNonZero {
		t.Errorf("RandomPadding produced only empty padding across 200 draws")
	}
	_ = seenZero
}

func TestRandomPadding_ParticipatesInMAC(t *testing.T) {
	secret := sampleSecret()
	base := sampleEnvelope()
	if err := RandomPadding(&base); err != nil {
		t.Fatalf("RandomPadding: %v", err)
	}
	signed := Sign(base, secret)
	if err := Verify(signed, secret); err != nil {
		t.Fatalf("Verify on signed+padded envelope: %v", err)
	}

	signed.Padding = append(signed.Padding, 0xff)
	if err := Verify(signed, secret); !errors.Is(err, ErrMacMismatch) {
		t.Errorf("Verify after appending to padding: err = %v, want ErrMacMismatch", err)
	}
}

func TestCanonical_LengthPrefixedFields(t *testing.T) {
	secret := sampleSecret()
	a := Sign(Envelope{ID: "ab", From: "cd", Payload: []byte("ef")}, secret)
	b := Sign(Envelope{ID: "abc", From: "d", Payload: []byte("ef")}, secret)
	if bytes.Equal(a.Mac, b.Mac) {
		t.Errorf("length-boundary attack worked: MACs collide across re-sliced envelopes")
	}
}

func TestServer_VerifierAccepts(t *testing.T) {
	secret := sampleSecret()
	delivered := make(chan Envelope, 1)
	v := VerifierFunc(func(_ context.Context, env Envelope) error { return Verify(env, secret) })
	handler := NewServer(-1, ReceiverFunc(func(ctx context.Context, e Envelope) error {
		delivered <- e
		return nil
	}), v, nil)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	env := Sign(sampleEnvelope(), secret)
	c := &Client{HTTP: srv.Client()}
	if _, err := c.Send(context.Background(), srv.URL, env); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got := <-delivered
	if got.ID != env.ID {
		t.Errorf("delivered ID = %q, want %q", got.ID, env.ID)
	}
}

func TestServer_VerifierRejects401(t *testing.T) {
	v := VerifierFunc(func(_ context.Context, env Envelope) error { return ErrMacMismatch })
	handler := NewServer(-1, ReceiverFunc(func(ctx context.Context, e Envelope) error {
		t.Error("receiver should not fire when verify fails")
		return nil
	}), v, nil)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	c := &Client{HTTP: srv.Client()}
	_, err := c.Send(context.Background(), srv.URL, sampleEnvelope())
	if err == nil {
		t.Fatal("expected error when verifier rejects")
	}

	if !containsAny(err.Error(), "401", "Unauthorized", "unauthorized") {
		t.Errorf("err = %v, want 401", err)
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if bytes.Contains([]byte(s), []byte(sub)) {
			return true
		}
	}
	return false
}

func TestServer_VerifierCatchesWrongSecret(t *testing.T) {
	secret := sampleSecret()
	otherSecret := make([]byte, 32)
	for i := range otherSecret {
		otherSecret[i] = 99
	}
	v := VerifierFunc(func(_ context.Context, env Envelope) error { return Verify(env, secret) })
	handler := NewServer(-1, ReceiverFunc(func(ctx context.Context, e Envelope) error {
		t.Error("receiver should not fire; verifier must reject")
		return nil
	}), v, nil)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	env := Sign(sampleEnvelope(), otherSecret)
	c := &Client{HTTP: srv.Client()}
	_, err := c.Send(context.Background(), srv.URL, env)
	if err == nil {
		t.Fatal("expected error from verifier rejecting wrong-secret signature")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("401")) {
		t.Errorf("err = %v, want 401", err)
	}
}

func TestRoundTrip_Signed(t *testing.T) {
	secret := sampleSecret()
	received := make(chan Envelope, 1)
	v := VerifierFunc(func(_ context.Context, env Envelope) error { return Verify(env, secret) })
	handler := NewServer(-1, ReceiverFunc(func(ctx context.Context, e Envelope) error {
		received <- e
		return nil
	}), v, nil)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	env := Sign(sampleEnvelope(), secret)
	c := &Client{HTTP: srv.Client()}
	if _, err := c.Send(context.Background(), srv.URL, env); err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case got := <-received:
		if err := Verify(got, secret); err != nil {
			t.Errorf("verify on received envelope: %v", err)
		}
		if got.From != env.From {
			t.Errorf("From = %q, want %q", got.From, env.From)
		}
	default:
		t.Fatal("no envelope received")
	}

	_ = http.StatusOK
}

func TestServer_StatusResponder_WritesResponseBody(t *testing.T) {
	secret := sampleSecret()
	var gotReq Envelope
	responder := StatusResponderFunc(func(_ context.Context, req Envelope) (Envelope, error) {
		gotReq = req
		resp := Envelope{
			ID:        "resp-1",
			Timestamp: req.Timestamp + 1,
			From:      "server-onion",
			Kind:      KindStatus,
			Payload:   []byte(`{"state":"online"}`),
		}
		return Sign(resp, secret), nil
	})

	receiver := ReceiverFunc(func(context.Context, Envelope) error {
		t.Error("receiver must not fire for Kind=status with a responder")
		return nil
	})
	v := VerifierFunc(func(_ context.Context, env Envelope) error { return Verify(env, secret) })
	handler := NewServer(-1, receiver, v, responder)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	probe := Sign(Envelope{
		ID:        "probe-1",
		Timestamp: 1_700_000_000,
		From:      "client-onion",
		Kind:      KindStatus,
		Payload:   []byte(`{"state":"online"}`),
	}, secret)

	c := &Client{HTTP: srv.Client()}
	resp, err := c.Probe(context.Background(), srv.URL, probe)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if resp == nil {
		t.Fatal("Probe returned nil envelope")
	}
	if err := Verify(*resp, secret); err != nil {
		t.Errorf("response envelope MAC: %v", err)
	}
	if resp.From != "server-onion" {
		t.Errorf("response From = %q, want server-onion", resp.From)
	}
	if gotReq.ID != "probe-1" {
		t.Errorf("responder saw req.ID = %q, want probe-1", gotReq.ID)
	}
}

func TestServer_StatusResponder_EmptyIDMeansBare200(t *testing.T) {

	secret := sampleSecret()
	responder := StatusResponderFunc(func(context.Context, Envelope) (Envelope, error) {
		return Envelope{}, nil
	})
	v := VerifierFunc(func(_ context.Context, env Envelope) error { return Verify(env, secret) })
	handler := NewServer(-1, ReceiverFunc(func(context.Context, Envelope) error { return nil }), v, responder)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	probe := Sign(Envelope{
		ID: "p", Timestamp: 1, From: "client", Kind: KindStatus, Payload: []byte("{}"),
	}, secret)
	c := &Client{HTTP: srv.Client()}
	_, err := c.Probe(context.Background(), srv.URL, probe)
	if !errors.Is(err, ErrEmptyResponse) {
		t.Errorf("err = %v, want ErrEmptyResponse", err)
	}
}

func TestServer_NilStatusResponder_FallsThroughToReceiver(t *testing.T) {

	secret := sampleSecret()
	delivered := make(chan Envelope, 1)
	v := VerifierFunc(func(_ context.Context, env Envelope) error { return Verify(env, secret) })
	handler := NewServer(-1, ReceiverFunc(func(_ context.Context, e Envelope) error {
		delivered <- e
		return nil
	}), v, nil)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	env := Sign(Envelope{
		ID: "s", Timestamp: 1, From: "client", Kind: KindStatus, Payload: []byte("{}"),
	}, secret)
	c := &Client{HTTP: srv.Client()}
	if _, err := c.Send(context.Background(), srv.URL, env); err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case got := <-delivered:
		if got.Kind != KindStatus {
			t.Errorf("receiver got Kind=%q, want %q", got.Kind, KindStatus)
		}
	default:
		t.Fatal("receiver did not fire on Kind=status with nil responder")
	}
}

func TestServer_StatusResponderError_Returns500(t *testing.T) {
	secret := sampleSecret()
	responder := StatusResponderFunc(func(context.Context, Envelope) (Envelope, error) {
		return Envelope{}, errors.New("responder-broken")
	})
	v := VerifierFunc(func(_ context.Context, env Envelope) error { return Verify(env, secret) })
	handler := NewServer(-1, ReceiverFunc(func(context.Context, Envelope) error { return nil }), v, responder)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	env := Sign(Envelope{
		ID: "p", Timestamp: 1, From: "client", Kind: KindStatus, Payload: []byte("{}"),
	}, secret)
	c := &Client{HTTP: srv.Client()}
	_, err := c.Probe(context.Background(), srv.URL, env)
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("500")) {
		t.Errorf("err = %v, want 500-wrapped error", err)
	}
}
