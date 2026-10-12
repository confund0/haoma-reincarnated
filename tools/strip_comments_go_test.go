package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAndStrip(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := stripFile(path); err != nil {
		t.Fatalf("stripFile: %v", err)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	return string(out)
}

func TestIsDirectiveLineHaoma(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"//haoma:direction in", true},
		{"// haoma: x", false},
		{"//haoma:", true},
		{"// an ordinary comment", false},
	}
	for _, tc := range cases {
		if got := isDirectiveLine(tc.text); got != tc.want {
			t.Errorf("isDirectiveLine(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

func TestHaomaDirectiveKeptExactly(t *testing.T) {
	src := "package p\n\n//haoma:direction in\nfunc f() {}\n"
	got := writeAndStrip(t, src)
	if !containsLine(got, "//haoma:direction in") {
		t.Errorf("directive line missing from output: %q", got)
	}
}

func TestHaomaWithSpaceStripped(t *testing.T) {
	src := "package p\n\n// haoma: x\nfunc f() {}\n"
	got := writeAndStrip(t, src)
	if strings.Contains(got, "haoma") {
		t.Errorf("non-directive haoma comment was kept: %q", got)
	}
}

func TestHaomaInStringUntouched(t *testing.T) {
	src := "package p\n\nconst s = \"//haoma:direction in\"\n"
	got := writeAndStrip(t, src)
	if !containsLine(got, `const s = "//haoma:direction in"`) {
		t.Errorf("string literal was altered: %q", got)
	}
}

func TestHaomaInBlockCommentStripped(t *testing.T) {
	src := "package p\n\n/* haoma:direction in */\nfunc f() {}\n"
	got := writeAndStrip(t, src)
	if strings.Contains(got, "haoma") {
		t.Errorf("block comment directive-lookalike was kept: %q", got)
	}
}

func TestHaomaGroupKeepsOnlyDirective(t *testing.T) {
	src := "package p\n\n// some commentary\n//haoma:direction in\nfunc f() {}\n"
	got := writeAndStrip(t, src)
	if strings.Contains(got, "some commentary") {
		t.Errorf("commentary above the directive was kept: %q", got)
	}
	if !containsLine(got, "//haoma:direction in") {
		t.Errorf("directive line missing from output: %q", got)
	}
}

func TestHaomaDirectiveIdempotent(t *testing.T) {
	src := "package p\n\n//haoma:direction in\nfunc f() {}\n"
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := stripFile(path); err != nil {
		t.Fatalf("first stripFile: %v", err)
	}
	once, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back once: %v", err)
	}
	if _, err := stripFile(path); err != nil {
		t.Fatalf("second stripFile: %v", err)
	}
	twice, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back twice: %v", err)
	}
	if string(once) != string(twice) {
		t.Errorf("second strip changed the file:\nonce:  %q\ntwice: %q", once, twice)
	}
}

// A directive with commentary above and below it must stay directly
// above its declaration, so go/ast still puts it in the Doc.
func TestDirectiveStaysAttachedToItsDeclaration(t *testing.T) {
	cases := []struct {
		name, src, directive, decl string
	}{
		{
			name:      "haoma",
			src:       "package p\n\n// commentary above\n//haoma:direction in\n// more commentary\nconst A = 1\n",
			directive: "//haoma:direction in",
			decl:      "const A = 1",
		},
		{
			name:      "go:embed",
			src:       "package p\n\nimport _ \"embed\"\n\n// commentary above\n//go:embed x.txt\n// more commentary\nvar b []byte\n",
			directive: "//go:embed x.txt",
			decl:      "var b []byte",
		},
		{
			name:      "trailing comment on code",
			src:       "package p\n\nvar z = 0 // trailing\n//haoma:direction out\n// more\nconst B = 2\n",
			directive: "//haoma:direction out",
			decl:      "const B = 2",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := writeAndStrip(t, tc.src)
			want := tc.directive + "\n" + tc.decl + "\n"
			if !strings.Contains(got, want) {
				t.Fatalf("directive not directly above its declaration:\n%s", got)
			}
			if strings.Contains(got, "commentary") || strings.Contains(got, "trailing") || strings.Contains(got, "// more") {
				t.Errorf("commentary was kept:\n%s", got)
			}
			f, err := parser.ParseFile(token.NewFileSet(), "out.go", got, parser.ParseComments)
			if err != nil {
				t.Fatalf("output does not parse: %v", err)
			}
			var doc *ast.CommentGroup
			for _, d := range f.Decls {
				if g, ok := d.(*ast.GenDecl); ok && g.Tok != token.IMPORT && strings.Contains(tc.decl, g.Tok.String()+" ") {
					if g.Doc != nil {
						doc = g.Doc
					}
				}
			}
			if doc == nil || len(doc.List) != 1 || doc.List[0].Text != tc.directive {
				t.Errorf("Doc of the declaration = %v, want only %q", doc, tc.directive)
			}
			if again := writeAndStrip(t, got); again != got {
				t.Errorf("second run changed the file:\n%s\n---\n%s", got, again)
			}
		})
	}
}

func containsLine(s, needle string) bool {
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) == needle {
			return true
		}
	}
	return false
}
