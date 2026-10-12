package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"testing"
)

const rigSource = "../haoma-rig/logs.go"

func TestDenylistMatchesTheRig(t *testing.T) {
	if _, err := os.Stat(rigSource); err != nil {
		t.Fatalf("cannot read the rig's list at %s: %v\n"+
			"the rig moved. Point this test at its new home, or the two lists drift apart in silence.",
			rigSource, err)
	}
	want, err := stringSliceVar(rigSource, "denylist")
	if err != nil {
		t.Fatal(err)
	}
	if len(want) == 0 {
		t.Fatal("read no entries from the rig's denylist — the shape of that file changed")
	}
	if len(want) != len(denylist) {
		t.Fatalf("the rig gates on %d signatures, this tool holds %d", len(want), len(denylist))
	}
	for i := range want {
		if want[i] != denylist[i] {
			t.Errorf("entry %d: the rig has %q, this tool has %q", i, want[i], denylist[i])
		}
	}
}

func stringSliceVar(path, name string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, id := range vs.Names {
				if id.Name != name || i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.CompositeLit)
				if !ok {
					continue
				}
				for _, el := range lit.Elts {
					bl, ok := el.(*ast.BasicLit)
					if !ok || bl.Kind != token.STRING {
						continue
					}
					s, err := strconv.Unquote(bl.Value)
					if err != nil {
						return nil, err
					}
					out = append(out, s)
				}
			}
		}
	}
	return out, nil
}
