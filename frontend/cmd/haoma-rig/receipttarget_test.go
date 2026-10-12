package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"haoma-frontend/internal/logging"
	"haoma-frontend/internal/logid"
)

const (
	receiptProductChatSettings = "../haoma/chat_settings.go"
	receiptProductRelay        = "../haoma/relay.go"
)

const (
	fixtureChatID   = "4d64c624d111baed132a40abcb196fdd"
	fixturePeerA    = "aaaaaaaa1111111111111111111111aa"
	fixturePeerB    = "bbbbbbbb2222222222222222222222bb"
	fixtureMsgA     = "168dfbe1a59160a0cea1c58fefe85d01"
	fixtureMsgB     = "9c02e73fbb41d0a2c5ee0716a0a9c711"
	fixtureEnvelop  = "b9828febb119e2db7467e7480d07a76e"
	fixtureReceiptA = "e15e2452a4dafed889dcbd4a1ffb7ea3"
	fixtureReceiptB = "77c1904ab8d0e5321fa6bb0913d4e2c8"
)

func fixtureSenders() []receiptSender {
	return []receiptSender{
		{device: "dev-a", peerID: fixturePeerA, msgID: fixtureMsgA},
		{device: "dev-b", peerID: fixturePeerB, msgID: fixtureMsgB},
	}
}

func readerLog(t *testing.T, emit func(l *slog.Logger)) []string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "haoma.log")
	logger, closer, err := logging.New(logging.Config{Level: "debug", File: path, Service: "haoma"})
	if err != nil {
		t.Fatalf("build a logger the way the daemon does: %v", err)
	}
	emit(logger)
	if err := closer(); err != nil {
		t.Fatalf("close the log: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the log back: %v", err)
	}
	var lines []string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func emitDecrypted(l *slog.Logger, peerID, msgID string) {
	l.Debug(inboxDecryptedLine,
		slog.String("envelope_id", fixtureEnvelop),
		slog.String("peer_id", peerID),
		slog.String("chat_id", fixtureChatID),
		slog.String("kind", "text"),
		slog.String("msg_id", msgID),
		slog.Uint64("sender_seq", 7),
	)
}

func emitGrouped(l *slog.Logger, pending, groups, routing int) {
	l.Debug(receiptGroupedLine,
		slog.String("chat_id", fixtureChatID),
		slog.Int("pending", pending),
		slog.Int("groups", groups),
		slog.Int("routing_devices", routing),
	)
}

func emitRecorded(l *slog.Logger, msgID, peerID string) {
	l.Debug(receiptRecordedLine,
		slog.String("msg_id", msgID),
		slog.String("peer_id", peerID),
		slog.String("source", receiptSourceRecorded),
	)
}

func emitFallback(l *slog.Logger, msgID, peerID, source string) {
	l.Debug(receiptFallbackLine,
		slog.String("msg_id", msgID),
		slog.String("peer_id", peerID),
		slog.String("source", source),
	)
}

func emitEnvelope(l *slog.Logger, peerID, receiptMsgID string, msgIDs ...string) {
	shortAcked := make([]string, 0, len(msgIDs))
	for _, id := range msgIDs {
		shortAcked = append(shortAcked, logid.Short(id))
	}
	l.Debug(receiptShippedLine,
		slog.String("chat_id", fixtureChatID),
		slog.String("peer_id", peerID),
		slog.String("envelope_id", fixtureEnvelop),
		slog.String("receipt_msg_id", receiptMsgID),
		slog.Uint64("sender_seq", 11),
		slog.Int("targets", len(msgIDs)),
		slog.String("acked_msg_ids", strings.Join(shortAcked, ",")),
	)
	for _, id := range msgIDs {
		l.Debug(receiptAckLine,
			slog.String("msg_id", id),
			slog.String("chat_id", fixtureChatID),
			slog.String("peer_id", peerID),
			slog.String("receipt_msg_id", receiptMsgID),
			slog.String("envelope_id", fixtureEnvelop),
		)
	}
}

func healthyLog(t *testing.T) []string {
	return readerLog(t, func(l *slog.Logger) {
		emitDecrypted(l, fixturePeerA, fixtureMsgA)
		emitDecrypted(l, fixturePeerB, fixtureMsgB)
		emitGrouped(l, 2, 2, 2)
		emitRecorded(l, fixtureMsgA, fixturePeerA)
		emitRecorded(l, fixtureMsgB, fixturePeerB)
		emitEnvelope(l, fixturePeerA, fixtureReceiptA, fixtureMsgA)
		emitEnvelope(l, fixturePeerB, fixtureReceiptB, fixtureMsgB)
	})
}

func TestReceiptVerdictsPassOnAHealthyReading(t *testing.T) {
	lines := healthyLog(t)
	senders := fixtureSenders()

	if err := assertTwoSendingDevices("peer", lines, senders); err != nil {
		t.Errorf("premise verdict failed on a healthy reading: %v", err)
	}
	acks, shipments := parseReceiptAcks(lines), parseReceiptShipments(lines)
	if len(acks) != 2 || len(shipments) != 2 {
		t.Fatalf("read %d acknowledgement(s) and %d envelope(s) from a healthy reading, want 2 and 2", len(acks), len(shipments))
	}
	if err := assertOneEnvelopePerSender(acks, shipments, senders); err != nil {
		t.Errorf("envelope verdict failed on a healthy reading: %v", err)
	}
	if err := assertReceiptGrouping("peer", 0, receiptGroupedLines(lines, fixtureChatID), 2); err != nil {
		t.Errorf("grouping verdict failed on a healthy reading: %v", err)
	}
	if err := assertReceiptTargetRecorded("peer", lines, senders); err != nil {
		t.Errorf("recorded-source verdict failed on a healthy reading: %v", err)
	}
}

func TestReceiptVerdictsBiteWhenOneDeviceIsAddressedForBoth(t *testing.T) {
	lines := readerLog(t, func(l *slog.Logger) {
		emitDecrypted(l, fixturePeerA, fixtureMsgA)
		emitDecrypted(l, fixturePeerB, fixtureMsgB)
		emitGrouped(l, 2, 1, 2)
		emitFallback(l, fixtureMsgA, fixturePeerA, "row_records_no_sending_device")
		emitFallback(l, fixtureMsgB, fixturePeerA, "row_records_no_sending_device")
		emitEnvelope(l, fixturePeerA, fixtureReceiptA, fixtureMsgA, fixtureMsgB)
	})
	senders := fixtureSenders()

	if err := assertTwoSendingDevices("peer", lines, senders); err != nil {
		t.Fatalf("premise verdict failed, so the case below would prove nothing: %v", err)
	}

	err := assertOneEnvelopePerSender(parseReceiptAcks(lines), parseReceiptShipments(lines), senders)
	if err == nil {
		t.Fatal("the envelope verdict passed on a reading acked to one device — the gate does not bite")
	}
	t.Logf("envelope verdict: %v", err)
	for _, want := range []string{"one envelope", "dev-a", "dev-b"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the failure does not name %q: %v", want, err)
		}
	}

	gErr := assertReceiptGrouping("peer", 0, receiptGroupedLines(lines, fixtureChatID), 2)
	if gErr == nil {
		t.Fatal("the grouping verdict passed on a reading split into one group — the gate does not bite")
	}
	t.Logf("grouping verdict: %v", gErr)

	rErr := assertReceiptTargetRecorded("peer", lines, senders)
	if rErr == nil {
		t.Fatal("the recorded-source verdict passed on a reading that took the fallback — the gate does not bite")
	}
	t.Logf("recorded-source verdict: %v", rErr)
	if !strings.Contains(rErr.Error(), "row_records_no_sending_device") {
		t.Errorf("the failure does not name the source it found: %v", rErr)
	}
}

func TestReceiptRecordedVerdictBitesWhenTheEnvelopesLookRight(t *testing.T) {
	lines := readerLog(t, func(l *slog.Logger) {
		emitDecrypted(l, fixturePeerA, fixtureMsgA)
		emitDecrypted(l, fixturePeerB, fixtureMsgB)
		emitGrouped(l, 2, 2, 2)
		emitFallback(l, fixtureMsgA, fixturePeerA, "row_records_no_sending_device")
		emitFallback(l, fixtureMsgB, fixturePeerB, "recorded_device_is_not_in_this_chats_routing_set")
		emitEnvelope(l, fixturePeerA, fixtureReceiptA, fixtureMsgA)
		emitEnvelope(l, fixturePeerB, fixtureReceiptB, fixtureMsgB)
	})
	senders := fixtureSenders()

	if err := assertOneEnvelopePerSender(parseReceiptAcks(lines), parseReceiptShipments(lines), senders); err != nil {
		t.Fatalf("the envelope verdict failed, so this case is not the one it claims to be: %v", err)
	}
	err := assertReceiptTargetRecorded("peer", lines, senders)
	if err == nil {
		t.Fatal("the recorded-source verdict passed on two envelopes the reader addressed by the fallback — a run like this proves nothing and must fail")
	}
	t.Logf("recorded-source verdict: %v", err)
}

func TestReceiptEnvelopeVerdictBitesWhenBothEnvelopesGoToOneDevice(t *testing.T) {
	lines := readerLog(t, func(l *slog.Logger) {
		emitEnvelope(l, fixturePeerA, fixtureReceiptA, fixtureMsgA)
		emitEnvelope(l, fixturePeerA, fixtureReceiptB, fixtureMsgB)
	})
	err := assertOneEnvelopePerSender(parseReceiptAcks(lines), parseReceiptShipments(lines), fixtureSenders())
	if err == nil {
		t.Fatal("the envelope verdict passed on two envelopes addressed to one device")
	}
	t.Logf("envelope verdict: %v", err)
	if !strings.Contains(err.Error(), logid.Short(fixturePeerB)) {
		t.Errorf("the failure does not name the device that should have been addressed: %v", err)
	}
}

func TestReceiptPremiseBitesWhenBothMessagesCameFromOneDevice(t *testing.T) {
	lines := readerLog(t, func(l *slog.Logger) {
		emitDecrypted(l, fixturePeerA, fixtureMsgA)
		emitDecrypted(l, fixturePeerA, fixtureMsgB)
		emitGrouped(l, 2, 1, 2)
		emitRecorded(l, fixtureMsgA, fixturePeerA)
		emitRecorded(l, fixtureMsgB, fixturePeerA)
		emitEnvelope(l, fixturePeerA, fixtureReceiptA, fixtureMsgA, fixtureMsgB)
	})
	err := assertTwoSendingDevices("peer", lines, fixtureSenders())
	if err == nil {
		t.Fatal("the premise verdict passed on two messages from one device — the gate would then read one envelope as a defect")
	}
	t.Logf("premise verdict: %v", err)
}

func TestReceiptRecordedVerdictBitesWhenNothingSaysHowTheTargetWasPicked(t *testing.T) {
	lines := readerLog(t, func(l *slog.Logger) {
		emitDecrypted(l, fixturePeerA, fixtureMsgA)
		emitDecrypted(l, fixturePeerB, fixtureMsgB)
		emitEnvelope(l, fixturePeerA, fixtureReceiptA, fixtureMsgA)
		emitEnvelope(l, fixturePeerB, fixtureReceiptB, fixtureMsgB)
	})
	err := assertReceiptTargetRecorded("peer", lines, fixtureSenders())
	if err == nil {
		t.Fatal("the recorded-source verdict passed on a log that says nothing about the target choice")
	}
	t.Logf("recorded-source verdict: %v", err)
}

func TestReceiptEnvelopeVerdictBitesWhenAnAckMatchesNoEnvelope(t *testing.T) {
	lines := readerLog(t, func(l *slog.Logger) {
		emitEnvelope(l, fixturePeerA, fixtureReceiptA, fixtureMsgA)

		l.Debug(receiptAckLine,
			slog.String("msg_id", fixtureMsgB),
			slog.String("chat_id", fixtureChatID),
			slog.String("peer_id", fixturePeerB),
			slog.String("receipt_msg_id", fixtureReceiptB),
			slog.String("envelope_id", fixtureEnvelop),
		)
	})
	err := assertOneEnvelopePerSender(parseReceiptAcks(lines), parseReceiptShipments(lines), fixtureSenders())
	if err == nil {
		t.Fatal("the envelope verdict passed on an acknowledgement matching no envelope")
	}
	t.Logf("envelope verdict: %v", err)
}

func TestReceiptAckReadsTheMessageIDNotTheReceiptID(t *testing.T) {
	lines := readerLog(t, func(l *slog.Logger) { emitEnvelope(l, fixturePeerA, fixtureReceiptA, fixtureMsgA) })
	acks := parseReceiptAcks(lines)
	if len(acks) != 1 {
		t.Fatalf("read %d acknowledgement(s), want 1", len(acks))
	}
	if acks[0].msgID != logid.Short(fixtureMsgA) {
		t.Errorf("msg_id = %q, want %q — the message the receipt covered", acks[0].msgID, logid.Short(fixtureMsgA))
	}
	if acks[0].receiptMsgID != logid.Short(fixtureReceiptA) {
		t.Errorf("receipt_msg_id = %q, want %q — the envelope that covered it", acks[0].receiptMsgID, logid.Short(fixtureReceiptA))
	}
}

func TestLogAttrValueMatchesTheWholeName(t *testing.T) {
	const line = `time=2026-08-17T09:00:00+01:00 level=DEBUG msg="x" receipt_msg_id=rrrrrrrr… msg_id=mmmmmmmm… peer_id=pppppppp…`
	got, ok := logAttrValue(line, "msg_id")
	if !ok || got != "mmmmmmmm…" {
		t.Errorf("logAttrValue(msg_id) = %q,%v — it read the receipt's id instead", got, ok)
	}
	if got, ok := logAttrValue(line, "receipt_msg_id"); !ok || got != "rrrrrrrr…" {
		t.Errorf("logAttrValue(receipt_msg_id) = %q,%v", got, ok)
	}
	if _, ok := logAttrValue(line, "envelope_id"); ok {
		t.Error("logAttrValue found an attribute the line does not carry")
	}
}

func TestReceiptLogStringsAreStillInTheProduct(t *testing.T) {
	sources := map[string]string{}
	for _, path := range []string{receiptProductChatSettings, receiptProductRelay} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("cannot read the product source at %s: %v\n"+
				"the file moved. Point this test at its new home, or the gate and the reader drift apart in silence.", path, err)
		}
		sources[path] = string(raw)
	}
	for _, want := range []string{
		receiptGroupedLine,
		receiptShippedLine,
		receiptAckLine,
		receiptRecordedLine,
		receiptFallbackLine,
		receiptSourceRecorded,
		fieldReceiptMsgID,
		fieldReceiptGroups,
		fieldRoutingDevices,
	} {
		if !strings.Contains(sources[receiptProductChatSettings], want) {
			t.Errorf("%s no longer holds %q — the gate matches on it and would measure nothing", receiptProductChatSettings, want)
		}
	}
	if !strings.Contains(sources[receiptProductRelay], inboxDecryptedLine) {
		t.Errorf("%s no longer holds %q — the gate reads its premise from it", receiptProductRelay, inboxDecryptedLine)
	}
}
