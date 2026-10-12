package main

import (
	"encoding/json"
	"strings"
	"testing"

	"haoma/internal/files"
)

func TestDumpPrintsNoFileToken(t *testing.T) {
	const tok = "Q9vRtokEnValue-opens-the-blob-8c7d6e5f4a3b2c1d0e"
	const msgID = "0a0b0c0d0e0f00112233445566778899"

	tokenRow, err := json.Marshal(files.TokenRow{Token: tok, RecipientPeerID: "peer-x", MsgID: msgID})
	if err != nil {
		t.Fatal(err)
	}
	fetchRow, err := json.Marshal(files.Fetch{Token: tok, MsgID: msgID, PeerID: "peer-y", UrlPath: "/files/" + tok, State: files.FetchStatePending})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		key, group string
		value      []byte
	}{
		{"file-token:" + tok, "file-token", tokenRow},
		{"file-blob:" + msgID + ":" + tok, "file-blob", []byte{}},
		{"fetch:" + tok, "fetch", fetchRow},
	}
	for _, c := range cases {
		group, key := classify([]byte(c.key))
		if group != c.group {
			t.Fatalf("%s: group %q, want %q", c.key[:10], group, c.group)
		}
		out := key + "\n" + renderValue(group, c.value)
		if strings.Contains(out, tok[:8]) {
			t.Errorf("%s: the dump shows the token:\n%s", c.group, out)
		}
		if !strings.Contains(out, "(elided, ") {
			t.Errorf("%s: the dump does not say a token was there:\n%s", c.group, out)
		}
		if c.group != "file-token" && c.group != "fetch" && !strings.Contains(key, msgID) {
			t.Errorf("%s: the dump dropped the msg id: %s", c.group, key)
		}
		if len(c.value) > 0 && !strings.Contains(out, msgID) {
			t.Errorf("%s: the dump dropped the msg id from the value:\n%s", c.group, out)
		}
	}
	if out := renderValue("fetch", []byte("not json "+tok)); strings.Contains(out, tok[:8]) {
		t.Fatalf("an unreadable row printed its bytes: %s", out)
	}
}
