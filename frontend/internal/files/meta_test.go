package files_test

import (
	"errors"
	"sync"
	"testing"

	"haoma-frontend/internal/files"
)

func contactMeta(msgID string) files.Metadata {
	return files.Metadata{
		MsgID: msgID, ChatID: testChatID, Direction: files.DirIn,
		Token: "contact-token-1", OfferPeerID: "0000000000000000000000000000cccc",
		OriginalName: "a.pdf", Size: 1024, Sha256Ciphertext: "c0ffee",
		State: files.StateDownloading,
	}
}

func TestPutMetaIfAbsent_SecondWriteIsRefused(t *testing.T) {
	mgr, _, _ := newManager(t)
	first := contactMeta(testMsgA)
	if err := mgr.PutMetaIfAbsent(first); err != nil {
		t.Fatalf("first write: %v", err)
	}
	second := contactMeta(testMsgA)
	second.Token = "contact-token-2"
	second.State = files.StatePending
	if err := mgr.PutMetaIfAbsent(second); !errors.Is(err, files.ErrMetaHeld) {
		t.Fatalf("second write: err = %v, want ErrMetaHeld", err)
	}
	got, err := mgr.GetMeta(testMsgA)
	if err != nil {
		t.Fatal(err)
	}
	if got.Token != first.Token || got.State != first.State {
		t.Fatalf("the refused write changed the row: token=%q state=%q", got.Token, got.State)
	}
	if _, err := mgr.GetMetaByToken("contact-token-2"); !errors.Is(err, files.ErrMetaNotFound) {
		t.Fatalf("the refused write left a token entry (err=%v)", err)
	}
}

func TestPutMetaIfAbsent_RowWrittenBetweenReadAndWrite(t *testing.T) {
	mgr, _, _ := newManager(t)
	other := contactMeta(testMsgA)
	other.Token = "other-writer-token"
	var once sync.Once
	files.SetPutIfAbsentCheckpoint(t, func(string) {
		once.Do(func() {
			if err := mgr.PutMeta(other); err != nil {
				t.Errorf("the other writer failed: %v", err)
			}
		})
	})
	if err := mgr.PutMetaIfAbsent(contactMeta(testMsgA)); !errors.Is(err, files.ErrMetaHeld) {
		t.Fatalf("err = %v, want ErrMetaHeld — two writers both took one msg id", err)
	}
	got, err := mgr.GetMeta(testMsgA)
	if err != nil {
		t.Fatal(err)
	}
	if got.Token != other.Token {
		t.Fatalf("token = %q, want the other writer's %q — the late write landed over it", got.Token, other.Token)
	}
}

func TestPutMetaAndSiblingOffer_RefusedWhenMetadataIsHeld(t *testing.T) {
	for _, race := range []bool{false, true} {
		mgr, _, _ := newManager(t)
		o := heldSiblingOffer(testMsgA)
		held := contactMeta(testMsgA)
		if race {
			var once sync.Once
			files.SetPutIfAbsentCheckpoint(t, func(string) {
				once.Do(func() {
					if err := mgr.PutMeta(held); err != nil {
						t.Errorf("the contact's write failed: %v", err)
					}
				})
			})
		} else if err := mgr.PutMeta(held); err != nil {
			t.Fatal(err)
		}
		if err := mgr.PutMetaAndSiblingOffer(heldMeta(o), o); !errors.Is(err, files.ErrMetaHeld) {
			t.Fatalf("race=%t: err = %v, want ErrMetaHeld", race, err)
		}
		got, err := mgr.GetMeta(testMsgA)
		if err != nil {
			t.Fatal(err)
		}
		if got.Direction != files.DirIn || got.FetchedFromSibling || got.Token != held.Token {
			t.Fatalf("race=%t: the contact's row was overwritten: %+v", race, got)
		}
		if _, err := mgr.GetSiblingOffer(testMsgA); !errors.Is(err, files.ErrSiblingOfferNotFound) {
			t.Fatalf("race=%t: a refused sibling offer left its key held (err=%v)", race, err)
		}
		if _, err := mgr.SiblingOfferMsgID(o.StagingID); !errors.Is(err, files.ErrSiblingOfferNotFound) {
			t.Fatalf("race=%t: a refused sibling offer left its staging-id entry (err=%v)", race, err)
		}
	}
}
