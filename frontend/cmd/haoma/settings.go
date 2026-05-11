package main

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"haoma-frontend/internal/store"
)

const settingNickKey = "setting:nick"

const defaultSelfNick = "mynick"

const selfNickMaxLen = 32

func loadSelfNick(st *store.Store) (nick string, isDefault bool, err error) {
	raw, err := st.Get([]byte(settingNickKey))
	if errors.Is(err, store.ErrNotFound) {
		return defaultSelfNick, true, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("settings: load nick: %w", err)
	}
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return defaultSelfNick, true, nil
	}
	return s, s == defaultSelfNick, nil
}

func saveSelfNick(st *store.Store, nick string) (string, error) {
	clean, err := validateSelfNick(nick)
	if err != nil {
		return "", err
	}
	if err := st.Put([]byte(settingNickKey), []byte(clean)); err != nil {
		return "", fmt.Errorf("settings: save nick: %w", err)
	}
	return clean, nil
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
	clean, err := saveSelfNick(d.store, nick)
	if err != nil {
		return "", err
	}
	d.selfNickCache.Store(&clean)
	return clean, nil
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

var _ = atomic.Pointer[string]{}
