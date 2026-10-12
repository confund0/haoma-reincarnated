package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"testing"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/linkeddevice"
	"haoma-frontend/internal/pair"
	"haoma-frontend/internal/signal"
	"haoma-frontend/internal/store"
)

func wiredDaemon(t *testing.T) (*daemon, string, *int) {
	t.Helper()
	root := t.TempDir()
	st, err := store.Unlock(t.TempDir(), "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Lock() })
	sigState, _, err := signal.LoadOrBootstrap(st, 4)
	if err != nil {
		t.Fatal(err)
	}
	posted := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/peers":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"peers":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/peers":
			posted++
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	d := &daemon{
		store:         st,
		signalState:   sigState,
		stores:        signal.NewStores(st, sigState),
		chats:         chat.NewStore(st),
		linkedDevices: linkeddevice.New(st),
		backendClient: backendapi.New(srv.URL, "", nil),
		emptyMarker:   mustMarker(t, root),
	}
	t.Cleanup(d.wireEmptyMarker())
	d.startEmptyCheck(context.Background(), func(f func()) { f() })
	if !markerThere(t, root) {
		t.Fatal("startEmptyCheck wrote no file on an empty device")
	}
	return d, root, &posted
}

func TestEmptyMarkerWiringRemovesTheFile(t *testing.T) {
	cases := []struct {
		name string
		grow func(t *testing.T, d *daemon, posted *int)
	}{
		{"a chat", func(t *testing.T, d *daemon, _ *int) {
			if _, err := d.chats.CreateDirect("peer-a"); err != nil {
				t.Fatalf("CreateDirect: %v", err)
			}
		}},
		{"a linked device", func(t *testing.T, d *daemon, _ *int) {
			if _, err := d.linkedDevices.Link("peer-b", 10); err != nil {
				t.Fatalf("Link: %v", err)
			}
		}},
		{"a contact", func(t *testing.T, d *daemon, posted *int) {
			remote, err := signal.Bootstrap(3)
			if err != nil {
				t.Fatal(err)
			}
			inv, _, err := pair.Build(remote, remote, []string{"remote-onion"}, "remote", nil)
			if err != nil {
				t.Fatal(err)
			}
			_, mine, err := pair.Build(d.signalState, d.signalState, []string{"local-onion"}, "local", nil)
			if err != nil {
				t.Fatal(err)
			}
			minted := backendapi.MintedOnion{
				Address:    "fakeonion4567890123456789012345678901234567890123456789",
				PrivateKey: "ZmFrZS1iYXNlNjQta2V5",
			}
			if err := pair.Import(context.Background(), d.stores, d.backendClient, inv, mine, minted); err != nil {
				t.Fatalf("Import: %v", err)
			}
			if *posted != 1 {
				t.Fatalf("peer posted %d times, want 1", *posted)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, root, posted := wiredDaemon(t)
			c.grow(t, d, posted)
			if markerThere(t, root) {
				t.Errorf("the file is still there after %s was added", c.name)
			}
		})
	}
}

func TestRunWiresTheEmptyMarker(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "run.go", nil, 0)
	if err != nil {
		t.Fatalf("parse run.go: %v", err)
	}
	first := map[string]token.Pos{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name.Name != "run" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := ""
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				name = fun.Name
			case *ast.SelectorExpr:
				if x, ok := fun.X.(*ast.Ident); ok && x.Name == "store" {
					name = "store." + fun.Sel.Name
				} else {
					name = fun.Sel.Name
				}
			}
			if _, seen := first[name]; !seen && name != "" {
				first[name] = call.Pos()
			}
			return true
		})
	}
	for _, name := range []string{"newEmptyMarker", "wireEmptyMarker", "startEmptyCheck", "store.Unlock"} {
		if _, ok := first[name]; !ok {
			t.Errorf("run() does not call %s", name)
		}
	}
	if t.Failed() {
		return
	}
	if first["newEmptyMarker"] > first["store.Unlock"] {
		t.Error("run() calls newEmptyMarker after the store opens")
	}
	if first["wireEmptyMarker"] > first["startEmptyCheck"] {
		t.Error("run() calls startEmptyCheck before wireEmptyMarker")
	}
}
