package main

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"

	"haoma-frontend/internal/store"
)

type row struct {
	rawKey      []byte
	renderedKey string
	value       []byte
	group       string
}

func main() {
	dataDir := flag.String("data-dir", "", "haoma frontend data-dir (contains meta.json + badger/)")
	flag.Parse()

	if *dataDir == "" {
		fmt.Fprintln(os.Stderr, "--data-dir required")
		os.Exit(2)
	}
	pw := os.Getenv("HAOMA_FRONTEND_PASSPHRASE")
	if pw == "" {
		fmt.Fprintln(os.Stderr, "HAOMA_FRONTEND_PASSPHRASE empty")
		os.Exit(2)
	}

	st, err := store.Unlock(*dataDir, pw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "unlock: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = st.Lock() }()

	var rows []row
	err = st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = true
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			keyCopy := append([]byte(nil), item.Key()...)
			var val []byte
			if err := item.Value(func(v []byte) error {
				val = append([]byte(nil), v...)
				return nil
			}); err != nil {
				return err
			}
			g, rk := classify(keyCopy)
			rows = append(rows, row{rawKey: keyCopy, renderedKey: rk, value: val, group: g})
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "iterate: %v\n", err)
		os.Exit(1)
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].group != rows[j].group {
			return rows[i].group < rows[j].group
		}
		return rows[i].renderedKey < rows[j].renderedKey
	})

	counts := map[string]int{}
	for _, r := range rows {
		counts[r.group]++
	}
	groups := make([]string, 0, len(counts))
	for g := range counts {
		groups = append(groups, g)
	}
	sort.Strings(groups)
	fmt.Println("=== summary ===")
	for _, g := range groups {
		fmt.Printf("  %-24s %d\n", g, counts[g])
	}
	fmt.Println()

	fmt.Println("=== rows ===")
	curGroup := ""
	for _, r := range rows {
		if r.group != curGroup {
			fmt.Printf("\n## %s\n\n", r.group)
			curGroup = r.group
		}
		fmt.Printf("KEY  %s\n", r.renderedKey)
		fmt.Printf("VAL  %s\n\n", renderValue(r.group, r.value))
	}
}

func classify(key []byte) (string, string) {
	s := string(key)
	switch {
	case strings.HasPrefix(s, "evt:"):
		rest := key[len("evt:"):]
		colon := -1
		for i, b := range rest {
			if b == ':' {
				colon = i
				break
			}
		}
		if colon < 0 || len(rest)-colon-1 != 17 {
			return "evt", asciiOrHex(key)
		}
		chatID := string(rest[:colon])
		tail := rest[colon+1:]
		if len(tail) != 17 || tail[8] != ':' {
			return "evt", asciiOrHex(key)
		}
		ts := binary.BigEndian.Uint64(tail[:8])
		seq := binary.BigEndian.Uint64(tail[9:])
		displayTs := time.Unix(int64(ts), 0).UTC().Format("2006-01-02T15:04:05Z")
		return "evt", fmt.Sprintf("evt:%s:%s:%d", chatID, displayTs, seq)

	case strings.HasPrefix(s, "evt-envid:"):
		return "evt-envid", s
	case strings.HasPrefix(s, "evt-msgid:"):
		return "evt-msgid", s
	case s == "events:next_recv_seq":
		return "events-counter", s
	case strings.HasPrefix(s, "chat:"):
		return "chat", s
	case strings.HasPrefix(s, "chat-by-peer:"):
		return "chat-by-peer", s
	case strings.HasPrefix(s, "peer:"):
		return "peer", s
	case strings.HasPrefix(s, "pair_secret:"):
		return "pair_secret", s
	case strings.HasPrefix(s, "pair_mykeys:"):
		return "pair_mykeys", s
	case strings.HasPrefix(s, "signal:"):
		return "signal", s
	case strings.HasPrefix(s, "badger!"):
		return "badger-internal", s
	default:
		return "other", asciiOrHex(key)
	}
}

func asciiOrHex(key []byte) string {
	if printable(key) {
		return string(key)
	}
	return "hex:" + hex.EncodeToString(key)
}

func renderValue(group string, v []byte) string {
	switch group {
	case "events-counter":
		if len(v) == 8 {
			return fmt.Sprintf("uint64 BE = %d", binary.BigEndian.Uint64(v))
		}
		return hexPreview(v)
	case "peer":
		if len(v) == 8 {
			return fmt.Sprintf("uint64 BE = %d", binary.BigEndian.Uint64(v))
		}
		return quotedOrHex(v)
	case "chat", "evt":
		return prettyJSON(v)
	case "chat-by-peer", "evt-envid", "evt-msgid":

		if printable(v) {
			return fmt.Sprintf("%q", string(v))
		}
		return hexPreview(v)
	case "pair_secret":
		return hexPreview(v)
	case "pair_mykeys":
		return jsonOrHex(v)
	case "signal":
		return jsonOrHex(v)
	default:
		return jsonOrHex(v)
	}
}

func prettyJSON(v []byte) string {
	var anyv any
	if err := json.Unmarshal(v, &anyv); err != nil {
		return jsonOrHex(v)
	}
	b, err := json.MarshalIndent(anyv, "     ", "  ")
	if err != nil {
		return jsonOrHex(v)
	}
	return string(b)
}

func jsonOrHex(v []byte) string {
	if len(v) == 0 {
		return "<empty>"
	}
	var anyv any
	if err := json.Unmarshal(v, &anyv); err == nil {
		b, _ := json.MarshalIndent(anyv, "     ", "  ")
		return string(b)
	}
	return hexPreview(v)
}

func quotedOrHex(v []byte) string {
	if printable(v) {
		return fmt.Sprintf("%q", string(v))
	}
	return hexPreview(v)
}

func printable(v []byte) bool {
	if len(v) == 0 {
		return false
	}
	for _, b := range v {
		if b < 0x20 || b > 0x7e {
			return false
		}
	}
	return true
}

func hexPreview(v []byte) string {
	const max = 96
	if len(v) <= max {
		return fmt.Sprintf("hex(%d) %s", len(v), hex.EncodeToString(v))
	}
	return fmt.Sprintf("hex(%d) %s…", len(v), hex.EncodeToString(v[:max]))
}
