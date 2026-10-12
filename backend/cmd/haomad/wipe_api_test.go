package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"haoma/internal/eventbus"
)

func withFastWipeGrace(t *testing.T) {
	t.Helper()
	prev := wipeClientGraceForTest
	wipeClientGraceForTest = 10 * time.Millisecond
	t.Cleanup(func() { wipeClientGraceForTest = prev })
}

func captureExit(t *testing.T) <-chan int {
	t.Helper()
	got := make(chan int, 1)
	prev := exitAfterWipe
	exitAfterWipe = func(code int) { got <- code }
	t.Cleanup(func() { exitAfterWipe = prev })
	return got
}

func newDataRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "backend"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "vault.enc"), []byte("HAOMAVLT"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func postWipe(t *testing.T, d *daemon) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/wipe", strings.NewReader("{}"))
	d.apiHandler().ServeHTTP(rec, req)
	return rec
}

func TestWipe_ErasesTheRootAndStops(t *testing.T) {
	withFastWipeGrace(t)
	exited := captureExit(t)

	d := newTestDaemon(t)
	root := newDataRoot(t)
	d.cfg.dataRoot = root

	rec := postWipe(t, d)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /wipe: status %d, body %s", rec.Code, rec.Body.String())
	}
	var out wipeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if !out.Started {
		t.Fatal("the answer did not say the erasure started")
	}

	select {
	case code := <-exited:
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon never stopped after erasing")
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("the root still holds %d entries", len(entries))
	}
}

func TestWipe_RefusedWithNoDataRoot(t *testing.T) {
	withFastWipeGrace(t)
	exited := captureExit(t)

	d := newTestDaemon(t)
	d.cfg.dataRoot = ""

	rec := postWipe(t, d)
	if rec.Code == http.StatusOK {
		t.Fatalf("a wipe with no data root was accepted: %s", rec.Body.String())
	}

	select {
	case code := <-exited:
		t.Fatalf("the daemon stopped anyway, with code %d", code)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestWipe_TellsAttachedClientsFirst(t *testing.T) {
	withFastWipeGrace(t)
	exited := captureExit(t)

	d := newTestDaemon(t)
	d.cfg.dataRoot = newDataRoot(t)

	events, cancel := d.bus.Subscribe("", 4)
	defer cancel()

	if rec := postWipe(t, d); rec.Code != http.StatusOK {
		t.Fatalf("POST /wipe: status %d", rec.Code)
	}

	select {
	case ev := <-events:
		if ev.Topic != "device.destroy" {
			t.Fatalf("topic = %q, want device.wiped", ev.Topic)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no client was told the device is being erased")
	}
	<-exited
}

func withFastEraseShutdownWait(t *testing.T, d time.Duration) {
	t.Helper()
	prev := eraseShutdownTimeoutForTest
	eraseShutdownTimeoutForTest = d
	t.Cleanup(func() { eraseShutdownTimeoutForTest = prev })
}

func withRunLoop(t *testing.T, d *daemon) (asked <-chan struct{}, finish func()) {
	t.Helper()
	stopped := make(chan struct{})
	runDone := make(chan struct{})
	var once sync.Once
	d.stopRun = func() { once.Do(func() { close(stopped) }) }
	d.runDone = runDone
	return stopped, func() { close(runDone) }
}

func TestWipe_TheDaemonStopsBeforeTheFilesGo(t *testing.T) {
	withFastWipeGrace(t)
	withFastEraseShutdownWait(t, 30*time.Second)
	exited := captureExit(t)

	d := newTestDaemon(t)
	root := newDataRoot(t)
	d.cfg.dataRoot = root
	asked, finish := withRunLoop(t, d)

	go d.eraseAndExit("a test asked", "erase-one")

	select {
	case <-asked:
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon was never asked to stop")
	}

	time.Sleep(300 * time.Millisecond)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("the files went before the daemon stopped")
	}
	select {
	case <-exited:
		t.Fatal("the process ended before the shutdown finished")
	default:
	}

	finish()

	select {
	case code := <-exited:
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the erasure never ended the process")
	}
	entries, err = os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("the root still holds %d entries", len(entries))
	}
}

func TestWipe_AShutdownThatHangsStillErases(t *testing.T) {
	withFastWipeGrace(t)
	withFastEraseShutdownWait(t, 200*time.Millisecond)
	exited := captureExit(t)

	d := newTestDaemon(t)
	root := newDataRoot(t)
	d.cfg.dataRoot = root

	asked, _ := withRunLoop(t, d)

	go d.eraseAndExit("a test asked", "erase-one")

	select {
	case <-asked:
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon was never asked to stop")
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("a shutdown that never finished stopped the erasure too")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("the root still holds %d entries", len(entries))
	}
}

func TestWipe_TheLocalVerbTakesTheSameOrder(t *testing.T) {
	withFastWipeGrace(t)
	withFastEraseShutdownWait(t, 30*time.Second)
	exited := captureExit(t)

	d := newTestDaemon(t)
	root := newDataRoot(t)
	d.cfg.dataRoot = root
	asked, finish := withRunLoop(t, d)

	if rec := postWipe(t, d); rec.Code != http.StatusOK {
		t.Fatalf("POST /wipe: status %d, body %s", rec.Code, rec.Body.String())
	}

	select {
	case <-asked:
	case <-time.After(5 * time.Second):
		t.Fatal("the local verb erased without stopping the daemon")
	}
	select {
	case <-exited:
		t.Fatal("the local verb ended the process before the shutdown finished")
	default:
	}

	finish()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the local verb never ended the process")
	}
}

func TestWipe_TwoErasuresRunOnce(t *testing.T) {
	withFastWipeGrace(t)

	var exits atomic.Int32
	prev := exitAfterWipe
	exitAfterWipe = func(int) { exits.Add(1) }
	t.Cleanup(func() { exitAfterWipe = prev })

	d := newTestDaemon(t)
	d.cfg.dataRoot = newDataRoot(t)

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.eraseAndExit("a test asked three times", "erase-three")
		}()
	}
	wg.Wait()

	if got := exits.Load(); got != 1 {
		t.Fatalf("%d erasures ran, want 1", got)
	}
}

func TestWipe_TheRefusedSecondCallIsLogged(t *testing.T) {
	withFastWipeGrace(t)
	buf := captureLog(t)

	var exits atomic.Int32
	prev := exitAfterWipe
	exitAfterWipe = func(int) { exits.Add(1) }
	t.Cleanup(func() { exitAfterWipe = prev })

	d := newTestDaemon(t)
	d.cfg.dataRoot = newDataRoot(t)

	d.eraseAndExit("the operator asked", "erase-first")
	d.eraseAndExit("the other device commanded it", "erase-second")

	if got := exits.Load(); got != 1 {
		t.Fatalf("%d erasures ran, want 1", got)
	}
	if lines := logLines(buf, "refused, an erasure is already running",
		"erase_id=erase-second", "the other device commanded it"); len(lines) != 1 {
		t.Fatalf("the refused call did not name its reason and erase id; got %d lines:\n%s",
			len(lines), buf.String())
	}
	if lines := logLines(buf, "refused, an erasure is already running",
		"erase-first"); len(lines) != 0 {
		t.Fatalf("the refusal named the first call's erase id:\n%s", buf.String())
	}
}

func TestWipe_TheAnnouncementCarriesTheEraseID(t *testing.T) {
	withFastWipeGrace(t)
	buf := captureLog(t)
	exited := captureExit(t)

	d := newTestDaemon(t)
	d.cfg.dataRoot = newDataRoot(t)

	events, cancel := d.bus.Subscribe("", 4)
	defer cancel()

	if rec := postWipe(t, d); rec.Code != http.StatusOK {
		t.Fatalf("POST /wipe: status %d", rec.Code)
	}

	var got wipeAnnouncement
	select {
	case ev := <-events:
		a, ok := ev.Payload.(wipeAnnouncement)
		if !ok {
			t.Fatalf("payload type = %T, want wipeAnnouncement", ev.Payload)
		}
		got = a
	case <-time.After(5 * time.Second):
		t.Fatal("no client was told the device is being erased")
	}
	<-exited

	if got.EraseID == "" {
		t.Fatal("the announcement carried no erase id")
	}
	if got.Reason == "" {
		t.Fatal("the announcement no longer carries a reason")
	}

	if lines := logLines(buf, "wipe: erasing this device",
		"erase_id="+got.EraseID); len(lines) != 1 {
		t.Fatalf("the deciding line did not name the announcement's erase id; got %d:\n%s",
			len(lines), buf.String())
	}
	if lines := logLines(buf, "attached clients told to stop",
		"erase_id="+got.EraseID, "told=1"); len(lines) != 1 {
		t.Fatalf("the announcement line did not report one client told; got %d:\n%s",
			len(lines), buf.String())
	}
}

func TestWipe_ADroppedAnnouncementIsNotReportedAsTold(t *testing.T) {
	buf := captureLog(t)

	d := newTestDaemon(t)

	_, cancel := d.bus.Subscribe("", 1)
	defer cancel()

	d.bus.Publish(eventbus.TopicInboxReceived, nil)

	d.announceWipe("a test asked", "erase-dropped")

	if lines := logLines(buf, "a client was not told to stop",
		"erase_id=erase-dropped", "told=0", "dropped=1"); len(lines) != 1 {
		t.Fatalf("a dropped announcement was not reported; got %d lines:\n%s",
			len(lines), buf.String())
	}
	if lines := logLines(buf, "attached clients told to stop"); len(lines) != 0 {
		t.Fatalf("a dropped announcement was reported as told:\n%s", buf.String())
	}
}

func TestEraseRoots_APhoneLosesWhatTheAppWroteBesideTheDataRoot(t *testing.T) {
	data := filepath.Join(t.TempDir(), "io.haoma.calculator")
	cfg := filepath.Join(data, "files", "haoma")
	libTarget := t.TempDir()
	mustWrite := func(path string) string {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	vault := mustWrite(filepath.Join(cfg, "vault.enc"))
	keys := mustWrite(filepath.Join(data, "files", "unlock_keys.json"))
	sent := mustWrite(filepath.Join(data, "cache", "saf-in", "photo.jpg"))
	prefs := mustWrite(filepath.Join(data, "shared_prefs", "MainActivity.xml"))
	binary := mustWrite(filepath.Join(libTarget, "libhaomad.so"))
	lib := filepath.Join(data, "lib")
	if err := os.Symlink(libTarget, lib); err != nil {
		t.Fatal(err)
	}

	if !eraseRoots(cfg, "android", "test-erase") {
		t.Fatal("eraseRoots reported a failure")
	}

	for _, gone := range []string{vault, keys, sent, prefs} {
		if _, err := os.Lstat(gone); err == nil {
			t.Errorf("%s survived", filepath.Base(gone))
		}
	}
	for _, kept := range []string{lib, binary} {
		if _, err := os.Lstat(kept); err != nil {
			t.Errorf("%s was removed", kept)
		}
	}
}

func TestEraseRoots_APhonePathOfTheWrongShapeStillErasesTheDataRoot(t *testing.T) {
	root := newDataRoot(t)

	if eraseRoots(root, "android", "test-erase") {
		t.Fatal("an erasure that skipped the app's directories reported success")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("the data root still holds %d entries", len(entries))
	}
}
