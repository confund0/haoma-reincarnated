package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"haoma-frontend/internal/store"
)

const settingChatFontScaleKey = "setting:chat_font_scale"

const (
	defaultChatFontScale = 1.0
	minChatFontScale     = 0.85
	maxChatFontScale     = 1.30
)

func loadChatFontScale(st *store.Store) (float64, error) {
	raw, err := st.Get([]byte(settingChatFontScaleKey))
	if errors.Is(err, store.ErrNotFound) {
		return defaultChatFontScale, nil
	}
	if err != nil {
		return 0, fmt.Errorf("appearance: load chat-font-scale: %w", err)
	}
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return defaultChatFontScale, nil
	}
	v, perr := strconv.ParseFloat(s, 64)
	if perr != nil {
		return defaultChatFontScale, nil
	}
	return clampChatFontScale(v), nil
}

func saveChatFontScale(st *store.Store, scale float64) (float64, error) {
	clean := clampChatFontScale(scale)
	enc := strconv.FormatFloat(clean, 'f', -1, 64)
	if err := st.Put([]byte(settingChatFontScaleKey), []byte(enc)); err != nil {
		return 0, fmt.Errorf("appearance: save chat-font-scale: %w", err)
	}
	return clean, nil
}

func clampChatFontScale(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
		return defaultChatFontScale
	}
	if v < minChatFontScale {
		return minChatFontScale
	}
	if v > maxChatFontScale {
		return maxChatFontScale
	}
	return v
}

func (d *daemon) chatFontScale() float64 {
	if p := d.chatFontScaleCache.Load(); p != nil {
		return *p
	}
	return defaultChatFontScale
}

func (d *daemon) setChatFontScale(scale float64) (float64, error) {
	clean, err := saveChatFontScale(d.store, scale)
	if err != nil {
		return 0, err
	}
	d.chatFontScaleCache.Store(&clean)
	return clean, nil
}

func (d *daemon) loadChatFontScaleInto() error {
	v, err := loadChatFontScale(d.store)
	if err != nil {
		return err
	}
	d.chatFontScaleCache.Store(&v)
	return nil
}
