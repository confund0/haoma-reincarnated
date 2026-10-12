package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStripTable(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "line comment alone is dropped, not left blank",
			in:   "int a;\n// a comment\nint b;\n",
			want: "int a;\nint b;\n",
		},
		{
			name: "trailing line comment keeps the code, drops the comment",
			in:   "int a; // trailing\n",
			want: "int a; \n",
		},
		{
			name: "block comment alone on its own line is dropped",
			in:   "int a;\n/* comment */\nint b;\n",
			want: "int a;\nint b;\n",
		},
		{
			name: "block comment between tokens becomes one space",
			in:   "a/**/b;\n",
			want: "a b;\n",
		},
		{
			name: "multi-line block comment: a middle line that held only comment text is dropped",
			in:   "int a; /* one\ntwo\nthree */ int b;\n",
			want: "int a; \n int b;\n",
		},
		{
			name: "string literal with escaped quote is untouched",
			in:   `const char* s = "a \"// not a comment\" b";` + "\n",
			want: `const char* s = "a \"// not a comment\" b";` + "\n",
		},
		{
			name: "string literal with a slash-star is untouched",
			in:   `const char* s = "/* not a comment */";` + "\n",
			want: `const char* s = "/* not a comment */";` + "\n",
		},
		{
			name: "char literal is untouched, including escaped quote",
			in:   `char c = '\''; // drop this` + "\n",
			want: `char c = '\''; ` + "\n",
		},
		{
			name: "digit separators are not char literals",
			in:   "int x = 1'000'000; // money\n",
			want: "int x = 1'000'000; \n",
		},
		{
			name: "an odd count of digit-separator quotes does not misread as a char literal",
			in:   "int x = 1'000'000'000;\n",
			want: "int x = 1'000'000'000;\n",
		},
		{
			name: "a real char literal right after a number is still a char literal",
			in:   "f(1, 'a'); // ok\n",
			want: "f(1, 'a'); \n",
		},
		{
			name: "raw string is untouched, comment-looking text inside included",
			in:   "auto s = R\"(// not a comment /* also not */)\"; // real\n",
			want: "auto s = R\"(// not a comment /* also not */)\"; \n",
		},
		{
			name: "u8R prefixed raw string",
			in:   "auto s = u8R\"X(raw // text)X\";\n",
			want: "auto s = u8R\"X(raw // text)X\";\n",
		},
		{
			name: "LR prefixed raw string",
			in:   "auto s = LR\"(wide raw)\";\n",
			want: "auto s = LR\"(wide raw)\";\n",
		},
		{
			name: "line continuation inside a // comment swallows the next line",
			in:   "// this comment \\\ncontinues here\ncode();\n",
			want: "code();\n",
		},
		{
			name: "preprocessor line keeps its directive, drops its comment",
			in:   "#define FOO 1 // the foo flag\n#define BAR 2\n",
			want: "#define FOO 1 \n#define BAR 2\n",
		},
		{
			name: "license header is kept verbatim, later comments are not",
			in:   "// Copyright 2026 Example Corp.\n// All rights reserved.\n\nint a; // drop\n",
			want: "// Copyright 2026 Example Corp.\n// All rights reserved.\n\nint a; \n",
		},
		{
			name: "block license header is kept verbatim",
			in:   "/* SPDX-License-Identifier: MIT */\nint a; /* drop */\n",
			want: "/* SPDX-License-Identifier: MIT */\nint a;  \n",
		},
		{
			name: "a non-license first comment is stripped like any other",
			in:   "// just a note, nothing legal here\nint a;\n",
			want: "int a;\n",
		},
		{
			name: "blank line runs collapse to one",
			in:   "int a;\n\n\n\nint b;\n",
			want: "int a;\n\nint b;\n",
		},
		{
			name: "comment-only lines dropped leave no gap, no extra collapse needed",
			in:   "int a;\n// one\n// two\n// three\nint b;\n",
			want: "int a;\nint b;\n",
		},
		{
			name: "pragma once header, untouched apart from its own trailing comment",
			in:   "#pragma once // guard\n#include <string>\n",
			want: "#pragma once \n#include <string>\n",
		},
		{
			name: "uR prefixed raw string",
			in:   "auto s = uR\"(small raw // text)\";\n",
			want: "auto s = uR\"(small raw // text)\";\n",
		},
		{
			name: "UR prefixed raw string",
			in:   "auto s = UR\"(big raw /* text */)\";\n",
			want: "auto s = UR\"(big raw /* text */)\";\n",
		},
		{
			name: "a bare NOLINT line comment is kept, not dropped",
			in:   "int a;\n// NOLINT\nint b;\n",
			want: "int a;\n// NOLINT\nint b;\n",
		},
		{
			name: "a NOLINT comment with a check name and a reason is kept whole",
			in:   "x(); // NOLINT(bugprone-foo): because reasons\n",
			want: "x(); // NOLINT(bugprone-foo): because reasons\n",
		},
		{
			name: "NOLINTNEXTLINE is kept",
			in:   "// NOLINTNEXTLINE(bugprone-easily-swappable-parameters): test helper\nvoid f(int a, int b);\n",
			want: "// NOLINTNEXTLINE(bugprone-easily-swappable-parameters): test helper\nvoid f(int a, int b);\n",
		},
		{
			name: "NOLINTBEGIN and NOLINTEND are both kept",
			in:   "// NOLINTBEGIN(modernize-use-auto)\nint x = 1;\n// NOLINTEND(modernize-use-auto)\n",
			want: "// NOLINTBEGIN(modernize-use-auto)\nint x = 1;\n// NOLINTEND(modernize-use-auto)\n",
		},
		{
			name: "a block-comment NOLINT is kept",
			in:   "int a; /* NOLINT(cppcoreguidelines-foo) */\n",
			want: "int a; /* NOLINT(cppcoreguidelines-foo) */\n",
		},
		{
			name: "clang-format off/on pair is kept",
			in:   "// clang-format off\nint   a  =  1;\n// clang-format on\nint b = 2; // drop\n",
			want: "// clang-format off\nint   a  =  1;\n// clang-format on\nint b = 2; \n",
		},
		{
			name: "an ordinary comment that merely mentions NOLINT in its body is still stripped",
			in:   "// see the NOLINT comment below for why\nint a;\n",
			want: "int a;\n",
		},
		{
			name: "a license header after a pragma-once guard is still kept",
			in:   "#pragma once\n// Copyright 2026 Example Corp.\n// All rights reserved.\n\nint a; // drop\n",
			want: "#pragma once\n// Copyright 2026 Example Corp.\n// All rights reserved.\n\nint a; \n",
		},
		{
			name: "a license header after a full #ifndef/#define include guard is still kept",
			in:   "#ifndef FOO_H\n#define FOO_H\n/* Copyright 2026 Example Corp. */\nint a; /* drop */\n#endif\n",
			want: "#ifndef FOO_H\n#define FOO_H\n/* Copyright 2026 Example Corp. */\nint a;  \n#endif\n",
		},
		{
			name: "a guard-line comment is itself the first comment, no header follows it",
			in:   "#pragma once // not a license, just a guard note\n// Copyright 2026 Example Corp.\nint a;\n",
			want: "#pragma once \nint a;\n",
		},
		{
			name: "a header made of two adjacent block comments keeps both, not just the first",
			in:   "/* Copyright 2026 Example Corp. */\n/* All rights reserved. */\nint a; /* drop */\n",
			want: "/* Copyright 2026 Example Corp. */\n/* All rights reserved. */\nint a;  \n",
		},
		{
			name: "a header mixing a block comment then line comments keeps the whole run",
			in:   "/* SPDX-License-Identifier: MIT */\n// generated by hand, not a tool\nint a; // drop\n",
			want: "/* SPDX-License-Identifier: MIT */\n// generated by hand, not a tool\nint a; \n",
		},
		{
			name: "a header mixing line comments then a block comment keeps the whole run",
			in:   "// first line, no legal words yet\n/* Copyright 2026 Example Corp. */\nint a; /* drop */\n",
			want: "// first line, no legal words yet\n/* Copyright 2026 Example Corp. */\nint a;  \n",
		},
		{
			name: "a blank line between two header comments breaks the grouping",
			in:   "/* Copyright 2026 Example Corp. */\n\n/* not part of the header */\nint a;\n",
			want: "/* Copyright 2026 Example Corp. */\n\nint a;\n",
		},
		{
			name: "a //haoma: directive line comment is kept exactly",
			in:   "int a;\n//haoma:direction in\nint b;\n",
			want: "int a;\n//haoma:direction in\nint b;\n",
		},
		{
			name: "// haoma: with a space after the slashes is not a directive, stripped",
			in:   "int a;\n// haoma: x\nint b;\n",
			want: "int a;\nint b;\n",
		},
		{
			name: "a haoma: directive inside a /* */ block is stripped with the block",
			in:   "int a; /*haoma:direction in*/\nint b;\n",
			want: "int a;  \nint b;\n",
		},
		{
			name: "a haoma: directive looking string literal is untouched",
			in:   `const char* s = "//haoma:direction in";` + "\n",
			want: `const char* s = "//haoma:direction in";` + "\n",
		},
		{
			name: "commentary above a directive is dropped, the directive line stays",
			in:   "// some commentary\n//haoma:direction in\nint a;\n",
			want: "//haoma:direction in\nint a;\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Strip([]byte(tc.in))
			if err != nil {
				t.Fatalf("Strip returned error: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("Strip(%q)\n got: %q\nwant: %q", tc.in, string(got), tc.want)
			}
		})
	}
}

func TestStripIdempotent(t *testing.T) {
	srcs := []string{
		"int a;\n// a comment\nint b;\n",
		"a/**/b;\n",
		"int a; /* one\ntwo\nthree */ int b;\n",
		"// Copyright 2026 Example Corp.\n// All rights reserved.\n\nint a; // drop\n",
		"auto s = R\"(// not a comment)\"; // real\n",
		"int a;\n\n\n\nint b;\n",
		"// NOLINTNEXTLINE(bugprone-easily-swappable-parameters): test helper\nvoid f(int a, int b);\n",
		"#pragma once\n// Copyright 2026 Example Corp.\n// All rights reserved.\n\nint a; // drop\n",
		"/* Copyright 2026 Example Corp. */\n/* All rights reserved. */\nint a; /* drop */\n",
		"int a;\n//haoma:direction in\nint b;\n",
	}
	for _, src := range srcs {
		once, err := Strip([]byte(src))
		if err != nil {
			t.Fatalf("first Strip failed on %q: %v", src, err)
		}
		twice, err := Strip(once)
		if err != nil {
			t.Fatalf("second Strip failed on %q: %v", src, err)
		}
		if string(once) != string(twice) {
			t.Errorf("not idempotent for %q:\nfirst:  %q\nsecond: %q", src, once, twice)
		}
	}
}

func TestStripUnterminated(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"block comment", "int a; /* never closes\n"},
		{"string literal", "const char* s = \"never closes\n"},
		{"char literal", "char c = 'a\n"},
		{"raw string", "auto s = R\"(never closes\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Strip([]byte(tc.in))
			if err == nil {
				t.Fatalf("expected an error for unterminated %s, got none", tc.name)
			}
			if !strings.Contains(err.Error(), "unterminated") {
				t.Errorf("error %q does not mention 'unterminated'", err.Error())
			}
		})
	}
}

func TestStripNoChangeWhenNoComments(t *testing.T) {
	src := "int add(int a, int b) {\n    return a + b;\n}\n"
	got, err := Strip([]byte(src))
	if err != nil {
		t.Fatalf("Strip returned error: %v", err)
	}
	if string(got) != src {
		t.Errorf("expected no change, got %q", got)
	}
}

func TestShouldSkipDir(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"src", false},
		{"test", false},
		{".git", true},
		{"node_modules", true},
		{"build", true},
		{"testdata", true},
		{"haoma_desktop_core_autogen", true},
	}
	for _, tc := range cases {
		if got := shouldSkipDir(tc.name); got != tc.want {
			t.Errorf("shouldSkipDir(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestShouldSkipFile(t *testing.T) {
	cases := []struct {
		name       string
		path       string
		mode       fs.FileMode
		wantSkip   bool
		wantReason string
	}{
		{"ordinary cpp file", "/a/b/foo.cpp", 0, false, ""},
		{"ordinary header", "/a/b/foo.h", 0, false, ""},
		{"non-cpp extension", "/a/b/foo.go", 0, true, ""},
		{"moc generated file", "/a/b/moc_widget.cpp", 0, true, "generated"},
		{"a symlink is skipped, not followed", "/a/b/foo.cpp", fs.ModeSymlink, true, "symlink"},
		{"a symlink to a non-cpp name is still just skipped", "/a/b/foo.go", fs.ModeSymlink, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			skip, reason := shouldSkipFile(tc.path, tc.mode)
			if skip != tc.wantSkip || reason != tc.wantReason {
				t.Errorf("shouldSkipFile(%q, %v) = (%v, %q), want (%v, %q)",
					tc.path, tc.mode, skip, reason, tc.wantSkip, tc.wantReason)
			}
		})
	}
}

// TestSymlinkNotFollowedOrRewritten proves the real failure mode this
// guards against: without the check, os.Rename(tmp, path) on a
// symlink would replace the link itself. Here a symlink named *.cpp
// points at a file with a comment; walking the directory must leave
// both the link and its target untouched.
func TestSymlinkNotFollowedOrRewritten(t *testing.T) {
	base := t.TempDir()
	// The target sits outside the walked directory, so the only way
	// its content would change is if the walk followed the symlink
	// into it — a plain "it's a .cpp file in the walked tree too"
	// mistake in the test would not catch that.
	target := filepath.Join(base, "real.cpp")
	targetSrc := "int a; // a comment\n"
	if err := os.WriteFile(target, []byte(targetSrc), 0644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "walked")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.cpp")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks not supported here: %v", err)
	}

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if skip, _ := shouldSkipFile(path, d.Type()); skip {
			return nil
		}
		_, err = stripFile(path)
		return err
	})
	if err != nil {
		t.Fatalf("walk failed: %v", err)
	}

	gotLink, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("Lstat(link): %v", err)
	}
	if gotLink.Mode()&fs.ModeSymlink == 0 {
		t.Error("link.cpp is no longer a symlink: it was rewritten")
	}
	gotTarget, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(gotTarget) != targetSrc {
		t.Errorf("real.cpp was modified through the symlink: got %q want %q", gotTarget, targetSrc)
	}
}

// TestTestdataGoldenFixtureUntouched proves the real failure this
// guards against: desktop/tools/framegen compares its generated
// header against testdata/fixture/golden/frame_fixture.h byte for
// byte. Stripping that fixture's comment would make the test fail
// against the generator's own (unstripped) output.
func TestTestdataGoldenFixtureUntouched(t *testing.T) {
	dir := t.TempDir()
	golden := filepath.Join(dir, "testdata", "fixture", "golden")
	if err := os.MkdirAll(golden, 0755); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(golden, "frame_fixture.h")
	fixtureSrc := "#pragma once\n}  // namespace haoma::wire\n"
	if err := os.WriteFile(fixture, []byte(fixtureSrc), 0644); err != nil {
		t.Fatal(err)
	}

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if shouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if skip, _ := shouldSkipFile(path, d.Type()); skip {
			return nil
		}
		_, err = stripFile(path)
		return err
	})
	if err != nil {
		t.Fatalf("walk failed: %v", err)
	}

	got, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if string(got) != fixtureSrc {
		t.Errorf("golden fixture under testdata/ was rewritten: got %q want %q", got, fixtureSrc)
	}
}
