package main

import "strings"

var msgIDKeys = []string{
	"msg_id",
	"edit_msg_id",
	"delete_msg_id",
	"reaction_msg_id",
	"target_msg_id",
	"offer_msg_id",
	"key_msg_id",
	"receipt_msg_id",
}

var envelopeIDKeys = []string{
	"envelope_id",
	"ack_id",
}

var peerIDKeys = []string{
	"peer_id",
	"peer",
	"existing_peer_id",
	"sibling_peer_id",
	"device_id",
	"local_peer_id",
	"recipient_peer_id",
	"from_peer_id",
	"offer_peer_id",
	"offering_device",

	"recorded_peer_id",
}

var kindKeys = []string{
	"kind",
	"wire_kind",
	"rail_kind",
	"acked_kind",
}

var stateKeys = []string{
	"state",
	"delivery_state",
	"transfer_state",
	"outbox_state",
	"row_state",
	"backend_state",
	"outcome",
}

var tokenKeys = []string{"token"}

var ackKeys = []string{"ack_id", "acked_kind", "receipt_msg_id"}

func has(list []string, key string) bool {
	for _, k := range list {
		if k == key {
			return true
		}
	}
	return false
}

func values(e entry, list []string) []string {
	var out []string
	for _, a := range e.attrs {
		if has(list, a.key) {
			out = append(out, a.val)
		}
	}
	return out
}

func matchesAny(e entry, list []string, want string) bool {
	for _, a := range e.attrs {
		if has(list, a.key) && sameID(a.val, want) {
			return true
		}
	}
	return false
}

type unknownKeys struct {
	seen map[string]string
}

func newUnknownKeys() *unknownKeys { return &unknownKeys{seen: map[string]string{}} }

func (u *unknownKeys) note(e entry) {
	for _, a := range e.attrs {
		if _, done := u.seen[a.key]; done {
			continue
		}
		switch {
		case strings.HasSuffix(a.key, "msg_id") && !has(msgIDKeys, a.key):
			u.seen[a.key] = "a message id"
		case strings.HasSuffix(a.key, "envelope_id") && !has(envelopeIDKeys, a.key):
			u.seen[a.key] = "an envelope id"
		case strings.HasSuffix(a.key, "peer_id") && !has(peerIDKeys, a.key):
			u.seen[a.key] = "a peer id"
		case strings.HasSuffix(a.key, "kind") && !has(kindKeys, a.key):
			u.seen[a.key] = "a wire kind"
		}
	}
}

func (u *unknownKeys) list() []string {
	var out []string
	for k, group := range u.seen {
		out = append(out, k+" looks like "+group)
	}
	return out
}
