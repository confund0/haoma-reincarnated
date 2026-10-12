package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dgraph-io/badger/v4"

	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/store"
)

const settingNickKey = "setting:nick"

const settingNickAtKey = "setting:nick-at"

const defaultSelfNick = "mynick"

const selfNickMaxLen = 32

type selfNickState struct {
	nick      string
	at        int64
	isDefault bool
}

func loadSelfNick(st *store.Store) (nick string, isDefault bool, err error) {
	cur, err := loadSelfNickState(st)
	if err != nil {
		return "", false, err
	}
	return cur.nick, cur.isDefault, nil
}

func loadSelfNickState(st *store.Store) (selfNickState, error) {
	if st == nil {
		return selfNickState{}, errors.New("settings: no store")
	}
	cur := selfNickState{nick: defaultSelfNick, isDefault: true}
	err := st.View(func(txn *badger.Txn) error {
		raw, err := txnBytes(txn, []byte(settingNickKey))
		if err != nil {
			return err
		}
		if s := strings.TrimSpace(string(raw)); s != "" {
			cur.nick = s
			cur.isDefault = s == defaultSelfNick
		}
		stamp, err := txnBytes(txn, []byte(settingNickAtKey))
		if err != nil {
			return err
		}
		if len(stamp) == 8 {
			cur.at = int64(binary.BigEndian.Uint64(stamp))
		}
		return nil
	})
	if err != nil {
		return selfNickState{}, fmt.Errorf("settings: load nick: %w", err)
	}
	return cur, nil
}

func txnBytes(txn *badger.Txn, key []byte) ([]byte, error) {
	item, err := txn.Get(key)
	if errors.Is(err, badger.ErrKeyNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return item.ValueCopy(nil)
}

func writeSelfNick(st *store.Store, clean string, at int64) error {
	if st == nil {
		return errors.New("settings: no store")
	}
	var stamp [8]byte
	binary.BigEndian.PutUint64(stamp[:], uint64(at))
	if err := st.Update(func(txn *badger.Txn) error {
		if err := txn.Set([]byte(settingNickKey), []byte(clean)); err != nil {
			return err
		}
		return txn.Set([]byte(settingNickAtKey), stamp[:])
	}); err != nil {
		return fmt.Errorf("settings: save nick: %w", err)
	}
	return nil
}

func selfNickWins(inAt, heldAt int64, in, held string) bool {
	switch {
	case inAt > heldAt:
		return in != held
	case inAt < heldAt:
		return false
	default:
		return in > held
	}
}

func bumpPastSelfNick(ts, held int64) int64 {
	if ts <= held {
		return held + 1
	}
	return ts
}

func saveSelfNick(st *store.Store, nick string) (string, int64, error) {
	clean, err := validateSelfNick(nick)
	if err != nil {
		return "", 0, err
	}
	cur, err := loadSelfNickState(st)
	if err != nil {
		return "", 0, err
	}
	at := bumpPastSelfNick(time.Now().Unix(), cur.at)
	if err := writeSelfNick(st, clean, at); err != nil {
		return "", 0, err
	}
	return clean, at, nil
}

func validateSelfNick(nick string) (string, error) {
	clean := strings.TrimSpace(nick)
	if clean == "" {
		return "", errors.New("nick must not be empty")
	}
	if len(clean) > selfNickMaxLen {
		return "", fmt.Errorf("nick too long (max %d chars)", selfNickMaxLen)
	}
	for _, r := range clean {
		if r < 0x20 || r == 0x7f {
			return "", errors.New("nick must not contain control characters")
		}
	}
	return clean, nil
}

func (d *daemon) selfNick() string {
	if p := d.selfNickCache.Load(); p != nil {
		return *p
	}
	return defaultSelfNick
}

func (d *daemon) setSelfNick(nick string) (string, error) {
	d.selfNickWrite.Lock()
	defer d.selfNickWrite.Unlock()
	clean, at, err := saveSelfNick(d.store, nick)
	if err != nil {
		return "", err
	}
	d.selfNickCache.Store(&clean)
	slog.Debug("self-nick: set here by the operator",
		slog.Int64("nick_at", at),
	)
	return clean, nil
}

func (d *daemon) mergeSelfNick(logger *slog.Logger, in string, inAt int64) (moved bool, err error) {
	clean, err := validateSelfNick(in)
	if err != nil {
		return false, err
	}
	d.selfNickWrite.Lock()
	defer d.selfNickWrite.Unlock()

	cur, err := loadSelfNickState(d.store)
	if err != nil {
		return false, err
	}
	next, at := cur.nick, cur.at
	switch {
	case cur.isDefault:
		next, at = clean, inAt
	default:
		if selfNickWins(inAt, cur.at, clean, cur.nick) {
			next = clean
		}
		if inAt > cur.at {
			at = inAt
		}
	}
	if next == cur.nick && at == cur.at {
		logger.Debug("self-nick: a sibling's name changed nothing here",
			slog.String("outcome", "noop_agreed"),
			slog.Int64("offered_at", inAt),
			slog.Int64("held_at", cur.at),
		)
		return false, nil
	}
	if err := writeSelfNick(d.store, next, at); err != nil {
		return false, err
	}
	d.selfNickCache.Store(&next)
	logger.Debug("self-nick: merged from a sibling",
		slog.String("outcome", "applied"),
		slog.Bool("name_changed", next != cur.nick),
		slog.Int64("offered_at", inAt),
		slog.Int64("held_at", cur.at),
		slog.Int64("nick_at", at),
	)
	return true, nil
}

func (d *daemon) loadSelfNickInto() error {
	nick, _, err := loadSelfNick(d.store)
	if err != nil {
		return err
	}
	d.selfNickCache.Store(&nick)
	return nil
}

func (d *daemon) selfNickIsDefault() bool {
	return d.selfNick() == defaultSelfNick
}

func (d *daemon) selfNickToSend() (nick string, at int64, ok bool) {
	cur, err := loadSelfNickState(d.store)
	if err != nil {
		slog.Warn("self-nick: this device's own name is not readable; nothing is sent",
			slog.Any("err", err),
		)
		return "", 0, false
	}
	if cur.isDefault {
		return "", 0, false
	}
	return cur.nick, cur.at, true
}

func (d *daemon) pushSelfNick() {
	if d.ipcSrv == nil {
		return
	}
	push(d.ipcSrv, ipc.FrameNick, "", ipc.NickPayload{
		Nick:      d.selfNick(),
		IsDefault: d.selfNickIsDefault(),
	})
}

func (d *daemon) adoptSelfNickFromBundle(nick string, at int64) {
	if nick == "" {
		slog.Debug("devicelink: the identity bundle had no self-nick; this device keeps its own")
		return
	}
	clean, err := validateSelfNick(nick)
	if err != nil {

		slog.Warn("devicelink: the identity bundle's self-nick was refused; this device keeps its own",
			slog.Any("err", err),
		)
		return
	}
	d.selfNickWrite.Lock()
	err = writeSelfNick(d.store, clean, at)
	if err == nil {
		d.selfNickCache.Store(&clean)
	}
	d.selfNickWrite.Unlock()
	if err != nil {
		slog.Warn("devicelink: storing the identity bundle's self-nick failed; this device keeps its own",
			slog.Any("err", err),
		)
		return
	}
	slog.Debug("devicelink: self-nick adopted from the identity bundle",
		slog.Int64("nick_at", at),
	)
	d.pushSelfNick()
}

var _ = atomic.Pointer[string]{}
