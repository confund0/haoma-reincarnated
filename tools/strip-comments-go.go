// strip-comments-go walks one or more directories and rewrites every
// .go file with most of its comments removed. Preserved:
//
//   - The first comment group in a file IF it looks like a license /
//     copyright / SPDX header (case-insensitive substring match).
//   - Any //go:* directive (build tags, embed, generate, etc.).
//   - Any //nolint:* / //noinspection / //export / //line directive.
//
// Everything else — package docs, function docs, field docs, inline
// trailing comments — is dropped.
//
// Generated files (header line containing "Code generated ... DO NOT
// EDIT.") are left untouched.
//
// Used by tools/release.sh against the public-cut tree, NOT against
// the private repo. Safe to run multiple times; idempotent.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	verbose := flag.Bool("v", false, "print every file processed")
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: strip-comments-go [-v] <dir>...")
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
				name := d.Name()
				// Skip vendored / generated trees we should never touch.
				if name == ".git" || name == "node_modules" || name == "vendor" || name == "build" || name == "tmp" {
					return filepath.SkipDir
				}
				return nil
			}
			if filepath.Ext(path) != ".go" {
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
	fmt.Printf("strip-comments-go: processed=%d skipped=%d errored=%d\n",
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
	if isGenerated(src) {
		return false, nil
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return false, fmt.Errorf("parse: %w", err)
	}

	keepGroups := make(map[*ast.CommentGroup]bool)
	for i, cg := range f.Comments {
		if i == 0 && isLicenseHeader(cg) {
			keepGroups[cg] = true
			continue
		}
		if hasDirective(cg) {
			keepGroups[cg] = true
		}
	}

	var trimmed []*ast.CommentGroup
	for _, cg := range f.Comments {
		if !keepGroups[cg] {
			continue
		}
		// Inside a kept group, drop any non-directive lines so
		// "// commentary\n//go:embed foo" becomes just the directive.
		filtered := &ast.CommentGroup{}
		for _, c := range cg.List {
			if i := indexOfGroupInComments(f.Comments, cg); i == 0 && isLicenseHeader(cg) {
				filtered.List = append(filtered.List, c)
				continue
			}
			if isDirectiveLine(c.Text) {
				filtered.List = append(filtered.List, c)
			}
		}
		if len(filtered.List) > 0 {
			trimmed = append(trimmed, filtered)
		}
	}
	f.Comments = trimmed

	clearAllDocs(f, keepGroups)

	tmp := path + ".strip-tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return false, err
	}
	cfg := &printer.Config{Mode: printer.UseSpaces | printer.TabIndent, Tabwidth: 8}
	if err := cfg.Fprint(out, fset, f); err != nil {
		out.Close()
		os.Remove(tmp)
		return false, fmt.Errorf("print: %w", err)
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return false, err
	}
	return true, nil
}

func indexOfGroupInComments(all []*ast.CommentGroup, target *ast.CommentGroup) int {
	for i, cg := range all {
		if cg == target {
			return i
		}
	}
	return -1
}

func isGenerated(src []byte) bool {
	// Per https://golang.org/s/generatedcode the marker is
	// "Code generated ... DO NOT EDIT." in the first non-blank,
	// non-build-constraint line.
	for _, line := range strings.Split(string(src), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "//go:build") || strings.HasPrefix(t, "// +build") {
			continue
		}
		if strings.HasPrefix(t, "//") {
			body := strings.TrimSpace(strings.TrimPrefix(t, "//"))
			if strings.HasPrefix(body, "Code generated ") && strings.HasSuffix(body, " DO NOT EDIT.") {
				return true
			}
		}
		return false
	}
	return false
}

func isLicenseHeader(cg *ast.CommentGroup) bool {
	if cg == nil {
		return false
	}
	for _, c := range cg.List {
		body := strings.ToLower(c.Text)
		if strings.Contains(body, "license") ||
			strings.Contains(body, "copyright") ||
			strings.Contains(body, "spdx") {
			return true
		}
	}
	return false
}

func hasDirective(cg *ast.CommentGroup) bool {
	if cg == nil {
		return false
	}
	for _, c := range cg.List {
		if isDirectiveLine(c.Text) {
			return true
		}
	}
	return false
}

func isDirectiveLine(text string) bool {
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(t, "//") {
		return false
	}
	body := strings.TrimPrefix(t, "//")
	// Note: directives are //X with no space. Common forms:
	//   //go:embed, //go:build, //go:generate, //go:noinline, //go:linkname, //go:nosplit
	//   //nolint:..., //noinspection, //export, //line
	switch {
	case strings.HasPrefix(body, "go:"):
		return true
	case strings.HasPrefix(body, "nolint:"), strings.HasPrefix(body, "nolint "):
		return true
	case strings.HasPrefix(body, "noinspection"):
		return true
	case strings.HasPrefix(body, "export "):
		return true
	case strings.HasPrefix(body, "line "):
		return true
	case strings.HasPrefix(body, "+build"):
		return true
	}
	return false
}

func clearAllDocs(f *ast.File, keep map[*ast.CommentGroup]bool) {
	clear := func(cg **ast.CommentGroup) {
		if *cg != nil && !keep[*cg] {
			*cg = nil
		}
	}
	clear(&f.Doc)
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.GenDecl:
			clear(&x.Doc)
			for _, spec := range x.Specs {
				switch s := spec.(type) {
				case *ast.ValueSpec:
					clear(&s.Doc)
					clear(&s.Comment)
				case *ast.TypeSpec:
					clear(&s.Doc)
					clear(&s.Comment)
				case *ast.ImportSpec:
					clear(&s.Doc)
					clear(&s.Comment)
				}
			}
		case *ast.FuncDecl:
			clear(&x.Doc)
		case *ast.Field:
			clear(&x.Doc)
			clear(&x.Comment)
		}
		return true
	})
}
