package events

import (
	"fmt"

	badger "github.com/dgraph-io/badger/v4"

	"haoma-frontend/internal/chat"
)

type Mutator string

const (
	MutatorAppend Mutator = "append"

	MutatorApplyEdit Mutator = "apply_edit"

	MutatorApplyDelete Mutator = "apply_delete"

	MutatorMarkRead Mutator = "mark_read"

	MutatorApplyReadReceipt Mutator = "apply_read_receipt"

	MutatorUpdateDeliveryState Mutator = "update_delivery_state"

	MutatorApplyDeliveredReceipt Mutator = "apply_delivered_receipt"

	MutatorUpdateFileBody Mutator = "update_file_body"

	MutatorUpdateInvitationBody Mutator = "update_invitation_body"

	MutatorMarkReadReceiptSent Mutator = "mark_read_receipt_sent"

	MutatorMarkReadReceiptSentSuppressed Mutator = "mark_read_receipt_sent_suppressed"

	MutatorSuppressPendingReadReceipts Mutator = "suppress_pending_read_receipts"

	MutatorClearReceiptEnvelope Mutator = "clear_receipt_envelope"

	MutatorApplyInboundReadAt Mutator = "apply_inbound_read_at"

	MutatorApplyDeliverySnapshot Mutator = "apply_delivery_snapshot"

	MutatorApplyReceiptSentClaim Mutator = "apply_receipt_sent_claim"

	MutatorRecordArrivedFrom Mutator = "record_arrived_from"

	MutatorRecordEnvelopeOutcome Mutator = "record_envelope_outcome"
)

var AllMutators = []Mutator{
	MutatorAppend,
	MutatorApplyEdit,
	MutatorApplyDelete,
	MutatorMarkRead,
	MutatorApplyReadReceipt,
	MutatorUpdateDeliveryState,
	MutatorApplyDeliveredReceipt,
	MutatorUpdateFileBody,
	MutatorUpdateInvitationBody,
	MutatorMarkReadReceiptSent,
	MutatorMarkReadReceiptSentSuppressed,
	MutatorSuppressPendingReadReceipts,
	MutatorClearReceiptEnvelope,
	MutatorApplyInboundReadAt,
	MutatorApplyDeliverySnapshot,
	MutatorApplyReceiptSentClaim,
	MutatorRecordArrivedFrom,
	MutatorRecordEnvelopeOutcome,
}

type MutatorClass int

const (
	ClassMutatorInvalid MutatorClass = iota

	ClassJournal

	ClassExempt
)

var mutatorRegistry = map[Mutator]MutatorClass{

	MutatorMarkRead:         ClassJournal,
	MutatorApplyReadReceipt: ClassJournal,

	MutatorAppend:      ClassJournal,
	MutatorApplyEdit:   ClassJournal,
	MutatorApplyDelete: ClassJournal,

	MutatorSuppressPendingReadReceipts:   ClassExempt,
	MutatorMarkReadReceiptSentSuppressed: ClassExempt,
	MutatorClearReceiptEnvelope:          ClassExempt,

	MutatorUpdateDeliveryState:   ClassJournal,
	MutatorApplyDeliveredReceipt: ClassJournal,
	MutatorMarkReadReceiptSent:   ClassJournal,

	MutatorUpdateFileBody: ClassExempt,

	MutatorApplyInboundReadAt:    ClassJournal,
	MutatorApplyDeliverySnapshot: ClassJournal,

	MutatorApplyReceiptSentClaim: ClassExempt,

	MutatorRecordArrivedFrom: ClassExempt,

	MutatorRecordEnvelopeOutcome: ClassExempt,

	MutatorUpdateInvitationBody: ClassExempt,
}

func Classify(m Mutator) (MutatorClass, bool) {
	c, ok := mutatorRegistry[m]
	return c, ok
}

func touch(txn *badger.Txn, m Mutator, chatID chat.ChatID, msgID string) (wrote bool, err error) {
	class, ok := Classify(m)
	if !ok || class == ClassMutatorInvalid {
		return false, fmt.Errorf("events: unclassified mutator %q — add it to mutator_registry.go", m)
	}
	if class != ClassJournal {
		return false, nil
	}
	wrote, err = journalTouch(txn, chatID, msgID)
	if err != nil {
		return false, err
	}
	if !wrote {

		return false, nil
	}

	recordJournalTouch(txn, m, msgID)
	return true, nil
}
