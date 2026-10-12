// strip-comments-cpp walks one or more directories and rewrites every
// C/C++ source and header file with its comments removed. It is a
// small hand-written scanner, not a regex pass: it tracks string
// literals, char literals, raw strings and preprocessor lines well
// enough not to touch a comment-looking sequence inside any of them.
//
// Preserved:
//
//   - The first comment in the file IF it looks like a license /
//     copyright / SPDX header (case-insensitive substring match),
//     same rule as tools/strip-comments-go.go. A preprocessor line
//     (#pragma once, an include guard's #ifndef/#define) ahead of it
//     does not count against it being "first": a license header
//     after the guard is still the first comment. Adjacent comments
//     — several // lines, several /* */ blocks, or a mix of both,
//     with no blank line and no code between them — are grouped and
//     judged as one header, not just the first one of the run.
//   - Preprocessor lines, apart from any comment inside them: a
//     comment on a #define line is still stripped, the directive
//     text is not touched otherwise.
//   - Any comment, anywhere, that starts (after the // or /* and an
//     optional one space) with NOLINT, NOLINTNEXTLINE, NOLINTBEGIN,
//     NOLINTEND, or "clang-format off"/"clang-format on" — clang-tidy
//     and clang-format read these, so a stripped tree must keep them
//     exactly as written.
//
// Dropped: // line comments, /* */ block comments. A single-line
// block comment that sits directly between two tokens (a/**/b) is
// replaced with one space, so the tokens stay apart. A line that only
// held a comment (nothing else, before or after stripping) is dropped
// outright, not left blank — this also applies to the inner lines of
// a multi-line block comment. Runs of blank lines the removal leaves
// behind collapse to one.
//
// Understood and left alone: "string" literals (with \ escapes),
// 'c' char literals (including an escaped quote), raw strings R"delim(...)delim"
// (also u8R, LR, uR, UR prefixes), a backslash-newline splice inside
// a // comment (the comment continues onto the next line, same as
// the compiler sees it), and a digit separator (1'000'000), which is
// not a char literal.
//
// Generated files (moc_*, anything under a *_autogen directory) and
// anything under a build/ directory are left untouched. So is
// anything under a testdata/ directory, the same convention the Go
// toolchain uses: a fixture there can be an exact-match golden file a
// test compares byte for byte, and must keep its comment exactly as
// committed. A symlink is skipped too, not followed and not
// rewritten: os.Rename onto one would replace the link itself, not
// the file it points at.
//
// Used by tools/release.sh. Safe to run twice; the second run changes nothing.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

var cppExts = map[string]bool{
	".c": true, ".cc": true, ".cpp": true, ".cxx": true,
	".h": true, ".hh": true, ".hpp": true,
}

// shouldSkipDir reports whether a directory, by its base name alone,
// should never be walked into: vcs metadata, a build output tree, a
// Qt automoc *_autogen directory, or testdata (the Go toolchain's own
// convention for a fixture tree — a golden file there can be an
// exact-match comparison a test makes byte for byte, and must keep
// its comment exactly as committed).
func shouldSkipDir(name string) bool {
	return name == ".git" || name == "node_modules" || name == "build" ||
		name == "testdata" || strings.HasSuffix(name, "_autogen")
}

// shouldSkipFile reports whether path should be left alone: a
// non-C/C++ extension (reason "", not worth a log line even with
// -v), a symlink (os.Rename onto one would replace the link itself,
// not the file it points at — never follow it, never write through
// it), or a moc_* generated file.
func shouldSkipFile(path string, mode fs.FileMode) (skip bool, reason string) {
	if !cppExts[filepath.Ext(path)] {
		return true, ""
	}
	if mode&fs.ModeSymlink != 0 {
		return true, "symlink"
	}
	if strings.HasPrefix(filepath.Base(path), "moc_") {
		return true, "generated"
	}
	return false, ""
}

func main() {
	verbose := flag.Bool("v", false, "print every file processed")
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: strip-comments-cpp [-v] <dir>...")
		os.Exit(2)
	}

	var stats struct {
		processed, skipped, errored int
	}
	for _, root := range flag.Args() {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if shouldSkipDir(d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if skip, reason := shouldSkipFile(path, d.Type()); skip {
				if reason != "" {
					if *verbose {
						fmt.Println("skipped ", path, "("+reason+")")
					}
					stats.skipped++
				}
				return nil
			}
			ok, err := stripFile(path)
			if err != nil {
				stats.errored++
				fmt.Fprintf(os.Stderr, "ERROR %s: %v\n", path, err)
				return nil
			}
			if ok {
				stats.processed++
				if *verbose {
					fmt.Println("stripped", path)
				}
			} else {
				stats.skipped++
				if *verbose {
					fmt.Println("skipped ", path)
				}
			}
			return nil
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "walk %s: %v\n", root, err)
			os.Exit(1)
		}
	}
	fmt.Printf("strip-comments-cpp: processed=%d skipped=%d errored=%d\n",
		stats.processed, stats.skipped, stats.errored)
	if stats.errored > 0 {
		os.Exit(1)
	}
}

func stripFile(path string) (bool, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	out, err := Strip(src)
	if err != nil {
		return false, err
	}
	if bytes.Equal(out, src) {
		return false, nil
	}
	tmp := path + ".strip-tmp"
	if err := os.WriteFile(tmp, out, 0644); err != nil {
		os.Remove(tmp)
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return false, err
	}
	return true, nil
}

// Strip removes comments from C/C++ source, keeping a leading license
// header verbatim. It fails loudly (non-nil error) on an unterminated
// comment, string, char or raw-string literal rather than returning a
// half-stripped file.
func Strip(src []byte) ([]byte, error) {
	n := len(src)
	var out bytes.Buffer
	keepLine := make(map[int]bool)

	lineOf := func(pos int) int {
		if pos > n {
			pos = n
		}
		return bytes.Count(src[:pos], []byte{'\n'})
	}
	markLines := func(from, to int) {
		a, b := lineOf(from), lineOf(to)
		for k := a; k <= b; k++ {
			keepLine[k] = true
		}
	}

	firstCommentSeen := false
	sawRealContent := false

	// A leading #pragma once / #ifndef+#define include guard does not
	// count as "real content" ahead of the first comment: a license
	// header right after the guard is still the file's first comment.
	i := skipGuardPreamble(src)
	out.Write(src[:i])

	for i < n {
		c := src[i]

		isBlock := c == '/' && i+1 < n && src[i+1] == '*'
		isLine := c == '/' && i+1 < n && src[i+1] == '/'

		if isBlock || isLine {
			var end int
			var hasNL bool
			var newlines []int
			var err error
			if isBlock {
				end, hasNL, err = scanBlockComment(src, i)
			} else {
				end, newlines = lineCommentEnd(src, i)
			}
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineOf(i)+1, err)
			}

			// clang-tidy and clang-format read these; keep them
			// exactly as written, wherever they sit.
			if isKeepDirective(src[i+2:end], isLine) {
				out.Write(src[i:end])
				firstCommentSeen = true
				i = end
				continue
			}

			if !firstCommentSeen && !sawRealContent {
				groupEnd := groupFirstComment(src, i)
				firstCommentSeen = true
				if isLicense(src[i:groupEnd]) {
					out.Write(src[i:groupEnd])
					markLines(i, groupEnd)
					i = groupEnd
					continue
				}
			}
			firstCommentSeen = true

			if isBlock {
				if hasNL {
					for _, b := range src[i:end] {
						if b == '\n' {
							out.WriteByte('\n')
						}
					}
				} else {
					out.WriteByte(' ')
				}
			} else {
				for range newlines {
					out.WriteByte('\n')
				}
			}
			i = end
			continue
		}

		// Raw string: R"(...)" and the u8R/LR/uR/UR variants.
		if c == 'R' || c == 'u' || c == 'U' || c == 'L' {
			if plen, ok := detectRawPrefix(src, i); ok {
				end, err := scanRaw(src, i, plen)
				if err != nil {
					return nil, fmt.Errorf("line %d: %w", lineOf(i)+1, err)
				}
				out.Write(src[i:end])
				markLines(i, end)
				sawRealContent = true
				i = end
				continue
			}
		}

		// Normal string literal.
		if c == '"' {
			end, err := scanDelimited(src, i, '"')
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineOf(i)+1, err)
			}
			out.Write(src[i:end])
			sawRealContent = true
			i = end
			continue
		}

		// Char literal, or a digit separator (1'000'000) that is not one.
		if c == '\'' {
			var prev byte
			if b := out.Bytes(); len(b) > 0 {
				prev = b[len(b)-1]
			}
			var next byte
			if i+1 < n {
				next = src[i+1]
			}
			if isDigitSepContext(prev, next) {
				out.WriteByte(c)
				sawRealContent = true
				i++
				continue
			}
			end, err := scanDelimited(src, i, '\'')
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineOf(i)+1, err)
			}
			out.Write(src[i:end])
			sawRealContent = true
			i = end
			continue
		}

		out.WriteByte(c)
		if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
			sawRealContent = true
		}
		i++
	}

	return collapseBlankRuns(src, out.Bytes(), keepLine), nil
}

// scanBlockComment finds the end of a /* ... */ comment starting at i
// (src[i]=='/' , src[i+1]=='*'). It returns the index right after the
// closing "*/" and whether the comment spans more than one line.
func scanBlockComment(src []byte, i int) (end int, hasNewline bool, err error) {
	n := len(src)
	j := i + 2
	for j+1 < n {
		if src[j] == '*' && src[j+1] == '/' {
			end = j + 2
			return end, bytes.IndexByte(src[i:end], '\n') != -1, nil
		}
		j++
	}
	return 0, false, fmt.Errorf("unterminated block comment")
}

// lineCommentEnd finds the end of a `//` comment starting at i,
// honoring backslash-newline splices (the comment continues onto the
// next physical line). It returns the index right after the
// terminating newline (or len(src) at EOF) and every newline position
// it consumed along the way, so the caller can keep the same number
// of lines in its output.
func lineCommentEnd(src []byte, i int) (end int, newlines []int) {
	n := len(src)
	j := i + 2
	for j < n {
		if src[j] == '\n' {
			newlines = append(newlines, j)
			if j > 0 && src[j-1] == '\\' {
				j++
				continue
			}
			return j + 1, newlines
		}
		j++
	}
	return n, newlines
}

// groupFirstComment extends the comment starting at i to any
// immediately following comment — // or /* */, in any mix — with no
// blank line and no code between them, the same grouping
// tools/strip-comments-go.go applies via ast.CommentGroup. A license
// header made of several // lines, several /* */ blocks, or a run of
// both, is judged and kept as one unit, not just its first comment.
func groupFirstComment(src []byte, i int) int {
	n := len(src)
	end := i
	pos := i
	for pos < n {
		var consumedEnd int
		var endsAtLineStart bool
		switch {
		case pos+1 < n && src[pos] == '/' && src[pos+1] == '*':
			e, _, err := scanBlockComment(src, pos)
			if err != nil {
				return end
			}
			consumedEnd, endsAtLineStart = e, false
		case pos+1 < n && src[pos] == '/' && src[pos+1] == '/':
			e, _ := lineCommentEnd(src, pos)
			consumedEnd, endsAtLineStart = e, true
		default:
			return end
		}
		end = consumedEnd

		m := consumedEnd
		if !endsAtLineStart {
			// A block comment's end does not include its own line's
			// terminating newline: the rest of that line must be
			// only whitespace (or another comment right there) for
			// grouping to carry on.
			for m < n && (src[m] == ' ' || src[m] == '\t' || src[m] == '\r') {
				m++
			}
			if m < n && src[m] == '/' && m+1 < n && (src[m+1] == '/' || src[m+1] == '*') {
				pos = m
				continue
			}
			if m >= n || src[m] != '\n' {
				return end // code follows on the same line
			}
			m++ // consume this line's own newline
		}
		for m < n && (src[m] == ' ' || src[m] == '\t') {
			m++
		}
		if m < n && src[m] == '/' && m+1 < n && (src[m+1] == '/' || src[m+1] == '*') {
			pos = m
			continue
		}
		return end
	}
	return end
}

// skipGuardPreamble returns how far into src a run of blank lines and
// #pragma / #ifndef / #define lines (a #pragma once or an include
// guard) extends, so the comment right after it still counts as the
// file's first comment for the license-header check. It copies
// nothing itself; the caller writes src[:result] through unchanged.
func skipGuardPreamble(src []byte) int {
	n := len(src)
	i := 0
	for {
		for i < n {
			j := i
			for j < n && (src[j] == ' ' || src[j] == '\t' || src[j] == '\r') {
				j++
			}
			if j < n && src[j] == '\n' {
				i = j + 1
				continue
			}
			break
		}
		if i >= n {
			return i
		}
		lineStart := i
		j := i
		for j < n && (src[j] == ' ' || src[j] == '\t') {
			j++
		}
		line := src[j:]
		matched := false
		for _, kw := range [...]string{"#pragma", "#ifndef", "#define"} {
			if bytes.HasPrefix(line, []byte(kw)) {
				matched = true
				break
			}
		}
		if !matched {
			return lineStart
		}
		// Consume to the end of this line, but only if it holds no
		// comment: a comment there is the file's first comment, and
		// must go through the normal license/strip handling instead
		// of being swallowed as part of the guard.
		k := i
		for k < n {
			if src[k] == '/' && k+1 < n && (src[k+1] == '/' || src[k+1] == '*') {
				return lineStart
			}
			if src[k] == '\n' {
				if k > 0 && src[k-1] == '\\' {
					k++
					continue
				}
				k++
				break
			}
			k++
		}
		i = k
	}
}

// isKeepDirective reports whether a comment's content — the text
// right after its // or /*, with at most one leading space skipped —
// starts with a directive clang-tidy or clang-format act on: NOLINT
// also covers NOLINTNEXTLINE, NOLINTBEGIN and NOLINTEND, since they
// all share that prefix.
//
// //haoma:* is a separate case: it is this repo's own directive form
// (see CLAUDE.md), kept only for a // line comment and only with no
// space after the slashes — "// haoma: x" is not a directive and is
// stripped like any comment. A /* */ block is always stripped, even
// if its content starts with "haoma:".
func isKeepDirective(content []byte, isLineComment bool) bool {
	if isLineComment && bytes.HasPrefix(content, []byte("haoma:")) {
		return true
	}
	content = bytes.TrimPrefix(content, []byte(" "))
	for _, p := range [...]string{"NOLINT", "clang-format off", "clang-format on"} {
		if bytes.HasPrefix(content, []byte(p)) {
			return true
		}
	}
	return false
}

// scanDelimited scans a "..." or '...' literal starting at i (src[i]
// is the opening delim), handling \ escapes. It returns the index
// right after the closing delim.
func scanDelimited(src []byte, i int, delim byte) (int, error) {
	n := len(src)
	j := i + 1
	for j < n {
		if src[j] == '\\' && j+1 < n {
			j += 2
			continue
		}
		if src[j] == delim {
			return j + 1, nil
		}
		j++
	}
	if delim == '"' {
		return 0, fmt.Errorf("unterminated string literal")
	}
	return 0, fmt.Errorf("unterminated char literal")
}

var rawStringPrefixes = []string{"u8R", "LR", "UR", "uR", "R"}

// detectRawPrefix reports whether src[i:] starts a raw string literal
// (one of the u8R/LR/uR/UR/R prefixes followed by a quote), and is not
// part of a longer identifier.
func detectRawPrefix(src []byte, i int) (int, bool) {
	if i > 0 && isIdentChar(src[i-1]) {
		return 0, false
	}
	n := len(src)
	for _, cand := range rawStringPrefixes {
		l := len(cand)
		if i+l < n && string(src[i:i+l]) == cand && src[i+l] == '"' {
			return l + 1, true
		}
	}
	return 0, false
}

// scanRaw scans a raw string literal R"delim(...)delim". i points at
// the first character of the prefix; prefixLen includes the prefix
// and the opening quote, so src[i+prefixLen] is the first delimiter
// character (or the '(' if the delimiter is empty).
func scanRaw(src []byte, i, prefixLen int) (int, error) {
	n := len(src)
	j := i + prefixLen
	delimStart := j
	for j < n && src[j] != '(' {
		if src[j] == '\n' || src[j] == '\\' || src[j] == ')' || src[j] == ' ' {
			return 0, fmt.Errorf("unterminated raw string literal")
		}
		j++
	}
	if j >= n {
		return 0, fmt.Errorf("unterminated raw string literal")
	}
	delim := src[delimStart:j]
	j++ // skip '('
	closer := make([]byte, 0, len(delim)+2)
	closer = append(closer, ')')
	closer = append(closer, delim...)
	closer = append(closer, '"')
	idx := bytes.Index(src[j:], closer)
	if idx == -1 {
		return 0, fmt.Errorf("unterminated raw string literal")
	}
	return j + idx + len(closer), nil
}

func isIdentChar(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func isHexOrDigit(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

// isDigitSepContext reports whether a ' between prev and next is a
// C++14 digit separator (1'000'000) rather than the start of a char
// literal: it sits directly between two digit/hex-digit characters.
func isDigitSepContext(prev, next byte) bool {
	return isHexOrDigit(prev) && isHexOrDigit(next)
}

func isLicense(comment []byte) bool {
	lower := strings.ToLower(string(comment))
	return strings.Contains(lower, "license") ||
		strings.Contains(lower, "copyright") ||
		strings.Contains(lower, "spdx")
}

// collapseBlankRuns compares the original source against the stripped
// output line by line (both have the same number of lines, since
// every path above preserves exactly as many '\n' as it consumed):
//
//   - a line kept verbatim (license header, raw string content) is
//     never dropped or touched, even if it looks blank;
//   - a line that was already blank in the original stays, but a run
//     of such lines collapses to one;
//   - a line that only held a comment (blank after stripping, but not
//     blank originally) is dropped outright.
func collapseBlankRuns(orig, stripped []byte, keepLine map[int]bool) []byte {
	trailingNL := len(orig) > 0 && orig[len(orig)-1] == '\n'
	origLines := bytes.Split(orig, []byte{'\n'})
	stripLines := bytes.Split(stripped, []byte{'\n'})
	if trailingNL {
		origLines = origLines[:len(origLines)-1]
		stripLines = stripLines[:len(stripLines)-1]
	}

	var kept [][]byte
	var protected []bool
	for k := range origLines {
		if keepLine[k] {
			kept = append(kept, stripLines[k])
			protected = append(protected, true)
			continue
		}
		if len(bytes.TrimSpace(origLines[k])) == 0 {
			kept = append(kept, []byte{})
			protected = append(protected, false)
			continue
		}
		if len(bytes.TrimSpace(stripLines[k])) == 0 {
			continue // comment-only line: drop it, don't leave it blank
		}
		kept = append(kept, stripLines[k])
		protected = append(protected, false)
	}

	var final [][]byte
	prevBlank := false
	for idx, l := range kept {
		isBlank := len(l) == 0 && !protected[idx]
		if isBlank && prevBlank {
			continue
		}
		final = append(final, l)
		prevBlank = isBlank
	}

	result := bytes.Join(final, []byte{'\n'})
	if trailingNL {
		result = append(result, '\n')
	}
	return result
}
