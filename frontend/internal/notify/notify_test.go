package notify

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"
)

type fakeRunner struct {
	mu          sync.Mutex
	Calls       [][]string
	nextOutputs []string
	nextErr     error
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	full := append([]string{name}, args...)
	f.Calls = append(f.Calls, full)
	out := ""
	if len(f.nextOutputs) > 0 {
		out = f.nextOutputs[0]
		f.nextOutputs = f.nextOutputs[1:]
	}
	return out, f.nextErr
}

func (f *fakeRunner) queueOutput(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextOutputs = append(f.nextOutputs, s)
}

func (f *fakeRunner) snapshot() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]string, len(f.Calls))
	for i, c := range f.Calls {
		out[i] = append([]string(nil), c...)
	}
	return out
}

func fakeLookPath(present map[string]string) LookPath {
	return func(name string) (string, error) {
		if path, ok := present[name]; ok {
			return path, nil
		}
		return "", errors.New("not found")
	}
}

func waitForCalls(t *testing.T, f *fakeRunner, want int) [][]string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got := f.snapshot()
		if len(got) >= want {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("waitForCalls: wanted %d call(s); saw %d", want, len(f.snapshot()))
	return nil
}

func TestRenderBannerPrivacyMatrix(t *testing.T) {
	ev := Event{ChatID: "c1", PeerLabel: "Alice", Body: "hi there"}
	cases := []struct {
		name                             string
		showSender, body                 bool
		wantTitle, wantTx                string
		wantRedactSender, wantRedactBody bool
	}{
		{"both off", false, false, "Haoma", "New message", true, true},
		{"sender only", true, false, "Alice", "New message", false, true},
		{"body only", false, true, "Haoma", "hi there", true, false},
		{"both on", true, true, "Alice", "hi there", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			title, body, redS, redB := renderBanner(ev, Privacy{ShowSender: tc.showSender, ShowBody: tc.body})
			if title != tc.wantTitle || body != tc.wantTx {
				t.Errorf("got (%q,%q); want (%q,%q)", title, body, tc.wantTitle, tc.wantTx)
			}
			if redS != tc.wantRedactSender || redB != tc.wantRedactBody {
				t.Errorf("redaction bools got (sender=%v,body=%v); want (sender=%v,body=%v)",
					redS, redB, tc.wantRedactSender, tc.wantRedactBody)
			}
		})
	}
}

func TestRenderBannerTitleTruncation(t *testing.T) {
	long := "Tokyo-Mitsubishi-Banking-Corporation-Japan"
	got, _, _, _ := renderBanner(Event{PeerLabel: long}, Privacy{ShowSender: true, ShowBody: false})
	if len([]rune(got)) != 25 {
		t.Errorf("expected 25-rune title, got %d (%q)", len([]rune(got)), got)
	}
}

func TestRenderBannerEmptyBodyFalsy(t *testing.T) {

	_, body, _, redB := renderBanner(Event{Body: ""}, Privacy{ShowBody: true})
	if body != "New message" {
		t.Errorf("empty body should fall back; got %q", body)
	}
	if !redB {
		t.Errorf("empty body should report RedactedBody=true; got false")
	}
}

func TestNotifyBroadcastFiresWithoutBackend(t *testing.T) {
	r := &fakeRunner{}

	d := New(r, fakeLookPath(map[string]string{}), "android")
	dec := d.Notify(context.Background(),
		Event{ChatID: "c1", PeerLabel: "Alice", Body: "hi"},
		Privacy{ShellEnabled: true, ShowSender: false, ShowBody: false})
	if !dec.Fired {
		t.Errorf("Fired should be true even without a backend; got false")
	}
	if dec.Title != "Haoma" || dec.Body != "New message" {
		t.Errorf("redacted defaults expected; got (%q,%q)", dec.Title, dec.Body)
	}
	if !dec.RedactedSender || !dec.RedactedBody {
		t.Errorf("both redaction bools should be true; got (sender=%v,body=%v)",
			dec.RedactedSender, dec.RedactedBody)
	}
	time.Sleep(10 * time.Millisecond)
	if calls := r.snapshot(); len(calls) != 0 {
		t.Errorf("no backend should mean no shellout; got %v", calls)
	}
}

func TestNotifyDisabledNoOps(t *testing.T) {
	r := &fakeRunner{}
	d := New(r, fakeLookPath(map[string]string{"notify-send": "/usr/bin/notify-send"}), "linux")
	dec := d.Notify(context.Background(), Event{ChatID: "c1"}, Privacy{ShellEnabled: false})
	if dec.Fired || dec.Title != "" || dec.Body != "" {
		t.Errorf("disabled Notify should no-op; got %+v", dec)
	}
	time.Sleep(10 * time.Millisecond)
	if calls := r.snapshot(); len(calls) != 0 {
		t.Errorf("expected zero shellouts; got %d (%v)", len(calls), calls)
	}
}

func TestLinuxFirstEmitNoReplaceFlag(t *testing.T) {
	r := &fakeRunner{}
	r.queueOutput("42")
	d := New(r, fakeLookPath(map[string]string{"notify-send": "/usr/bin/notify-send"}), "linux")
	d.Notify(context.Background(), Event{ChatID: "c1", PeerLabel: "Alice", Body: "hi"},
		Privacy{ShellEnabled: true, ShowSender: true, ShowBody: true})
	calls := waitForCalls(t, r, 1)
	args := calls[0]
	if args[0] != "/usr/bin/notify-send" {
		t.Fatalf("expected notify-send; got %q", args[0])
	}
	for _, a := range args {
		if a == "-r" {
			t.Errorf("first emit should NOT carry -r; got %v", args)
		}
	}

	if args[len(args)-2] != "Alice" || args[len(args)-1] != "hi" {
		t.Errorf("banner args wrong; got %v", args[len(args)-2:])
	}
}

func TestLinuxSecondEmitReplacesFirst(t *testing.T) {
	r := &fakeRunner{}
	r.queueOutput("42")
	r.queueOutput("43")
	d := New(r, fakeLookPath(map[string]string{"notify-send": "/usr/bin/notify-send"}), "linux")
	d.Notify(context.Background(), Event{ChatID: "c1", PeerLabel: "Alice", Body: "first"},
		Privacy{ShellEnabled: true, ShowSender: true, ShowBody: true})
	waitForCalls(t, r, 1)
	d.Notify(context.Background(), Event{ChatID: "c1", PeerLabel: "Alice", Body: "second"},
		Privacy{ShellEnabled: true, ShowSender: true, ShowBody: true})
	calls := waitForCalls(t, r, 2)
	second := calls[1]
	foundReplace := false
	for i, a := range second {
		if a == "-r" && i+1 < len(second) && second[i+1] == "42" {
			foundReplace = true
			break
		}
	}
	if !foundReplace {
		t.Errorf("second emit should carry -r 42; got %v", second)
	}
}

func TestLinuxDifferentChatsKeepIndependentIDs(t *testing.T) {
	r := &fakeRunner{}
	r.queueOutput("1")
	r.queueOutput("2")
	r.queueOutput("3")
	d := New(r, fakeLookPath(map[string]string{"notify-send": "/usr/bin/notify-send"}), "linux")

	d.Notify(context.Background(), Event{ChatID: "alice", Body: "a"}, Privacy{ShellEnabled: true})
	waitForCalls(t, r, 1)
	d.Notify(context.Background(), Event{ChatID: "bob", Body: "b"}, Privacy{ShellEnabled: true})
	waitForCalls(t, r, 2)
	d.Notify(context.Background(), Event{ChatID: "alice", Body: "a2"}, Privacy{ShellEnabled: true})
	calls := waitForCalls(t, r, 3)

	third := calls[2]
	wantReplace := false
	for i, a := range third {
		if a == "-r" && i+1 < len(third) && third[i+1] == "1" {
			wantReplace = true
		}
	}
	if !wantReplace {
		t.Errorf("third emit (alice round-2) should -r 1; got %v", third)
	}
}

func TestLinuxDismissUsesGdbusWhenAvailable(t *testing.T) {
	r := &fakeRunner{}
	r.queueOutput("99")
	d := New(r, fakeLookPath(map[string]string{
		"notify-send": "/usr/bin/notify-send",
		"gdbus":       "/usr/bin/gdbus",
	}), "linux")
	d.Notify(context.Background(), Event{ChatID: "c1"}, Privacy{ShellEnabled: true})
	waitForCalls(t, r, 1)
	d.Dismiss(context.Background(), "c1")
	calls := r.snapshot()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls; got %d", len(calls))
	}
	if calls[1][0] != "/usr/bin/gdbus" {
		t.Errorf("dismiss should use gdbus; got %q", calls[1][0])
	}
	joined := strings.Join(calls[1], " ")
	if !strings.Contains(joined, "CloseNotification") || !strings.Contains(joined, "99") {
		t.Errorf("dismiss should call CloseNotification 99; got %v", calls[1])
	}
}

func TestLinuxDismissFallsBackToNotifySendWhenNoGdbus(t *testing.T) {
	r := &fakeRunner{}
	r.queueOutput("42")
	d := New(r, fakeLookPath(map[string]string{"notify-send": "/usr/bin/notify-send"}), "linux")
	d.Notify(context.Background(), Event{ChatID: "c1"}, Privacy{ShellEnabled: true})
	waitForCalls(t, r, 1)
	d.Dismiss(context.Background(), "c1")
	calls := r.snapshot()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls; got %d", len(calls))
	}
	args := calls[1]
	if args[0] != "/usr/bin/notify-send" {
		t.Errorf("fallback should use notify-send; got %q", args[0])
	}
	hasReplace := false
	for i, a := range args {
		if a == "-r" && i+1 < len(args) && args[i+1] == "42" {
			hasReplace = true
		}
	}
	if !hasReplace {
		t.Errorf("fallback should carry -r 42; got %v", args)
	}
}

func TestLinuxDismissNoLiveIDIsNoOp(t *testing.T) {
	r := &fakeRunner{}
	d := New(r, fakeLookPath(map[string]string{"notify-send": "/usr/bin/notify-send"}), "linux")
	d.Dismiss(context.Background(), "never-fired")
	if calls := r.snapshot(); len(calls) != 0 {
		t.Errorf("dismiss with no live id should no-op; got %v", calls)
	}
}

func TestDispatcherCachesDetection(t *testing.T) {
	r := &fakeRunner{}
	r.queueOutput("1")
	r.queueOutput("2")
	probes := 0
	lp := func(name string) (string, error) {
		if name == "notify-send" {
			probes++
			return "/usr/bin/notify-send", nil
		}
		return "", errors.New("not found")
	}
	d := New(r, lp, "linux")
	d.Notify(context.Background(), Event{ChatID: "c1"}, Privacy{ShellEnabled: true})
	waitForCalls(t, r, 1)
	d.Notify(context.Background(), Event{ChatID: "c2"}, Privacy{ShellEnabled: true})
	waitForCalls(t, r, 2)
	if probes != 1 {
		t.Errorf("expected exactly 1 LookPath probe; got %d", probes)
	}
}

func TestDispatcherUnknownOSSilentlyDisables(t *testing.T) {
	r := &fakeRunner{}
	d := New(r, fakeLookPath(map[string]string{}), "openbsd")
	dec := d.Notify(context.Background(), Event{ChatID: "c1"}, Privacy{ShellEnabled: true})
	if !dec.Fired {
		t.Errorf("Notify should still report Fired=true (caller suppresses by setting Privacy); detection is the silent layer")
	}
	time.Sleep(10 * time.Millisecond)
	if calls := r.snapshot(); len(calls) != 0 {
		t.Errorf("unsupported OS should produce no shellouts; got %v", calls)
	}
}

func TestTermuxFiresWithIDTag(t *testing.T) {
	r := &fakeRunner{}
	d := New(r, fakeLookPath(map[string]string{"termux-notification": "/data/data/com.termux/files/usr/bin/termux-notification"}), "android")
	d.Notify(context.Background(), Event{ChatID: "c1", PeerLabel: "Alice", Body: "hi"},
		Privacy{ShellEnabled: true, ShowSender: true, ShowBody: true})
	calls := waitForCalls(t, r, 1)
	args := calls[0]
	if !strings.HasSuffix(args[0], "termux-notification") {
		t.Errorf("expected termux-notification; got %q", args[0])
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--id haoma-c1") {
		t.Errorf("termux call should carry --id haoma-c1; got %v", args)
	}
}

func TestWindowsUsesPowerShellWithEncodedCommand(t *testing.T) {
	r := &fakeRunner{}
	d := New(r, fakeLookPath(map[string]string{"powershell": `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`}), "windows")
	d.Notify(context.Background(), Event{ChatID: "c1", PeerLabel: "Alice", Body: "hi"},
		Privacy{ShellEnabled: true, ShowSender: true, ShowBody: true})
	calls := waitForCalls(t, r, 1)
	args := calls[0]
	if !strings.HasSuffix(args[0], "powershell.exe") {
		t.Errorf("expected powershell.exe; got %q", args[0])
	}
	hasEncoded := false
	for _, a := range args {
		if a == "-EncodedCommand" {
			hasEncoded = true
		}
	}
	if !hasEncoded {
		t.Errorf("Windows backend should use -EncodedCommand to bypass quoting; got %v", args)
	}

	encoded := args[len(args)-1]
	decoded, err := decodePowerShell(encoded)
	if err != nil {
		t.Fatalf("decode -EncodedCommand: %v", err)
	}
	for _, want := range []string{
		"haoma-c1",
		"Alice",
		"hi",
		"ToastNotification",
		"ToastGeneric",
	} {
		if !strings.Contains(decoded, want) {
			t.Errorf("decoded script missing %q; got %q", want, decoded)
		}
	}
}

func TestWindowsFallsBackToPwshWhenLegacyMissing(t *testing.T) {
	r := &fakeRunner{}
	d := New(r, fakeLookPath(map[string]string{"pwsh": `C:\Program Files\PowerShell\7\pwsh.exe`}), "windows")
	d.Notify(context.Background(), Event{ChatID: "c1", Body: "hi"}, Privacy{ShellEnabled: true})
	calls := waitForCalls(t, r, 1)
	if !strings.HasSuffix(calls[0][0], "pwsh.exe") {
		t.Errorf("expected pwsh.exe fallback; got %q", calls[0][0])
	}
}

func TestWindowsDismissCallsHistoryRemove(t *testing.T) {
	r := &fakeRunner{}
	d := New(r, fakeLookPath(map[string]string{"powershell": `powershell.exe`}), "windows")
	d.Notify(context.Background(), Event{ChatID: "c1", Body: "hi"}, Privacy{ShellEnabled: true})
	waitForCalls(t, r, 1)
	d.Dismiss(context.Background(), "c1")
	calls := r.snapshot()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls (send + dismiss); got %d", len(calls))
	}
	decoded, err := decodePowerShell(calls[1][len(calls[1])-1])
	if err != nil {
		t.Fatalf("decode dismiss script: %v", err)
	}
	if !strings.Contains(decoded, "History.Remove") {
		t.Errorf("dismiss script should call History.Remove; got %q", decoded)
	}
	if !strings.Contains(decoded, "haoma-c1") {
		t.Errorf("dismiss script should target tag haoma-c1; got %q", decoded)
	}
}

func TestWindowsEscapesXMLAngleBrackets(t *testing.T) {
	r := &fakeRunner{}
	d := New(r, fakeLookPath(map[string]string{"powershell": `powershell.exe`}), "windows")
	d.Notify(context.Background(), Event{ChatID: "c1", PeerLabel: "<script>", Body: "a & b > c"},
		Privacy{ShellEnabled: true, ShowSender: true, ShowBody: true})
	calls := waitForCalls(t, r, 1)
	decoded, _ := decodePowerShell(calls[0][len(calls[0])-1])
	for _, want := range []string{"&lt;script&gt;", "a &amp; b &gt; c"} {
		if !strings.Contains(decoded, want) {
			t.Errorf("XML escape missing %q; got %q", want, decoded)
		}
	}
	for _, banned := range []string{"<script>", "a & b > c"} {
		if strings.Contains(decoded, banned) {
			t.Errorf("decoded script should NOT contain raw %q; got %q", banned, decoded)
		}
	}
}

func decodePowerShell(b64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", err
	}
	if len(raw)%2 != 0 {
		return "", errors.New("odd byte length — not UTF-16LE")
	}
	codes := make([]uint16, len(raw)/2)
	for i := range codes {
		codes[i] = uint16(raw[i*2]) | uint16(raw[i*2+1])<<8
	}
	return string(utf16.Decode(codes)), nil
}

func TestDarwinUsesOsascript(t *testing.T) {
	r := &fakeRunner{}
	d := New(r, fakeLookPath(map[string]string{"osascript": "/usr/bin/osascript"}), "darwin")
	d.Notify(context.Background(), Event{ChatID: "c1", PeerLabel: "Alice", Body: "hi"},
		Privacy{ShellEnabled: true, ShowSender: true, ShowBody: true})
	calls := waitForCalls(t, r, 1)
	args := calls[0]
	if args[0] != "/usr/bin/osascript" {
		t.Errorf("expected osascript; got %q", args[0])
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "display notification") || !strings.Contains(joined, "Alice") {
		t.Errorf("osascript args should contain banner content; got %v", args)
	}
}
