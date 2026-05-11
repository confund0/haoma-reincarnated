package msg

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

const Version = 2

type Kind string

const (
	KindText     Kind = "text"
	KindEdit     Kind = "edit"
	KindDelete   Kind = "delete"
	KindReaction Kind = "reaction"
	KindRead     Kind = "read"
	KindPresence Kind = "presence"

	KindFileOffer   Kind = "file_offer"
	KindFileReceipt Kind = "file_receipt"
	KindFileKey     Kind = "file_key"

	KindCallOffer  Kind = "call_offer"
	KindCallAccept Kind = "call_accept"
	KindCallReject Kind = "call_reject"
	KindCallEnd    Kind = "call_end"

	KindRotateRequest Kind = "rotate_request"
	KindRotateAccept  Kind = "rotate_accept"
	KindRotateAddress Kind = "rotate_address"
	KindRotateConfirm Kind = "rotate_confirm"
	KindRotateCancel  Kind = "rotate_cancel"
)

const (
	PresenceAvailable = "available"
	PresenceAway      = "away"
	PresenceBusy      = "busy"
)

type Wrapper struct {
	V             int             `json:"v"`
	Seq           uint64          `json:"seq"`
	Ts            int64           `json:"ts"`
	MsgID         string          `json:"msg_id"`
	Kind          Kind            `json:"kind"`
	ExpireSeconds uint32          `json:"expire_seconds,omitempty"`
	Body          json.RawMessage `json:"body,omitempty"`
}

type TextBody struct {
	Text          string `json:"text"`
	PresenceState string `json:"presence_state,omitempty"`
	SenderNick    string `json:"sender_nick,omitempty"`
}

type EditBody struct {
	Target string `json:"target"`
	Text   string `json:"text"`
}

type DeleteBody struct {
	Target string `json:"target"`
}

type ReadBody struct {
	Targets       []string `json:"targets"`
	PresenceState string   `json:"presence_state,omitempty"`
}

type PresenceBody struct {
	State string `json:"state"`
}

type FileOfferBody struct {
	Token            string `json:"token"`
	UrlPath          string `json:"url_path"`
	Name             string `json:"name"`
	Size             uint64 `json:"size"`
	Mime             string `json:"mime"`
	Sha256Ciphertext string `json:"sha256_ciphertext"`
}

type FileReceiptBody struct {
	Token string `json:"token"`
}

type FileKeyBody struct {
	Token    string `json:"token"`
	KeyBytes string `json:"key"`
	Nonce    string `json:"nonce"`
}

type ReactionBody struct {
	Target string `json:"target"`
	Emoji  string `json:"emoji"`
}

var ErrUnsupportedVersion = errors.New("msg: unsupported wrapper version")

var ErrMissingField = errors.New("msg: required field missing or zero")

func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("msg: generate id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func BuildText(seq uint64, ts int64, msgID, text string, expireSeconds uint32, presenceState, senderNick string) (*Wrapper, error) {
	if seq == 0 {
		return nil, fmt.Errorf("%w: seq must be >= 1", ErrMissingField)
	}
	if ts <= 0 {
		return nil, fmt.Errorf("%w: ts must be > 0", ErrMissingField)
	}
	if msgID == "" {
		return nil, fmt.Errorf("%w: msg_id required", ErrMissingField)
	}
	switch presenceState {
	case "", PresenceAvailable, PresenceAway, PresenceBusy:
	default:
		return nil, fmt.Errorf("%w: presence_state must be empty|available|away|busy, got %q", ErrMissingField, presenceState)
	}
	body, err := json.Marshal(TextBody{Text: text, PresenceState: presenceState, SenderNick: senderNick})
	if err != nil {
		return nil, fmt.Errorf("msg: marshal text body: %w", err)
	}
	return &Wrapper{
		V:             Version,
		Seq:           seq,
		Ts:            ts,
		MsgID:         msgID,
		Kind:          KindText,
		ExpireSeconds: expireSeconds,
		Body:          body,
	}, nil
}

func BuildEdit(seq uint64, ts int64, msgID, targetMsgID, text string, expireSeconds uint32) (*Wrapper, error) {
	if seq == 0 {
		return nil, fmt.Errorf("%w: seq must be >= 1", ErrMissingField)
	}
	if ts <= 0 {
		return nil, fmt.Errorf("%w: ts must be > 0", ErrMissingField)
	}
	if msgID == "" {
		return nil, fmt.Errorf("%w: msg_id required", ErrMissingField)
	}
	if targetMsgID == "" {
		return nil, fmt.Errorf("%w: target required", ErrMissingField)
	}
	if text == "" {
		return nil, fmt.Errorf("%w: text required", ErrMissingField)
	}
	body, err := json.Marshal(EditBody{Target: targetMsgID, Text: text})
	if err != nil {
		return nil, fmt.Errorf("msg: marshal edit body: %w", err)
	}
	return &Wrapper{
		V:             Version,
		Seq:           seq,
		Ts:            ts,
		MsgID:         msgID,
		Kind:          KindEdit,
		ExpireSeconds: expireSeconds,
		Body:          body,
	}, nil
}

func BuildDelete(seq uint64, ts int64, msgID, targetMsgID string, expireSeconds uint32) (*Wrapper, error) {
	if seq == 0 {
		return nil, fmt.Errorf("%w: seq must be >= 1", ErrMissingField)
	}
	if ts <= 0 {
		return nil, fmt.Errorf("%w: ts must be > 0", ErrMissingField)
	}
	if msgID == "" {
		return nil, fmt.Errorf("%w: msg_id required", ErrMissingField)
	}
	if targetMsgID == "" {
		return nil, fmt.Errorf("%w: target required", ErrMissingField)
	}
	body, err := json.Marshal(DeleteBody{Target: targetMsgID})
	if err != nil {
		return nil, fmt.Errorf("msg: marshal delete body: %w", err)
	}
	return &Wrapper{
		V:             Version,
		Seq:           seq,
		Ts:            ts,
		MsgID:         msgID,
		Kind:          KindDelete,
		ExpireSeconds: expireSeconds,
		Body:          body,
	}, nil
}

func BuildReaction(seq uint64, ts int64, msgID, targetMsgID, emoji string, expireSeconds uint32) (*Wrapper, error) {
	if seq == 0 {
		return nil, fmt.Errorf("%w: seq must be >= 1", ErrMissingField)
	}
	if ts <= 0 {
		return nil, fmt.Errorf("%w: ts must be > 0", ErrMissingField)
	}
	if msgID == "" {
		return nil, fmt.Errorf("%w: msg_id required", ErrMissingField)
	}
	if targetMsgID == "" {
		return nil, fmt.Errorf("%w: target required", ErrMissingField)
	}
	body, err := json.Marshal(ReactionBody{Target: targetMsgID, Emoji: emoji})
	if err != nil {
		return nil, fmt.Errorf("msg: marshal reaction body: %w", err)
	}
	return &Wrapper{
		V:             Version,
		Seq:           seq,
		Ts:            ts,
		MsgID:         msgID,
		Kind:          KindReaction,
		ExpireSeconds: expireSeconds,
		Body:          body,
	}, nil
}

func BuildRead(seq uint64, ts int64, msgID string, targets []string, expireSeconds uint32, presenceState string) (*Wrapper, error) {
	if seq == 0 {
		return nil, fmt.Errorf("%w: seq must be >= 1", ErrMissingField)
	}
	if ts <= 0 {
		return nil, fmt.Errorf("%w: ts must be > 0", ErrMissingField)
	}
	if msgID == "" {
		return nil, fmt.Errorf("%w: msg_id required", ErrMissingField)
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("%w: targets required", ErrMissingField)
	}
	switch presenceState {
	case "", PresenceAvailable, PresenceAway, PresenceBusy:
	default:
		return nil, fmt.Errorf("%w: presence_state must be empty|available|away|busy, got %q", ErrMissingField, presenceState)
	}
	body, err := json.Marshal(ReadBody{Targets: targets, PresenceState: presenceState})
	if err != nil {
		return nil, fmt.Errorf("msg: marshal read body: %w", err)
	}
	return &Wrapper{
		V:             Version,
		Seq:           seq,
		Ts:            ts,
		MsgID:         msgID,
		Kind:          KindRead,
		ExpireSeconds: expireSeconds,
		Body:          body,
	}, nil
}

func BuildPresence(seq uint64, ts int64, msgID, state string, expireSeconds uint32) (*Wrapper, error) {
	if seq == 0 {
		return nil, fmt.Errorf("%w: seq must be >= 1", ErrMissingField)
	}
	if ts <= 0 {
		return nil, fmt.Errorf("%w: ts must be > 0", ErrMissingField)
	}
	if msgID == "" {
		return nil, fmt.Errorf("%w: msg_id required", ErrMissingField)
	}
	switch state {
	case PresenceAvailable, PresenceAway, PresenceBusy:
	default:
		return nil, fmt.Errorf("%w: state must be available|away|busy, got %q", ErrMissingField, state)
	}
	body, err := json.Marshal(PresenceBody{State: state})
	if err != nil {
		return nil, fmt.Errorf("msg: marshal presence body: %w", err)
	}
	return &Wrapper{
		V:             Version,
		Seq:           seq,
		Ts:            ts,
		MsgID:         msgID,
		Kind:          KindPresence,
		ExpireSeconds: expireSeconds,
		Body:          body,
	}, nil
}

func Marshal(w *Wrapper) ([]byte, error) {
	if w == nil {
		return nil, errors.New("msg: nil wrapper")
	}
	return json.Marshal(w)
}

func Unmarshal(data []byte) (*Wrapper, error) {
	var w Wrapper
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, fmt.Errorf("msg: decode wrapper: %w", err)
	}
	if w.V != Version {
		return nil, fmt.Errorf("%w: got %d, want %d", ErrUnsupportedVersion, w.V, Version)
	}
	if w.Seq == 0 {
		return nil, fmt.Errorf("%w: seq", ErrMissingField)
	}
	if w.Ts <= 0 {
		return nil, fmt.Errorf("%w: ts", ErrMissingField)
	}
	if w.Kind == "" {
		return nil, fmt.Errorf("%w: kind", ErrMissingField)
	}
	if w.MsgID == "" {
		return nil, fmt.Errorf("%w: msg_id", ErrMissingField)
	}
	return &w, nil
}

func (w *Wrapper) Text() (*TextBody, error) {
	if w.Kind != KindText {
		return nil, fmt.Errorf("msg: wrapper kind is %q, not %q", w.Kind, KindText)
	}
	var b TextBody
	if err := json.Unmarshal(w.Body, &b); err != nil {
		return nil, fmt.Errorf("msg: decode text body: %w", err)
	}
	return &b, nil
}

func (w *Wrapper) Edit() (*EditBody, error) {
	if w.Kind != KindEdit {
		return nil, fmt.Errorf("msg: wrapper kind is %q, not %q", w.Kind, KindEdit)
	}
	var b EditBody
	if err := json.Unmarshal(w.Body, &b); err != nil {
		return nil, fmt.Errorf("msg: decode edit body: %w", err)
	}
	if b.Target == "" {
		return nil, fmt.Errorf("%w: target", ErrMissingField)
	}
	if b.Text == "" {
		return nil, fmt.Errorf("%w: text", ErrMissingField)
	}
	return &b, nil
}

func (w *Wrapper) Delete() (*DeleteBody, error) {
	if w.Kind != KindDelete {
		return nil, fmt.Errorf("msg: wrapper kind is %q, not %q", w.Kind, KindDelete)
	}
	var b DeleteBody
	if err := json.Unmarshal(w.Body, &b); err != nil {
		return nil, fmt.Errorf("msg: decode delete body: %w", err)
	}
	if b.Target == "" {
		return nil, fmt.Errorf("%w: target", ErrMissingField)
	}
	return &b, nil
}

func (w *Wrapper) Read() (*ReadBody, error) {
	if w.Kind != KindRead {
		return nil, fmt.Errorf("msg: wrapper kind is %q, not %q", w.Kind, KindRead)
	}
	var b ReadBody
	if err := json.Unmarshal(w.Body, &b); err != nil {
		return nil, fmt.Errorf("msg: decode read body: %w", err)
	}
	if len(b.Targets) == 0 {
		return nil, fmt.Errorf("%w: targets", ErrMissingField)
	}
	return &b, nil
}

func (w *Wrapper) Presence() (*PresenceBody, error) {
	if w.Kind != KindPresence {
		return nil, fmt.Errorf("msg: wrapper kind is %q, not %q", w.Kind, KindPresence)
	}
	var b PresenceBody
	if err := json.Unmarshal(w.Body, &b); err != nil {
		return nil, fmt.Errorf("msg: decode presence body: %w", err)
	}
	switch b.State {
	case PresenceAvailable, PresenceAway, PresenceBusy:
	default:
		return nil, fmt.Errorf("%w: state must be available|away|busy, got %q", ErrMissingField, b.State)
	}
	return &b, nil
}

func BuildFileOffer(seq uint64, ts int64, msgID, token, urlPath, name string, size uint64, mime, sha256Ciphertext string, expireSeconds uint32) (*Wrapper, error) {
	if seq == 0 {
		return nil, fmt.Errorf("%w: seq must be >= 1", ErrMissingField)
	}
	if ts <= 0 {
		return nil, fmt.Errorf("%w: ts must be > 0", ErrMissingField)
	}
	if msgID == "" {
		return nil, fmt.Errorf("%w: msg_id required", ErrMissingField)
	}
	if token == "" {
		return nil, fmt.Errorf("%w: token required", ErrMissingField)
	}
	if urlPath == "" {
		return nil, fmt.Errorf("%w: url_path required", ErrMissingField)
	}
	if size == 0 {
		return nil, fmt.Errorf("%w: size must be > 0", ErrMissingField)
	}
	if sha256Ciphertext == "" {
		return nil, fmt.Errorf("%w: sha256_ciphertext required", ErrMissingField)
	}
	body, err := json.Marshal(FileOfferBody{
		Token:            token,
		UrlPath:          urlPath,
		Name:             name,
		Size:             size,
		Mime:             mime,
		Sha256Ciphertext: sha256Ciphertext,
	})
	if err != nil {
		return nil, fmt.Errorf("msg: marshal file_offer body: %w", err)
	}
	return &Wrapper{
		V:             Version,
		Seq:           seq,
		Ts:            ts,
		MsgID:         msgID,
		Kind:          KindFileOffer,
		ExpireSeconds: expireSeconds,
		Body:          body,
	}, nil
}

func BuildFileReceipt(seq uint64, ts int64, msgID, token string, expireSeconds uint32) (*Wrapper, error) {
	if seq == 0 {
		return nil, fmt.Errorf("%w: seq must be >= 1", ErrMissingField)
	}
	if ts <= 0 {
		return nil, fmt.Errorf("%w: ts must be > 0", ErrMissingField)
	}
	if msgID == "" {
		return nil, fmt.Errorf("%w: msg_id required", ErrMissingField)
	}
	if token == "" {
		return nil, fmt.Errorf("%w: token required", ErrMissingField)
	}
	body, err := json.Marshal(FileReceiptBody{Token: token})
	if err != nil {
		return nil, fmt.Errorf("msg: marshal file_receipt body: %w", err)
	}
	return &Wrapper{
		V:             Version,
		Seq:           seq,
		Ts:            ts,
		MsgID:         msgID,
		Kind:          KindFileReceipt,
		ExpireSeconds: expireSeconds,
		Body:          body,
	}, nil
}

func BuildFileKey(seq uint64, ts int64, msgID, token string, keyBytes, nonce []byte, expireSeconds uint32) (*Wrapper, error) {
	if seq == 0 {
		return nil, fmt.Errorf("%w: seq must be >= 1", ErrMissingField)
	}
	if ts <= 0 {
		return nil, fmt.Errorf("%w: ts must be > 0", ErrMissingField)
	}
	if msgID == "" {
		return nil, fmt.Errorf("%w: msg_id required", ErrMissingField)
	}
	if token == "" {
		return nil, fmt.Errorf("%w: token required", ErrMissingField)
	}
	if len(keyBytes) != 32 {
		return nil, fmt.Errorf("%w: key must be 32 bytes, got %d", ErrMissingField, len(keyBytes))
	}
	if len(nonce) != 24 {
		return nil, fmt.Errorf("%w: nonce must be 24 bytes, got %d", ErrMissingField, len(nonce))
	}
	body, err := json.Marshal(FileKeyBody{
		Token:    token,
		KeyBytes: base64.StdEncoding.EncodeToString(keyBytes),
		Nonce:    base64.StdEncoding.EncodeToString(nonce),
	})
	if err != nil {
		return nil, fmt.Errorf("msg: marshal file_key body: %w", err)
	}
	return &Wrapper{
		V:             Version,
		Seq:           seq,
		Ts:            ts,
		MsgID:         msgID,
		Kind:          KindFileKey,
		ExpireSeconds: expireSeconds,
		Body:          body,
	}, nil
}

func (w *Wrapper) FileOffer() (*FileOfferBody, error) {
	if w.Kind != KindFileOffer {
		return nil, fmt.Errorf("msg: wrapper kind is %q, not %q", w.Kind, KindFileOffer)
	}
	var b FileOfferBody
	if err := json.Unmarshal(w.Body, &b); err != nil {
		return nil, fmt.Errorf("msg: decode file_offer body: %w", err)
	}
	if b.Token == "" {
		return nil, fmt.Errorf("%w: token", ErrMissingField)
	}
	if b.UrlPath == "" {
		return nil, fmt.Errorf("%w: url_path", ErrMissingField)
	}
	if b.Size == 0 {
		return nil, fmt.Errorf("%w: size", ErrMissingField)
	}
	if b.Sha256Ciphertext == "" {
		return nil, fmt.Errorf("%w: sha256_ciphertext", ErrMissingField)
	}
	return &b, nil
}

func (w *Wrapper) FileReceipt() (*FileReceiptBody, error) {
	if w.Kind != KindFileReceipt {
		return nil, fmt.Errorf("msg: wrapper kind is %q, not %q", w.Kind, KindFileReceipt)
	}
	var b FileReceiptBody
	if err := json.Unmarshal(w.Body, &b); err != nil {
		return nil, fmt.Errorf("msg: decode file_receipt body: %w", err)
	}
	if b.Token == "" {
		return nil, fmt.Errorf("%w: token", ErrMissingField)
	}
	return &b, nil
}

func (w *Wrapper) FileKey() (body *FileKeyBody, key, nonce []byte, err error) {
	if w.Kind != KindFileKey {
		return nil, nil, nil, fmt.Errorf("msg: wrapper kind is %q, not %q", w.Kind, KindFileKey)
	}
	var b FileKeyBody
	if err := json.Unmarshal(w.Body, &b); err != nil {
		return nil, nil, nil, fmt.Errorf("msg: decode file_key body: %w", err)
	}
	if b.Token == "" {
		return nil, nil, nil, fmt.Errorf("%w: token", ErrMissingField)
	}
	key, err = base64.StdEncoding.DecodeString(b.KeyBytes)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("msg: decode file_key key: %w", err)
	}
	if len(key) != 32 {
		return nil, nil, nil, fmt.Errorf("%w: key must be 32 bytes, got %d", ErrMissingField, len(key))
	}
	nonce, err = base64.StdEncoding.DecodeString(b.Nonce)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("msg: decode file_key nonce: %w", err)
	}
	if len(nonce) != 24 {
		return nil, nil, nil, fmt.Errorf("%w: nonce must be 24 bytes, got %d", ErrMissingField, len(nonce))
	}
	return &b, key, nonce, nil
}

func (w *Wrapper) Reaction() (*ReactionBody, error) {
	if w.Kind != KindReaction {
		return nil, fmt.Errorf("msg: wrapper kind is %q, not %q", w.Kind, KindReaction)
	}
	var b ReactionBody
	if err := json.Unmarshal(w.Body, &b); err != nil {
		return nil, fmt.Errorf("msg: decode reaction body: %w", err)
	}
	if b.Target == "" {
		return nil, fmt.Errorf("%w: target", ErrMissingField)
	}
	return &b, nil
}

const (
	ModalityAudio  = "audio"
	ModalityVideo  = "video"
	ModalityScreen = "screen"
)

const (
	CallRejectUserDeclined = "user_declined"
	CallRejectBusy         = "busy"
	CallRejectTimeout      = "timeout"
	CallRejectUnsupported  = "unsupported_modalities"
)

type CallOfferBody struct {
	CallID      string            `json:"call_id"`
	Modalities  []string          `json:"modalities"`
	Tokens      map[string]string `json:"tokens,omitempty"`
	OutboundKey []byte            `json:"outbound_key,omitempty"`
}

type CallAcceptBody struct {
	CallID      string            `json:"call_id"`
	Modalities  []string          `json:"modalities"`
	Tokens      map[string]string `json:"tokens,omitempty"`
	OutboundKey []byte            `json:"outbound_key,omitempty"`
}

type CallRejectBody struct {
	CallID string `json:"call_id"`
	Reason string `json:"reason"`
}

type CallEndBody struct {
	CallID string `json:"call_id"`
}

const CallOutboundKeyBytes = 32

func BuildCallOffer(seq uint64, ts int64, msgID, callID string, modalities []string, tokens map[string]string, outboundKey []byte, expireSeconds uint32) (*Wrapper, error) {
	if seq == 0 {
		return nil, fmt.Errorf("%w: seq must be >= 1", ErrMissingField)
	}
	if ts <= 0 {
		return nil, fmt.Errorf("%w: ts must be > 0", ErrMissingField)
	}
	if msgID == "" {
		return nil, fmt.Errorf("%w: msg_id required", ErrMissingField)
	}
	if callID == "" {
		return nil, fmt.Errorf("%w: call_id required", ErrMissingField)
	}
	if len(modalities) == 0 {
		return nil, fmt.Errorf("%w: modalities required", ErrMissingField)
	}
	if outboundKey != nil && len(outboundKey) != CallOutboundKeyBytes {
		return nil, fmt.Errorf("%w: outbound_key must be %d bytes, got %d", ErrMissingField, CallOutboundKeyBytes, len(outboundKey))
	}
	body, err := json.Marshal(CallOfferBody{
		CallID:      callID,
		Modalities:  modalities,
		Tokens:      tokens,
		OutboundKey: outboundKey,
	})
	if err != nil {
		return nil, fmt.Errorf("msg: marshal call_offer body: %w", err)
	}
	return &Wrapper{
		V:             Version,
		Seq:           seq,
		Ts:            ts,
		MsgID:         msgID,
		Kind:          KindCallOffer,
		ExpireSeconds: expireSeconds,
		Body:          body,
	}, nil
}

func BuildCallAccept(seq uint64, ts int64, msgID, callID string, modalities []string, tokens map[string]string, outboundKey []byte, expireSeconds uint32) (*Wrapper, error) {
	if seq == 0 {
		return nil, fmt.Errorf("%w: seq must be >= 1", ErrMissingField)
	}
	if ts <= 0 {
		return nil, fmt.Errorf("%w: ts must be > 0", ErrMissingField)
	}
	if msgID == "" {
		return nil, fmt.Errorf("%w: msg_id required", ErrMissingField)
	}
	if callID == "" {
		return nil, fmt.Errorf("%w: call_id required", ErrMissingField)
	}
	if len(modalities) == 0 {
		return nil, fmt.Errorf("%w: modalities required", ErrMissingField)
	}
	if outboundKey != nil && len(outboundKey) != CallOutboundKeyBytes {
		return nil, fmt.Errorf("%w: outbound_key must be %d bytes, got %d", ErrMissingField, CallOutboundKeyBytes, len(outboundKey))
	}
	body, err := json.Marshal(CallAcceptBody{
		CallID:      callID,
		Modalities:  modalities,
		Tokens:      tokens,
		OutboundKey: outboundKey,
	})
	if err != nil {
		return nil, fmt.Errorf("msg: marshal call_accept body: %w", err)
	}
	return &Wrapper{
		V:             Version,
		Seq:           seq,
		Ts:            ts,
		MsgID:         msgID,
		Kind:          KindCallAccept,
		ExpireSeconds: expireSeconds,
		Body:          body,
	}, nil
}

func BuildCallReject(seq uint64, ts int64, msgID, callID, reason string, expireSeconds uint32) (*Wrapper, error) {
	if seq == 0 {
		return nil, fmt.Errorf("%w: seq must be >= 1", ErrMissingField)
	}
	if ts <= 0 {
		return nil, fmt.Errorf("%w: ts must be > 0", ErrMissingField)
	}
	if msgID == "" {
		return nil, fmt.Errorf("%w: msg_id required", ErrMissingField)
	}
	if callID == "" {
		return nil, fmt.Errorf("%w: call_id required", ErrMissingField)
	}
	body, err := json.Marshal(CallRejectBody{CallID: callID, Reason: reason})
	if err != nil {
		return nil, fmt.Errorf("msg: marshal call_reject body: %w", err)
	}
	return &Wrapper{
		V:             Version,
		Seq:           seq,
		Ts:            ts,
		MsgID:         msgID,
		Kind:          KindCallReject,
		ExpireSeconds: expireSeconds,
		Body:          body,
	}, nil
}

func BuildCallEnd(seq uint64, ts int64, msgID, callID string, expireSeconds uint32) (*Wrapper, error) {
	if seq == 0 {
		return nil, fmt.Errorf("%w: seq must be >= 1", ErrMissingField)
	}
	if ts <= 0 {
		return nil, fmt.Errorf("%w: ts must be > 0", ErrMissingField)
	}
	if msgID == "" {
		return nil, fmt.Errorf("%w: msg_id required", ErrMissingField)
	}
	if callID == "" {
		return nil, fmt.Errorf("%w: call_id required", ErrMissingField)
	}
	body, err := json.Marshal(CallEndBody{CallID: callID})
	if err != nil {
		return nil, fmt.Errorf("msg: marshal call_end body: %w", err)
	}
	return &Wrapper{
		V:             Version,
		Seq:           seq,
		Ts:            ts,
		MsgID:         msgID,
		Kind:          KindCallEnd,
		ExpireSeconds: expireSeconds,
		Body:          body,
	}, nil
}

func (w *Wrapper) CallOffer() (*CallOfferBody, error) {
	if w.Kind != KindCallOffer {
		return nil, fmt.Errorf("msg: wrapper kind is %q, not %q", w.Kind, KindCallOffer)
	}
	var b CallOfferBody
	if err := json.Unmarshal(w.Body, &b); err != nil {
		return nil, fmt.Errorf("msg: decode call_offer body: %w", err)
	}
	if b.CallID == "" {
		return nil, fmt.Errorf("%w: call_id", ErrMissingField)
	}
	if len(b.Modalities) == 0 {
		return nil, fmt.Errorf("%w: modalities", ErrMissingField)
	}
	if len(b.OutboundKey) != 0 && len(b.OutboundKey) != CallOutboundKeyBytes {
		return nil, fmt.Errorf("msg: call_offer outbound_key must be %d bytes, got %d", CallOutboundKeyBytes, len(b.OutboundKey))
	}
	return &b, nil
}

func (w *Wrapper) CallAccept() (*CallAcceptBody, error) {
	if w.Kind != KindCallAccept {
		return nil, fmt.Errorf("msg: wrapper kind is %q, not %q", w.Kind, KindCallAccept)
	}
	var b CallAcceptBody
	if err := json.Unmarshal(w.Body, &b); err != nil {
		return nil, fmt.Errorf("msg: decode call_accept body: %w", err)
	}
	if b.CallID == "" {
		return nil, fmt.Errorf("%w: call_id", ErrMissingField)
	}
	if len(b.Modalities) == 0 {
		return nil, fmt.Errorf("%w: modalities", ErrMissingField)
	}
	if len(b.OutboundKey) != 0 && len(b.OutboundKey) != CallOutboundKeyBytes {
		return nil, fmt.Errorf("msg: call_accept outbound_key must be %d bytes, got %d", CallOutboundKeyBytes, len(b.OutboundKey))
	}
	return &b, nil
}

func (w *Wrapper) CallReject() (*CallRejectBody, error) {
	if w.Kind != KindCallReject {
		return nil, fmt.Errorf("msg: wrapper kind is %q, not %q", w.Kind, KindCallReject)
	}
	var b CallRejectBody
	if err := json.Unmarshal(w.Body, &b); err != nil {
		return nil, fmt.Errorf("msg: decode call_reject body: %w", err)
	}
	if b.CallID == "" {
		return nil, fmt.Errorf("%w: call_id", ErrMissingField)
	}
	return &b, nil
}

func (w *Wrapper) CallEnd() (*CallEndBody, error) {
	if w.Kind != KindCallEnd {
		return nil, fmt.Errorf("msg: wrapper kind is %q, not %q", w.Kind, KindCallEnd)
	}
	var b CallEndBody
	if err := json.Unmarshal(w.Body, &b); err != nil {
		return nil, fmt.Errorf("msg: decode call_end body: %w", err)
	}
	if b.CallID == "" {
		return nil, fmt.Errorf("%w: call_id", ErrMissingField)
	}
	return &b, nil
}

const (
	RotateCancelUserDeclined = "user_declined"
	RotateCancelTimeout      = "timeout"
	RotateCancelConflict     = "concurrent_rotation"
	RotateCancelInternal     = "internal_error"

	RotateCancelCooldown = "cooldown"

	RotateCancelInCall = "in_call"
)

type RotateRequestBody struct {
	RotationID string `json:"rotation_id"`
	ProposedAt int64  `json:"proposed_at"`
}

type RotateAcceptBody struct {
	RotationID string `json:"rotation_id"`
}

type RotateAddressBody struct {
	RotationID string `json:"rotation_id"`
	NewAddress string `json:"new_address"`
}

type RotateConfirmBody struct {
	RotationID string `json:"rotation_id"`
}

type RotateCancelBody struct {
	RotationID string `json:"rotation_id"`
	Reason     string `json:"reason,omitempty"`
}

func BuildRotateRequest(seq uint64, ts int64, msgID, rotationID string, proposedAt int64, expireSeconds uint32) (*Wrapper, error) {
	if seq == 0 {
		return nil, fmt.Errorf("%w: seq must be >= 1", ErrMissingField)
	}
	if ts <= 0 {
		return nil, fmt.Errorf("%w: ts must be > 0", ErrMissingField)
	}
	if msgID == "" {
		return nil, fmt.Errorf("%w: msg_id required", ErrMissingField)
	}
	if rotationID == "" {
		return nil, fmt.Errorf("%w: rotation_id required", ErrMissingField)
	}
	if proposedAt <= 0 {
		return nil, fmt.Errorf("%w: proposed_at must be > 0", ErrMissingField)
	}
	body, err := json.Marshal(RotateRequestBody{RotationID: rotationID, ProposedAt: proposedAt})
	if err != nil {
		return nil, fmt.Errorf("msg: marshal rotate_request body: %w", err)
	}
	return &Wrapper{
		V:             Version,
		Seq:           seq,
		Ts:            ts,
		MsgID:         msgID,
		Kind:          KindRotateRequest,
		ExpireSeconds: expireSeconds,
		Body:          body,
	}, nil
}

func BuildRotateAccept(seq uint64, ts int64, msgID, rotationID string, expireSeconds uint32) (*Wrapper, error) {
	if seq == 0 {
		return nil, fmt.Errorf("%w: seq must be >= 1", ErrMissingField)
	}
	if ts <= 0 {
		return nil, fmt.Errorf("%w: ts must be > 0", ErrMissingField)
	}
	if msgID == "" {
		return nil, fmt.Errorf("%w: msg_id required", ErrMissingField)
	}
	if rotationID == "" {
		return nil, fmt.Errorf("%w: rotation_id required", ErrMissingField)
	}
	body, err := json.Marshal(RotateAcceptBody{RotationID: rotationID})
	if err != nil {
		return nil, fmt.Errorf("msg: marshal rotate_accept body: %w", err)
	}
	return &Wrapper{
		V:             Version,
		Seq:           seq,
		Ts:            ts,
		MsgID:         msgID,
		Kind:          KindRotateAccept,
		ExpireSeconds: expireSeconds,
		Body:          body,
	}, nil
}

func BuildRotateAddress(seq uint64, ts int64, msgID, rotationID, newAddress string, expireSeconds uint32) (*Wrapper, error) {
	if seq == 0 {
		return nil, fmt.Errorf("%w: seq must be >= 1", ErrMissingField)
	}
	if ts <= 0 {
		return nil, fmt.Errorf("%w: ts must be > 0", ErrMissingField)
	}
	if msgID == "" {
		return nil, fmt.Errorf("%w: msg_id required", ErrMissingField)
	}
	if rotationID == "" {
		return nil, fmt.Errorf("%w: rotation_id required", ErrMissingField)
	}
	if newAddress == "" {
		return nil, fmt.Errorf("%w: new_address required", ErrMissingField)
	}
	body, err := json.Marshal(RotateAddressBody{RotationID: rotationID, NewAddress: newAddress})
	if err != nil {
		return nil, fmt.Errorf("msg: marshal rotate_address body: %w", err)
	}
	return &Wrapper{
		V:             Version,
		Seq:           seq,
		Ts:            ts,
		MsgID:         msgID,
		Kind:          KindRotateAddress,
		ExpireSeconds: expireSeconds,
		Body:          body,
	}, nil
}

func BuildRotateConfirm(seq uint64, ts int64, msgID, rotationID string, expireSeconds uint32) (*Wrapper, error) {
	if seq == 0 {
		return nil, fmt.Errorf("%w: seq must be >= 1", ErrMissingField)
	}
	if ts <= 0 {
		return nil, fmt.Errorf("%w: ts must be > 0", ErrMissingField)
	}
	if msgID == "" {
		return nil, fmt.Errorf("%w: msg_id required", ErrMissingField)
	}
	if rotationID == "" {
		return nil, fmt.Errorf("%w: rotation_id required", ErrMissingField)
	}
	body, err := json.Marshal(RotateConfirmBody{RotationID: rotationID})
	if err != nil {
		return nil, fmt.Errorf("msg: marshal rotate_confirm body: %w", err)
	}
	return &Wrapper{
		V:             Version,
		Seq:           seq,
		Ts:            ts,
		MsgID:         msgID,
		Kind:          KindRotateConfirm,
		ExpireSeconds: expireSeconds,
		Body:          body,
	}, nil
}

func BuildRotateCancel(seq uint64, ts int64, msgID, rotationID, reason string, expireSeconds uint32) (*Wrapper, error) {
	if seq == 0 {
		return nil, fmt.Errorf("%w: seq must be >= 1", ErrMissingField)
	}
	if ts <= 0 {
		return nil, fmt.Errorf("%w: ts must be > 0", ErrMissingField)
	}
	if msgID == "" {
		return nil, fmt.Errorf("%w: msg_id required", ErrMissingField)
	}
	if rotationID == "" {
		return nil, fmt.Errorf("%w: rotation_id required", ErrMissingField)
	}
	body, err := json.Marshal(RotateCancelBody{RotationID: rotationID, Reason: reason})
	if err != nil {
		return nil, fmt.Errorf("msg: marshal rotate_cancel body: %w", err)
	}
	return &Wrapper{
		V:             Version,
		Seq:           seq,
		Ts:            ts,
		MsgID:         msgID,
		Kind:          KindRotateCancel,
		ExpireSeconds: expireSeconds,
		Body:          body,
	}, nil
}

func (w *Wrapper) RotateRequest() (*RotateRequestBody, error) {
	if w.Kind != KindRotateRequest {
		return nil, fmt.Errorf("msg: wrapper kind is %q, not %q", w.Kind, KindRotateRequest)
	}
	var b RotateRequestBody
	if err := json.Unmarshal(w.Body, &b); err != nil {
		return nil, fmt.Errorf("msg: decode rotate_request body: %w", err)
	}
	if b.RotationID == "" {
		return nil, fmt.Errorf("%w: rotation_id", ErrMissingField)
	}
	if b.ProposedAt <= 0 {
		return nil, fmt.Errorf("%w: proposed_at", ErrMissingField)
	}
	return &b, nil
}

func (w *Wrapper) RotateAccept() (*RotateAcceptBody, error) {
	if w.Kind != KindRotateAccept {
		return nil, fmt.Errorf("msg: wrapper kind is %q, not %q", w.Kind, KindRotateAccept)
	}
	var b RotateAcceptBody
	if err := json.Unmarshal(w.Body, &b); err != nil {
		return nil, fmt.Errorf("msg: decode rotate_accept body: %w", err)
	}
	if b.RotationID == "" {
		return nil, fmt.Errorf("%w: rotation_id", ErrMissingField)
	}
	return &b, nil
}

func (w *Wrapper) RotateAddress() (*RotateAddressBody, error) {
	if w.Kind != KindRotateAddress {
		return nil, fmt.Errorf("msg: wrapper kind is %q, not %q", w.Kind, KindRotateAddress)
	}
	var b RotateAddressBody
	if err := json.Unmarshal(w.Body, &b); err != nil {
		return nil, fmt.Errorf("msg: decode rotate_address body: %w", err)
	}
	if b.RotationID == "" {
		return nil, fmt.Errorf("%w: rotation_id", ErrMissingField)
	}
	if b.NewAddress == "" {
		return nil, fmt.Errorf("%w: new_address", ErrMissingField)
	}
	return &b, nil
}

func (w *Wrapper) RotateConfirm() (*RotateConfirmBody, error) {
	if w.Kind != KindRotateConfirm {
		return nil, fmt.Errorf("msg: wrapper kind is %q, not %q", w.Kind, KindRotateConfirm)
	}
	var b RotateConfirmBody
	if err := json.Unmarshal(w.Body, &b); err != nil {
		return nil, fmt.Errorf("msg: decode rotate_confirm body: %w", err)
	}
	if b.RotationID == "" {
		return nil, fmt.Errorf("%w: rotation_id", ErrMissingField)
	}
	return &b, nil
}

func (w *Wrapper) RotateCancel() (*RotateCancelBody, error) {
	if w.Kind != KindRotateCancel {
		return nil, fmt.Errorf("msg: wrapper kind is %q, not %q", w.Kind, KindRotateCancel)
	}
	var b RotateCancelBody
	if err := json.Unmarshal(w.Body, &b); err != nil {
		return nil, fmt.Errorf("msg: decode rotate_cancel body: %w", err)
	}
	if b.RotationID == "" {
		return nil, fmt.Errorf("%w: rotation_id", ErrMissingField)
	}
	return &b, nil
}
