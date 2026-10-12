package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/files"
)

func TestFileRowsRenderWithoutTokensOrKeys(t *testing.T) {
	const tok = "hK3vQ9zR7tW1xY5bN8mC2dF6gJ4kL0pS-uE_aI9oT3w"
	const tok2 = "Zq8rT2vW6xY0aB4cD8eF2gH6iJ0kL4mN8oP2qR6sT0u"
	const msgID = "0a0b0c0d0e0f00112233445566778899"
	key := bytes.Repeat([]byte{0xa1, 0x07}, 16)
	nonce := bytes.Repeat([]byte{0xb2, 0x09}, 12)

	meta, err := json.Marshal(files.Metadata{
		MsgID: msgID, ChatID: chat.ChatID("chat-1"), Direction: files.DirOut,
		Token: tok, RecipientTokens: map[string]string{"peer-a": tok, "peer-b": tok2},
		KeyBytes: key, Nonce: nonce, KeyEnvelopes: map[string]string{"peer-a": "env-1"},
		State: files.StateReady,
	})
	if err != nil {
		t.Fatal(err)
	}
	ev, err := json.Marshal(events.Event{
		ChatID: chat.ChatID("chat-1"), Kind: events.KindFile, MsgID: msgID,
		Body: json.RawMessage(`{"token":"` + tok + `","url_path":"/files/` + tok + `","name":"a.jpg","state":"ready"}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	secrets := []string{
		tok[:8], tok2[:8],
		base64.StdEncoding.EncodeToString(key)[:8],
		base64.StdEncoding.EncodeToString(nonce)[:8],
	}
	cases := []struct {
		key   string
		value []byte
		keep  []string
	}{
		{"file:" + msgID, meta, []string{msgID, "peer-a", "peer-b", "env-1"}},
		{"file-by-token:" + tok, []byte(msgID), []string{msgID}},
		{"evt:chat-1:xxxxxxxx:xxxxxxxx", ev, []string{msgID, "a.jpg"}},
	}
	for _, c := range cases {
		group, renderedKey := classify([]byte(c.key))
		out := renderedKey + "\n" + renderValue(group, c.value)
		for _, s := range secrets {
			if strings.Contains(out, s) {
				t.Errorf("%s: the dump shows %q:\n%s", group, s, out)
			}
		}
		for _, k := range c.keep {
			if !strings.Contains(out, k) {
				t.Errorf("%s: the dump dropped %q:\n%s", group, k, out)
			}
		}
	}
}
