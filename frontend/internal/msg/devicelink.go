package msg

import (
	"encoding/json"
	"errors"
	"fmt"
)

const (
	IdentityPrivateLen = 32
	IdentityPublicLen  = 33
)

type DeviceLinkIdentityBody struct {
	Public     []byte `json:"identity_public"`
	Private    []byte `json:"identity_private"`
	SelfNick   string `json:"self_nick,omitempty"`
	SelfNickAt int64  `json:"self_nick_at,omitempty"`
}

var ErrIdentityKeyShape = errors.New("msg: identity key wrong length")

func BuildDeviceLinkIdentity(seq uint64, ts int64, msgID string, public, private []byte, selfNick string, selfNickAt int64) (*Wrapper, error) {
	if seq == 0 {
		return nil, fmt.Errorf("%w: seq must be >= 1", ErrMissingField)
	}
	if ts <= 0 {
		return nil, fmt.Errorf("%w: ts must be > 0", ErrMissingField)
	}
	if msgID == "" {
		return nil, fmt.Errorf("%w: msg_id required", ErrMissingField)
	}
	if len(public) != IdentityPublicLen {
		return nil, fmt.Errorf("%w: public is %d bytes, want %d", ErrIdentityKeyShape, len(public), IdentityPublicLen)
	}
	if len(private) != IdentityPrivateLen {
		return nil, fmt.Errorf("%w: private is %d bytes, want %d", ErrIdentityKeyShape, len(private), IdentityPrivateLen)
	}
	body, err := json.Marshal(DeviceLinkIdentityBody{
		Public:     public,
		Private:    private,
		SelfNick:   selfNick,
		SelfNickAt: selfNickAt,
	})
	if err != nil {
		return nil, fmt.Errorf("msg: marshal devicelink_identity body: %w", err)
	}
	return &Wrapper{
		V:     Version,
		Seq:   seq,
		Ts:    ts,
		MsgID: msgID,
		Kind:  KindDeviceLinkIdentity,

		Body: body,
	}, nil
}

func (w *Wrapper) DeviceLinkIdentity() (*DeviceLinkIdentityBody, error) {
	if w.Kind != KindDeviceLinkIdentity {
		return nil, fmt.Errorf("msg: wrapper kind is %q, not %q", w.Kind, KindDeviceLinkIdentity)
	}
	var b DeviceLinkIdentityBody
	if err := json.Unmarshal(w.Body, &b); err != nil {
		return nil, fmt.Errorf("msg: decode devicelink_identity body: %w", err)
	}
	if len(b.Public) != IdentityPublicLen {
		return nil, fmt.Errorf("%w: public is %d bytes, want %d", ErrIdentityKeyShape, len(b.Public), IdentityPublicLen)
	}
	if len(b.Private) != IdentityPrivateLen {
		return nil, fmt.Errorf("%w: private is %d bytes, want %d", ErrIdentityKeyShape, len(b.Private), IdentityPrivateLen)
	}
	return &b, nil
}

func (b *DeviceLinkIdentityBody) Zero() {
	for i := range b.Private {
		b.Private[i] = 0
	}
	for i := range b.Public {
		b.Public[i] = 0
	}
}
