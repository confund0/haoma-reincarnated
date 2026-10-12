package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"haoma-frontend/internal/events"
	"haoma-frontend/internal/syncrail"
)

var localOnlyEventFields = map[string]string{
	"RecvSeq":             "daemon-local monotonic id; meaningless on another device's store",
	"ChatID":              "chat ids never converge; the routing key is the fingerprint, resolved by the caller (fingerprintForChat) before snapshotOf runs",
	"RecvTs":              "when THIS device learned the row; not a fact about the message",
	"SenderSeq":           "the sender's per-peer wrapper counter; carries no meaning to a sibling's own store",
	"SenderPeerID":        "peerIDs are minted per pairing and never converge; the receiving device maps Inbound onto its OWN peerID for the fingerprint",
	"ArrivedFromPeerID":   "the contact's device that sent the row to THIS device; peerIDs are minted per pairing, so a sibling could not address it, and the row it receives is one it did not receive from the contact at all — its own receipt falls back to the chat's stored device",
	"EnvelopeID":          "a per-device outbox artifact; a sibling has no envelope for a row it did not send",
	"EnvelopeIDs":         "same as EnvelopeID",
	"FailedEnvelopeIDs":   "same as EnvelopeID: which of this device's envelopes failed",
	"DecryptStatus":       "a local decrypt outcome; snapshotOf only ever ships Text/File/Reaction rows that decrypted here in the first place",
	"FailReason":          "paired with DecryptStatus; same reason",
	"RawBlob":             "libsignal ciphertext kept for local forensics only; never shipped",
	"MirroredFromSibling": "records HOW this device learned the row, not a fact about the row; the receiving device sets its own copy of this flag when it applies the snapshot",
	"ReceiptClaimOrigin":  "gates whether ReadReceiptSentAt crosses at all; the bool itself has no wire counterpart, only the honest, origin-asserted timestamp (ReceiptSentAt) crosses",
}

func symTextBody(text string) json.RawMessage {
	b, _ := json.Marshal(events.TextBody{Text: text})
	return b
}

type crossingField struct {
	build   func() events.Event
	present func(snap syncrail.RowSnapshot) bool
}

var crossingEventFields = map[string]crossingField{
	"Direction": {
		build: func() events.Event {
			return events.Event{Direction: events.DirIn, Kind: events.KindText, MsgID: "m-direction", Body: symTextBody("x")}
		},
		present: func(snap syncrail.RowSnapshot) bool { return snap.Inbound },
	},
	"Kind": {
		build: func() events.Event {
			return events.Event{Direction: events.DirOut, Kind: events.KindFile, MsgID: "m-kind", Body: symTextBody("x")}
		},
		present: func(snap syncrail.RowSnapshot) bool { return snap.Kind == string(events.KindFile) },
	},
	"DisplayTs": {
		build: func() events.Event {
			return events.Event{Direction: events.DirOut, Kind: events.KindText, MsgID: "m-displayts", Body: symTextBody("x"), DisplayTs: 1742600101}
		},
		present: func(snap syncrail.RowSnapshot) bool { return snap.DisplayTs == 1742600101 },
	},
	"SenderTs": {
		build: func() events.Event {
			return events.Event{Direction: events.DirOut, Kind: events.KindText, MsgID: "m-senderts", Body: symTextBody("x"), SenderTs: 1742600102}
		},
		present: func(snap syncrail.RowSnapshot) bool { return snap.SenderTs == 1742600102 },
	},
	"MsgID": {
		build: func() events.Event {
			return events.Event{Direction: events.DirOut, Kind: events.KindText, MsgID: "m-distinctive-msg-id", Body: symTextBody("x")}
		},
		present: func(snap syncrail.RowSnapshot) bool { return snap.MsgID == "m-distinctive-msg-id" },
	},
	"Body": {
		build: func() events.Event {
			return events.Event{Direction: events.DirOut, Kind: events.KindText, MsgID: "m-body", Body: symTextBody("distinctive body text")}
		},
		present: func(snap syncrail.RowSnapshot) bool {
			var tb events.TextBody
			_ = json.Unmarshal(snap.Body, &tb)
			return tb.Text == "distinctive body text"
		},
	},
	"DeliveryState": {
		build: func() events.Event {
			return events.Event{Direction: events.DirOut, Kind: events.KindText, MsgID: "m-deliverystate", Body: symTextBody("x"), DeliveryState: events.DeliveryDelivered}
		},
		present: func(snap syncrail.RowSnapshot) bool { return snap.DeliveryRank == events.DeliveryDelivered },
	},
	"ExpireSeconds": {
		build: func() events.Event {
			return events.Event{Direction: events.DirOut, Kind: events.KindText, MsgID: "m-expireseconds", Body: symTextBody("x"), ExpireSeconds: 12345}
		},
		present: func(snap syncrail.RowSnapshot) bool { return snap.ExpireSeconds == 12345 },
	},
	"ReadAt": {
		build: func() events.Event {
			return events.Event{Direction: events.DirIn, Kind: events.KindText, MsgID: "m-readat", Body: symTextBody("x"), ReadAt: 1742600103}
		},
		present: func(snap syncrail.RowSnapshot) bool { return snap.ReadAt == 1742600103 },
	},
	"EditedAt": {
		build: func() events.Event {
			return events.Event{Direction: events.DirOut, Kind: events.KindText, MsgID: "m-editedat", Body: symTextBody("x"), EditedAt: 1742600104}
		},
		present: func(snap syncrail.RowSnapshot) bool { return snap.EditedAt == 1742600104 },
	},
	"DeletedAt": {
		build: func() events.Event {
			return events.Event{Direction: events.DirOut, Kind: events.KindText, MsgID: "m-deletedat", Body: symTextBody("x"), DeletedAt: 1742600105}
		},
		present: func(snap syncrail.RowSnapshot) bool { return snap.DeletedAt == 1742600105 },
	},
	"ReadReceiptSentAt": {
		build: func() events.Event {
			return events.Event{
				Direction: events.DirIn, Kind: events.KindText, MsgID: "m-receiptsentat", Body: symTextBody("x"),
				ReadReceiptSentAt: 1742600106, ReceiptClaimOrigin: true,
			}
		},
		present: func(snap syncrail.RowSnapshot) bool { return snap.ReceiptSentAt == 1742600106 },
	},
	"ReceiptClaimGeneration": {
		build: func() events.Event {
			return events.Event{
				Direction: events.DirIn, Kind: events.KindText, MsgID: "m-receiptgen", Body: symTextBody("x"),
				ReadReceiptSentAt: 1742600107, ReceiptClaimOrigin: true, ReceiptClaimGeneration: 7,
			}
		},
		present: func(snap syncrail.RowSnapshot) bool { return snap.ReceiptGeneration == 7 },
	},
}

func TestSnapshotOf_Symmetry(t *testing.T) {
	typ := reflect.TypeOf(events.Event{})
	live := map[string]bool{}
	var unclassified []string
	var both []string
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		live[name] = true
		_, crosses := crossingEventFields[name]
		_, local := localOnlyEventFields[name]
		switch {
		case crosses && local:
			both = append(both, name)
		case !crosses && !local:
			unclassified = append(unclassified, name)
		}
	}
	if len(both) > 0 {
		t.Fatalf("fields classified as BOTH crossing and local-only: %v", both)
	}
	if len(unclassified) > 0 {
		t.Fatalf("events.Event field(s) %v are not classified in gapfill_answer_test.go — "+
			"decide whether each crosses to a sibling (add to crossingEventFields and to "+
			"snapshotOf) or must not (add to localOnlyEventFields with a reason)", unclassified)
	}
	var stale []string
	for name := range crossingEventFields {
		if !live[name] {
			stale = append(stale, name)
		}
	}
	for name := range localOnlyEventFields {
		if !live[name] {
			stale = append(stale, name)
		}
	}
	if len(stale) > 0 {
		t.Fatalf("classification entries %v name no field on events.Event — remove them", stale)
	}
}

func TestSnapshotOf_CrossingFieldsAreWired(t *testing.T) {
	for name, field := range crossingEventFields {
		ev := field.build()
		snap, ok := snapshotOf(ev, "fp-symmetry")
		if !ok {
			t.Errorf("%s: snapshotOf refused a row built for this check", name)
			continue
		}
		if !field.present(snap) {
			t.Errorf("%s: declared crossing in crossingEventFields, but its value is not readable "+
				"off the snapshot snapshotOf built — the field is classified and never wired", name)
		}
	}
}

func TestSnapshotOf_InheritedReceiptClaimDoesNotReExport(t *testing.T) {
	body, _ := json.Marshal(events.TextBody{Text: "hi"})
	ev := events.Event{
		Direction:          events.DirIn,
		Kind:               events.KindText,
		MsgID:              "msg-in-2",
		Body:               body,
		ReadReceiptSentAt:  1742600110,
		ReceiptClaimOrigin: false,
	}
	snap, ok := snapshotOf(ev, "fp-alice")
	if !ok {
		t.Fatal("snapshotOf refused a plain inbound text row")
	}
	if snap.ReceiptSentAt != 0 {
		t.Errorf("ReceiptSentAt = %d, want 0 — an inherited claim re-exported past its origin", snap.ReceiptSentAt)
	}
}

func TestSnapshotOf_SuppressionSentinelNeverCrosses(t *testing.T) {
	body, _ := json.Marshal(events.TextBody{Text: "hi"})
	ev := events.Event{
		Direction:          events.DirIn,
		Kind:               events.KindText,
		MsgID:              "msg-in-3",
		Body:               body,
		ReadReceiptSentAt:  events.ReadReceiptSuppressedSentinel,
		ReceiptClaimOrigin: true,
	}
	snap, ok := snapshotOf(ev, "fp-alice")
	if !ok {
		t.Fatal("snapshotOf refused a plain inbound text row")
	}
	if snap.ReceiptSentAt != 0 {
		t.Errorf("ReceiptSentAt = %d, want 0 — the suppression sentinel crossed raw, carrying one device's privacy choice onto the other", snap.ReceiptSentAt)
	}
}

func TestSnapshotOf_OutboundCarriesTheReadAxisAndTheDeliveryRank(t *testing.T) {
	body, err := json.Marshal(events.TextBody{Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	ev := events.Event{
		Direction:     events.DirOut,
		Kind:          events.KindFile,
		MsgID:         "msg-out-1",
		DisplayTs:     1742600000,
		ExpireSeconds: 3600,
		Body:          body,
		ReadAt:        1742600200,
		DeliveryState: events.DeliveryDelivered,
	}
	snap, ok := snapshotOf(ev, "fp-alice")
	if !ok {
		t.Fatal("snapshotOf refused a plain outbound file row")
	}
	if snap.Inbound {
		t.Error("Inbound = true, want false")
	}
	if snap.ReadAt != 1742600200 {
		t.Errorf("ReadAt = %d, want 1742600200 — the peer's read time on my own row did not cross", snap.ReadAt)
	}
	if snap.DeliveryRank != events.DeliveryDelivered {
		t.Errorf("DeliveryRank = %q, want %q", snap.DeliveryRank, events.DeliveryDelivered)
	}
	if snap.DeliveryFailed {
		t.Error("DeliveryFailed = true on a delivered row")
	}

	if snap.ReceiptSentAt != 0 || snap.ReceiptGeneration != 0 {
		t.Errorf("an outbound row carried a receipt claim: sent_at=%d generation=%d — that axis is inbound-only",
			snap.ReceiptSentAt, snap.ReceiptGeneration)
	}
}

func TestSnapshotOf_FailureRidesBesideTheRankNotInsideIt(t *testing.T) {
	body, _ := json.Marshal(events.TextBody{Text: "hi"})
	ev := events.Event{
		Direction:     events.DirOut,
		Kind:          events.KindText,
		MsgID:         "msg-out-2",
		Body:          body,
		DeliveryState: events.DeliveryFailed,
	}
	snap, ok := snapshotOf(ev, "fp-alice")
	if !ok {
		t.Fatal("snapshotOf refused a plain outbound text row")
	}
	if !snap.DeliveryFailed {
		t.Error("DeliveryFailed = false, want true")
	}
	if snap.DeliveryRank != "" {
		t.Errorf("DeliveryRank = %q, want empty — failure must not ship as a rank value", snap.DeliveryRank)
	}
}

func TestSnapshotOf_InboundFileTransferStateNeverCrosses(t *testing.T) {
	body, _ := json.Marshal(struct {
		State string `json:"state"`
	}{State: "downloading"})
	ev := events.Event{
		Direction:     events.DirIn,
		Kind:          events.KindFile,
		MsgID:         "msg-in-file",
		Body:          body,
		DeliveryState: "downloading",
	}
	snap, ok := snapshotOf(ev, "fp-alice")
	if !ok {
		t.Fatal("snapshotOf refused a plain inbound file row")
	}
	if snap.DeliveryRank != "" || snap.DeliveryFailed {
		t.Errorf("an inbound file's transfer state crossed: rank=%q failed=%v — a device holding no bytes "+
			"could render the file as ready because a sibling's cache said so",
			snap.DeliveryRank, snap.DeliveryFailed)
	}
}

func TestSnapshotOf_FileBodyCarriesNoTransferStateOrToken(t *testing.T) {
	const token = "tok-distinctive-7f3e"
	raw, _ := json.Marshal(FileEventBody{
		Token:            token,
		UrlPath:          "/files/" + token,
		Name:             "report.pdf",
		Size:             4096,
		Mime:             "application/pdf",
		Sha256Ciphertext: "ab12cd34",
		State:            "ready",
		BytesReceived:    4096,
		LastError:        "dial failed",
		Caption:          "the numbers",
	})
	for _, dir := range []events.Direction{events.DirIn, events.DirOut} {
		t.Run(string(dir), func(t *testing.T) {
			snap, ok := snapshotOf(events.Event{
				Direction: dir, Kind: events.KindFile, MsgID: "m-file-" + string(dir), Body: raw,
			}, "fp-alice")
			if !ok {
				t.Fatal("snapshotOf refused a plain file row")
			}
			if strings.Contains(string(snap.Body), token) {
				t.Fatalf("the token crossed: %s", snap.Body)
			}
			var got FileEventBody
			if err := json.Unmarshal(snap.Body, &got); err != nil {
				t.Fatal(err)
			}
			if got.State != "pending" || got.Token != "" || got.UrlPath != "" || got.BytesReceived != 0 || got.LastError != "" {
				t.Errorf("transfer fields crossed: state=%q token=%q url_path=%q bytes=%d error=%q, want pending and nothing else",
					got.State, got.Token, got.UrlPath, got.BytesReceived, got.LastError)
			}
			if got.Name != "report.pdf" || got.Size != 4096 || got.Mime != "application/pdf" ||
				got.Sha256Ciphertext != "ab12cd34" || got.Caption != "the numbers" {
				t.Errorf("what the file is did not cross: %+v", got)
			}
		})
	}
}

var fileBodyFieldsOnGapFill = map[string]string{
	"Name":             "crosses",
	"Size":             "crosses",
	"Mime":             "crosses",
	"Sha256Ciphertext": "crosses",
	"Caption":          "crosses",
	"State":            "crosses as pending: the receiving device holds no bytes",
	"Token":            "dropped: a capability minted for this device or for the contact it was sent to",
	"UrlPath":          "dropped: the same capability's source path",
	"BytesReceived":    "dropped: this device's own transfer",
	"LastError":        "dropped: this device's own transfer",
}

func TestGapFileBody_EveryFieldIsDecided(t *testing.T) {
	typ := reflect.TypeOf(FileEventBody{})
	live := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		live[name] = true
		if fileBodyFieldsOnGapFill[name] == "" {
			t.Errorf("FileEventBody.%s has no entry in fileBodyFieldsOnGapFill — decide whether gapFileBody ships it", name)
		}
	}
	for name := range fileBodyFieldsOnGapFill {
		if !live[name] {
			t.Errorf("fileBodyFieldsOnGapFill names %s, which FileEventBody no longer has", name)
		}
	}
}

func TestSnapshotOf_DeletedFileRowShipsNoBody(t *testing.T) {
	snap, ok := snapshotOf(events.Event{
		Direction: events.DirIn, Kind: events.KindFile, MsgID: "m-file-gone",
		Body: json.RawMessage("null"), DeletedAt: 1742600000,
	}, "fp-alice")
	if !ok {
		t.Fatal("snapshotOf refused a tombstoned file row — the delete would never cross")
	}
	if string(snap.Body) != "null" || snap.DeletedAt != 1742600000 {
		t.Errorf("body=%s deleted_at=%d, want null and the tombstone's time", snap.Body, snap.DeletedAt)
	}
}

var consumedOnApply = map[string]string{
	"Fingerprint":       "resolveChatByFingerprint, to route the row to a chat",
	"MsgID":             "everywhere — the merge key",
	"Kind":              "insertGapRow's kind switch; applyRowConvergence's kind gate",
	"Inbound":           "insertGapRow's inbound/outbound branch; applyRowConvergence's inbound/outbound branch",
	"DisplayTs":         "insertGapRow's Append{In,Out}bound DisplayTs; bumpChatActivity",
	"SenderTs":          "insertGapRow's AppendInbound SenderTs (outbound derives its own from DisplayTs)",
	"ExpireSeconds":     "insertGapRow's Append{In,Out}bound and AppendReactionBreadcrumb ExpireSeconds",
	"Body":              "gapfill.Incoming.Body (the edit/insert decision) and insertGapRow's Append* Body; a file row's body passes gapFileBody in applyGapRow first",
	"EditedAt":          "gapfill.Incoming.EditedAt (the edit/insert decision) and ApplyEdit's editedAt argument",
	"DeletedAt":         "gapfill.Incoming.DeletedAt (the edit/insert decision) and ApplyDelete's deletedAt argument",
	"Swept":             "gapfill.Incoming.Swept (the edit/insert decision)",
	"ReadAt":            "applyRowConvergence, both branches",
	"DeliveryRank":      "applyRowConvergence's outbound branch, via ApplyDeliverySnapshot",
	"DeliveryFailed":    "applyRowConvergence's outbound branch, via ApplyDeliverySnapshot",
	"ReceiptSentAt":     "applyRowConvergence's inbound branch, via ApplyReceiptSentClaim",
	"ReceiptGeneration": "applyRowConvergence's inbound branch, via ApplyReceiptSentClaim",
}

func TestRowSnapshot_EveryFieldIsConsumedOnApply(t *testing.T) {
	typ := reflect.TypeOf(syncrail.RowSnapshot{})
	var unclassified, stale []string
	live := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		live[name] = true
		if consumedOnApply[name] == "" {
			unclassified = append(unclassified, name)
		}
	}
	for name := range consumedOnApply {
		if !live[name] {
			stale = append(stale, name)
		}
	}
	if len(unclassified) > 0 {
		t.Fatalf("syncrail.RowSnapshot field(s) %v have no entry in consumedOnApply — "+
			"wire it into applyGapRow/insertGapRow/applyRowConvergence and name where here, "+
			"or the field converges nothing on arrival", unclassified)
	}
	if len(stale) > 0 {
		t.Fatalf("consumedOnApply names field(s) %v that no longer exist on RowSnapshot — remove them", stale)
	}
}
