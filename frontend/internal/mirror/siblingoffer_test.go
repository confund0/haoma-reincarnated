package mirror_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"haoma-frontend/internal/mirror"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/syncrail"
)

const contactToken = "contact-token-3a9c"

func fileOfferWrapper(t *testing.T, seq uint64) (*msg.Wrapper, []byte) {
	t.Helper()
	w, err := msg.BuildFileOffer(seq, 1700000000, "file-msg-id", contactToken, "/files/"+contactToken,
		"photo.jpg", 4096, "image/jpeg", "c0ffee", "a caption", 0)
	if err != nil {
		t.Fatalf("BuildFileOffer: %v", err)
	}
	plain, err := msg.Marshal(w)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return w, plain
}

func siblingToken(sib string) string { return "own-token-for-" + sib }

func buildSiblingOffer(t *testing.T) mirror.SiblingOfferFunc {
	return func(sib string, primary *msg.Wrapper) ([]byte, syncrail.SiblingOffer, error) {
		tok := siblingToken(sib)
		w, err := msg.BuildFileOffer(primary.Seq, primary.Ts, primary.MsgID, tok, "/files/"+tok,
			"photo.jpg", 4096, "image/jpeg", "c0ffee", "a caption", 0)
		if err != nil {
			t.Fatalf("BuildFileOffer: %v", err)
		}
		plain, err := msg.Marshal(w)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		return plain, syncrail.SiblingOffer{
			StagingID:        "staging-for-" + sib,
			Token:            tok,
			Key:              bytes.Repeat([]byte{0x42}, syncrail.KeySize),
			Nonce:            bytes.Repeat([]byte{0x24}, syncrail.NonceSize),
			Sha256Ciphertext: "c0ffee",
			BlobSize:         5000,
			PlaintextSize:    4000,
		}, nil
	}
}

func openRailBody(t *testing.T, payload []byte) syncrail.MirrorBody {
	t.Helper()
	key, err := syncrail.DeriveKey([]byte(identity))
	if err != nil {
		t.Fatal(err)
	}
	env, err := syncrail.OpenEnvelope(key, payload)
	if err != nil {
		t.Fatalf("OpenEnvelope: %v", err)
	}
	body, err := syncrail.ParseMirrorBody(env)
	if err != nil {
		t.Fatalf("ParseMirrorBody: %v", err)
	}
	return body
}

func TestDispatch_FileOffer_EachSiblingGetsItsOwnOffer(t *testing.T) {
	primary, plain := fileOfferWrapper(t, 9)
	f := &fakeSender{}
	if _, err := mirror.Dispatch(context.Background(), f, testRail(t, []string{sibA, sibB}, knownPeer()),
		mirror.SingleTarget(peerID), preparedSend(plain), mirror.NewFileOfferOp(chatID, buildSiblingOffer(t))); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	rails := f.railSends()
	if len(rails) != 2 {
		t.Fatalf("rail sends = %d, want one per sibling (2)", len(rails))
	}
	for i, sib := range []string{sibA, sibB} {
		if rails[i].PeerID != sib {
			t.Fatalf("rail send %d went to %q, want %q", i, rails[i].PeerID, sib)
		}
		body := openRailBody(t, rails[i].Payload)
		if body.SiblingOffer == nil {
			t.Fatalf("%s got a plain mirror, not a sibling offer", sib)
		}
		if body.SiblingOffer.Token != siblingToken(sib) {
			t.Errorf("%s: offer token = %q, want its own %q", sib, body.SiblingOffer.Token, siblingToken(sib))
		}
		if err := body.SiblingOffer.Check(); err != nil {
			t.Errorf("%s: offer cannot open a blob: %v", sib, err)
		}
		w, err := msg.Unmarshal(body.Wrapper)
		if err != nil {
			t.Fatalf("%s: wrapper: %v", sib, err)
		}
		fo, err := w.FileOffer()
		if err != nil {
			t.Fatalf("%s: file offer body: %v", sib, err)
		}
		if fo.Token != siblingToken(sib) {
			t.Errorf("%s: wrapper token = %q, want its own %q", sib, fo.Token, siblingToken(sib))
		}
		if fo.Caption != "a caption" {
			t.Errorf("%s: caption = %q, want it kept", sib, fo.Caption)
		}
		if w.MsgID != primary.MsgID || w.Seq != primary.Seq || w.Ts != primary.Ts {
			t.Errorf("%s: wrapper (msg_id=%s seq=%d ts=%d) differs from the primary (msg_id=%s seq=%d ts=%d)",
				sib, w.MsgID, w.Seq, w.Ts, primary.MsgID, primary.Seq, primary.Ts)
		}
		if body.MsgID != primary.MsgID {
			t.Errorf("%s: body msg id = %q, want %q", sib, body.MsgID, primary.MsgID)
		}
		if bytes.Contains(body.Wrapper, []byte(contactToken)) {
			t.Errorf("%s: the contact's token reached a sibling", sib)
		}
	}
	if bytes.Equal(rails[0].Payload, rails[1].Payload) {
		t.Error("two siblings got identical ciphertext — sealed once for the whole loop")
	}
}

func TestDispatch_FileOffer_WithoutASiblingOfferReachesNoSibling(t *testing.T) {
	logs := captureLogs(t, slog.LevelDebug)
	_, plain := fileOfferWrapper(t, 1)
	f := &fakeSender{}
	if _, err := mirror.Dispatch(context.Background(), f, testRail(t, []string{sibA}, knownPeer()),
		mirror.SingleTarget(peerID), preparedSend(plain), mirror.NewOp(msg.KindFileOffer, chatID)); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if n := len(f.railSends()); n != 0 {
		t.Fatalf("rail sends = %d, want 0 — the contact's offer was mirrored", n)
	}
	if !strings.Contains(logs.String(), "file offer has no sibling offer to send") {
		t.Errorf("the refusal was not logged:\n%s", logs.String())
	}
}

func TestDispatch_FileOffer_OneSiblingFailsTheOtherStillGetsItsOffer(t *testing.T) {
	_, plain := fileOfferWrapper(t, 1)
	build := buildSiblingOffer(t)
	f := &fakeSender{}
	res, err := mirror.Dispatch(context.Background(), f, testRail(t, []string{sibA, sibB}, knownPeer()),
		mirror.SingleTarget(peerID), preparedSend(plain),
		mirror.NewFileOfferOp(chatID, func(sib string, primary *msg.Wrapper) ([]byte, syncrail.SiblingOffer, error) {
			if sib == sibA {
				return nil, syncrail.SiblingOffer{}, errors.New("no token minted")
			}
			return build(sib, primary)
		}))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(res.EnvelopeIDs) != 1 {
		t.Fatalf("contact send lost: %v", res.EnvelopeIDs)
	}
	rails := f.railSends()
	if len(rails) != 1 || rails[0].PeerID != sibB {
		t.Fatalf("rail sends = %v, want exactly one to %s", rails, sibB)
	}
	if body := openRailBody(t, rails[0].Payload); body.SiblingOffer == nil || body.SiblingOffer.Token != siblingToken(sibB) {
		t.Fatalf("%s did not get its own offer", sibB)
	}
}

func TestDispatch_FileKeyReachesTheContactOnly(t *testing.T) {
	if c, _ := mirror.Classify(msg.KindFileKey); c != mirror.ClassNoMirror {
		t.Fatalf("KindFileKey class = %v, want ClassNoMirror", c)
	}
	w, err := msg.BuildFileKey(1, 1700000000, "key-msg-id", contactToken,
		bytes.Repeat([]byte{1}, syncrail.KeySize), bytes.Repeat([]byte{2}, syncrail.NonceSize), 0)
	if err != nil {
		t.Fatalf("BuildFileKey: %v", err)
	}
	plain, err := msg.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSender{}
	if _, err := mirror.Dispatch(context.Background(), f, testRail(t, []string{sibA}, knownPeer()),
		mirror.SingleTarget(peerID), preparedSend(plain), mirror.NewOp(msg.KindFileKey, chatID)); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if n := len(f.railSends()); n != 0 {
		t.Fatalf("rail sends = %d, want 0 — the contact's file key reached a sibling", n)
	}
}

func TestDispatch_FileOffer_NoSiblingsNeverBuildsAnOffer(t *testing.T) {
	_, plain := fileOfferWrapper(t, 1)
	f := &fakeSender{}
	called := false
	if _, err := mirror.Dispatch(context.Background(), f, testRail(t, nil, knownPeer()),
		mirror.SingleTarget(peerID), preparedSend(plain),
		mirror.NewFileOfferOp(chatID, func(string, *msg.Wrapper) ([]byte, syncrail.SiblingOffer, error) {
			called = true
			return nil, syncrail.SiblingOffer{}, errors.New("unreachable")
		})); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if called || f.calls() != 1 {
		t.Fatalf("builder called=%t sends=%d, want false and 1", called, f.calls())
	}
}

func TestDispatch_FileOffer_ReportsOnlyTheSiblingsThatGotTheirOffer(t *testing.T) {
	_, plain := fileOfferWrapper(t, 1)
	const sibC = "sibling-c"
	build := buildSiblingOffer(t)
	f := &fakeSender{errFor: map[string]error{sibB: errors.New("router refused")}}
	res, err := mirror.Dispatch(context.Background(), f, testRail(t, []string{sibA, sibB, sibC}, knownPeer()),
		mirror.SingleTarget(peerID), preparedSend(plain),
		mirror.NewFileOfferOp(chatID, func(sib string, primary *msg.Wrapper) ([]byte, syncrail.SiblingOffer, error) {
			if sib == sibA {
				return nil, syncrail.SiblingOffer{}, errors.New("no blob staged")
			}
			return build(sib, primary)
		}))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(res.SiblingOffersSent) != 1 || res.SiblingOffersSent[0] != sibC {
		t.Fatalf("SiblingOffersSent = %v, want [%s]", res.SiblingOffersSent, sibC)
	}

	_, text := textWrapper(t, 1)
	res, err = mirror.Dispatch(context.Background(), &fakeSender{}, testRail(t, []string{sibA}, knownPeer()),
		mirror.SingleTarget(peerID), preparedSend(text), mirror.NewOp(msg.KindText, chatID))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.SiblingOffersSent) != 0 {
		t.Fatalf("a text mirror reported sibling offers: %v", res.SiblingOffersSent)
	}
}

func TestDispatch_FileReceiptReachesTheContactOnly(t *testing.T) {
	if c, _ := mirror.Classify(msg.KindFileReceipt); c != mirror.ClassNoMirror {
		t.Fatalf("KindFileReceipt class = %v, want ClassNoMirror", c)
	}
	w, err := msg.BuildFileReceipt(1, 1700000000, "receipt-msg-id", contactToken, 0)
	if err != nil {
		t.Fatalf("BuildFileReceipt: %v", err)
	}
	plain, err := msg.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSender{}
	if _, err := mirror.Dispatch(context.Background(), f, testRail(t, []string{sibA}, knownPeer()),
		mirror.SingleTarget(peerID), preparedSend(plain), mirror.NewOp(msg.KindFileReceipt, chatID)); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if n := len(f.railSends()); n != 0 {
		t.Fatalf("rail sends = %d, want 0 — the contact's token reached a sibling", n)
	}
	if f.calls() != 1 {
		t.Fatalf("sends = %d, want 1: the contact", f.calls())
	}
}
