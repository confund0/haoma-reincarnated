package main

import (
	"bufio"
	"container/heap"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type attr struct {
	key string
	val string
}

type entry struct {
	account string
	process string
	file    string
	ts      time.Time
	hasTS   bool
	level   string
	msg     string
	raw     string
	attrs   []attr
}

func (e entry) get(key string) string {
	for _, a := range e.attrs {
		if a.key == key {
			return a.val
		}
	}
	return ""
}

type source struct {
	account string
	process string
	file    string
	path    string
	rotated bool
	stderr  bool

	loose bool
}

const maxDepth = 2

func discover(root string) ([]source, error) {
	clean := filepath.Clean(root)
	info, err := os.Stat(clean)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", clean)
	}
	var out []source
	err = filepath.WalkDir(clean, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {

			return nil
		}
		rel, rerr := filepath.Rel(clean, p)
		if rerr == nil && strings.Count(rel, string(filepath.Separator)) >= maxDepth {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if s, ok := classify(clean, p); ok {
			out = append(out, s)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	nested := false
	for _, s := range out {
		if !s.loose {
			nested = true
			break
		}
	}
	if nested {
		kept := out[:0]
		for _, s := range out {
			if !s.loose {
				kept = append(kept, s)
			}
		}
		out = kept
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].account != out[j].account {
			return out[i].account < out[j].account
		}
		if out[i].process != out[j].process {
			return out[i].process < out[j].process
		}
		return out[i].file < out[j].file
	})
	return out, nil
}

func classify(root, path string) (source, bool) {
	name := filepath.Base(path)
	s := source{file: name, path: path}
	base := name
	if strings.HasSuffix(base, ".prev") {
		s.rotated = true
		base = strings.TrimSuffix(base, ".prev")
	}
	switch {
	case strings.HasSuffix(base, ".log"):
		s.process = strings.TrimSuffix(base, ".log")
	case strings.HasSuffix(base, ".stderr"):
		s.process = strings.TrimSuffix(base, ".stderr")
		s.stderr = true
	default:
		return source{}, false
	}
	if s.process == "" {
		return source{}, false
	}
	dir := filepath.Dir(path)
	if filepath.Clean(dir) == filepath.Clean(root) {
		s.account = accountName(filepath.Base(filepath.Clean(root)))
		s.loose = true
	} else {
		s.account = accountName(filepath.Base(dir))
	}
	return s, true
}

func nestedRunDirs(root string) []string {
	var out []string
	top, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	for _, d := range top {
		if !d.IsDir() {
			continue
		}
		dir := filepath.Join(root, d.Name())
		inner, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, sub := range inner {
			if !sub.IsDir() {
				continue
			}
			files, err := os.ReadDir(filepath.Join(dir, sub.Name()))
			if err != nil {
				continue
			}
			holdsLog := false
			for _, f := range files {
				if !f.IsDir() && strings.Contains(f.Name(), ".log") {
					holdsLog = true
					break
				}
			}
			if holdsLog {
				out = append(out, dir)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

func accountName(dir string) string {
	if t := strings.TrimSuffix(dir, "-vault"); t != "" {
		return t
	}
	return dir
}

func isKey(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '_' || c == '-' || c == '.'
		if !ok {
			return false
		}
	}
	c := s[0]
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_'
}

func readValue(s string) (string, int) {
	if s == "" {
		return "", 0
	}
	if s[0] != '"' {
		if sp := strings.IndexByte(s, ' '); sp >= 0 {
			return s[:sp], sp
		}
		return s, len(s)
	}
	var b strings.Builder
	i := 1
	for i < len(s) {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			default:
				b.WriteByte(s[i+1])
			}
			i += 2
			continue
		}
		if c == '"' {
			return b.String(), i + 1
		}
		b.WriteByte(c)
		i++
	}
	return b.String(), len(s)
}

func parseLine(raw string) entry {
	e := entry{raw: raw}
	rest := raw
	for {
		trimmed := strings.TrimLeft(rest, " ")
		if trimmed == "" {
			break
		}
		rest = trimmed
		eq := strings.IndexByte(rest, '=')
		if eq <= 0 {
			break
		}
		key := rest[:eq]
		if !isKey(key) {

			sp := strings.IndexByte(rest, ' ')
			if sp < 0 {
				break
			}
			rest = rest[sp+1:]
			continue
		}
		val, n := readValue(rest[eq+1:])
		rest = rest[eq+1+n:]
		switch key {
		case "time":
			if t, err := time.Parse(time.RFC3339Nano, val); err == nil {
				e.ts, e.hasTS = t, true
			}
		case "level":
			e.level = val
		case "msg":
			e.msg = val
		default:
			e.attrs = append(e.attrs, attr{key: key, val: val})
		}
	}
	return e
}

type stream struct {
	src     source
	f       *os.File
	sc      *bufio.Scanner
	pending entry
	live    bool
	last    time.Time
	hasLast bool
	seq     int
}

const scanBuffer = 1024 * 1024

func openStream(s source) (*stream, error) {
	f, err := os.Open(s.path)
	if err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), scanBuffer)
	st := &stream{src: s, f: f, sc: sc}
	st.advance()
	return st, nil
}

func (s *stream) advance() {
	for s.sc.Scan() {
		text := s.sc.Text()
		if strings.TrimSpace(text) == "" {
			continue
		}
		e := parseLine(text)
		e.account = s.src.account
		e.process = s.src.process
		e.file = s.src.file
		if e.hasTS {
			s.last, s.hasLast = e.ts, true
		} else if s.hasLast {
			e.ts = s.last
		}
		s.seq++
		s.pending = e
		s.live = true
		return
	}
	s.live = false
}

func (s *stream) close() { s.f.Close() }

type streamHeap []*stream

func (h streamHeap) Len() int { return len(h) }
func (h streamHeap) Less(i, j int) bool {
	a, b := h[i], h[j]
	if !a.pending.ts.Equal(b.pending.ts) {
		return a.pending.ts.Before(b.pending.ts)
	}
	if a.src.account != b.src.account {
		return a.src.account < b.src.account
	}
	if a.src.file != b.src.file {
		return a.src.file < b.src.file
	}
	return a.seq < b.seq
}
func (h streamHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *streamHeap) Push(x interface{}) { *h = append(*h, x.(*stream)) }
func (h *streamHeap) Pop() interface{} {
	old := *h
	n := len(old)
	it := old[n-1]
	*h = old[:n-1]
	return it
}

func eachEntry(srcs []source, fn func(entry) bool) error {
	h := &streamHeap{}
	var opened []*stream
	defer func() {
		for _, s := range opened {
			s.close()
		}
	}()
	for _, s := range srcs {
		st, err := openStream(s)
		if err != nil {
			return err
		}
		opened = append(opened, st)
		if st.live {
			*h = append(*h, st)
		}
	}
	heap.Init(h)
	for h.Len() > 0 {
		st := (*h)[0]
		if !fn(st.pending) {
			return nil
		}
		st.advance()
		if st.live {
			heap.Fix(h, 0)
		} else {
			heap.Pop(h)
		}
	}
	return nil
}

func pick(srcs []source, rotated, withStderr bool) []source {
	var out []source
	for _, s := range srcs {
		if s.rotated && !rotated {
			continue
		}
		if s.stderr && !withStderr {
			continue
		}
		out = append(out, s)
	}
	return out
}

func logsOnly(srcs []source) []source {
	var out []source
	for _, s := range srcs {
		if !s.stderr {
			out = append(out, s)
		}
	}
	return out
}
