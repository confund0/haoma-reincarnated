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

	"github.com/dgraph-io/badger/v4"

	"haoma/internal/store"
)

type row struct {
	rawKey      []byte
	renderedKey string
	value       []byte
	group       string
}

func main() {
	storeDir := flag.String("store", "", "haomad backend store dir (contains meta.json + badger/)")
	flag.Parse()

	if *storeDir == "" {
		fmt.Fprintln(os.Stderr, "--store required")
		os.Exit(2)
	}
	pw := os.Getenv("HAOMA_PASSPHRASE")
	if pw == "" {
		fmt.Fprintln(os.Stderr, "HAOMA_PASSPHRASE empty")
		os.Exit(2)
	}

	st, err := store.Unlock(*storeDir, pw)
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
	case strings.HasPrefix(s, "contact:"):
		return "contact", s
	case strings.HasPrefix(s, "addr:"):
		return "addr", s
	case strings.HasPrefix(s, "state:"):
		return "state", s
	case strings.HasPrefix(s, "outbox:"):
		return "outbox", s
	case strings.HasPrefix(s, "outbox-due:"):
		return "outbox-due", s
	case strings.HasPrefix(s, "outbox-state:"):
		return "outbox-state", s
	case strings.HasPrefix(s, "xport-queue:"):
		return "xport-queue", s
	case strings.HasPrefix(s, "pair-invite:"):
		return "pair-invite", s
	case strings.HasPrefix(s, "inbox:"):
		return "inbox", s
	case strings.HasPrefix(s, "inbox-envid:"):
		return "inbox-envid", s
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
	case "contact", "outbox", "inbox":
		return prettyJSON(v)
	case "addr", "outbox-state":
		return quotedOrHex(v)
	case "outbox-due":

		return quotedOrHex(v)
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

var _ = binary.BigEndian
