package files_test

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/files"
)

func takeHeld(t *testing.T, mgr *files.Manager, o files.SiblingOffer) {
	t.Helper()
	if err := mgr.PutMetaAndSiblingOffer(heldMeta(o), o); err != nil {
		t.Fatalf("PutMetaAndSiblingOffer: %v", err)
	}
}

func heldMeta(o files.SiblingOffer) files.Metadata {
	return files.Metadata{
		MsgID: o.MsgID, ChatID: chat.ChatID("0123456789abcdef0123456789abcdef"),
		Direction: files.DirOut, State: files.StatePending,
		FetchedFromSibling: true, StagingID: o.StagingID,
	}
}

func heldSiblingOffer(msgID string) files.SiblingOffer {
	return files.SiblingOffer{
		MsgID:         msgID,
		ServingPeerID: "0000000000000000000000000000d0d0",
		StagingID:     "staging-of-" + msgID,
		Token:         "sibling-token-77aa",
		UrlPath:       "/files/sibling-token-77aa",
		Key:           bytes.Repeat([]byte{0x3c}, 32),
		Nonce:         bytes.Repeat([]byte{0x7e}, 24),
		BlobSize:      5000,
		PlaintextSize: 4000,

		Sha256Ciphertext: "c0ffee",
	}
}

func TestSiblingOffer_PutGetListDelete(t *testing.T) {
	mgr, _, _ := newManager(t)
	a, b := heldSiblingOffer("msg-a"), heldSiblingOffer("msg-b")
	for _, o := range []files.SiblingOffer{a, b} {
		takeHeld(t, mgr, o)
	}
	got, err := mgr.GetSiblingOffer("msg-a")
	if err != nil {
		t.Fatalf("GetSiblingOffer: %v", err)
	}
	if got.Token != a.Token || got.UrlPath != a.UrlPath || got.ServingPeerID != a.ServingPeerID ||
		!bytes.Equal(got.Key, a.Key) || !bytes.Equal(got.Nonce, a.Nonce) {
		t.Fatalf("record changed on the way through the store")
	}
	list, err := mgr.ListSiblingOffers()
	if err != nil || len(list) != 2 {
		t.Fatalf("ListSiblingOffers = %d, %v; want 2", len(list), err)
	}
	if err := mgr.DeleteSiblingOffer("msg-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.GetSiblingOffer("msg-a"); !errors.Is(err, files.ErrSiblingOfferNotFound) {
		t.Fatalf("after delete: err = %v, want ErrSiblingOfferNotFound", err)
	}
}

func TestSiblingOffer_PutRefusesAnIncompleteRecord(t *testing.T) {
	mgr, _, _ := newManager(t)
	spoil := map[string]func(*files.SiblingOffer){
		"no msg id":         func(o *files.SiblingOffer) { o.MsgID = "" },
		"no serving device": func(o *files.SiblingOffer) { o.ServingPeerID = "" },
		"no token":          func(o *files.SiblingOffer) { o.Token = "" },
		"no url path":       func(o *files.SiblingOffer) { o.UrlPath = "" },
		"no key":            func(o *files.SiblingOffer) { o.Key = nil },
		"no nonce":          func(o *files.SiblingOffer) { o.Nonce = nil },
	}
	for name, f := range spoil {
		t.Run(name, func(t *testing.T) {
			o := heldSiblingOffer("msg-x")
			f(&o)
			if err := mgr.PutMetaAndSiblingOffer(heldMeta(o), o); err == nil {
				t.Fatal("an incomplete record was held")
			}
		})
	}
}

func TestSiblingOffer_DeleteMetaTakesTheKeyWithIt(t *testing.T) {
	mgr, _, _ := newManager(t)
	o := heldSiblingOffer("msg-with-row")
	takeHeld(t, mgr, o)
	if err := mgr.DeleteMeta(o.MsgID); err != nil {
		t.Fatalf("DeleteMeta: %v", err)
	}
	if _, err := mgr.GetSiblingOffer(o.MsgID); !errors.Is(err, files.ErrSiblingOfferNotFound) {
		t.Fatalf("sibling offer outlived DeleteMeta (err=%v)", err)
	}
}

func TestSiblingOffer_NotSeenByTheMetadataScans(t *testing.T) {
	mgr, _, _ := newManager(t)
	takeHeld(t, mgr, heldSiblingOffer("msg-only-offer"))
	rows, err := mgr.ListInboundInFlight()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("ListInboundInFlight read a sibling offer as a row: %+v", rows)
	}

	m, err := mgr.GetMeta("msg-only-offer")
	if err != nil || !m.FetchedFromSibling || len(m.KeyBytes) != 0 {
		t.Fatalf("GetMeta = %+v, %v; want the take's own row, holding no key", m, err)
	}
}

func TestSiblingOffer_NeverRendersItsSecrets(t *testing.T) {
	o := heldSiblingOffer("msg-log")
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})).
		Debug("held", slog.Any("offer", o))
	out := buf.String() + fmt.Sprintf("%v %+v %#v", o, o, o)
	for _, secret := range []string{o.Token, hex.EncodeToString(o.Key), hex.EncodeToString(o.Nonce), fmt.Sprint(o.Key)} {
		if strings.Contains(out, secret) {
			t.Fatalf("a secret reached the rendered output: %q in %s", secret, out)
		}
	}
}

func TestSiblingOffer_StagingIDResolvesAfterTheSeal(t *testing.T) {
	mgr, _, _ := newManager(t)
	o := heldSiblingOffer("msg-sealed")
	takeHeld(t, mgr, o)
	if err := mgr.DeleteSiblingOffer(o.MsgID); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.GetSiblingOffer(o.MsgID); !errors.Is(err, files.ErrSiblingOfferNotFound) {
		t.Fatalf("the key outlived the seal (err=%v)", err)
	}
	if got, err := mgr.SiblingOfferMsgID(o.StagingID); err != nil || got != o.MsgID {
		t.Fatalf("SiblingOfferMsgID = %q, %v; want %q", got, err, o.MsgID)
	}
}

func TestSiblingOffer_StagingIDGoesWithTheRow(t *testing.T) {
	for _, sealed := range []bool{false, true} {
		mgr, _, _ := newManager(t)
		o := heldSiblingOffer("msg-row")
		meta := files.Metadata{
			MsgID: o.MsgID, ChatID: chat.ChatID("0123456789abcdef0123456789abcdef"),
			Direction: files.DirOut, State: files.StatePending,
			FetchedFromSibling: true, StagingID: o.StagingID,
		}
		if err := mgr.PutMetaAndSiblingOffer(meta, o); err != nil {
			t.Fatal(err)
		}
		if sealed {
			if err := mgr.DeleteSiblingOffer(o.MsgID); err != nil {
				t.Fatal(err)
			}
		}
		if err := mgr.DeleteMeta(o.MsgID); err != nil {
			t.Fatal(err)
		}
		if _, err := mgr.SiblingOfferMsgID(o.StagingID); !errors.Is(err, files.ErrSiblingOfferNotFound) {
			t.Fatalf("sealed=%t: the staging-id entry outlived the row (err=%v)", sealed, err)
		}
		if _, err := mgr.GetSiblingOffer(o.MsgID); !errors.Is(err, files.ErrSiblingOfferNotFound) {
			t.Fatalf("sealed=%t: the key outlived the row (err=%v)", sealed, err)
		}
	}
}

func TestPutMetaAndSiblingOffer_AllOrNothing(t *testing.T) {
	mgr, _, _ := newManager(t)
	meta := files.Metadata{
		MsgID: "msg-take", ChatID: chat.ChatID("0123456789abcdef0123456789abcdef"),
		Direction: files.DirOut, State: files.StatePending, FetchedFromSibling: true,
	}
	bad := heldSiblingOffer("msg-take")
	bad.Key = nil
	if err := mgr.PutMetaAndSiblingOffer(meta, bad); err == nil {
		t.Fatal("an offer with no key was taken")
	}
	if _, err := mgr.GetMeta("msg-take"); !errors.Is(err, files.ErrMetaNotFound) {
		t.Fatalf("a refused take left a metadata row behind (err=%v)", err)
	}

	failing := heldSiblingOffer("msg-take")
	failing.StagingID = strings.Repeat("s", 70000)
	if err := mgr.PutMetaAndSiblingOffer(meta, failing); err == nil {
		t.Fatal("precondition: the oversized staging-id entry was written")
	}
	if _, err := mgr.GetMeta("msg-take"); !errors.Is(err, files.ErrMetaNotFound) {
		t.Fatalf("a take that failed inside the transaction left a metadata row behind (err=%v)", err)
	}
	if _, err := mgr.GetSiblingOffer("msg-take"); !errors.Is(err, files.ErrSiblingOfferNotFound) {
		t.Fatalf("a take that failed inside the transaction left the key behind (err=%v)", err)
	}
	if err := mgr.PutMetaAndSiblingOffer(meta, heldSiblingOffer("msg-take")); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.GetMeta("msg-take"); err != nil {
		t.Fatalf("no metadata row after a take: %v", err)
	}
	if _, err := mgr.GetSiblingOffer("msg-take"); err != nil {
		t.Fatalf("no held offer after a take: %v", err)
	}
	other := heldSiblingOffer("msg-other")
	if err := mgr.PutMetaAndSiblingOffer(meta, other); err == nil {
		t.Fatal("a take with two different msg ids was written")
	}
}

func TestMarkSiblingFileSealed_RefusesAFileDeletedMeanwhile(t *testing.T) {
	mgr, _, _ := newManager(t)
	o := heldSiblingOffer("msg-mark")
	takeHeld(t, mgr, o)
	sealed := heldMeta(o)
	sealed.State = files.StateReady
	sealed.SealedPath = "/sealed/path"

	if err := mgr.DeleteMeta(o.MsgID); err != nil {
		t.Fatal(err)
	}
	if err := mgr.MarkSiblingFileSealed(sealed); !errors.Is(err, files.ErrSiblingFileGone) {
		t.Fatalf("MarkSiblingFileSealed after a delete: err = %v, want ErrSiblingFileGone", err)
	}
	if _, err := mgr.GetMeta(o.MsgID); !errors.Is(err, files.ErrMetaNotFound) {
		t.Fatalf("a deleted file's metadata came back (err=%v)", err)
	}

	o2 := heldSiblingOffer("msg-mark-2")
	takeHeld(t, mgr, o2)
	sealed2 := heldMeta(o2)
	sealed2.State = files.StateReady
	if err := mgr.MarkSiblingFileSealed(sealed2); err != nil {
		t.Fatalf("MarkSiblingFileSealed on a live file: %v", err)
	}
	if m, err := mgr.GetMeta(o2.MsgID); err != nil || m.State != files.StateReady {
		t.Fatalf("metadata after the sealed write: %+v, %v", m, err)
	}
}

func TestSiblingOffer_NoVerbRendersItsSecrets(t *testing.T) {
	o := heldSiblingOffer("msg-verbs")
	secrets := []string{o.Token, hex.EncodeToString(o.Key), hex.EncodeToString(o.Nonce),
		fmt.Sprint(o.Key), fmt.Sprint(o.Nonce), fmt.Sprintf("%x", o.Key)}
	for _, verb := range []string{"%d", "%v", "%+v", "%#v", "%s", "%x"} {
		for _, arg := range []any{o, &o} {
			out := fmt.Sprintf(verb, arg)
			for _, secret := range secrets {
				if strings.Contains(out, secret) {
					t.Fatalf("%s on %T rendered a secret: %q in %s", verb, arg, secret, out)
				}
			}
		}
	}
}

func TestSiblingOffer_WritesItsIdsShort(t *testing.T) {
	o := heldSiblingOffer("5c7b2897f04bdf9ebcd91c769cf84344")
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})).
		Debug("held", slog.Any("offer", o))
	logged := buf.String()
	out := logged + fmt.Sprintf("%v %+v %#v", o, o, o)
	for _, full := range []string{o.MsgID, o.ServingPeerID, o.StagingID} {
		if strings.Contains(out, full) {
			t.Errorf("a full id reached the rendered output: %q in %s", full, out)
		}
	}
	for _, want := range []string{"offer.msg_id=5c7b2897…", "offer.serving_peer_id=00000000…", "offer.staging_id=staging-…"} {
		if !strings.Contains(logged, want) {
			t.Errorf("expected %q in the log line: %s", want, logged)
		}
	}
}
