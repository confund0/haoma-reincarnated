package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"haoma-frontend/internal/files"
)

func TestSiblingOfferRecordRendersWithoutItsSecrets(t *testing.T) {
	const token = "sibling-token-9f8e7d6c5b4a"
	held := files.SiblingOffer{
		MsgID:         "msg-sib-1",
		ServingPeerID: "0000000000000000000000000000d0d0",
		Token:         token,
		UrlPath:       "/files/" + token,
		Key:           bytes.Repeat([]byte{0xa1}, 32),
		Nonce:         bytes.Repeat([]byte{0xb2}, 24),
	}
	raw, err := json.Marshal(held)
	if err != nil {
		t.Fatal(err)
	}

	group, _ := classify([]byte("sibling-offer:msg-sib-1"))
	if group != "sibling-offer" {
		t.Fatalf("classify put the record in group %q, want its own group", group)
	}
	out := renderValue(group, raw)
	secrets := []string{
		token[:8],
		base64.StdEncoding.EncodeToString(held.Key)[:8],
		base64.StdEncoding.EncodeToString(held.Nonce)[:8],
	}
	for _, s := range secrets {
		if strings.Contains(out, s) {
			t.Fatalf("the dump shows %q:\n%s", s, out)
		}
	}
	for _, want := range []string{"msg-sib-1", held.ServingPeerID, "(elided, 26 chars)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the dump dropped %q:\n%s", want, out)
		}
	}

	junk := append([]byte("not json "), []byte(token)...)
	if out := renderValue(group, junk); strings.Contains(out, token[:8]) {
		t.Fatalf("an unreadable record printed its bytes: %s", out)
	}
}

func TestGapOfferRecordRendersWithoutItsSecrets(t *testing.T) {
	const token = "gap-token-1a2b3c4d5e6f7a8b"
	kept := files.GapOffer{
		MsgID:           "0123456789abcdef0123456789abcdef",
		RequesterPeerID: "0000000000000000000000000000d0d0",
		StagingID:       "5a1b2c3d4e5f60718293a4b5c6d7e8f9",
		Token:           token,
		UrlPath:         "/files/" + token,
		Key:             bytes.Repeat([]byte{0xa1}, 32),
		Nonce:           bytes.Repeat([]byte{0xb2}, 24),
		StagedAt:        1742643890,
	}
	raw, err := json.Marshal(kept)
	if err != nil {
		t.Fatal(err)
	}
	group, _ := classify([]byte("gap-offer:" + kept.MsgID + ":" + kept.RequesterPeerID))
	if group != "gap-offer" {
		t.Fatalf("classify put the record in group %q, want its own group", group)
	}
	out := renderValue(group, raw)
	for _, s := range []string{token[:8], base64.StdEncoding.EncodeToString(kept.Key)[:8], base64.StdEncoding.EncodeToString(kept.Nonce)[:8]} {
		if strings.Contains(out, s) {
			t.Fatalf("the dump shows %q:\n%s", s, out)
		}
	}
	for _, want := range []string{kept.MsgID, kept.StagingID} {
		if !strings.Contains(out, want) {
			t.Fatalf("the dump dropped %q:\n%s", want, out)
		}
	}
}
