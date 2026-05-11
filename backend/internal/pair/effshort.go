package pair

import (
	_ "embed"
	"errors"
	"fmt"
	"strings"
)

//go:embed effshort_words.txt
var effShortRaw string

var EFFShort = func() []string {
	lines := strings.Split(strings.TrimRight(effShortRaw, "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}()

var effShortIndex = func() map[string]int {
	m := make(map[string]int, len(EFFShort))
	for i, w := range EFFShort {
		m[w] = i
	}
	return m
}()

func IsEFFShort(word string) bool {
	_, ok := effShortIndex[strings.ToLower(strings.TrimSpace(word))]
	return ok
}

func EFFShortIndexOf(word string) int {
	i, ok := effShortIndex[strings.ToLower(strings.TrimSpace(word))]
	if !ok {
		return -1
	}
	return i
}

const EFFShortCount = 1296

type ErrInvalidEFFShortWord struct{ Word string }

func (e ErrInvalidEFFShortWord) Error() string {
	return fmt.Sprintf("pair: invalid eff-short word: %q", e.Word)
}

func ValidateEFFShortPhrase(words []string) ([]string, error) {
	if len(words) == 0 {
		return nil, errors.New("pair: empty word list")
	}
	out := make([]string, 0, len(words))
	for _, w := range words {
		c := strings.ToLower(strings.TrimSpace(w))
		if c == "" {
			continue
		}
		if _, ok := effShortIndex[c]; !ok {
			return nil, ErrInvalidEFFShortWord{Word: w}
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, errors.New("pair: empty word list (after trim)")
	}
	return out, nil
}

func init() {
	if len(EFFShort) != EFFShortCount {
		panic(fmt.Sprintf("pair: effshort wordlist has %d entries, expected %d", len(EFFShort), EFFShortCount))
	}
}
