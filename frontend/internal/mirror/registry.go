package mirror

import "haoma-frontend/internal/msg"

type Class int

const (
	ClassInvalid Class = iota

	ClassMirror

	ClassNoMirror
)

var registry = map[msg.Kind]Class{
	msg.KindText:          ClassMirror,
	msg.KindEdit:          ClassMirror,
	msg.KindDelete:        ClassMirror,
	msg.KindReaction:      ClassMirror,
	msg.KindFileOffer:     ClassMirror,
	msg.KindFileDelivered: ClassMirror,

	msg.KindFileReceipt: ClassNoMirror,

	msg.KindFileKey: ClassNoMirror,

	msg.KindRead:     ClassNoMirror,
	msg.KindPresence: ClassNoMirror,

	msg.KindCallOffer:   ClassNoMirror,
	msg.KindCallAccept:  ClassNoMirror,
	msg.KindCallReject:  ClassNoMirror,
	msg.KindCallEnd:     ClassNoMirror,
	msg.KindCallControl: ClassNoMirror,

	msg.KindRotateRequest: ClassNoMirror,
	msg.KindRotateAccept:  ClassNoMirror,
	msg.KindRotateAddress: ClassNoMirror,
	msg.KindRotateConfirm: ClassNoMirror,
	msg.KindRotateCancel:  ClassNoMirror,

	msg.KindDeviceLinkIdentity: ClassNoMirror,

	msg.KindPairingInvitation: ClassNoMirror,
}

func Classify(k msg.Kind) (Class, bool) {
	c, ok := registry[k]
	return c, ok
}
