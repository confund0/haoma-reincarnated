package mirror_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/mirror"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/peerstate"
	"haoma-frontend/internal/syncrail"
)

const (
	peerID     = "peer-1"
	peerID2    = "peer-2"
	peerFP     = "fp-of-the-person"
	chatID     = "chat-1"
	sibA       = "sibling-a"
	sibB       = "sibling-b"
	identity   = "identity-private-key-bytes"
	peerCipher = "libsignal-ciphertext-for-peer"
)

type fakeSender struct {
	reqs     []backendapi.SendRequest
	resp     backendapi.SendResponse
	err      error
	errFor   map[string]error
	railErr  error
	inFlight bool
	overlap  bool
}

func (f *fakeSender) Send(_ context.Context, req backendapi.SendRequest) (backendapi.SendResponse, error) {
	if f.inFlight {
		f.overlap = true
	}
	f.inFlight = true
	defer func() { f.inFlight = false }()
	f.reqs = append(f.reqs, req)
	if err, ok := f.errFor[req.PeerID]; ok {
		return backendapi.SendResponse{}, err
	}
	if req.PeerID != peerID && req.PeerID != peerID2 && f.railErr != nil {
		return backendapi.SendResponse{}, f.railErr
	}
	if f.err != nil {
		return backendapi.SendResponse{}, f.err
	}
	resp := f.resp
	if resp.EnvelopeID == "" {
		resp.EnvelopeID = "env-" + req.PeerID
	}
	return resp, nil
}

func (f *fakeSender) calls() int { return len(f.reqs) }

func (f *fakeSender) railSends() []backendapi.SendRequest {
	var out []backendapi.SendRequest
	for _, r := range f.reqs {
		if r.PeerID != peerID && r.PeerID != peerID2 {
			out = append(out, r)
		}
	}
	return out
}

type fakeFingerprints struct {
	byPeer map[string]peerstate.MetaRecord
	errFor map[string]error
}

func (f fakeFingerprints) Get(peerID string) (peerstate.MetaRecord, error) {
	if err, ok := f.errFor[peerID]; ok {
		return peerstate.MetaRecord{}, err
	}
	return f.byPeer[peerID], nil
}

func railKey() ([]byte, error) { return syncrail.DeriveKey([]byte(identity)) }

func captureLogs(t *testing.T, level slog.Level) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: level})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func testRail(t *testing.T, siblings []string, fps mirror.Fingerprints) mirror.Rail {
	t.Helper()
	return mirror.NewRail(railKey, func() []string { return siblings }, fps)
}

func knownPeer() mirror.Fingerprints {
	return fakeFingerprints{byPeer: map[string]peerstate.MetaRecord{
		peerID:  {Fingerprint: peerFP},
		peerID2: {Fingerprint: peerFP},
	}}
}

func textWrapper(t *testing.T, seq uint64) (*msg.Wrapper, []byte) {
	t.Helper()
	w, err := msg.BuildText(seq, 1700000000, "msg-id-abc", "hello", 0, "", "", nil, 0)
	if err != nil {
		t.Fatalf("BuildText: %v", err)
	}
	plain, err := msg.Marshal(w)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return w, plain
}

func preparedSend(plain []byte) mirror.PrepareFunc {
	return func(_ context.Context, deviceID string) (backendapi.SendRequest, []byte, error) {
		return backendapi.SendRequest{
			PeerID:         deviceID,
			Payload:        []byte(peerCipher),
			PresenceSource: backendapi.PresenceSourceHaoma,
		}, plain, nil
	}
}

func TestDispatch_PassesThroughRequestAndResponse(t *testing.T) {
	_, plain := textWrapper(t, 7)
	f := &fakeSender{resp: backendapi.SendResponse{EnvelopeID: "env-1"}}
	got, err := mirror.Dispatch(context.Background(), f, testRail(t, nil, knownPeer()),
		mirror.SingleTarget(peerID), preparedSend(plain), mirror.NewOp(msg.KindText, chatID))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if f.calls() != 1 || f.reqs[0].PeerID != peerID {
		t.Fatalf("underlying Send not called with the request (calls=%d peer=%q)", f.calls(), f.reqs[0].PeerID)
	}
	if got.Primary.EnvelopeID != "env-1" {
		t.Errorf("Primary.EnvelopeID = %q, want env-1 (response must pass through unchanged)", got.Primary.EnvelopeID)
	}
	if got.Primary.Seq != 7 {
		t.Errorf("Primary.Seq = %d, want 7 (read back off the accepted device's wrapper)", got.Primary.Seq)
	}
	if len(got.EnvelopeIDs) != 1 || got.EnvelopeIDs[0] != "env-1" {
		t.Errorf("EnvelopeIDs = %v, want [env-1]", got.EnvelopeIDs)
	}
}

func TestDispatch_UnclassifiedKindFailsClosed(t *testing.T) {
	_, plain := textWrapper(t, 1)
	f := &fakeSender{}
	_, err := mirror.Dispatch(context.Background(), f, testRail(t, []string{sibA}, knownPeer()),
		mirror.SingleTarget(peerID), preparedSend(plain), mirror.NewOp(msg.Kind("never_registered"), chatID))
	if err == nil {
		t.Fatal("expected an error for an unclassified kind")
	}
	if f.calls() != 0 {
		t.Errorf("Send called %d times for an unclassified kind; want 0 (fail closed, peer AND rail)", f.calls())
	}
}

func TestDispatch_EmptyTargetsFailsClosed(t *testing.T) {
	_, plain := textWrapper(t, 1)
	f := &fakeSender{}
	_, err := mirror.Dispatch(context.Background(), f, testRail(t, nil, knownPeer()),
		mirror.FanoutTargets(nil), preparedSend(plain), mirror.NewOp(msg.KindText, chatID))
	if err == nil {
		t.Fatal("expected an error for empty targets")
	}
	if f.calls() != 0 {
		t.Errorf("Send called %d times for empty targets; want 0", f.calls())
	}
}

func TestDispatch_AllDevicesFailReturnsDispatchError(t *testing.T) {
	_, plain := textWrapper(t, 1)
	sentinel := errors.New("boom")
	f := &fakeSender{err: sentinel}
	_, err := mirror.Dispatch(context.Background(), f, testRail(t, []string{sibA}, knownPeer()),
		mirror.SingleTarget(peerID), preparedSend(plain), mirror.NewOp(msg.KindText, chatID))
	if !errors.Is(err, sentinel) {
		t.Errorf("Dispatch err = %v, want the underlying send error", err)
	}
	var derr *mirror.DispatchError
	if !errors.As(err, &derr) {
		t.Fatalf("Dispatch err = %v, want a *mirror.DispatchError", err)
	}
	if derr.Stage != mirror.StageSend {
		t.Errorf("Stage = %q, want %q (the send itself was attempted and refused)", derr.Stage, mirror.StageSend)
	}
	if len(f.railSends()) != 0 {
		t.Errorf("rail sent %d envelopes after a failed peer send; want 0", len(f.railSends()))
	}
}

func TestDispatch_PrepareFailureIsStagedAsPrepare(t *testing.T) {
	f := &fakeSender{}
	sentinel := errors.New("no session")
	prepare := func(_ context.Context, deviceID string) (backendapi.SendRequest, []byte, error) {
		return backendapi.SendRequest{}, nil, sentinel
	}
	_, err := mirror.Dispatch(context.Background(), f, testRail(t, nil, knownPeer()),
		mirror.SingleTarget(peerID), prepare, mirror.NewOp(msg.KindText, chatID))
	var derr *mirror.DispatchError
	if !errors.As(err, &derr) {
		t.Fatalf("Dispatch err = %v, want a *mirror.DispatchError", err)
	}
	if derr.Stage != mirror.StagePrepare {
		t.Errorf("Stage = %q, want %q", derr.Stage, mirror.StagePrepare)
	}
	if f.calls() != 0 {
		t.Errorf("Send called %d times when prepare failed; want 0 (nothing to send)", f.calls())
	}
}

func TestDispatch_NoSiblings_BehaviourNeutral(t *testing.T) {
	_, plain := textWrapper(t, 1)
	f := &fakeSender{}
	if _, err := mirror.Dispatch(context.Background(), f, testRail(t, nil, knownPeer()),
		mirror.SingleTarget(peerID), preparedSend(plain), mirror.NewOp(msg.KindText, chatID)); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if f.calls() != 1 {
		t.Fatalf("Send called %d times with zero siblings; want exactly 1 (no rail work at all)", f.calls())
	}
	got := f.reqs[0]
	if got.PeerID != peerID || !bytes.Equal(got.Payload, []byte(peerCipher)) || got.PresenceSource != backendapi.PresenceSourceHaoma {
		t.Errorf("peer request mutated by Dispatch: got %+v", got)
	}
}

func TestDispatch_OneSibling_SealedAndKindless(t *testing.T) {
	_, plain := textWrapper(t, 1)
	f := &fakeSender{}
	if _, err := mirror.Dispatch(context.Background(), f, testRail(t, []string{sibA}, knownPeer()),
		mirror.SingleTarget(peerID), preparedSend(plain), mirror.NewOp(msg.KindText, chatID)); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if f.calls() != 2 {
		t.Fatalf("Send called %d times; want 2 (peer + one sibling)", f.calls())
	}
	rails := f.railSends()
	if len(rails) != 1 || rails[0].PeerID != sibA {
		t.Fatalf("rail sends = %+v; want exactly one addressed to %q", rails, sibA)
	}
	r := rails[0]
	if bytes.Equal(r.Payload, []byte(peerCipher)) {
		t.Error("rail payload is the peer's ciphertext; a sibling holds a different session and cannot open it")
	}
	if bytes.Equal(r.Payload, plain) {
		t.Error("rail payload is the bare plaintext wrapper; it must be sealed")
	}
	if bytes.Contains(r.Payload, plain) {
		t.Error("rail payload contains the plaintext wrapper verbatim; seal is not applied")
	}
	if r.Kind != "" {
		t.Errorf("rail send Kind = %q, want empty — a plaintext wire-kind hint tells haomad this is a sync envelope", r.Kind)
	}
	if r.PresenceSource != "" {
		t.Errorf("rail send PresenceSource = %q, want empty — nothing about a rail envelope may travel in clear", r.PresenceSource)
	}
}

func TestDispatch_TwoSiblings_SerialFanOut(t *testing.T) {
	_, plain := textWrapper(t, 1)
	f := &fakeSender{}
	if _, err := mirror.Dispatch(context.Background(), f, testRail(t, []string{sibA, sibB}, knownPeer()),
		mirror.SingleTarget(peerID), preparedSend(plain), mirror.NewOp(msg.KindText, chatID)); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if f.calls() != 3 {
		t.Fatalf("Send called %d times; want 3 (peer + two siblings)", f.calls())
	}
	if f.overlap {
		t.Error("rail sends overlapped; fan-out must be serial")
	}
	order := []string{f.reqs[0].PeerID, f.reqs[1].PeerID, f.reqs[2].PeerID}
	want := []string{peerID, sibA, sibB}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("send order = %v, want %v (device first, then siblings in set order)", order, want)
		}
	}
	if bytes.Equal(f.reqs[1].Payload, f.reqs[2].Payload) {
		t.Error("both siblings got identical sealed bytes; identical payloads to two peerIDs hand haomad the sibling correlation")
	}
}

func TestDispatch_SealedPayloadOpensToTheOriginalWrapper(t *testing.T) {
	w, plain := textWrapper(t, 1)
	f := &fakeSender{}
	if _, err := mirror.Dispatch(context.Background(), f, testRail(t, []string{sibA}, knownPeer()),
		mirror.SingleTarget(peerID), preparedSend(plain), mirror.NewOp(msg.KindText, chatID)); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	rails := f.railSends()
	if len(rails) != 1 {
		t.Fatalf("rail sends = %d, want 1", len(rails))
	}
	key, err := syncrail.DeriveKey([]byte(identity))
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	env, err := syncrail.OpenEnvelope(key, rails[0].Payload)
	if err != nil {
		t.Fatalf("OpenEnvelope on the rail payload: %v (a sibling derives the same key from the copied identity)", err)
	}
	if env.Kind != syncrail.KindMirror {
		t.Errorf("envelope kind = %q, want %q", env.Kind, syncrail.KindMirror)
	}
	body, err := syncrail.ParseMirrorBody(env)
	if err != nil {
		t.Fatalf("ParseMirrorBody: %v", err)
	}
	if !bytes.Equal(body.Wrapper, plain) {
		t.Error("mirror body wrapper != the plaintext the peer was sent; the sibling would apply a different mutation")
	}
	if body.Fingerprint != peerFP {
		t.Errorf("fingerprint = %q, want %q — it is the sibling's only routing key", body.Fingerprint, peerFP)
	}
	if body.MsgID != w.MsgID {
		t.Errorf("msg id = %q, want %q (dedupe key)", body.MsgID, w.MsgID)
	}
	if body.DisplayTs != w.Ts {
		t.Errorf("display ts = %d, want %d", body.DisplayTs, w.Ts)
	}
	if body.WireKind != string(msg.KindText) {
		t.Errorf("wire kind = %q, want %q", body.WireKind, msg.KindText)
	}
	if body.OriginChatID != chatID || body.OriginPeerID != peerID {
		t.Errorf("origin audit fields = (%q,%q), want (%q,%q)", body.OriginChatID, body.OriginPeerID, chatID, peerID)
	}
}

func TestDispatch_NoMirrorKindSkipsTheRail(t *testing.T) {
	for _, kind := range []msg.Kind{msg.KindPresence, msg.KindRead, msg.KindCallOffer, msg.KindRotateRequest} {
		f := &fakeSender{}
		_, plain := textWrapper(t, 1)
		if _, err := mirror.Dispatch(context.Background(), f, testRail(t, []string{sibA}, knownPeer()),
			mirror.SingleTarget(peerID), preparedSend(plain), mirror.NewOp(kind, chatID)); err != nil {
			t.Fatalf("Dispatch(%s): %v", kind, err)
		}
		if f.calls() != 1 {
			t.Errorf("kind %s: Send called %d times; want 1 (peer only, no mirror)", kind, f.calls())
		}
	}
}

func TestDispatch_RailSendErrorLeavesPeerSendSuccessful(t *testing.T) {
	_, plain := textWrapper(t, 1)
	f := &fakeSender{
		resp:    backendapi.SendResponse{EnvelopeID: "env-peer"},
		railErr: errors.New("sibling unreachable"),
	}
	got, err := mirror.Dispatch(context.Background(), f, testRail(t, []string{sibA, sibB}, knownPeer()),
		mirror.SingleTarget(peerID), preparedSend(plain), mirror.NewOp(msg.KindText, chatID))
	if err != nil {
		t.Fatalf("Dispatch returned %v; a rail failure must never fail the peer send", err)
	}
	if got.Primary.EnvelopeID != "env-peer" {
		t.Errorf("Primary.EnvelopeID = %q, want env-peer (peer response unchanged by rail faults)", got.Primary.EnvelopeID)
	}
	if len(f.railSends()) != 2 {
		t.Errorf("rail attempts = %d, want 2 — one failing sibling must not abort the fan-out", len(f.railSends()))
	}
}

func TestDispatch_RailKeyErrorSkipsMirror(t *testing.T) {
	_, plain := textWrapper(t, 1)
	f := &fakeSender{}
	broken := mirror.NewRail(
		func() ([]byte, error) { return nil, errors.New("no identity loaded") },
		func() []string { return []string{sibA} },
		knownPeer(),
	)
	if _, err := mirror.Dispatch(context.Background(), f, broken, mirror.SingleTarget(peerID), preparedSend(plain), mirror.NewOp(msg.KindText, chatID)); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if f.calls() != 1 {
		t.Errorf("Send called %d times; want 1 (peer only — an unsealed mirror must never ship)", f.calls())
	}
}

func TestDispatch_MissingFingerprintSkipsMirror(t *testing.T) {
	_, plain := textWrapper(t, 1)
	f := &fakeSender{}
	fps := fakeFingerprints{byPeer: map[string]peerstate.MetaRecord{peerID: {Fingerprint: ""}}}
	if _, err := mirror.Dispatch(context.Background(), f, testRail(t, []string{sibA}, fps),
		mirror.SingleTarget(peerID), preparedSend(plain), mirror.NewOp(msg.KindText, chatID)); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if f.calls() != 1 {
		t.Errorf("Send called %d times; want 1 (peer only — no fingerprint, no routable mirror)", f.calls())
	}
}

func TestDispatch_FingerprintLookupErrorSkipsMirror(t *testing.T) {
	_, plain := textWrapper(t, 1)
	f := &fakeSender{}
	fps := fakeFingerprints{errFor: map[string]error{peerID: errors.New("store closed")}}
	if _, err := mirror.Dispatch(context.Background(), f, testRail(t, []string{sibA}, fps),
		mirror.SingleTarget(peerID), preparedSend(plain), mirror.NewOp(msg.KindText, chatID)); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if f.calls() != 1 {
		t.Errorf("Send called %d times; want 1 (peer only)", f.calls())
	}
}

func TestDispatch_UnwiredRailStillSendsToPeer(t *testing.T) {
	_, plain := textWrapper(t, 1)
	f := &fakeSender{}
	if _, err := mirror.Dispatch(context.Background(), f, mirror.Rail{}, mirror.SingleTarget(peerID), preparedSend(plain), mirror.NewOp(msg.KindText, chatID)); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if f.calls() != 1 {
		t.Errorf("Send called %d times; want 1 (peer only)", f.calls())
	}
}

func TestDispatch_EmptyPlaintextSkipsMirror(t *testing.T) {
	f := &fakeSender{}
	prepare := func(_ context.Context, deviceID string) (backendapi.SendRequest, []byte, error) {
		return backendapi.SendRequest{PeerID: deviceID, Payload: []byte(peerCipher)}, nil, nil
	}
	if _, err := mirror.Dispatch(context.Background(), f, testRail(t, []string{sibA}, knownPeer()),
		mirror.SingleTarget(peerID), prepare, mirror.NewOp(msg.KindText, chatID)); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if f.calls() != 1 {
		t.Errorf("Send called %d times; want 1 (peer only — an empty mirror body is worse than none)", f.calls())
	}
}

func TestDispatch_UnparseablePlaintextSkipsMirror(t *testing.T) {
	f := &fakeSender{}
	prepare := func(_ context.Context, deviceID string) (backendapi.SendRequest, []byte, error) {
		return backendapi.SendRequest{PeerID: deviceID, Payload: []byte(peerCipher)}, []byte("not a wrapper"), nil
	}
	if _, err := mirror.Dispatch(context.Background(), f, testRail(t, []string{sibA}, knownPeer()),
		mirror.SingleTarget(peerID), prepare, mirror.NewOp(msg.KindText, chatID)); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if f.calls() != 1 {
		t.Errorf("Send called %d times; want 1 (peer only)", f.calls())
	}
}

func TestDispatch_TwoDevices_BothSent_MirrorOnce(t *testing.T) {
	f := &fakeSender{}
	seqs := map[string]uint64{peerID: 5, peerID2: 6}
	prepare := func(_ context.Context, deviceID string) (backendapi.SendRequest, []byte, error) {
		_, plain := textWrapper(t, seqs[deviceID])
		return backendapi.SendRequest{PeerID: deviceID, Payload: []byte(peerCipher)}, plain, nil
	}
	got, err := mirror.Dispatch(context.Background(), f, testRail(t, []string{sibA}, knownPeer()),
		mirror.FanoutTargets([]string{peerID, peerID2}), prepare, mirror.NewOp(msg.KindText, chatID))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(got.EnvelopeIDs) != 2 {
		t.Fatalf("EnvelopeIDs = %v, want 2 entries (both devices accepted)", got.EnvelopeIDs)
	}
	if f.calls() != 3 {
		t.Fatalf("Send called %d times; want 3 (two devices + exactly one mirror)", f.calls())
	}
	rails := f.railSends()
	if len(rails) != 1 {
		t.Fatalf("rail sends = %d, want 1 — a two-device contact must not double the mirror", len(rails))
	}
}

func TestDispatch_OneDevicePrepareFails_OtherSucceeds(t *testing.T) {
	f := &fakeSender{}
	prepare := func(_ context.Context, deviceID string) (backendapi.SendRequest, []byte, error) {
		if deviceID == peerID {
			return backendapi.SendRequest{}, nil, errors.New("no session")
		}
		_, plain := textWrapper(t, 9)
		return backendapi.SendRequest{PeerID: deviceID, Payload: []byte(peerCipher)}, plain, nil
	}
	got, err := mirror.Dispatch(context.Background(), f, testRail(t, []string{sibA}, knownPeer()),
		mirror.FanoutTargets([]string{peerID, peerID2}), prepare, mirror.NewOp(msg.KindText, chatID))
	if err != nil {
		t.Fatalf("Dispatch: %v, want success — one device failing must not fail the send", err)
	}
	if len(got.EnvelopeIDs) != 1 {
		t.Fatalf("EnvelopeIDs = %v, want exactly 1 (peerID2 only)", got.EnvelopeIDs)
	}
	if len(got.Skipped) != 1 || got.Skipped[0].DeviceID != peerID || got.Skipped[0].Stage != mirror.StagePrepare {
		t.Fatalf("Skipped = %+v, want one entry for %q staged as %q", got.Skipped, peerID, mirror.StagePrepare)
	}
	if got.Primary.DeviceID != peerID2 {
		t.Errorf("Primary.DeviceID = %q, want %q (the only device that accepted)", got.Primary.DeviceID, peerID2)
	}

	if f.calls() != 2 {
		t.Fatalf("Send called %d times; want 2 (peerID2 + one mirror)", f.calls())
	}
}

func TestDispatch_OneDeviceSendFails_OtherSucceeds(t *testing.T) {
	f := &fakeSender{errFor: map[string]error{peerID: errors.New("backend refused")}}
	prepare := func(_ context.Context, deviceID string) (backendapi.SendRequest, []byte, error) {
		_, plain := textWrapper(t, 3)
		return backendapi.SendRequest{PeerID: deviceID, Payload: []byte(peerCipher)}, plain, nil
	}
	got, err := mirror.Dispatch(context.Background(), f, testRail(t, nil, knownPeer()),
		mirror.FanoutTargets([]string{peerID, peerID2}), prepare, mirror.NewOp(msg.KindText, chatID))
	if err != nil {
		t.Fatalf("Dispatch: %v, want success — one device failing must not fail the send", err)
	}
	if len(got.Skipped) != 1 || got.Skipped[0].Stage != mirror.StageSend {
		t.Fatalf("Skipped = %+v, want one entry staged as %q", got.Skipped, mirror.StageSend)
	}
	if got.Primary.DeviceID != peerID2 {
		t.Errorf("Primary.DeviceID = %q, want %q", got.Primary.DeviceID, peerID2)
	}
}

func TestDispatch_AllDevicesPrepareFail_SendFails(t *testing.T) {
	f := &fakeSender{}
	sentinel := errors.New("no session anywhere")
	prepare := func(_ context.Context, deviceID string) (backendapi.SendRequest, []byte, error) {
		return backendapi.SendRequest{}, nil, sentinel
	}
	_, err := mirror.Dispatch(context.Background(), f, testRail(t, nil, knownPeer()),
		mirror.FanoutTargets([]string{peerID, peerID2}), prepare, mirror.NewOp(msg.KindText, chatID))
	if !errors.Is(err, sentinel) {
		t.Errorf("Dispatch err = %v, want it to wrap %v", err, sentinel)
	}
	if f.calls() != 0 {
		t.Errorf("Send called %d times; want 0 (nothing could be prepared)", f.calls())
	}
}

func TestDispatch_FingerprintIteratesAcceptedDevices(t *testing.T) {
	f := &fakeSender{}
	fps := fakeFingerprints{byPeer: map[string]peerstate.MetaRecord{
		peerID:  {Fingerprint: ""},
		peerID2: {Fingerprint: peerFP},
	}}
	prepare := func(_ context.Context, deviceID string) (backendapi.SendRequest, []byte, error) {
		_, plain := textWrapper(t, 4)
		return backendapi.SendRequest{PeerID: deviceID, Payload: []byte(peerCipher)}, plain, nil
	}
	if _, err := mirror.Dispatch(context.Background(), f, testRail(t, []string{sibA}, fps),
		mirror.FanoutTargets([]string{peerID, peerID2}), prepare, mirror.NewOp(msg.KindText, chatID)); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	rails := f.railSends()
	if len(rails) != 1 {
		t.Fatalf("rail sends = %d, want 1 — the second accepted device's fingerprint must still produce a mirror", len(rails))
	}
	key, err := syncrail.DeriveKey([]byte(identity))
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	env, err := syncrail.OpenEnvelope(key, rails[0].Payload)
	if err != nil {
		t.Fatalf("OpenEnvelope: %v", err)
	}
	body, err := syncrail.ParseMirrorBody(env)
	if err != nil {
		t.Fatalf("ParseMirrorBody: %v", err)
	}
	if body.Fingerprint != peerFP {
		t.Errorf("fingerprint = %q, want %q — must come from the device that actually had one on file", body.Fingerprint, peerFP)
	}
}

func TestDispatch_EmptyWrapperOnASuccessfulSendIsSilent(t *testing.T) {
	logs := captureLogs(t, slog.LevelWarn)
	f := &fakeSender{resp: backendapi.SendResponse{EnvelopeID: "env-bundle"}}
	prepare := func(_ context.Context, deviceID string) (backendapi.SendRequest, []byte, error) {
		return backendapi.SendRequest{PeerID: deviceID, Payload: []byte(peerCipher)}, nil, nil
	}
	got, err := mirror.Dispatch(context.Background(), f, testRail(t, []string{sibA}, knownPeer()),
		mirror.SingleTarget(peerID), prepare, mirror.NewOp(msg.KindDeviceLinkIdentity, ""))
	if err != nil {
		t.Fatalf("Dispatch: %v — withholding the plaintext is the point of this call site, not a failure", err)
	}
	if got.Primary.EnvelopeID != "env-bundle" {
		t.Errorf("Primary.EnvelopeID = %q, want env-bundle", got.Primary.EnvelopeID)
	}
	if got.Primary.Seq != 0 {
		t.Errorf("Primary.Seq = %d, want 0 — there is no wrapper to read one from", got.Primary.Seq)
	}
	if out := logs.String(); out != "" {
		t.Errorf("a healthy send logged at WARN or above:\n%s", out)
	}
}

func TestDispatch_UnparseableWrapperStillWarns(t *testing.T) {
	logs := captureLogs(t, slog.LevelWarn)
	f := &fakeSender{}
	prepare := func(_ context.Context, deviceID string) (backendapi.SendRequest, []byte, error) {
		return backendapi.SendRequest{PeerID: deviceID, Payload: []byte(peerCipher)}, []byte("not a wrapper"), nil
	}
	if _, err := mirror.Dispatch(context.Background(), f, testRail(t, nil, knownPeer()),
		mirror.SingleTarget(peerID), prepare, mirror.NewOp(msg.KindDeviceLinkIdentity, "")); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !strings.Contains(logs.String(), "wrapper did not parse") {
		t.Errorf("a wrapper that failed to parse produced no warning; logs:\n%s", logs.String())
	}
}

func TestDispatch_MissingWrapperAlarmsEvenWithNoFingerprintAnywhere(t *testing.T) {
	logs := captureLogs(t, slog.LevelError)
	f := &fakeSender{}
	fps := fakeFingerprints{byPeer: map[string]peerstate.MetaRecord{peerID: {Fingerprint: ""}}}
	prepare := func(_ context.Context, deviceID string) (backendapi.SendRequest, []byte, error) {
		return backendapi.SendRequest{PeerID: deviceID, Payload: []byte(peerCipher)}, nil, nil
	}
	if _, err := mirror.Dispatch(context.Background(), f, testRail(t, []string{sibA}, fps),
		mirror.SingleTarget(peerID), prepare, mirror.NewOp(msg.KindText, chatID)); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !strings.Contains(logs.String(), "no plaintext wrapper on a mirror-class op") {
		t.Errorf("a mirror-class kind with no wrapper did not raise the ERROR; logs:\n%s", logs.String())
	}
	if len(f.railSends()) != 0 {
		t.Errorf("rail sends = %d, want 0 — nothing may ship without a wrapper", len(f.railSends()))
	}
}

func TestDispatch_MirroredWrapperComesFromThePrimaryDevice(t *testing.T) {
	f := &fakeSender{}

	fps := fakeFingerprints{byPeer: map[string]peerstate.MetaRecord{
		peerID:  {Fingerprint: ""},
		peerID2: {Fingerprint: peerFP},
	}}
	seqs := map[string]uint64{peerID: 11, peerID2: 22}
	prepare := func(_ context.Context, deviceID string) (backendapi.SendRequest, []byte, error) {
		_, plain := textWrapper(t, seqs[deviceID])
		return backendapi.SendRequest{PeerID: deviceID, Payload: []byte(peerCipher)}, plain, nil
	}
	got, err := mirror.Dispatch(context.Background(), f, testRail(t, []string{sibA}, fps),
		mirror.FanoutTargets([]string{peerID, peerID2}), prepare, mirror.NewOp(msg.KindText, chatID))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got.Primary.DeviceID != peerID || got.Primary.Seq != 11 {
		t.Fatalf("Primary = (%q, %d), want (%q, 11)", got.Primary.DeviceID, got.Primary.Seq, peerID)
	}
	rails := f.railSends()
	if len(rails) != 1 {
		t.Fatalf("rail sends = %d, want 1", len(rails))
	}
	key, err := syncrail.DeriveKey([]byte(identity))
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	env, err := syncrail.OpenEnvelope(key, rails[0].Payload)
	if err != nil {
		t.Fatalf("OpenEnvelope: %v", err)
	}
	body, err := syncrail.ParseMirrorBody(env)
	if err != nil {
		t.Fatalf("ParseMirrorBody: %v", err)
	}
	if body.Fingerprint != peerFP {
		t.Errorf("fingerprint = %q, want %q — taken from the device that had one", body.Fingerprint, peerFP)
	}
	mirrored, err := msg.Unmarshal(body.Wrapper)
	if err != nil {
		t.Fatalf("the mirrored wrapper did not parse: %v", err)
	}
	if mirrored.Seq != got.Primary.Seq {
		t.Errorf("mirrored wrapper seq = %d, origin reports %d — one message must not carry two sender seqs",
			mirrored.Seq, got.Primary.Seq)
	}
	if body.OriginPeerID != got.Primary.DeviceID {
		t.Errorf("OriginPeerID = %q, want %q — the audit field must name the device the reply reports",
			body.OriginPeerID, got.Primary.DeviceID)
	}
}

func TestDispatch_NoAcceptedDeviceHasFingerprint_SkipsMirror(t *testing.T) {
	f := &fakeSender{}
	fps := fakeFingerprints{byPeer: map[string]peerstate.MetaRecord{
		peerID:  {Fingerprint: ""},
		peerID2: {Fingerprint: ""},
	}}
	prepare := func(_ context.Context, deviceID string) (backendapi.SendRequest, []byte, error) {
		_, plain := textWrapper(t, 2)
		return backendapi.SendRequest{PeerID: deviceID, Payload: []byte(peerCipher)}, plain, nil
	}
	if _, err := mirror.Dispatch(context.Background(), f, testRail(t, []string{sibA}, fps),
		mirror.FanoutTargets([]string{peerID, peerID2}), prepare, mirror.NewOp(msg.KindText, chatID)); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(f.railSends()) != 0 {
		t.Errorf("rail sends = %d, want 0 — no accepted device had a fingerprint", len(f.railSends()))
	}
}

func TestDispatch_EnvelopeIDsHoldNoRailEnvelope(t *testing.T) {
	want := []string{"env-" + peerID, "env-" + peerID2}
	check := func(t *testing.T, f *fakeSender, got mirror.Result) {
		t.Helper()
		if len(got.EnvelopeIDs) != len(want) || got.EnvelopeIDs[0] != want[0] || got.EnvelopeIDs[1] != want[1] {
			t.Fatalf("EnvelopeIDs = %v, want %v (no rail envelope)", got.EnvelopeIDs, want)
		}
		if n := len(f.railSends()); n != 2 {
			t.Fatalf("rail sends = %d, want 2 — the test needs the rail to run", n)
		}
		if got.Primary.EnvelopeID != want[0] {
			t.Errorf("Primary.EnvelopeID = %q, want %q", got.Primary.EnvelopeID, want[0])
		}
	}

	t.Run("text", func(t *testing.T) {
		f := &fakeSender{}
		_, plain := textWrapper(t, 3)
		got, err := mirror.Dispatch(context.Background(), f, testRail(t, []string{sibA, sibB}, knownPeer()),
			mirror.FanoutTargets([]string{peerID, peerID2}), preparedSend(plain), mirror.NewOp(msg.KindText, chatID))
		if err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		check(t, f, got)
	})

	t.Run("file offer", func(t *testing.T) {
		f := &fakeSender{}
		_, plain := fileOfferWrapper(t, 3)
		got, err := mirror.Dispatch(context.Background(), f, testRail(t, []string{sibA, sibB}, knownPeer()),
			mirror.FanoutTargets([]string{peerID, peerID2}), preparedSend(plain),
			mirror.NewFileOfferOp(chatID, buildSiblingOffer(t)))
		if err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		check(t, f, got)
	})
}
