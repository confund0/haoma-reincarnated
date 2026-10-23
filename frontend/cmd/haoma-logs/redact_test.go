package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLog(t *testing.T, dir, account, name, content string) string {
	t.Helper()
	accDir := filepath.Join(dir, account)
	if err := os.MkdirAll(accDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(accDir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func testKey(t *testing.T) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "redact.key")
	key, err := loadOrMakeKey(path)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestRedactOnionAndTokenShapes(t *testing.T) {
	dir := t.TempDir()
	onion := strings.Repeat("a", 56)
	path := writeLog(t, dir, "alice", "haomad.log",
		"time=2026-10-13T11:00:00Z level=INFO msg=\"xport: inbound envelope\" from="+onion+".onion\n"+
			"time=2026-10-13T11:00:01Z level=INFO msg=\"fetch attempt\" url=http://"+onion+".onion/audio/averylongtoken123456789\n")

	key := testKey(t)
	var out bytes.Buffer
	ok, err := redactDir(dir, key, "", &out)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Errorf("expected a clean self-check, got leftovers: %s", out.String())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if strings.Contains(s, onion) {
		t.Errorf("the full onion is still in the file:\n%s", s)
	}
	if !strings.Contains(s, "time=2026-10-13T11:00:00Z") {
		t.Errorf("the timestamp was mangled:\n%s", s)
	}
	if !strings.Contains(s, "aaaaaaaa…") {
		t.Errorf("want the onion shortened to its first 8 chars, got:\n%s", s)
	}
}

func TestRedactPhoneAppNameShapes(t *testing.T) {
	dir := t.TempDir()
	path := writeLog(t, dir, "alice", "haoma-gui.log",
		"2026-10-13T11:33:18.433Z INFO status ringing — incoming call from Bob\n"+
			"2026-10-13T11:33:19.667Z INFO status connected — call with Bob\n"+
			"2026-10-13T11:41:53.282Z INFO status call from Bob ended\n"+
			"2026-10-13T12:03:15.412Z WARN status missed call from Bob\n"+
			"2026-10-13T11:31:17.929Z INFO status notify (Bob): Bob\n"+
			"2026-10-13T11:00:10.000Z INFO status tor: closed 1 circuit(s) to 7590b945\n"+
			"2026-10-13T11:00:11.000Z INFO status attach (123 KB)\n"+
			"2026-10-13T11:00:12.000Z INFO status a shape nobody coded for\n")

	key := testKey(t)
	var out bytes.Buffer
	if _, err := redactDir(dir, key, "", &out); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(got), "\n"), "\n")
	if len(lines) != 8 {
		t.Fatalf("want 8 lines, got %d:\n%s", len(lines), got)
	}
	tag := nameTag(key, "Bob")

	for i, want := range []string{
		"incoming call from " + tag,
		"call with " + tag,
		"call from " + tag + " ended",
		"missed call from " + tag,
		"notify (" + tag + "): <text len=3>",
		"status tor: closed 1 circuit(s) to 7590b945",
		"status attach (123 KB)",
	} {
		if !strings.Contains(lines[i], want) {
			t.Errorf("line %d: want it to contain %q, got %q", i, want, lines[i])
		}
	}
	if strings.Contains(string(got), "Bob") {
		t.Errorf("the name Bob is still in the file:\n%s", got)
	}

	unknown := lines[7]
	if !strings.Contains(unknown, "<text len=24>") {
		t.Errorf("want the unknown status line blanked with its length, got %q", unknown)
	}
}

func TestRedactMultiWordAndUnicodeNames(t *testing.T) {
	dir := t.TempDir()
	path := writeLog(t, dir, "alice", "haoma-gui.log",
		"2026-10-13T11:00:00.000Z INFO status connected — call with Bob Smith\n"+
			"2026-10-13T11:00:01.000Z INFO status connected — call with Zoë Ann\n"+
			"2026-10-13T11:00:02.000Z INFO status connected — call with Bob 😀\n")

	key := testKey(t)
	var out bytes.Buffer
	ok, err := redactDir(dir, key, "", &out)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Errorf("want a clean self-check, got:\n%s", out.String())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	for _, name := range []string{"Bob Smith", "Zoë Ann", "Bob 😀"} {
		if strings.Contains(s, name) {
			t.Errorf("the name %q is still in the file:\n%s", name, s)
		}
		want := nameTag(key, name)
		if !strings.Contains(s, want) {
			t.Errorf("want the tag for %q, got:\n%s", name, s)
		}
	}
}

func TestRedactNameShapesWithNoTrailingParens(t *testing.T) {
	dir := t.TempDir()
	path := writeLog(t, dir, "alice", "haoma-gui.log",
		"2026-10-13T11:41:53.282Z INFO status call from Bob ended\n"+
			"2026-10-13T11:00:00.000Z INFO status calling Bob\n")

	key := testKey(t)
	var out bytes.Buffer
	if _, err := redactDir(dir, key, "", &out); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if strings.Contains(s, "Bob") {
		t.Errorf("the name Bob is still in the file:\n%s", s)
	}
	tag := nameTag(key, "Bob")
	for _, want := range []string{"call from " + tag + " ended", "calling " + tag} {
		if !strings.Contains(s, want) {
			t.Errorf("want %q, got:\n%s", want, s)
		}
	}
	if strings.Contains(s, "<text len=") {
		t.Errorf("want neither line blanked as unknown, got:\n%s", s)
	}
}

func TestRedactAttachKeptAsIs(t *testing.T) {
	dir := t.TempDir()
	content := "2026-10-13T11:00:00.000Z INFO status attach (123 KB)\n"
	path := writeLog(t, dir, "alice", "haoma-gui.log", content)

	key := testKey(t)
	var out bytes.Buffer
	if _, err := redactDir(dir, key, "", &out); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Errorf("want the exact safe attach shape kept as is, got:\n%s", got)
	}
}

func TestRedactAttachWithLabelTagged(t *testing.T) {
	dir := t.TempDir()
	path := writeLog(t, dir, "alice", "haoma-gui.log",
		"2026-10-13T11:00:00.000Z INFO status attach (0.5 MB) label=Bob\n")

	key := testKey(t)
	var out bytes.Buffer
	if _, err := redactDir(dir, key, "", &out); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "attach (0.5 MB) label=" + nameTag(key, "Bob")
	if !strings.Contains(string(got), want) {
		t.Errorf("want %q, got:\n%s", want, got)
	}
	if strings.Contains(string(got), "Bob") {
		t.Errorf("the name Bob is still in the file:\n%s", got)
	}
}

func TestRedactAttachUnknownShapeBlanked(t *testing.T) {
	dir := t.TempDir()
	path := writeLog(t, dir, "alice", "haoma-gui.log",
		"2026-10-13T11:00:00.000Z INFO status attach (ready)\n")

	key := testKey(t)
	var out bytes.Buffer
	if _, err := redactDir(dir, key, "", &out); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "<text len=") {
		t.Errorf("want a shape other than (<number> <unit>) blanked, got:\n%s", got)
	}
}

func TestRedactHFormResolvedAcrossFiles(t *testing.T) {
	dir := t.TempDir()

	daemon := writeLog(t, dir, "alice", "haoma.log",
		"time=2026-10-13T12:00:00Z level=INFO msg=\"peer\" nick=h:b2dd7d8a\n")
	gui := writeLog(t, dir, "bob", "haoma-gui.log",
		"2026-10-13T12:00:01.000Z INFO status connected — call with Carol\n")

	key := testKey(t)
	var out bytes.Buffer
	if _, err := redactDir(dir, key, "", &out); err != nil {
		t.Fatal(err)
	}
	wantTag := nameTag(key, "Carol")

	d, err := os.ReadFile(daemon)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(d), "nick="+wantTag) {
		t.Errorf("want the h: hash resolved to Carol's tag, got:\n%s", d)
	}

	g, err := os.ReadFile(gui)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(g), "call with "+wantTag) {
		t.Errorf("want the name Carol replaced by its own tag, got:\n%s", g)
	}
}

func TestRedactHFormFallbackWithNoMatchingName(t *testing.T) {
	dir := t.TempDir()
	path := writeLog(t, dir, "alice", "haoma.log",
		"time=2026-10-13T12:00:00Z level=INFO msg=\"peer\" nick=h:deadbeef\n")

	key := testKey(t)
	var out bytes.Buffer
	if _, err := redactDir(dir, key, "", &out); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "h:deadbeef") {
		t.Errorf("want the h: form replaced even with no matching name, got:\n%s", got)
	}
	if !strings.Contains(string(got), "nick:") {
		t.Errorf("want a tag in its place, got:\n%s", got)
	}
}

func TestRedactIdempotent(t *testing.T) {
	dir := t.TempDir()
	onion := strings.Repeat("b", 56)
	path := writeLog(t, dir, "alice", "haomad.log",
		"time=2026-10-13T11:00:00Z level=INFO msg=\"x\" from="+onion+".onion\n")
	gui := writeLog(t, dir, "alice", "haoma-gui.log",
		"2026-10-13T11:00:00.000Z INFO status connected — call with Dana\n"+
			"2026-10-13T11:00:01.000Z INFO status a shape nobody coded for\n")

	key := testKey(t)
	var out1 bytes.Buffer
	if _, err := redactDir(dir, key, "", &out1); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeGUI, err := os.ReadFile(gui)
	if err != nil {
		t.Fatal(err)
	}

	var out2 bytes.Buffer
	if _, err := redactDir(dir, key, "", &out2); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	afterGUI, err := os.ReadFile(gui)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(before, after) {
		t.Errorf("a second run changed the daemon log:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if !bytes.Equal(beforeGUI, afterGUI) {
		t.Errorf("a second run changed the gui log:\nbefore:\n%s\nafter:\n%s", beforeGUI, afterGUI)
	}

	for _, line := range strings.Split(strings.TrimRight(out2.String(), "\n"), "\n") {
		if !strings.Contains(line, "replaced 0 onion, 0 hex, 0 token, 0 name, 0 status") {
			t.Errorf("want a second run to report nothing replaced, got %q", line)
		}
	}
}

func TestRedactIPv4ReplacedKeepsLoopbackAndPort(t *testing.T) {
	dir := t.TempDir()
	path := writeLog(t, dir, "alice", "haoma-gui.log",
		"2026-10-13T11:00:00.000Z INFO call peer at 192.168.1.42:443 and 127.0.0.1\n")

	key := testKey(t)
	var out bytes.Buffer
	ok, err := redactDir(dir, key, "", &out)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Errorf("want a clean self-check once the IP is replaced, got:\n%s", out.String())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if strings.Contains(s, "192.168.1.42") {
		t.Errorf("want the non-loopback IP replaced, got:\n%s", s)
	}
	if !strings.Contains(s, "<ip>:443") {
		t.Errorf("want the port kept after the replaced IP, got:\n%s", s)
	}
	if !strings.Contains(s, "127.0.0.1") {
		t.Errorf("want the loopback address kept, got:\n%s", s)
	}
}

func TestRedactIPv6ReplacedKeepsLoopback(t *testing.T) {
	dir := t.TempDir()
	path := writeLog(t, dir, "alice", "haoma-gui.log",
		"2026-10-13T11:00:00.000Z INFO call peer at 2001:db8::1 and ::1\n")

	key := testKey(t)
	var out bytes.Buffer
	ok, err := redactDir(dir, key, "", &out)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Errorf("want a clean self-check once the IP is replaced, got:\n%s", out.String())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if strings.Contains(s, "2001:db8::1") {
		t.Errorf("want the non-loopback IPv6 address replaced, got:\n%s", s)
	}
	if !strings.Contains(s, "<ip>") {
		t.Errorf("want the replaced address to read <ip>, got:\n%s", s)
	}
	if !strings.Contains(s, "::1") {
		t.Errorf("want the loopback address kept, got:\n%s", s)
	}
}

func TestRedactIPv4MappedHexFormReplaced(t *testing.T) {
	dir := t.TempDir()
	path := writeLog(t, dir, "alice", "haoma-gui.log",
		"2026-10-13T11:00:00.000Z INFO call peer at ::ffff:c0a8:102\n")

	key := testKey(t)
	var out bytes.Buffer
	ok, err := redactDir(dir, key, "", &out)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Errorf("want a clean self-check once the mapped address is replaced, got:\n%s", out.String())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "c0a8:102") {
		t.Errorf("want the mapped address replaced, got:\n%s", got)
	}
	if !strings.Contains(string(got), "<ip>") {
		t.Errorf("want <ip> in its place, got:\n%s", got)
	}
}

func TestRedactIPv4MappedDottedFormReplaced(t *testing.T) {
	dir := t.TempDir()
	path := writeLog(t, dir, "alice", "haoma-gui.log",
		"2026-10-13T11:00:00.000Z INFO call peer at ::ffff:192.168.1.2\n")

	key := testKey(t)
	var out bytes.Buffer
	ok, err := redactDir(dir, key, "", &out)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Errorf("want a clean self-check once the mapped address is replaced, got:\n%s", out.String())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "192.168.1.2") {
		t.Errorf("want the mapped address's IPv4 part replaced, got:\n%s", got)
	}
}

func TestRedactIPv4MappedLoopbackKept(t *testing.T) {
	dir := t.TempDir()
	content := "2026-10-13T11:00:00.000Z INFO call peer at ::ffff:7f00:1 and ::ffff:127.0.0.1\n"
	path := writeLog(t, dir, "alice", "haoma-gui.log", content)

	key := testKey(t)
	var out bytes.Buffer
	if _, err := redactDir(dir, key, "", &out); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Errorf("want both mapped-loopback forms kept as is, got:\n%s", got)
	}
}

func TestRedactKeepsTorVersionString(t *testing.T) {
	dir := t.TempDir()
	path := writeLog(t, dir, "alice", "haoma.log",
		"time=2026-10-13T11:00:00Z level=INFO msg=\"tor: running v0.4.9.13\"\n")

	key := testKey(t)
	var out bytes.Buffer
	if _, err := redactDir(dir, key, "", &out); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "v0.4.9.13") {
		t.Errorf("want the tor version string kept as is, got:\n%s", got)
	}
}

func TestRedactSlogMsgWithWordStatusStaysUnchanged(t *testing.T) {
	dir := t.TempDir()
	content := "time=2026-10-13T11:00:00Z level=INFO msg=\"status changed for Bob\"\n"
	path := writeLog(t, dir, "alice", "haoma.log", content)

	key := testKey(t)
	var out bytes.Buffer
	if _, err := redactDir(dir, key, "", &out); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Errorf("want a slog line untouched by the status rule, got:\n%s", got)
	}
}

func TestRedactKeepsCodeIdentifiersAndLongNumbers(t *testing.T) {
	dir := t.TempDir()
	content := "time=2026-10-13T11:00:00Z level=INFO msg=\"MessengerStore ACTION_SCREEN_OFF updateCameraGranted BUILTIN_EARPIECE\" local_ns=8782351043227380 sender_pts_ns=9812345678901234\n"
	path := writeLog(t, dir, "alice", "haoma.log", content)

	key := testKey(t)
	var out bytes.Buffer
	if _, err := redactDir(dir, key, "", &out); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Errorf("want the identifiers and the long decimal numbers kept as is, got:\n%s", got)
	}
}

func TestRedactShortensMixedToken(t *testing.T) {
	dir := t.TempDir()
	path := writeLog(t, dir, "alice", "haoma.log",
		"time=2026-10-13T11:00:00Z level=INFO msg=\"upload\" session=ab3xQ29fKzZpLw\n")

	key := testKey(t)
	var out bytes.Buffer
	if _, err := redactDir(dir, key, "", &out); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "ab3xQ29fKzZpLw") {
		t.Errorf("want the mixed digit-and-letter token shortened, got:\n%s", got)
	}
	if !strings.Contains(string(got), "ab3xQ29f…") {
		t.Errorf("want the shortened prefix kept, got:\n%s", got)
	}
}

func TestRedactShortensDigitlessTokenKeyValue(t *testing.T) {
	dir := t.TempDir()
	path := writeLog(t, dir, "alice", "haoma.log",
		"time=2026-10-13T11:00:00Z level=INFO msg=\"auth\" auth_token=verylongwordwithnodigits\n")

	key := testKey(t)
	var out bytes.Buffer
	if _, err := redactDir(dir, key, "", &out); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "verylongwordwithnodigits") {
		t.Errorf("want the digitless value under auth_token= shortened anyway, got:\n%s", got)
	}
	if !strings.Contains(string(got), "auth_token=verylong…") {
		t.Errorf("want the shortened value kept under auth_token=, got:\n%s", got)
	}
}

func TestRedactShortensURLPathSegments(t *testing.T) {
	dir := t.TempDir()
	path := writeLog(t, dir, "alice", "haomad.log",
		"time=2026-10-13T11:00:00Z level=INFO msg=\"fetch\" url=https://example.onion/audio/sessionIdABCDEFGH123\n")

	key := testKey(t)
	var out bytes.Buffer
	if _, err := redactDir(dir, key, "", &out); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "sessionIdABCDEFGH123") {
		t.Errorf("want the long URL path segment shortened, got:\n%s", got)
	}
}

func TestSelfCheckHexThresholdMatchesRewrite(t *testing.T) {

	lo := scanLeftovers("peer=" + strings.Repeat("a", 20))
	if lo.hex != 1 {
		t.Errorf("want the self-check to flag a 20-char hex run as leftover, got %+v", lo)
	}
}

func TestRedactFailsClosedOnUnknownFile(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, "alice", "haoma.log", "time=2026-10-13T11:00:00Z level=INFO msg=\"x\"\n")
	writeLog(t, dir, "alice", "notes.md", "a plain note, not a log this tool knows\n")

	key := testKey(t)
	var out bytes.Buffer
	ok, err := redactDir(dir, key, "", &out)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Errorf("want an unknown file shape to fail the self-check")
	}
	if !strings.Contains(out.String(), "not redacted: notes.md") {
		t.Errorf("want the report to name the unredacted file, got:\n%s", out.String())
	}
}

func TestRedactSkipsEmptyUnknownFiles(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, "alice", "haoma.log", "time=2026-10-13T11:00:00Z level=INFO msg=\"x\"\n")
	writeLog(t, dir, "alice", ".bashrc", "")
	writeLog(t, dir, "alice", ".mcp.json", "")

	var out bytes.Buffer
	ok, err := redactDir(dir, testKey(t), "", &out)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Errorf("want empty files to be skipped, got a failed run:\n%s", out.String())
	}
	if strings.Contains(out.String(), "not redacted") {
		t.Errorf("want no empty file named as not redacted, got:\n%s", out.String())
	}
}

func TestRedactSkipsOwnKeyFileInsideDir(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, "alice", "haoma.log", "time=2026-10-13T11:00:00Z level=INFO msg=\"x\"\n")
	keyPath := filepath.Join(dir, "redact.key")
	key, err := loadOrMakeKey(keyPath)
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	ok, err := redactDir(dir, key, keyPath, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Errorf("want the key file inside the directory to be skipped, not flagged, got:\n%s", out.String())
	}
	if strings.Contains(out.String(), "redact.key") {
		t.Errorf("want no mention of the key file in the report, got:\n%s", out.String())
	}
}

func TestRedactMainCreatesKeyFile(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "sub", "redact.key")
	dir := t.TempDir()
	writeLog(t, dir, "alice", "haoma.log", "time=2026-10-13T11:00:00Z level=INFO msg=\"x\"\n")

	code := redactMain([]string{"-key", keyPath, dir})
	if code != 0 {
		t.Fatalf("want exit 0, got %d", code)
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("want the key file at mode 0600, got %o", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(keyPath))
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Errorf("want the key directory at mode 0700, got %o", dirInfo.Mode().Perm())
	}
	b, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != redactKeySize {
		t.Errorf("want a %d-byte key, got %d", redactKeySize, len(b))
	}
}

func TestRedactTagFlagMatchesWhatRedactWrites(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "redact.key")
	dir := t.TempDir()
	path := writeLog(t, dir, "alice", "haoma-gui.log",
		"2026-10-13T11:00:00.000Z INFO status connected — call with Eve\n")

	if code := redactMain([]string{"-key", keyPath, dir}); code != 0 {
		t.Fatalf("want exit 0, got %d", code)
	}
	key, err := loadOrMakeKey(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := nameTag(key, "Eve")
	if !strings.Contains(string(got), want) {
		t.Errorf("want the file to carry the tag %s, got:\n%s", want, got)
	}
}
