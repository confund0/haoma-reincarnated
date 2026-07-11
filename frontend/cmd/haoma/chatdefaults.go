package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"haoma-frontend/internal/store"
)

const (
	settingDefaultRetentionKey = "setting:default_retention_sec"
	settingSendReceiptsKey     = "setting:default_send_receipts"
)

const (
	defaultRetentionSecFallback uint64 = 28 * 24 * 3600
	defaultSendReceiptsFallback bool   = true
)

func loadDefaultRetention(st *store.Store) (uint64, error) {
	raw, err := st.Get([]byte(settingDefaultRetentionKey))
	if errors.Is(err, store.ErrNotFound) {
		return defaultRetentionSecFallback, nil
	}
	if err != nil {
		return 0, fmt.Errorf("chatdefaults: load retention: %w", err)
	}
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return defaultRetentionSecFallback, nil
	}
	v, perr := strconv.ParseUint(s, 10, 64)
	if perr != nil {
		return defaultRetentionSecFallback, nil
	}
	return v, nil
}

func loadSendReceipts(st *store.Store) (bool, error) {
	raw, err := st.Get([]byte(settingSendReceiptsKey))
	if errors.Is(err, store.ErrNotFound) {
		return defaultSendReceiptsFallback, nil
	}
	if err != nil {
		return false, fmt.Errorf("chatdefaults: load receipts: %w", err)
	}
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return defaultSendReceiptsFallback, nil
	}
	v, perr := strconv.ParseBool(s)
	if perr != nil {
		return defaultSendReceiptsFallback, nil
	}
	return v, nil
}

func saveChatDefaults(st *store.Store, retentionSec uint64, sendReceipts bool) error {
	if err := st.Put([]byte(settingDefaultRetentionKey), []byte(strconv.FormatUint(retentionSec, 10))); err != nil {
		return fmt.Errorf("chatdefaults: save retention: %w", err)
	}
	if err := st.Put([]byte(settingSendReceiptsKey), []byte(strconv.FormatBool(sendReceipts))); err != nil {
		return fmt.Errorf("chatdefaults: save receipts: %w", err)
	}
	return nil
}

func (d *daemon) defaultRetentionSec() uint64 {
	if p := d.defaultRetentionCache.Load(); p != nil {
		return *p
	}
	return defaultRetentionSecFallback
}

func (d *daemon) defaultSendReceipts() bool {
	if p := d.defaultSendReceiptsCache.Load(); p != nil {
		return *p
	}
	return defaultSendReceiptsFallback
}

func (d *daemon) setChatDefaults(retentionSec uint64, sendReceipts bool) error {
	if err := saveChatDefaults(d.store, retentionSec, sendReceipts); err != nil {
		return err
	}
	d.defaultRetentionCache.Store(&retentionSec)
	d.defaultSendReceiptsCache.Store(&sendReceipts)
	return nil
}

func (d *daemon) loadChatDefaultsInto() error {
	ret, err := loadDefaultRetention(d.store)
	if err != nil {
		return err
	}
	rcp, err := loadSendReceipts(d.store)
	if err != nil {
		return err
	}
	d.defaultRetentionCache.Store(&ret)
	d.defaultSendReceiptsCache.Store(&rcp)
	return nil
}
