package chat

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/dgraph-io/badger/v4"

	"haoma-frontend/internal/store"
)

const (
	prefixChat       = "chat:"
	prefixChatByPeer = "chat-by-peer:"
)

var ErrNotFound = errors.New("chat: not found")

type ChatID string

type Kind string

const (
	KindDirect Kind = "direct"
	KindGroup  Kind = "group"
)

const MaxMembersDirect = 2

const MaxMembersGroup = 20

type BaseChat struct {
	ID                ChatID   `json:"id"`
	Members           []string `json:"members"`
	RetentionTTL      uint32   `json:"retention_ttl"`
	LastTimerChangeTs int64    `json:"last_timer_change_ts,omitempty"`
	CreatedAt         int64    `json:"created_at"`
	LastActivityAt    int64    `json:"last_activity_at,omitempty"`
	UnreadCount       uint32   `json:"unread_count,omitempty"`
	GroupName         string   `json:"group_name,omitempty"`
	GroupAlias        string   `json:"group_alias,omitempty"`

	DisableReadReceipts bool `json:"disable_read_receipts,omitempty"`

	NotificationsMuted bool `json:"notifications_muted,omitempty"`
}

type DirectChat struct {
	BaseChat
	MaxMembers int    `json:"max_members"`
	PeerID     string `json:"peer_id"`
}

func (d *DirectChat) Kind() Kind { return KindDirect }

type GroupChat struct {
	BaseChat
	MaxMembers  int    `json:"max_members"`
	OwnerPeerID string `json:"owner_peer_id"`
}

func (g *GroupChat) Kind() Kind { return KindGroup }

type Chat interface {
	Kind() Kind
	ChatID() ChatID
	MemberList() []string
	Retention() uint32
	TimerChangeTs() int64
	ReadReceiptsDisabled() bool
	IsNotificationsMuted() bool
}

func (d *DirectChat) ChatID() ChatID             { return d.ID }
func (g *GroupChat) ChatID() ChatID              { return g.ID }
func (d *DirectChat) MemberList() []string       { return d.Members }
func (g *GroupChat) MemberList() []string        { return g.Members }
func (d *DirectChat) Retention() uint32          { return d.RetentionTTL }
func (g *GroupChat) Retention() uint32           { return g.RetentionTTL }
func (d *DirectChat) TimerChangeTs() int64       { return d.LastTimerChangeTs }
func (g *GroupChat) TimerChangeTs() int64        { return g.LastTimerChangeTs }
func (d *DirectChat) ReadReceiptsDisabled() bool { return d.DisableReadReceipts }
func (g *GroupChat) ReadReceiptsDisabled() bool  { return g.DisableReadReceipts }
func (d *DirectChat) IsNotificationsMuted() bool { return d.NotificationsMuted }
func (g *GroupChat) IsNotificationsMuted() bool  { return g.NotificationsMuted }

type record struct {
	Kind Kind            `json:"kind"`
	Data json.RawMessage `json:"data"`
}

type Store struct {
	st  *store.Store
	now func() time.Time
}

func NewStore(st *store.Store) *Store {
	return &Store{st: st, now: time.Now}
}

func NewID() (ChatID, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("chat: generate id: %w", err)
	}
	return ChatID(hex.EncodeToString(b[:])), nil
}

func (s *Store) CreateDirect(peerID string) (*DirectChat, error) {
	if peerID == "" {
		return nil, errors.New("chat: CreateDirect: empty peer id")
	}

	if existing, err := s.GetByDirectPeer(peerID); err == nil {
		return existing, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	id, err := NewID()
	if err != nil {
		return nil, err
	}
	now := s.now().Unix()
	dc := &DirectChat{
		BaseChat: BaseChat{
			ID:        id,
			Members:   []string{peerID},
			CreatedAt: now,
		},
		MaxMembers: MaxMembersDirect,
		PeerID:     peerID,
	}
	if err := s.putDirect(dc); err != nil {
		return nil, err
	}
	return dc, nil
}

func (s *Store) putDirect(dc *DirectChat) error {
	data, err := json.Marshal(dc)
	if err != nil {
		return fmt.Errorf("chat: marshal direct: %w", err)
	}
	rec := record{Kind: KindDirect, Data: data}
	raw, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("chat: marshal record: %w", err)
	}
	return s.st.Update(func(txn *badger.Txn) error {
		if err := txn.Set(chatKey(dc.ID), raw); err != nil {
			return err
		}
		return txn.Set(byPeerKey(dc.PeerID), []byte(dc.ID))
	})
}

func (s *Store) GetByDirectPeer(peerID string) (*DirectChat, error) {
	if peerID == "" {
		return nil, errors.New("chat: GetByDirectPeer: empty peer id")
	}
	var chatID ChatID
	err := s.st.View(func(txn *badger.Txn) error {
		item, err := txn.Get(byPeerKey(peerID))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return item.Value(func(v []byte) error {
			chatID = ChatID(append([]byte(nil), v...))
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	c, err := s.Get(chatID)
	if err != nil {
		return nil, err
	}
	dc, ok := c.(*DirectChat)
	if !ok {
		return nil, fmt.Errorf("chat: by-peer index for %s points at a non-direct chat", peerID)
	}
	return dc, nil
}

func (s *Store) Get(id ChatID) (Chat, error) {
	if id == "" {
		return nil, errors.New("chat: Get: empty id")
	}
	var raw []byte
	err := s.st.View(func(txn *badger.Txn) error {
		item, err := txn.Get(chatKey(id))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return item.Value(func(v []byte) error {
			raw = append([]byte(nil), v...)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return decodeRecord(raw)
}

func (s *Store) List() ([]Chat, error) {
	var out []Chat
	err := s.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefixChat)
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			var raw []byte
			if err := it.Item().Value(func(v []byte) error {
				raw = append([]byte(nil), v...)
				return nil
			}); err != nil {
				return err
			}
			c, err := decodeRecord(raw)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return nil
	})
	return out, err
}

func (s *Store) SetRetentionTTL(id ChatID, seconds uint32) error {
	if id == "" {
		return errors.New("chat: SetRetentionTTL: empty id")
	}
	return s.mutate(id, func(c Chat) error {
		switch v := c.(type) {
		case *DirectChat:
			v.RetentionTTL = seconds
		case *GroupChat:
			v.RetentionTTL = seconds
		default:
			return fmt.Errorf("chat: SetRetentionTTL: unknown chat type %T", c)
		}
		return nil
	})
}

func (s *Store) SetRetentionAndTimerTs(id ChatID, seconds uint32, ts int64) error {
	if id == "" {
		return errors.New("chat: SetRetentionAndTimerTs: empty id")
	}
	return s.mutate(id, func(c Chat) error {
		switch v := c.(type) {
		case *DirectChat:
			v.RetentionTTL = seconds
			v.LastTimerChangeTs = ts
		case *GroupChat:
			v.RetentionTTL = seconds
			v.LastTimerChangeTs = ts
		default:
			return fmt.Errorf("chat: SetRetentionAndTimerTs: unknown chat type %T", c)
		}
		return nil
	})
}

func (s *Store) SetGroupAlias(id ChatID, alias string) error {
	if id == "" {
		return errors.New("chat: SetGroupAlias: empty id")
	}
	return s.mutate(id, func(c Chat) error {
		switch v := c.(type) {
		case *DirectChat:
			v.GroupAlias = alias
		case *GroupChat:
			v.GroupAlias = alias
		default:
			return fmt.Errorf("chat: SetGroupAlias: unknown chat type %T", c)
		}
		return nil
	})
}

func (s *Store) SetDisableReadReceipts(id ChatID, disabled bool) error {
	if id == "" {
		return errors.New("chat: SetDisableReadReceipts: empty id")
	}
	return s.mutate(id, func(c Chat) error {
		switch v := c.(type) {
		case *DirectChat:
			v.DisableReadReceipts = disabled
		case *GroupChat:
			v.DisableReadReceipts = disabled
		default:
			return fmt.Errorf("chat: SetDisableReadReceipts: unknown chat type %T", c)
		}
		return nil
	})
}

func (s *Store) SetNotificationsMuted(id ChatID, muted bool) error {
	if id == "" {
		return errors.New("chat: SetNotificationsMuted: empty id")
	}
	return s.mutate(id, func(c Chat) error {
		switch v := c.(type) {
		case *DirectChat:
			v.NotificationsMuted = muted
		case *GroupChat:
			v.NotificationsMuted = muted
		default:
			return fmt.Errorf("chat: SetNotificationsMuted: unknown chat type %T", c)
		}
		return nil
	})
}

func (s *Store) SetGroupName(id ChatID, name string) error {
	if id == "" {
		return errors.New("chat: SetGroupName: empty id")
	}
	return s.mutate(id, func(c Chat) error {
		switch v := c.(type) {
		case *DirectChat:
			v.GroupName = name
		case *GroupChat:
			v.GroupName = name
		default:
			return fmt.Errorf("chat: SetGroupName: unknown chat type %T", c)
		}
		return nil
	})
}

func (s *Store) BumpActivity(id ChatID, ts int64) (bool, error) {
	if id == "" {
		return false, errors.New("chat: BumpActivity: empty id")
	}
	var changed bool
	err := s.mutate(id, func(c Chat) error {
		switch v := c.(type) {
		case *DirectChat:
			if ts > v.LastActivityAt {
				v.LastActivityAt = ts
				changed = true
			}
		case *GroupChat:
			if ts > v.LastActivityAt {
				v.LastActivityAt = ts
				changed = true
			}
		default:
			return fmt.Errorf("chat: BumpActivity: unknown chat type %T", c)
		}
		return nil
	})
	return changed, err
}

func (s *Store) IncrementUnread(id ChatID) (uint32, error) {
	if id == "" {
		return 0, errors.New("chat: IncrementUnread: empty id")
	}
	var newCount uint32
	err := s.mutate(id, func(c Chat) error {
		switch v := c.(type) {
		case *DirectChat:
			if v.UnreadCount < ^uint32(0) {
				v.UnreadCount++
			}
			newCount = v.UnreadCount
		case *GroupChat:
			if v.UnreadCount < ^uint32(0) {
				v.UnreadCount++
			}
			newCount = v.UnreadCount
		default:
			return fmt.Errorf("chat: IncrementUnread: unknown chat type %T", c)
		}
		return nil
	})
	return newCount, err
}

func (s *Store) ClearUnread(id ChatID) (bool, error) {
	if id == "" {
		return false, errors.New("chat: ClearUnread: empty id")
	}
	var changed bool
	err := s.mutate(id, func(c Chat) error {
		switch v := c.(type) {
		case *DirectChat:
			if v.UnreadCount != 0 {
				v.UnreadCount = 0
				changed = true
			}
		case *GroupChat:
			if v.UnreadCount != 0 {
				v.UnreadCount = 0
				changed = true
			}
		default:
			return fmt.Errorf("chat: ClearUnread: unknown chat type %T", c)
		}
		return nil
	})
	return changed, err
}

func (s *Store) mutate(id ChatID, mutFn func(Chat) error) error {
	return s.st.Update(func(txn *badger.Txn) error {
		item, err := txn.Get(chatKey(id))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		var raw []byte
		if err := item.Value(func(v []byte) error {
			raw = append([]byte(nil), v...)
			return nil
		}); err != nil {
			return err
		}
		c, err := decodeRecord(raw)
		if err != nil {
			return err
		}
		if err := mutFn(c); err != nil {
			return err
		}
		var (
			kind    Kind
			newData []byte
			mErr    error
		)
		switch v := c.(type) {
		case *DirectChat:
			kind = KindDirect
			newData, mErr = json.Marshal(v)
		case *GroupChat:
			kind = KindGroup
			newData, mErr = json.Marshal(v)
		default:
			return fmt.Errorf("chat: mutate: unknown chat type %T", c)
		}
		if mErr != nil {
			return fmt.Errorf("chat: marshal after mutate: %w", mErr)
		}
		rec := record{Kind: kind, Data: newData}
		newRaw, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("chat: marshal record after mutate: %w", err)
		}
		return txn.Set(chatKey(id), newRaw)
	})
}

func (s *Store) Delete(id ChatID) error {
	if id == "" {
		return errors.New("chat: Delete: empty id")
	}
	return s.st.Update(func(txn *badger.Txn) error {
		item, err := txn.Get(chatKey(id))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		var raw []byte
		if err := item.Value(func(v []byte) error {
			raw = append([]byte(nil), v...)
			return nil
		}); err != nil {
			return err
		}
		c, err := decodeRecord(raw)
		if err != nil {
			return err
		}
		if dc, ok := c.(*DirectChat); ok && dc.PeerID != "" {
			if err := txn.Delete(byPeerKey(dc.PeerID)); err != nil {
				return err
			}
		}
		return txn.Delete(chatKey(id))
	})
}

func SenderName(c Chat, direction string, senderPeerID string, peerAlias func(peerID string) string) string {
	if direction == "out" {
		return "me"
	}
	switch c.Kind() {
	case KindDirect:
		dc := c.(*DirectChat)
		return resolveAlias(dc.PeerID, peerAlias)
	case KindGroup:
		return resolveAlias(senderPeerID, peerAlias)
	default:
		return resolveAlias(senderPeerID, peerAlias)
	}
}

func resolveAlias(peerID string, peerAlias func(peerID string) string) string {
	if peerAlias != nil {
		if a := peerAlias(peerID); a != "" {
			return a
		}
	}
	if len(peerID) >= 8 {
		return peerID[:8]
	}
	return peerID
}

func decodeRecord(raw []byte) (Chat, error) {
	var rec record
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, fmt.Errorf("chat: decode record: %w", err)
	}
	switch rec.Kind {
	case KindDirect:
		var dc DirectChat
		if err := json.Unmarshal(rec.Data, &dc); err != nil {
			return nil, fmt.Errorf("chat: decode direct: %w", err)
		}
		return &dc, nil
	case KindGroup:
		var gc GroupChat
		if err := json.Unmarshal(rec.Data, &gc); err != nil {
			return nil, fmt.Errorf("chat: decode group: %w", err)
		}
		return &gc, nil
	default:
		return nil, fmt.Errorf("chat: unknown kind %q", rec.Kind)
	}
}

func chatKey(id ChatID) []byte {
	out := make([]byte, 0, len(prefixChat)+len(id))
	out = append(out, prefixChat...)
	out = append(out, id...)
	return out
}

func byPeerKey(peerID string) []byte {
	out := make([]byte, 0, len(prefixChatByPeer)+len(peerID))
	out = append(out, prefixChatByPeer...)
	out = append(out, peerID...)
	return out
}
