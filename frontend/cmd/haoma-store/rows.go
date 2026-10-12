package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/dgraph-io/badger/v4"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/crdt"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/files"
	"haoma-frontend/internal/linkeddevice"
	"haoma-frontend/internal/peerstate"
	"haoma-frontend/internal/store"
)

const eventPrefix = "evt:"

const identityKey = "signal:identity_pub"

const remoteIdentityPrefix = "signal:remote_ident:"

const filePrefix = "file:"

type msgRow struct {
	MsgID     string
	ChatID    chat.ChatID
	Kind      string
	Direction string
	DisplayTs int64

	SenderPeerID string

	ArrivedFromPeerID string

	DeliveryState string
	ReadAt        int64
	EditedAt      int64
	DeletedAt     int64
	ExpireSeconds uint32

	ReadReceiptSentAt int64

	MirroredFromSibling bool

	TextDigest string

	Text string

	FileName    string
	FileCaption string

	DecryptStatus string
	FailReason    string
}

type fileRow struct {
	MsgID        string
	ChatID       chat.ChatID
	Direction    string
	State        string
	OriginalName string
	Mime         string
	Size         uint64
	Sha256       string
	CreatedAt    int64
	UpdatedAt    int64
}

type fileBody struct {
	Name    string `json:"name"`
	Size    uint64 `json:"size"`
	State   string `json:"state"`
	Caption string `json:"caption,omitempty"`
}

type textBody struct {
	Text string `json:"text"`
}

func streamMessages(st *store.Store, keepText bool, fn func(msgRow) error) error {
	return st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(eventPrefix)
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.ValidForPrefix([]byte(eventPrefix)); it.Next() {
			var ev events.Event
			if err := it.Item().Value(func(v []byte) error {
				return json.Unmarshal(v, &ev)
			}); err != nil {
				return fmt.Errorf("read message row %s: %w", string(it.Item().Key()), err)
			}
			row := msgRow{
				MsgID:         ev.MsgID,
				ChatID:        ev.ChatID,
				Kind:          string(ev.Kind),
				Direction:     string(ev.Direction),
				DisplayTs:     ev.DisplayTs,
				SenderPeerID:  ev.SenderPeerID,
				DeliveryState: ev.DeliveryState,
				ReadAt:        ev.ReadAt,
				EditedAt:      ev.EditedAt,
				DeletedAt:     ev.DeletedAt,
				ExpireSeconds: ev.ExpireSeconds,
				DecryptStatus: string(ev.DecryptStatus),
				FailReason:    string(ev.FailReason),

				ReadReceiptSentAt:   ev.ReadReceiptSentAt,
				MirroredFromSibling: ev.MirroredFromSibling,
				ArrivedFromPeerID:   ev.ArrivedFromPeerID,
			}
			text, name, caption := readBody(ev)
			row.TextDigest = digest(text)
			row.FileName = name
			row.FileCaption = caption
			if keepText {
				row.Text = text
			}
			if err := fn(row); err != nil {
				return err
			}
		}
		return nil
	})
}

func readBody(ev events.Event) (text, name, caption string) {
	if len(ev.Body) == 0 {
		return "", "", ""
	}
	switch ev.Kind {
	case events.KindText:
		var b textBody
		if err := json.Unmarshal(ev.Body, &b); err != nil {
			return "", "", ""
		}
		return b.Text, "", ""
	case events.KindFile:
		var b fileBody
		if err := json.Unmarshal(ev.Body, &b); err != nil {
			return "", "", ""
		}
		return b.Caption, b.Name, b.Caption
	}
	return "", "", ""
}

func digest(text string) string {
	if text == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:8])
}

func streamFiles(st *store.Store, fn func(fileRow) error) error {
	return st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(filePrefix)
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.ValidForPrefix([]byte(filePrefix)); it.Next() {
			key := string(it.Item().Key())
			if !strings.HasPrefix(key, filePrefix) {
				continue
			}
			var m files.Metadata
			if err := it.Item().Value(func(v []byte) error {
				return json.Unmarshal(v, &m)
			}); err != nil {
				return fmt.Errorf("read file row %s: %w", key, err)
			}
			if err := fn(fileRow{
				MsgID:        m.MsgID,
				ChatID:       m.ChatID,
				Direction:    string(m.Direction),
				State:        string(m.State),
				OriginalName: m.OriginalName,
				Mime:         m.Mime,
				Size:         m.Size,
				Sha256:       m.Sha256Ciphertext,
				CreatedAt:    m.CreatedAt,
				UpdatedAt:    m.UpdatedAt,
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

type chatRow struct {
	ID   chat.ChatID
	Kind string

	PeerIDs []string

	PairedAt map[string]int64

	Fingerprint string

	GroupName      string
	RetentionTTL   uint32
	LastActivityAt int64
	LastReadAt     int64
	UnreadCount    uint32
}

func readChats(st *store.Store) ([]chatRow, error) {
	list, err := chat.NewStore(st).List()
	if err != nil {
		return nil, err
	}
	meta := peerstate.NewMeta(st)
	out := make([]chatRow, 0, len(list))
	for _, c := range list {
		row := chatRow{ID: c.ChatID(), Kind: string(c.Kind())}
		switch v := c.(type) {
		case *chat.DirectChat:
			row.Fingerprint = v.Fingerprint
			row.PeerIDs = append([]string(nil), v.PeerIDs...)
			if len(row.PeerIDs) == 0 && v.PeerID != "" {
				row.PeerIDs = []string{v.PeerID}
			}
			row.PairedAt = make(map[string]int64, len(row.PeerIDs))
			for _, id := range row.PeerIDs {
				at, err := meta.PairedAt(id)
				if err != nil {
					return nil, err
				}
				row.PairedAt[id] = at
			}
			row.RetentionTTL = v.RetentionTTL
			row.LastActivityAt = v.LastActivityAt
			row.LastReadAt = v.LastReadAt
			row.UnreadCount = v.UnreadCount
		case *chat.GroupChat:
			row.GroupName = v.GroupName
			row.RetentionTTL = v.RetentionTTL
			row.LastActivityAt = v.LastActivityAt
			row.LastReadAt = v.LastReadAt
			row.UnreadCount = v.UnreadCount
		}
		out = append(out, row)
	}
	return out, nil
}

func readIdentity(st *store.Store) (string, error) {
	raw, err := st.Get([]byte(identityKey))
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func countSiblingsOnMyIdentity(st *store.Store, siblings []string, myIdentity string) (int, error) {
	n := 0
	for _, peerID := range siblings {
		raw, err := st.Get([]byte(remoteIdentityPrefix + peerID))
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if hex.EncodeToString(raw) == myIdentity {
			n++
		}
	}
	return n, nil
}

func readContacts(st *store.Store) ([]peerstate.Person, error) {
	return peerstate.NewMeta(st).Persons()
}

func readLinkedDevices(st *store.Store) (crdt.TwoPSet, error) {
	return linkeddevice.New(st).Get()
}

type nameBook struct {
	byPeerID      map[string]string
	byFingerprint map[string]string

	fingerprintByPeerID map[string]string
}

func buildNameBook(people []peerstate.Person) *nameBook {
	nb := &nameBook{
		byPeerID:            map[string]string{},
		byFingerprint:       map[string]string{},
		fingerprintByPeerID: map[string]string{},
	}
	for _, p := range people {
		for _, id := range p.PeerIDs {
			nb.fingerprintByPeerID[id] = p.Fingerprint
		}
		name := p.Meta.Alias
		if name == "" {
			name = p.Meta.Nick
		}
		if name == "" {
			continue
		}
		nb.byFingerprint[p.Fingerprint] = name
		for _, id := range p.PeerIDs {
			nb.byPeerID[id] = name
		}
	}
	return nb
}

func (nb *nameBook) personFor(c chatRow) string {
	for _, id := range c.PeerIDs {
		if fp := nb.fingerprintByPeerID[id]; fp != "" {
			return fp
		}
	}
	return c.Fingerprint
}

func (nb *nameBook) nameForChat(c chatRow) string {
	if c.Kind == string(chat.KindGroup) {
		if c.GroupName != "" {
			return "group " + c.GroupName
		}
		return "group " + shortID(string(c.ID))
	}
	if fp := nb.personFor(c); fp != "" {
		return nb.forFingerprint(fp)
	}
	for _, id := range c.PeerIDs {
		if name, ok := nb.byPeerID[id]; ok {
			return name
		}
	}
	return "conversation " + shortID(string(c.ID))
}

func (nb *nameBook) forFingerprint(fp string) string {
	if fp == "" {
		return "no fingerprint"
	}
	if name, ok := nb.byFingerprint[fp]; ok {
		return name
	}
	return shortID(fp)
}

func (nb *nameBook) forPeerID(peerID string) string {
	if peerID == "" {
		return ""
	}
	if name, ok := nb.byPeerID[peerID]; ok {
		return name
	}
	return shortID(peerID)
}
