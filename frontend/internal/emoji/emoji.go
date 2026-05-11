package emoji

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
)

type EmojiEntry struct {
	Emoji            string   `json:"emoji"`
	Keywords         []string `json:"keywords"`
	TextReps         []string `json:"text_reps"`
	TerminalFriendly bool     `json:"terminal_friendly"`
	AsciiFallback    string   `json:"ascii_fallback"`
}

type Catalog struct {
	CategoriesOrder []string                `json:"categories_order"`
	Categories      map[string][]EmojiEntry `json:"categories"`
}

//go:embed data.json
var dataJSON []byte

var (
	loadOnce  sync.Once
	loadErr   error
	parsedCat Catalog

	allEntries          []EmojiEntry
	terminalFriendlySet []EmojiEntry
	byGlyph             map[string]*EmojiEntry
)

func load() {
	loadOnce.Do(func() {
		if err := json.Unmarshal(dataJSON, &parsedCat); err != nil {
			loadErr = fmt.Errorf("emoji: parse data.json: %w", err)
			return
		}
		byGlyph = make(map[string]*EmojiEntry, len(parsedCat.Categories)*16)
		for _, cat := range parsedCat.CategoriesOrder {
			entries := parsedCat.Categories[cat]
			for i := range entries {
				e := &entries[i]
				allEntries = append(allEntries, *e)
				if e.TerminalFriendly {
					terminalFriendlySet = append(terminalFriendlySet, *e)
				}
				byGlyph[e.Emoji] = e
			}
		}
	})
	if loadErr != nil {
		panic(loadErr)
	}
}

func Categories() []string {
	load()
	out := make([]string, len(parsedCat.CategoriesOrder))
	copy(out, parsedCat.CategoriesOrder)
	return out
}

func CategoryEntries(cat string) []EmojiEntry {
	load()
	src := parsedCat.Categories[cat]
	if src == nil {
		return nil
	}
	out := make([]EmojiEntry, len(src))
	copy(out, src)
	return out
}

func TerminalFriendly() []EmojiEntry {
	load()
	out := make([]EmojiEntry, len(terminalFriendlySet))
	copy(out, terminalFriendlySet)
	return out
}

func All() []EmojiEntry {
	load()
	out := make([]EmojiEntry, len(allEntries))
	copy(out, allEntries)
	return out
}

func Lookup(s string) *EmojiEntry {
	load()
	if e, ok := byGlyph[s]; ok {

		c := *e
		return &c
	}
	return nil
}

func RawJSON() []byte {
	return dataJSON
}

func IsUnicodeCapable() bool {
	unicodeOnce.Do(func() {
		for _, env := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
			v := os.Getenv(env)
			if v == "" {
				continue
			}
			lower := strings.ToLower(v)
			unicodeCached = strings.Contains(lower, "utf-8") || strings.Contains(lower, "utf8")
			return
		}

		unicodeCached = false
	})
	return unicodeCached
}

func Render(glyph string) string {
	if IsUnicodeCapable() {
		return glyph
	}
	if e := Lookup(glyph); e != nil && e.AsciiFallback != "" {
		return e.AsciiFallback
	}
	if glyph == "" {
		return ""
	}
	return ":?:"
}

var (
	unicodeOnce   sync.Once
	unicodeCached bool
)
