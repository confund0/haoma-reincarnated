package syncrail

import (
	"encoding/json"
	"errors"
	"fmt"
)

type BackfillSection string

const (
	SectionContacts BackfillSection = "contacts"

	SectionChats BackfillSection = "chats"

	SectionSelf BackfillSection = "self"
)

type BackfillRequestBody struct {
	Section BackfillSection `json:"section"`

	Cursor string `json:"cursor,omitempty"`

	Full bool `json:"full,omitempty"`

	DestroySalt *DestroySaltReport `json:"destroy_salt,omitempty"`
}

type ContactSnapshot struct {
	Fingerprint string `json:"fingerprint"`

	Nick   string `json:"nick,omitempty"`
	NickAt int64  `json:"nick_at,omitempty"`
	Alias  string `json:"alias,omitempty"`

	AliasAt int64 `json:"alias_at,omitempty"`

	Verified   bool  `json:"verified,omitempty"`
	VerifiedAt int64 `json:"verified_at,omitempty"`
	Blocked    bool  `json:"blocked,omitempty"`
	BlockedAt  int64 `json:"blocked_at,omitempty"`

	PairedAt int64 `json:"paired_at,omitempty"`

	PairedDevices []uint32 `json:"paired_devices,omitempty"`

	PairedDevicesAtMs int64 `json:"paired_devices_at_ms,omitempty"`
}

type DeletedContact struct {
	Fingerprint string `json:"fingerprint"`

	DeletedAt int64 `json:"deleted_at"`
}

type ContactDeleteBody struct {
	Deleted []DeletedContact `json:"deleted"`
}

func ParseContactDelete(env Envelope) (ContactDeleteBody, error) {
	if env.Kind != KindContactDelete {
		return ContactDeleteBody{}, fmt.Errorf("%w: %q", ErrUnknownKind, env.Kind)
	}
	var body ContactDeleteBody
	if err := json.Unmarshal(env.Body, &body); err != nil {
		return ContactDeleteBody{}, fmt.Errorf("syncrail: parse contact delete: %w", err)
	}
	body.Deleted = UsableDeletedContacts(body.Deleted)
	if len(body.Deleted) == 0 {
		return ContactDeleteBody{}, errors.New("syncrail: contact delete names nobody")
	}
	return body, nil
}

func UsableDeletedContacts(in []DeletedContact) []DeletedContact {
	out := in[:0]
	for _, c := range in {
		if c.Fingerprint != "" && c.DeletedAt > 0 {
			out = append(out, c)
		}
	}
	return out
}

type SettingsStamps struct {
	Muted        int64 `json:"muted,omitempty"`
	Receipts     int64 `json:"receipts,omitempty"`
	NickOverride int64 `json:"nick_override,omitempty"`
}

type ChatSnapshot struct {
	ChatID string `json:"chat_id"`

	Fingerprint string `json:"fingerprint"`

	RetentionSeconds uint32 `json:"retention_seconds,omitempty"`
	RetentionAt      int64  `json:"retention_at,omitempty"`

	Muted            bool   `json:"muted"`
	ReceiptsDisabled bool   `json:"receipts_disabled"`
	NickOverride     string `json:"nick_override"`

	SettingsAt SettingsStamps `json:"settings_at,omitempty"`
}

type SelfSnapshot struct {
	Nick   string `json:"nick"`
	NickAt int64  `json:"nick_at,omitempty"`
}

type SelfNickBody struct {
	Self SelfSnapshot `json:"self"`
}

func ParseSelfNick(env Envelope) (SelfNickBody, error) {
	if env.Kind != KindSelfNick {
		return SelfNickBody{}, fmt.Errorf("%w: %q", ErrUnknownKind, env.Kind)
	}
	var body SelfNickBody
	if err := json.Unmarshal(env.Body, &body); err != nil {
		return SelfNickBody{}, fmt.Errorf("syncrail: parse self-nick: %w", err)
	}
	if body.Self.Nick == "" {

		return SelfNickBody{}, errors.New("syncrail: self-nick names nothing")
	}
	return body, nil
}

type BackfillResponseBody struct {
	Section BackfillSection `json:"section"`

	Contacts []ContactSnapshot `json:"contacts,omitempty"`
	Chats    []ChatSnapshot    `json:"chats,omitempty"`

	DeletedContacts []DeletedContact `json:"deleted_contacts,omitempty"`

	Self *SelfSnapshot `json:"self,omitempty"`

	Cursor string `json:"cursor,omitempty"`

	More bool `json:"more,omitempty"`

	Full bool `json:"full,omitempty"`
}

type ChatSettingsBody struct {
	Chat ChatSnapshot `json:"chat"`
}

func ParseChatSettings(env Envelope) (ChatSettingsBody, error) {
	if env.Kind != KindChatSettings {
		return ChatSettingsBody{}, fmt.Errorf("%w: %q", ErrUnknownKind, env.Kind)
	}
	var body ChatSettingsBody
	if err := json.Unmarshal(env.Body, &body); err != nil {
		return ChatSettingsBody{}, fmt.Errorf("syncrail: parse chat settings: %w", err)
	}
	if body.Chat.Fingerprint == "" {

		return ChatSettingsBody{}, errors.New("syncrail: chat settings carry no fingerprint")
	}
	return body, nil
}

type ContactsBody struct {
	Contacts []ContactSnapshot `json:"contacts"`
	Chats    []ChatIdentity    `json:"chats,omitempty"`
}

type ChatIdentity struct {
	ChatID string `json:"chat_id"`

	Fingerprint string `json:"fingerprint"`
}

func ParseContacts(env Envelope) (ContactsBody, error) {
	if env.Kind != KindContacts {
		return ContactsBody{}, fmt.Errorf("%w: %q", ErrUnknownKind, env.Kind)
	}
	var body ContactsBody
	if err := json.Unmarshal(env.Body, &body); err != nil {
		return ContactsBody{}, fmt.Errorf("syncrail: parse contacts: %w", err)
	}
	named := body.Contacts[:0]
	for _, c := range body.Contacts {
		if c.Fingerprint != "" {
			named = append(named, c)
		}
	}
	body.Contacts = named
	attachable := body.Chats[:0]
	for _, c := range body.Chats {
		if c.Fingerprint != "" && c.ChatID != "" {
			attachable = append(attachable, c)
		}
	}
	body.Chats = attachable
	if len(body.Contacts) == 0 {
		return ContactsBody{}, errors.New("syncrail: contacts name nobody")
	}
	return body, nil
}

func ParseBackfillRequest(env Envelope) (BackfillRequestBody, error) {
	if env.Kind != KindBackfillRequest {
		return BackfillRequestBody{}, fmt.Errorf("%w: %q", ErrUnknownKind, env.Kind)
	}
	var body BackfillRequestBody
	if err := json.Unmarshal(env.Body, &body); err != nil {
		return BackfillRequestBody{}, fmt.Errorf("syncrail: parse backfill request: %w", err)
	}
	switch body.Section {
	case SectionContacts, SectionChats, SectionSelf:
	default:

		return BackfillRequestBody{}, fmt.Errorf("syncrail: unknown backfill section %q", body.Section)
	}
	return body, nil
}

func ParseBackfillResponse(env Envelope) (BackfillResponseBody, error) {
	if env.Kind != KindBackfillResponse {
		return BackfillResponseBody{}, fmt.Errorf("%w: %q", ErrUnknownKind, env.Kind)
	}
	var body BackfillResponseBody
	if err := json.Unmarshal(env.Body, &body); err != nil {
		return BackfillResponseBody{}, fmt.Errorf("syncrail: parse backfill response: %w", err)
	}
	return body, nil
}
