package files_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"haoma-frontend/internal/events"
	"haoma-frontend/internal/files"
	"haoma-frontend/internal/logid"
)

const (
	gapRequester = "0000000000000000000000000000d0d0"
	gapStaging   = "5a1b2c3d4e5f60718293a4b5c6d7e8f9"
)

func keptOffer(stagingID string) files.GapOffer {
	return files.GapOffer{
		MsgID:            testMsgA,
		RequesterPeerID:  gapRequester,
		StagingID:        stagingID,
		Token:            "gap-token-1a2b3c4d5e6f",
		UrlPath:          "/files/gap-token-1a2b3c4d5e6f",
		Key:              bytes.Repeat([]byte{0xa7}, 32),
		Nonce:            bytes.Repeat([]byte{0x5c}, 24),
		BlobSize:         5000,
		PlaintextSize:    4000,
		Sha256Ciphertext: "c0ffee",
		StagedAt:         1742643890,
	}
}

func readyInbound(t *testing.T, mgr *files.Manager) {
	t.Helper()
	if err := mgr.PutMeta(files.Metadata{
		MsgID: testMsgA, ChatID: testChatID, Direction: files.DirIn, State: files.StateReady,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestGapOffer_RecordAndDrop(t *testing.T) {
	mgr, _, _ := newManager(t)
	readyInbound(t, mgr)
	if err := mgr.RecordGapOffer(keptOffer(gapStaging)); err != nil {
		t.Fatal(err)
	}
	got, err := mgr.GetGapOffer(testMsgA, gapRequester)
	if err != nil {
		t.Fatal(err)
	}
	if got.StagingID != gapStaging || !bytes.Equal(got.Key, keptOffer(gapStaging).Key) || got.StagedAt != 1742643890 {
		t.Fatalf("kept offer did not round-trip: %v", got)
	}
	meta, _ := mgr.GetMeta(testMsgA)
	if len(meta.SiblingStagingIDs) != 1 || meta.SiblingStagingIDs[0] != gapStaging {
		t.Fatalf("SiblingStagingIDs = %v", meta.SiblingStagingIDs)
	}

	if err := mgr.DeleteGapOffer(testMsgA, gapRequester, gapStaging); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.GetGapOffer(testMsgA, gapRequester); !errors.Is(err, files.ErrGapOfferNotFound) {
		t.Fatalf("the kept offer and its key outlived the delete: %v", err)
	}
	meta, _ = mgr.GetMeta(testMsgA)
	if len(meta.SiblingStagingIDs) != 1 {
		t.Fatalf("SiblingStagingIDs = %v; the id stays until its blob is dropped", meta.SiblingStagingIDs)
	}
	if err := mgr.RemoveSiblingStagingID(testMsgA, gapStaging); err != nil {
		t.Fatal(err)
	}
	meta, _ = mgr.GetMeta(testMsgA)
	if len(meta.SiblingStagingIDs) != 0 {
		t.Fatalf("SiblingStagingIDs = %v after the drop", meta.SiblingStagingIDs)
	}
}

func TestGapOffer_DropLeavesANewerOffer(t *testing.T) {
	mgr, _, _ := newManager(t)
	readyInbound(t, mgr)
	const newer = "6b2c3d4e5f60718293a4b5c6d7e8f9a0"
	if err := mgr.RecordGapOffer(keptOffer(newer)); err != nil {
		t.Fatal(err)
	}
	if err := mgr.DeleteGapOffer(testMsgA, gapRequester, gapStaging); err != nil {
		t.Fatal(err)
	}
	if got, err := mgr.GetGapOffer(testMsgA, gapRequester); err != nil || got.StagingID != newer {
		t.Fatalf("the newer kept offer was dropped: %v %v", got, err)
	}
}

func TestGapOffer_RecordRefusedWithoutARow(t *testing.T) {
	mgr, _, _ := newManager(t)
	if err := mgr.RecordGapOffer(keptOffer(gapStaging)); !errors.Is(err, files.ErrMetaNotFound) {
		t.Fatalf("err = %v, want ErrMetaNotFound", err)
	}
	if _, err := mgr.GetGapOffer(testMsgA, gapRequester); !errors.Is(err, files.ErrGapOfferNotFound) {
		t.Fatalf("a kept offer was written with no row: %v", err)
	}
}

func TestJanitor_DropsTheKeptOffer(t *testing.T) {
	for _, trigger := range []string{"tombstone", "retention"} {
		t.Run(trigger, func(t *testing.T) {
			mgr, st, _ := newManager(t)
			bus := events.NewBus()
			now := int64(1742643890)
			log := events.New(st, bus, func() time.Time { return time.Unix(now, 0) })
			rec := &recordingDropper{}
			jan := files.NewJanitor(mgr, bus, nil, rec)
			readyInbound(t, mgr)
			if err := mgr.RecordGapOffer(keptOffer(gapStaging)); err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal(map[string]string{"name": "blob.bin"})
			if _, err := log.AppendOutbound(events.OutboundParams{
				ChatID: testChatID, Kind: events.KindFile, SenderSeq: 1,
				EnvelopeID: "env-kept", MsgID: testMsgA, ExpireSeconds: 1, Body: body,
			}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			go jan.Run(ctx)
			defer cancel()
			waitForSubscribers(t, bus, 1, 1)
			if trigger == "tombstone" {
				if _, err := log.ApplyDelete(testMsgA, now, ""); err != nil {
					t.Fatal(err)
				}
			} else if _, _, err := log.SweepExpired(now + 10); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) && len(rec.snapshot()) < 1 {
				time.Sleep(20 * time.Millisecond)
			}
			if calls := rec.snapshot(); len(calls) != 1 || calls[0] != gapStaging {
				t.Fatalf("DropFile calls = %v, want [%s]", calls, gapStaging)
			}
			if _, err := mgr.GetGapOffer(testMsgA, gapRequester); !errors.Is(err, files.ErrGapOfferNotFound) {
				t.Fatalf("the kept offer and its key outlived the row: %v", err)
			}
		})
	}
}

func TestGapOffer_RendersWithoutItsSecrets(t *testing.T) {
	o := keptOffer(gapStaging)
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logger.Info("x", slog.Any("kept", o))
	logger.Info("x", slog.Any("kept", &o))
	text := buf.String() + fmt.Sprint(o) + fmt.Sprintf("%v %+v %#v %d %x %s", o, o, o, o, o, o)
	for _, s := range []string{o.Token, hex.EncodeToString(o.Key), hex.EncodeToString(o.Nonce),
		base64.StdEncoding.EncodeToString(o.Key), base64.StdEncoding.EncodeToString(o.Nonce), fmt.Sprint(o.Key)} {
		if strings.Contains(text, s) {
			t.Fatalf("a secret reached the output: %q in\n%s", s, text)
		}
	}

	if strings.Contains(text, gapStaging) || !strings.Contains(text, logid.Short(gapStaging)) {
		t.Fatalf("the staging id is missing or in full:\n%s", text)
	}
}
