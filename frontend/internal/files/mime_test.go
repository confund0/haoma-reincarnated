package files_test

import (
	"errors"
	"strings"
	"testing"

	"haoma-frontend/internal/files"
)

var pngHeader = []byte{
	0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
	0x00, 0x00, 0x00, 0x0D,
	'I', 'H', 'D', 'R',
}

func TestReSniffMIME_KnownTypeAgrees(t *testing.T) {
	mgr, _, _ := newManager(t)
	if _, err := mgr.SealAtRest(testChatID, testMsgA, pngHeader); err != nil {
		t.Fatalf("SealAtRest: %v", err)
	}
	got, matches, err := mgr.ReSniffMIME(testChatID, testMsgA, "image/png")
	if err != nil {
		t.Fatalf("ReSniffMIME: %v", err)
	}
	if got != "image/png" {
		t.Errorf("sniffed = %q, want image/png", got)
	}
	if !matches {
		t.Error("expected matchesDeclared=true on identical declared+sniffed")
	}
}

func TestReSniffMIME_KnownTypeContradictsDeclared(t *testing.T) {
	mgr, _, _ := newManager(t)
	if _, err := mgr.SealAtRest(testChatID, testMsgA, pngHeader); err != nil {
		t.Fatalf("SealAtRest: %v", err)
	}
	got, matches, err := mgr.ReSniffMIME(testChatID, testMsgA, "application/pdf")
	if err != nil {
		t.Fatalf("ReSniffMIME: %v", err)
	}
	if got != "image/png" {
		t.Errorf("sniffed = %q, want image/png", got)
	}
	if matches {
		t.Error("expected matchesDeclared=false on declared/sniffed contradiction")
	}
}

func TestReSniffMIME_UnknownTypeIsBenign(t *testing.T) {
	mgr, _, _ := newManager(t)

	if _, err := mgr.SealAtRest(testChatID, testMsgA, []byte("just some text\n")); err != nil {
		t.Fatalf("SealAtRest: %v", err)
	}
	got, matches, err := mgr.ReSniffMIME(testChatID, testMsgA, "text/plain")
	if err != nil {
		t.Fatalf("ReSniffMIME: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty sniffed for unknown type, got %q", got)
	}
	if !matches {
		t.Error("unknown sniff with non-empty declared should NOT contradict (no library assertion)")
	}
}

func TestReSniffMIME_StripsParameters(t *testing.T) {
	mgr, _, _ := newManager(t)
	if _, err := mgr.SealAtRest(testChatID, testMsgA, pngHeader); err != nil {
		t.Fatalf("SealAtRest: %v", err)
	}

	_, matches, err := mgr.ReSniffMIME(testChatID, testMsgA, "image/png; name=cat.png")
	if err != nil {
		t.Fatalf("ReSniffMIME: %v", err)
	}
	if !matches {
		t.Error("declared with parameter should agree with bare sniffed mime")
	}
}

func TestReSniffMIME_EmptyDeclaredAgrees(t *testing.T) {
	mgr, _, _ := newManager(t)
	if _, err := mgr.SealAtRest(testChatID, testMsgA, pngHeader); err != nil {
		t.Fatalf("SealAtRest: %v", err)
	}

	_, matches, err := mgr.ReSniffMIME(testChatID, testMsgA, "")
	if err != nil {
		t.Fatalf("ReSniffMIME: %v", err)
	}
	if !matches {
		t.Error("empty declared should agree with any sniff result")
	}
}

func TestReSniffMIME_SealedNotFound(t *testing.T) {
	mgr, _, _ := newManager(t)
	_, _, err := mgr.ReSniffMIME(testChatID, testMsgA, "image/png")
	if !errors.Is(err, files.ErrSealedNotFound) {
		t.Errorf("err = %v, want ErrSealedNotFound", err)
	}
}

func TestReSniffMIME_RejectsNilManager(t *testing.T) {
	var mgr *files.Manager
	_, _, err := mgr.ReSniffMIME(testChatID, testMsgA, "")
	if err == nil || !strings.Contains(err.Error(), "nil manager") {
		t.Errorf("expected nil-manager error, got %v", err)
	}
}
