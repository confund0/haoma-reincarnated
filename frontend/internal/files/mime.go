package files

import (
	"errors"
	"fmt"
	"mime"
	"strings"

	"github.com/h2non/filetype"

	"haoma-frontend/internal/chat"
)

const mimeSniffBytes = 8192

func (m *Manager) ReSniffMIME(chatID chat.ChatID, msgID, declared string) (sniffed string, matchesDeclared bool, err error) {
	if m == nil {
		return "", false, errors.New("files: nil manager")
	}
	plaintext, err := m.UnsealAtRest(chatID, msgID)
	if err != nil {
		return "", false, err
	}
	defer zero(plaintext)

	head := plaintext
	if len(head) > mimeSniffBytes {
		head = head[:mimeSniffBytes]
	}
	kind, matchErr := filetype.Match(head)
	if matchErr != nil {

		return "", normalizeMIME(declared) == "", fmt.Errorf("files: mime sniff: %w", matchErr)
	}
	if kind == filetype.Unknown {

		return "", true, nil
	}
	sniffed = kind.MIME.Value
	return sniffed, mimeAgrees(declared, sniffed), nil
}

func mimeAgrees(declared, sniffed string) bool {
	d := normalizeMIME(declared)
	s := normalizeMIME(sniffed)
	if d == "" || s == "" {
		return true
	}
	return d == s
}

func normalizeMIME(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	mt, _, err := mime.ParseMediaType(s)
	if err != nil {

		return strings.ToLower(s)
	}
	return mt
}
