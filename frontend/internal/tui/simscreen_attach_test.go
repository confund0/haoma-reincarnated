package tui_test

import (
	"crypto/tls"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/ipcclient"
	"haoma-frontend/internal/tui"
)

func spinIPCServer(t *testing.T) (dir, addr string, cancel func()) {
	t.Helper()
	dir = t.TempDir()
	tlsCfg, err := ipc.LoadOrCreateTLS(dir)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := ipc.LoadOrCreateToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := ipc.NewServer(tok)
	httpSrv := &http.Server{Handler: srv.Handler(), TLSConfig: tlsCfg}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = httpSrv.Serve(tls.NewListener(ln, tlsCfg))
	}()
	return dir, ln.Addr().String(), func() { _ = httpSrv.Close() }
}

type simDrawer struct {
	app *tui.App

	stopped chan struct{}
}

var (
	simAppsMu sync.Mutex
	simApps   = map[tcell.SimulationScreen]*simDrawer{}
)

func registerSimApp(t *testing.T, sim tcell.SimulationScreen, app *tui.App) (stopped func()) {
	t.Helper()
	d := &simDrawer{app: app, stopped: make(chan struct{})}
	simAppsMu.Lock()
	simApps[sim] = d
	simAppsMu.Unlock()
	var once sync.Once
	mark := func() { once.Do(func() { close(d.stopped) }) }
	t.Cleanup(func() {
		mark()
		simAppsMu.Lock()
		delete(simApps, sim)
		simAppsMu.Unlock()
	})
	return mark
}

func simDrawerFor(sim tcell.SimulationScreen) *simDrawer {
	simAppsMu.Lock()
	defer simAppsMu.Unlock()
	return simApps[sim]
}

func screenText(sim tcell.SimulationScreen) string {
	cells, w, h := simCells(sim)
	var b strings.Builder
	for row := 0; row < h; row++ {
		for col := 0; col < w; col++ {
			runes := cells[row*w+col].Runes
			if len(runes) == 0 || runes[0] == 0 {
				b.WriteByte(' ')
			} else {
				b.WriteString(string(runes))
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func simCells(sim tcell.SimulationScreen) ([]tcell.SimCell, int, int) {
	read := func() ([]tcell.SimCell, int, int) {
		live, w, h := sim.GetContents()
		cells := make([]tcell.SimCell, len(live))
		copy(cells, live)
		return cells, w, h
	}
	d := simDrawerFor(sim)
	if d == nil {
		return read()
	}

	select {
	case <-d.stopped:
		return read()
	default:
	}
	type frame struct {
		cells []tcell.SimCell
		w, h  int
	}
	out := make(chan frame, 1)
	go d.app.OnDrawGoroutine(func() {
		cells, w, h := read()
		out <- frame{cells, w, h}
	})
	select {
	case f := <-out:
		return f.cells, f.w, f.h
	case <-d.stopped:
		return read()
	case <-time.After(30 * time.Second):
		panic("the application never ran a queued screen read and never stopped")
	}
}

func TestApp_AttachesAndRendersOnSimulationScreen(t *testing.T) {
	dir, addr, cancel := spinIPCServer(t)
	defer cancel()

	client, err := ipcclient.New(ipcclient.Config{
		FrontendDir: dir,
		Addr:        addr,
		ClientName:  "simtest",
	})
	if err != nil {
		t.Fatalf("ipcclient.New: %v", err)
	}
	go func() { _ = client.Run() }()

	app := tui.New(client)

	sim := tcell.NewSimulationScreen("")
	app.SetScreen(sim)
	stopped := registerSimApp(t, sim, app)

	runErr := make(chan error, 1)
	go func() { runErr <- app.Run(); stopped() }()
	defer app.Stop()

	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("App never painted expected chrome on fake glass; last frame:\n%s", screenText(sim))
		case err := <-runErr:
			t.Fatalf("App.Run returned before render (err=%v)", err)
		default:

			if strings.Contains(screenText(sim), "connecting to haoma") {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
}
