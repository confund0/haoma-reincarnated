package ipc

import (
	"encoding/json"
	"fmt"
)

type FrameType string

const (
	FrameHello   FrameType = "hello"
	FrameWelcome FrameType = "system.welcome"
	FramePing    FrameType = "ping"
	FramePong    FrameType = "pong"
	FrameError   FrameType = "system.error"

	FrameStatusEvent FrameType = "status_event"
	FrameInboxEntry  FrameType = "inbox_entry"

	FrameInviteCreate   FrameType = "invite_create"
	FrameInviteCreated  FrameType = "invite_created"
	FrameInviteAccept   FrameType = "invite_accept"
	FrameInviteAccepted FrameType = "invite_accepted"

	FrameSendText FrameType = "send_text"
	FrameTextSent FrameType = "text_sent"

	FrameSendEdit FrameType = "send_edit"
	FrameEditSent FrameType = "edit_sent"

	FrameSendDelete FrameType = "send_delete"
	FrameDeleteSent FrameType = "delete_sent"

	FrameSendReaction FrameType = "send_reaction"
	FrameReactionSent FrameType = "reaction_sent"

	FrameReadSent FrameType = "msg.read-receipt-sent"

	FrameTimelineEvent FrameType = "msg.timeline-event"

	FrameDeliveryStatus FrameType = "delivery.state-changed"

	FrameTorInfo         FrameType = "tor_info"
	FrameTorInfoResponse FrameType = "tor_info_response"

	FrameSystemInfo         FrameType = "system_info"
	FrameSystemInfoResponse FrameType = "system_info_response"

	FrameListPeers    FrameType = "list_peers"
	FramePeersListed  FrameType = "peers_listed"
	FrameListTimeline FrameType = "list_timeline"
	FrameTimelinePage FrameType = "timeline_page"

	FrameSetAlias     FrameType = "set_alias"
	FrameAliasUpdated FrameType = "alias_updated"

	FramePeerUpdated FrameType = "peer.updated"
	FrameChatUpdated FrameType = "chat.updated"

	FramePeerDeleted FrameType = "peer.deleted"
	FrameChatCleared FrameType = "chat.cleared"
	FrameChatDeleted FrameType = "chat.deleted"

	FrameChatActivityChanged FrameType = "chat.activity-changed"
	FrameChatUnreadChanged   FrameType = "chat.unread-changed"

	FrameEnsureChat  FrameType = "ensure_chat"
	FrameChatEnsured FrameType = "chat_ensured"

	FramePeerAction        FrameType = "peer_action"
	FramePeerActionApplied FrameType = "peer_action_applied"

	FrameInspectEvent   FrameType = "inspect_event"
	FrameEventInspected FrameType = "event_inspected"

	FrameGetPeerFingerprint FrameType = "get_peer_fingerprint"
	FramePeerFingerprint    FrameType = "peer_fingerprint"

	FrameGetChatSettings FrameType = "get_chat_settings"
	FrameChatSettings    FrameType = "chat.settings-changed"
	FrameSetChatSettings FrameType = "set_chat_settings"

	FrameMarkRead   FrameType = "mark_read"
	FrameMarkedRead FrameType = "marked_read"

	FrameClientFocus FrameType = "client_focus"

	FrameTimelineEventDeleted FrameType = "msg.deleted"

	FrameListChats         FrameType = "list_chats"
	FrameChatsListed       FrameType = "chats_listed"
	FrameChatAction        FrameType = "chat_action"
	FrameChatActionApplied FrameType = "chat_action_applied"

	FrameBackendStatus FrameType = "backend_status"

	FrameInviteDHT    FrameType = "invite_dht"
	FrameInvitedDHT   FrameType = "invited_dht"
	FrameAcceptDHT    FrameType = "accept_dht"
	FrameAcceptedDHT  FrameType = "pair.dht-accepted"
	FrameCancelDHT    FrameType = "cancel_dht"
	FrameCancelledDHT FrameType = "cancelled_dht"

	FramePairOnionInvite    FrameType = "pair_onion_invite"
	FramePairOnionStarted   FrameType = "pair_onion_started"
	FramePairOnionProbe     FrameType = "pair.onion-probe"
	FramePairOnionCompleted FrameType = "pair.onion-completed"
	FramePairOnionFailed    FrameType = "pair.onion-failed"
	FramePairOnionAccept    FrameType = "pair_onion_accept"
	FramePairOnionAccepted  FrameType = "pair_onion_accepted"
	FramePairOnionCancel    FrameType = "pair_onion_cancel"
	FramePairOnionCancelled FrameType = "pair_onion_cancelled"

	FrameSetPresenceOverride FrameType = "set_presence_override"
	FramePushPresence        FrameType = "push_presence"
	FramePresenceChanged     FrameType = "presence_changed"

	FramePeerLastSeenChanged FrameType = "peer.last-seen-changed"

	FrameSetNick FrameType = "set_nick"
	FrameNick    FrameType = "system.self-nick-changed"

	FramePeerPaired FrameType = "pair.completed"

	FrameSubscribe  FrameType = "subscribe"
	FrameSubscribed FrameType = "subscribed"

	FrameSetTorPassword      FrameType = "set_tor_password"
	FrameTorPasswordAccepted FrameType = "tor_password_accepted"

	FrameGetSettings     FrameType = "get_settings"
	FrameSettingsListed  FrameType = "settings_listed"
	FrameSyncSettings    FrameType = "sync_settings"
	FrameSettingsChanged FrameType = "system.settings-changed"

	FrameClientLockState     FrameType = "client_lock_state"
	FrameNotificationEmitted FrameType = "system.notification-emitted"

	FrameFileProgress FrameType = "msg.file-progress"

	FrameSendFile FrameType = "send_file"
	FrameFileSent FrameType = "file_sent"

	FrameListFiles FrameType = "list_files"
	FrameFilesList FrameType = "files_list"

	FrameSaveFile  FrameType = "save_file"
	FrameFileSaved FrameType = "file_saved"

	FrameOpenFile      FrameType = "open_file"
	FrameFileOpenReady FrameType = "file_open_ready"

	FrameImageStream      FrameType = "image_stream"
	FrameImageStreamReady FrameType = "image_stream_ready"

	FrameWipeOpenTransient  FrameType = "wipe_open_transient"
	FrameOpenTransientWiped FrameType = "open_transient_wiped"

	FrameRetryFiles         FrameType = "retry_files"
	FrameRetryFilesResponse FrameType = "retry_files_result"

	FrameStartCall        FrameType = "start_call"
	FrameCallStarted      FrameType = "call_started"
	FrameRespondCall      FrameType = "respond_call"
	FrameCallResponded    FrameType = "call_responded"
	FrameCallStateChanged FrameType = "call.state-changed"

	FrameCallStreamEvent FrameType = "call.stream-event"
	FrameCallControl     FrameType = "call_control"
	FrameCallControlled  FrameType = "call_controlled"

	FrameNewCircuitForPeer FrameType = "new_circuit_for_peer"
	FrameNewCircuitClosed  FrameType = "new_circuit_closed"

	FramePeerSelfProbe         FrameType = "peer_self_probe"
	FramePeerSelfProbed        FrameType = "peer_self_probed"
	FramePeerSelfReachChanged  FrameType = "peer.self-reach-changed"
	FrameExternalProbeBurst    FrameType = "external_probe_burst"
	FrameExternalProbeAccepted FrameType = "external_probe_accepted"
	FrameExternalReachChanged  FrameType = "health.external-reach-changed"

	FrameRotateBegin       FrameType = "rotate_begin"
	FrameRotateBegun       FrameType = "rotate_begun"
	FrameRotateUserAccept  FrameType = "rotate_user_accept"
	FrameRotateUserDecline FrameType = "rotate_user_decline"
	FrameRotateRequested   FrameType = "rotate.requested"
	FrameRotateLifecycle   FrameType = "rotate.lifecycle"
)

type Frame struct {
	Type    FrameType       `json:"type"`
	ID      string          `json:"id,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

func Decode(b []byte) (Frame, error) {
	var f Frame
	if err := json.Unmarshal(b, &f); err != nil {
		return Frame{}, fmt.Errorf("ipc: decode frame: %w", err)
	}
	if f.Type == "" {
		return Frame{}, fmt.Errorf("ipc: frame missing type")
	}
	return f, nil
}

func Encode(f Frame) ([]byte, error) {
	if f.Type == "" {
		return nil, fmt.Errorf("ipc: encode frame: missing type")
	}
	b, err := json.Marshal(f)
	if err != nil {
		return nil, fmt.Errorf("ipc: encode frame: %w", err)
	}
	return b, nil
}

func NewFrame(t FrameType, id string, v any) (Frame, error) {
	if v == nil {
		return Frame{Type: t, ID: id}, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return Frame{}, fmt.Errorf("ipc: marshal payload for %s: %w", t, err)
	}
	return Frame{Type: t, ID: id, Payload: b}, nil
}

type HelloPayload struct {
	ClientName    string `json:"client_name"`
	ClientVersion string `json:"client_version,omitempty"`
}

type WelcomePayload struct {
	DaemonVersion     string `json:"daemon_version"`
	ProtocolVersion   int    `json:"protocol_version"`
	SelfNick          string `json:"self_nick,omitempty"`
	SelfNickIsDefault bool   `json:"self_nick_is_default,omitempty"`
}

const ProtocolVersion = 36

type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type StatusEventPayload struct {
	Event   json.RawMessage `json:"event"`
	Actions json.RawMessage `json:"actions,omitempty"`
}

type InboxEntryPayload struct {
	ArrivalAt int64           `json:"arrival_at"`
	PeerID    string          `json:"peer_id"`
	Envelope  json.RawMessage `json:"envelope"`
}

type InviteCreateRequest struct {
	Nick string `json:"nick,omitempty"`
}

type InviteCreatedResponse struct {
	InviteJSON string `json:"invite_json"`
}

type InviteAcceptRequest struct {
	InviteJSON string `json:"invite_json"`
}

type InviteAcceptedResponse struct {
	PeerID              string `json:"peer_id"`
	Nick                string `json:"nick,omitempty"`
	IdentityFingerprint string `json:"identity_fingerprint"`
}

type SendTextRequest struct {
	PeerID       string `json:"peer_id"`
	Text         string `json:"text"`
	ReplyToMsgID string `json:"reply_to_msg_id,omitempty"`
}

type SendTextResponse struct {
	EnvelopeID string `json:"envelope_id"`
	MsgID      string `json:"msg_id"`
	SenderSeq  uint64 `json:"sender_seq"`
}

type SendFileRequest struct {
	PeerID string `json:"peer_id"`
	Path   string `json:"path"`
}

type SendFileResponse struct {
	EnvelopeID string `json:"envelope_id"`
	MsgID      string `json:"msg_id"`
	SenderSeq  uint64 `json:"sender_seq"`
	Name       string `json:"name"`
	Size       uint64 `json:"size"`
	Mime       string `json:"mime,omitempty"`
}

type SendEditRequest struct {
	PeerID      string `json:"peer_id"`
	TargetMsgID string `json:"target_msg_id"`
	Text        string `json:"text"`
}

type SendEditResponse struct {
	EnvelopeID  string `json:"envelope_id"`
	MsgID       string `json:"msg_id"`
	SenderSeq   uint64 `json:"sender_seq"`
	TargetMsgID string `json:"target_msg_id"`
}

type SendDeleteRequest struct {
	PeerID      string `json:"peer_id"`
	TargetMsgID string `json:"target_msg_id"`
}

type SendDeleteResponse struct {
	EnvelopeID  string `json:"envelope_id"`
	MsgID       string `json:"msg_id"`
	SenderSeq   uint64 `json:"sender_seq"`
	TargetMsgID string `json:"target_msg_id"`
}

type SendReactionRequest struct {
	PeerID      string `json:"peer_id"`
	TargetMsgID string `json:"target_msg_id"`
	Emoji       string `json:"emoji"`
}

type SendReactionResponse struct {
	EnvelopeID  string `json:"envelope_id"`
	MsgID       string `json:"msg_id"`
	SenderSeq   uint64 `json:"sender_seq"`
	TargetMsgID string `json:"target_msg_id"`
}

type ReadSentPayload struct {
	ChatID     string   `json:"chat_id"`
	EnvelopeID string   `json:"envelope_id"`
	MsgID      string   `json:"msg_id"`
	SenderSeq  uint64   `json:"sender_seq"`
	Targets    []string `json:"targets"`
}

type TimelineEventPayload struct {
	Event json.RawMessage `json:"event"`
}

type DeliveryStatusPayload struct {
	EnvelopeID string `json:"envelope_id"`
	State      string `json:"state"`
	At         int64  `json:"at"`
	Attempts   int    `json:"attempts"`
	LastError  string `json:"last_error,omitempty"`
}

type TorSlot struct {
	Slot      int    `json:"slot"`
	ServiceID string `json:"service_id"`
	URL       string `json:"url"`
}

type TorHealth struct {
	Bootstrap   int  `json:"bootstrap"`
	Ready       bool `json:"ready"`
	Unreachable bool `json:"unreachable"`
}

type TorInfoResponsePayload struct {
	Slots  []TorSlot `json:"slots"`
	Health TorHealth `json:"health"`
}

type SystemInfoComponent struct {
	Version   string `json:"version"`
	StartedAt string `json:"started_at"`
}

type SystemInfoResponsePayload struct {
	Haoma  SystemInfoComponent `json:"haoma"`
	Haomad SystemInfoComponent `json:"haomad"`
}

type PeerEntry struct {
	ID            string `json:"id"`
	ChatID        string `json:"chat_id,omitempty"`
	Nick          string `json:"nick,omitempty"`
	Alias         string `json:"alias,omitempty"`
	Label         string `json:"label,omitempty"`
	LastActiveAt  int64  `json:"last_active_at,omitempty"`
	LastPassiveAt int64  `json:"last_passive_at,omitempty"`
	RetiredAt     int64  `json:"retired_at,omitempty"`
	Accepting     bool   `json:"accepting,omitempty"`
	Chatty        string `json:"chatty,omitempty"`
	Effective     string `json:"effective,omitempty"`
}

type PeersListedResponse struct {
	Peers []PeerEntry `json:"peers"`
}

type SetAliasRequest struct {
	PeerID string `json:"peer_id"`
	Alias  string `json:"alias"`
}

type AliasUpdatedResponse struct {
	Peer PeerEntry `json:"peer"`
}

type PeerUpdatedPayload struct {
	Peer PeerEntry `json:"peer"`
}

type ChatUpdatedPayload struct {
	Chat ChatEntry `json:"chat"`
}

type PeerDeletedPayload struct {
	PeerID string `json:"peer_id"`
}

type ChatClearedPayload struct {
	ChatID       string `json:"chat_id"`
	DeletedCount int    `json:"deleted_count,omitempty"`
}

type ChatDeletedPayload struct {
	ChatID       string `json:"chat_id"`
	DeletedCount int    `json:"deleted_count,omitempty"`
}

type ChatActivityChangedPayload struct {
	ChatID         string `json:"chat_id"`
	LastActivityAt int64  `json:"last_activity_at"`
}

type ChatUnreadChangedPayload struct {
	ChatID      string `json:"chat_id"`
	UnreadCount uint32 `json:"unread_count"`
}

type EnsureChatRequest struct {
	PeerID string `json:"peer_id"`
}

type ChatEnsuredResponse struct {
	Peer PeerEntry `json:"peer"`
	Chat ChatEntry `json:"chat"`
}

type PeerAction string

const (
	PeerActionRetire PeerAction = "retire"
	PeerActionDelete PeerAction = "delete"
)

type PeerActionRequest struct {
	PeerID string     `json:"peer_id"`
	Action PeerAction `json:"action"`
}

type InspectEventRequest struct {
	MsgID string `json:"msg_id"`
}

type EventInspectedResponse struct {
	Event json.RawMessage `json:"event"`
}

type PeerActionAppliedResponse struct {
	Peer         PeerEntry  `json:"peer"`
	Action       PeerAction `json:"action"`
	DeletedCount int        `json:"deleted_count,omitempty"`
}

type ListTimelineRequest struct {
	PeerID          string `json:"peer_id"`
	Limit           int    `json:"limit,omitempty"`
	BeforeDisplayTs int64  `json:"before_display_ts,omitempty"`
}

type TimelinePageResponse struct {
	PeerID  string            `json:"peer_id"`
	Events  []json.RawMessage `json:"events"`
	HasMore bool              `json:"has_more"`
}

type InviteDHTRequest struct{}

type InvitedDHTResponse struct {
	GUID            string   `json:"guid"`
	IDWords         []string `json:"id_words"`
	PassphraseWords []string `json:"passphrase_words"`
	ExpiresAt       int64    `json:"expires_at"`
}

type AcceptDHTRequest struct {
	IDWords         []string `json:"id_words"`
	PassphraseWords []string `json:"passphrase_words"`
}

type AcceptedDHTResponse struct {
	PeerID              string `json:"peer_id"`
	Nick                string `json:"nick,omitempty"`
	IdentityFingerprint string `json:"identity_fingerprint"`
}

type CancelDHTRequest struct {
	GUID string `json:"guid"`
}

type PairOnionInviteRequest struct {
	Nick  string `json:"nick,omitempty"`
	Alias string `json:"alias,omitempty"`
}

type PairOnionStartedResponse struct {
	HandleID  string   `json:"handle_id"`
	Words     []string `json:"words"`
	ExpiresAt int64    `json:"expires_at"`
}

type PairOnionCompletedPush struct {
	HandleID            string `json:"handle_id"`
	PeerID              string `json:"peer_id"`
	Nick                string `json:"nick,omitempty"`
	IdentityFingerprint string `json:"identity_fingerprint"`
}

type PairOnionFailedPush struct {
	HandleID string `json:"handle_id"`
	Reason   string `json:"reason"`
	Detail   string `json:"detail,omitempty"`
}

type PairOnionAcceptRequest struct {
	Words []string `json:"words"`
	Nick  string   `json:"nick,omitempty"`
	Alias string   `json:"alias,omitempty"`
}

type PairOnionAcceptedResponse struct {
	PeerID              string `json:"peer_id"`
	Nick                string `json:"nick,omitempty"`
	IdentityFingerprint string `json:"identity_fingerprint"`
}

type PairOnionCancelRequest struct {
	HandleID string `json:"handle_id"`
}

type ChatKind string

const (
	ChatKindDirect ChatKind = "direct"
	ChatKindGroup  ChatKind = "group"
)

type ChatEntry struct {
	ChatID              string   `json:"chat_id"`
	Kind                ChatKind `json:"kind"`
	PeerID              string   `json:"peer_id,omitempty"`
	GroupName           string   `json:"group_name,omitempty"`
	GroupAlias          string   `json:"group_alias,omitempty"`
	Label               string   `json:"label,omitempty"`
	RetentionTTL        uint32   `json:"retention_ttl"`
	DisableReadReceipts bool     `json:"disable_read_receipts,omitempty"`
	NotificationsMuted  bool     `json:"notifications_muted,omitempty"`
	Members             []string `json:"members,omitempty"`
	CreatedAt           int64    `json:"created_at"`
	LastActivityAt      int64    `json:"last_activity_at,omitempty"`
	UnreadCount         uint32   `json:"unread_count,omitempty"`
	LastTimerChangeTs   int64    `json:"last_timer_change_ts,omitempty"`

	Accepting bool   `json:"accepting,omitempty"`
	Chatty    string `json:"chatty,omitempty"`
	Effective string `json:"effective,omitempty"`
}

type ChatsListedResponse struct {
	Chats []ChatEntry `json:"chats"`
}

type ChatAction string

const (
	ChatActionClear  ChatAction = "clear"
	ChatActionDelete ChatAction = "delete"
)

type ChatActionRequest struct {
	ChatID string     `json:"chat_id"`
	Action ChatAction `json:"action"`
}

type ChatActionAppliedResponse struct {
	Chat         ChatEntry  `json:"chat"`
	Action       ChatAction `json:"action"`
	DeletedCount int        `json:"deleted_count,omitempty"`
}

type GetPeerFingerprintRequest struct {
	PeerID string `json:"peer_id"`
}

type PeerFingerprintPayload struct {
	PeerID      string `json:"peer_id"`
	Fingerprint string `json:"fingerprint"`
}

type GetChatSettingsRequest struct {
	ChatID string `json:"chat_id"`
}

type ChatSettingsPayload struct {
	ChatID              string `json:"chat_id"`
	RetentionTTL        uint32 `json:"retention_ttl"`
	DisableReadReceipts bool   `json:"disable_read_receipts,omitempty"`

	NotificationsMuted bool `json:"notifications_muted,omitempty"`
}

type SetChatSettingsRequest struct {
	ChatID              string `json:"chat_id"`
	RetentionTTL        uint32 `json:"retention_ttl"`
	DisableReadReceipts bool   `json:"disable_read_receipts,omitempty"`
	NotificationsMuted  bool   `json:"notifications_muted,omitempty"`
}

type ClientFocusRequest struct {
	ChatID         string `json:"chat_id"`
	ScrollPosition int    `json:"scroll_position"`
}

type MarkReadRequest struct {
	ChatID string `json:"chat_id"`
}

type MarkedReadResponse struct {
	ChatID      string `json:"chat_id"`
	MarkedCount int    `json:"marked_count"`
}

type RetryFilesResponse struct {
	Enqueued int `json:"enqueued"`
}

type TimelineEventDeletedPayload struct {
	ChatID  string `json:"chat_id"`
	RecvSeq uint64 `json:"recv_seq"`
}

type BackendStatusPayload struct {
	BackendReachable bool      `json:"backend_reachable"`
	Tor              TorHealth `json:"tor"`

	OnionCount int `json:"onion_count"`
}

type SetPresenceOverrideRequest struct {
	State string `json:"state"`
}

type PushPresenceRequest struct {
	Target string `json:"target,omitempty"`
}

type PresenceChangedPayload struct {
	PeerID    string `json:"peer_id"`
	Accepting bool   `json:"accepting"`
	Chatty    string `json:"chatty,omitempty"`
	Effective string `json:"effective"`
}

type PeerLastSeenChangedPayload struct {
	PeerID        string `json:"peer_id"`
	LastActiveAt  int64  `json:"last_active_at"`
	LastPassiveAt int64  `json:"last_passive_at"`
}

type SetNickRequest struct {
	Nick string `json:"nick"`
}

type FileProgressPayload struct {
	ChatID        string `json:"chat_id"`
	MsgID         string `json:"msg_id"`
	BytesReceived uint64 `json:"bytes_received"`
	TotalBytes    uint64 `json:"total_bytes,omitempty"`
}

type ListFilesRequest struct {
	ChatID string `json:"chat_id"`
}

type FilesListResponse struct {
	ChatID string      `json:"chat_id"`
	Files  []FileEntry `json:"files"`
}

type FileEntry struct {
	MsgID         string `json:"msg_id"`
	Direction     string `json:"direction"`
	OriginalName  string `json:"original_name"`
	Mime          string `json:"mime,omitempty"`
	Size          uint64 `json:"size"`
	DisplayTs     int64  `json:"display_ts"`
	State         string `json:"state"`
	DeliveryState string `json:"delivery_state,omitempty"`

	Deletable bool `json:"deletable"`
}

type SaveFileRequest struct {
	ChatID  string `json:"chat_id"`
	MsgID   string `json:"msg_id"`
	DestDir string `json:"dest_dir"`
}

type SaveFileResponse struct {
	FullPath string `json:"full_path"`
}

type OpenFileRequest struct {
	ChatID string `json:"chat_id"`
	MsgID  string `json:"msg_id"`
}

type OpenFileReadyResponse struct {
	FullPath    string `json:"full_path"`
	SniffedMIME string `json:"sniffed_mime"`
	MIMEMatches bool   `json:"mime_matches"`
}

type ImageStreamRequest struct {
	ChatID string `json:"chat_id"`
	MsgID  string `json:"msg_id"`
}

type ImageStreamReadyResponse struct {
	BytesB64    string `json:"bytes_b64"`
	SniffedMIME string `json:"sniffed_mime"`
	MIMEMatches bool   `json:"mime_matches"`
}

type WipeOpenTransientRequest struct {
	MsgID string `json:"msg_id"`
}

type OpenTransientWipedResponse struct{}

type SetTorPasswordRequest struct {
	Password string `json:"password"`
}

type Settings struct {
	DefaultRetentionSec uint64 `json:"default_retention_sec"`
	DefaultSendReceipts bool   `json:"default_send_receipts"`

	IdleAction         string `json:"idle_action,omitempty"`
	IdleTimeoutSeconds int    `json:"idle_timeout_seconds,omitempty"`
	PinValiditySec     int    `json:"pin_validity_sec"`

	NotifyShellEnabled  bool `json:"notify_shell_enabled"`
	NotifyShowSender    bool `json:"notify_show_sender"`
	NotifyShowBody      bool `json:"notify_show_body"`
	NotificationsOnLock bool `json:"notifications_on_lock"`

	ThreatProfile    string   `json:"threat_profile,omitempty"`
	PanicAction      string   `json:"panic_action,omitempty"`
	SecurityWarnings []string `json:"security_warnings,omitempty"`

	HasTorPassword bool `json:"has_tor_password"`

	DefaultSaveDir        string `json:"default_save_dir,omitempty"`
	DefaultAttachStartDir string `json:"default_attach_start_dir,omitempty"`
}

type SettingsDomain string

const (
	SettingsDomainAll           SettingsDomain = ""
	SettingsDomainIdentity      SettingsDomain = "identity"
	SettingsDomainLock          SettingsDomain = "lock"
	SettingsDomainTor           SettingsDomain = "tor"
	SettingsDomainNotifications SettingsDomain = "notifications"
	SettingsDomainAdvanced      SettingsDomain = "advanced"
	SettingsDomainFiles         SettingsDomain = "files"
)

type GetSettingsRequest struct {
	Domain SettingsDomain `json:"domain,omitempty"`
}

type SettingsListedResponse struct {
	Settings Settings `json:"settings"`
}

type SyncSettingsRequest struct {
	Settings Settings `json:"settings"`
}

type SettingsChangedPayload struct {
	Settings Settings `json:"settings"`
}

type NickPayload struct {
	Nick      string `json:"nick"`
	IsDefault bool   `json:"is_default,omitempty"`
}

type PairOnionProbePush struct {
	HandleID string `json:"handle_id"`
	Attempt  int    `json:"attempt"`
	Ready    bool   `json:"ready"`
	Error    string `json:"error,omitempty"`
	At       int64  `json:"at"`
}

type PeerPairedPush struct {
	PeerID string `json:"peer_id"`
	Source string `json:"source"`
}

type SubscribeRequest struct {
	Topics []string `json:"topics,omitempty"`
}

type SubscribedResponse struct {
	Topics []string `json:"topics,omitempty"`
}

type ClientLockStateRequest struct {
	SoftLocked bool `json:"soft_locked"`
}

type NotificationEmittedPayload struct {
	ChatID         string `json:"chat_id"`
	PeerLabel      string `json:"peer_label,omitempty"`
	Title          string `json:"title"`
	Body           string `json:"body"`
	RedactedSender bool   `json:"redacted_sender,omitempty"`
	RedactedBody   bool   `json:"redacted_body,omitempty"`
}

const (
	CallActionAccept = "accept"
	CallActionReject = "reject"
	CallActionEnd    = "end"
)

type StartCallRequest struct {
	ChatID string `json:"chat_id"`
}

type CallStartedResponse struct {
	CallID string    `json:"call_id"`
	Call   CallEntry `json:"call"`
}

type RespondCallRequest struct {
	CallID string `json:"call_id"`
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
}

type CallRespondedResponse struct {
	CallID string `json:"call_id"`
	Status string `json:"status"`
}

type CallStateChangedPayload struct {
	Call CallEntry `json:"call"`
}

type CallEntry struct {
	CallID     string   `json:"call_id"`
	ChatID     string   `json:"chat_id"`
	PeerID     string   `json:"peer_id"`
	Direction  string   `json:"direction"`
	Status     string   `json:"status"`
	Modalities []string `json:"modalities"`
	StartedAt  int64    `json:"started_at"`
	UpdatedAt  int64    `json:"updated_at,omitempty"`
	EndedAt    int64    `json:"ended_at,omitempty"`
	FailReason string   `json:"fail_reason,omitempty"`
}

const (
	CallControlMute   = "mute"
	CallControlUnmute = "unmute"
)

type CallStreamEventPayload struct {
	CallID string `json:"call_id"`
	Side   string `json:"side"`
	Type   string `json:"type"`

	BytesIn       uint64  `json:"bytes_in,omitempty"`
	BytesOut      uint64  `json:"bytes_out,omitempty"`
	FramesIn      uint64  `json:"frames_in,omitempty"`
	FramesOut     uint64  `json:"frames_out,omitempty"`
	FramesDropped uint64  `json:"frames_dropped,omitempty"`
	JitterMs      float64 `json:"jitter_ms,omitempty"`
	CpuPct        float64 `json:"cpu_pct,omitempty"`

	Counter uint64 `json:"counter,omitempty"`
	Bytes   uint64 `json:"bytes,omitempty"`
	Muted   bool   `json:"muted,omitempty"`

	Reason string `json:"reason,omitempty"`
}

type CallControlRequest struct {
	CallID string `json:"call_id"`
	Action string `json:"action"`
}

type CallControlledResponse struct {
	CallID string `json:"call_id"`
	Action string `json:"action"`
}

type NewCircuitForPeerRequest struct {
	PeerID string `json:"peer_id"`
}

type NewCircuitClosedResponse struct {
	PeerID string `json:"peer_id"`
	Closed int    `json:"closed"`
}

type PeerSelfProbeRequest struct {
	PeerID string `json:"peer_id"`
}

type PeerSelfReachPayload struct {
	PeerID string `json:"peer_id"`
	Onion  string `json:"onion,omitempty"`
	Ok     bool   `json:"ok"`
	At     int64  `json:"at"`
}

type ExternalProbeBurstRequest struct{}

type ExternalProbeAcceptedResponse struct{}

type ExternalReachPayload struct {
	Ok         bool   `json:"ok"`
	LastTarget string `json:"last_target,omitempty"`
	At         int64  `json:"at"`
}

type RotateBeginRequest struct {
	PeerID string `json:"peer_id"`
}

type RotateBegunResponse struct {
	RotationID string `json:"rotation_id"`
	PeerID     string `json:"peer_id"`
	DeadlineAt int64  `json:"deadline_at"`
}

type RotateUserAcceptRequest struct {
	RotationID string `json:"rotation_id"`
}

type RotateUserDeclineRequest struct {
	RotationID string `json:"rotation_id"`
	Reason     string `json:"reason,omitempty"`
}

type RotateRequestedPush struct {
	RotationID string `json:"rotation_id"`
	PeerID     string `json:"peer_id"`
	StartedAt  int64  `json:"started_at"`
	DeadlineAt int64  `json:"deadline_at"`
}

type RotateLifecyclePush struct {
	RotationID string `json:"rotation_id"`
	PeerID     string `json:"peer_id"`
	Role       string `json:"role"`
	State      string `json:"state"`
	Reason     string `json:"reason,omitempty"`
}
