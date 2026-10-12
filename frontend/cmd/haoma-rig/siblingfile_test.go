package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const haomaSiblingSource = "../haoma/files_sibling.go"

const haomaFaultsSource = "../haoma/seeded_faults.go"

func TestSeededFaultNameMatchesHaoma(t *testing.T) {
	got := stringConst(t, haomaFaultsSource, "ignoreSiblingOfferEnv")
	if got != seedIgnoreSiblingOfferEnv {
		t.Fatalf("haoma reads %q, the rig sets %q", got, seedIgnoreSiblingOfferEnv)
	}
}

func TestSiblingSealedLineIsWhatHaomaLogs(t *testing.T) {
	src, err := os.ReadFile(haomaSiblingSource)
	if err != nil {
		t.Fatalf("cannot read %s: %v — haoma's sibling flow moved; point this test at it", haomaSiblingSource, err)
	}
	if !strings.Contains(string(src), strconv.Quote(siblingSealedLine)) {
		t.Fatalf("haoma no longer logs %q in %s", siblingSealedLine, haomaSiblingSource)
	}
}

func stringConst(t *testing.T, path, name string) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("cannot parse %s: %v", path, err)
	}
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
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
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok {
					t.Fatalf("%s in %s is not a string literal", name, path)
				}
				v, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatal(err)
				}
				return v
			}
		}
	}
	t.Fatalf("no const %s in %s", name, path)
	return ""
}

func writeReceiverLog(t *testing.T, name string, lines ...string) *rig {
	t.Helper()
	r := &rig{name: name, root: t.TempDir()}
	if err := os.WriteFile(filepath.Join(r.root, "haoma.log"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return r
}

func logAt(at, msg, msgID string, fields ...string) string {
	line := "time=" + at + " level=DEBUG msg=\"" + msg + "\" " + logMatchID("msg_id", msgID)
	for _, f := range fields {
		line += " " + f
	}
	return line
}

func TestAssertKeyWithDownload_ReadsTheOrderFromTheLog(t *testing.T) {
	const id = "4f1e9d2c7b6a5f4e3d2c1b0a99887766"
	const other = "11111111aaaaaaaabbbbbbbbcccccccc"
	const (
		early    = "receipt_msg_id=eeeeeeee…"
		postPull = "receipt_msg_id=pppppppp…"
		key1     = "key_msg_id=k1k1k1k1…"
		key2     = "key_msg_id=k2k2k2k2…"
	)
	answered := func(key, receipt string) string {
		return logAt("2026-09-25T10:00:02.000Z", keyShippedLine, id, key, receipt)
	}
	cases := map[string]struct {
		receiver, sender []string
		ok               bool
	}{
		"key before the bytes, answering the early receipt": {[]string{
			logAt("2026-09-25T10:00:00.000Z", earlyReceiptLine, id, early),
			logAt("2026-09-25T10:00:03.000Z", keyBeforeLine, id, key1),
			logAt("2026-09-25T10:00:05.000Z", fetchReadyLine, id),
			logAt("2026-09-25T10:00:05.100Z", "file receipt: post-pull receipt skipped", id),
		}, []string{answered(key1, early)}, true},
		"key soon after the bytes, answering the early receipt": {[]string{
			logAt("2026-09-25T10:00:00.000Z", earlyReceiptLine, id, early),
			logAt("2026-09-25T10:00:05.000Z", fetchReadyLine, id),
			logAt("2026-09-25T10:00:05.100Z", postPullReceiptLine, id, postPull),
			logAt("2026-09-25T10:00:06.000Z", keyAfterLine, id, key1),
		}, []string{answered(key1, early)}, true},
		"the sealing key answered the receipt after the download": {[]string{
			logAt("2026-09-25T10:00:00.000Z", earlyReceiptLine, id, early),
			logAt("2026-09-25T10:00:05.000Z", fetchReadyLine, id),
			logAt("2026-09-25T10:00:05.100Z", postPullReceiptLine, id, postPull),
			logAt("2026-09-25T10:00:07.000Z", keyAfterLine, id, key2),
		}, []string{answered(key2, postPull)}, false},
		"the sender's log does not name the key": {[]string{
			logAt("2026-09-25T10:00:00.000Z", earlyReceiptLine, id, early),
			logAt("2026-09-25T10:00:05.000Z", fetchReadyLine, id),
			logAt("2026-09-25T10:00:06.000Z", keyAfterLine, id, key1),
		}, []string{answered(key2, early)}, false},
		"no early receipt": {[]string{
			logAt("2026-09-25T10:00:05.000Z", fetchReadyLine, id),
			logAt("2026-09-25T10:00:05.100Z", postPullReceiptLine, id, postPull),
			logAt("2026-09-25T10:00:09.000Z", keyAfterLine, id, key1),
		}, []string{answered(key1, postPull)}, false},
		"receipt only after the download": {[]string{
			logAt("2026-09-25T10:00:05.000Z", fetchReadyLine, id),
			logAt("2026-09-25T10:00:06.000Z", earlyReceiptLine, id, early),
			logAt("2026-09-25T10:00:09.000Z", keyAfterLine, id, key1),
		}, []string{answered(key1, early)}, false},
		"key long after the bytes": {[]string{
			logAt("2026-09-25T10:00:00.000Z", earlyReceiptLine, id, early),
			logAt("2026-09-25T10:00:05.000Z", fetchReadyLine, id),
			logAt("2026-09-25T10:01:05.000Z", keyAfterLine, id, key1),
		}, []string{answered(key1, early)}, false},
		"another file's receipt does not count": {[]string{
			logAt("2026-09-25T10:00:00.000Z", earlyReceiptLine, other, early),
			logAt("2026-09-25T10:00:05.000Z", fetchReadyLine, id),
			logAt("2026-09-25T10:00:06.000Z", keyAfterLine, id, key1),
		}, []string{answered(key1, early)}, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			to := writeReceiverLog(t, "receiver", c.receiver...)
			from := writeReceiverLog(t, "sender", c.sender...)
			err := assertKeyWithDownload(from, to, id)
			if c.ok && err != nil {
				t.Fatalf("want pass, got %v", err)
			}
			if !c.ok && err == nil {
				t.Fatal("want a failure, got a pass")
			}
		})
	}
}

func TestKeyWithDownloadLinesAreWhatHaomaLogs(t *testing.T) {
	sources := []string{"../haoma/files_fetch.go", "../haoma/files_handshake.go"}
	var all string
	for _, p := range sources {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("cannot read %s: %v", p, err)
		}
		all += string(b)
	}
	for _, line := range []string{earlyReceiptLine, postPullReceiptLine, fetchReadyLine, keyBeforeLine, keyAfterLine, keyShippedLine} {
		if !strings.Contains(all, strconv.Quote(line)) {
			t.Errorf("haoma no longer logs %q", line)
		}
	}
	if got := stringConst(t, haomaFaultsSource, "skipEarlyReceiptEnv"); got != seedSkipEarlyReceiptEnv {
		t.Errorf("haoma reads %q, the rig sets %q", got, seedSkipEarlyReceiptEnv)
	}
	if got := stringConst(t, haomaFaultsSource, "ignoreEarlyReceiptEnv"); got != seedIgnoreEarlyReceiptEnv {
		t.Errorf("haoma reads %q, the rig sets %q", got, seedIgnoreEarlyReceiptEnv)
	}
}

func TestFileCasePreconditionFailsTheControlToo(t *testing.T) {
	at := func(level string) (devA, devB, peer *rig) {

		return &rig{name: "dev-a", logLevel: level, root: t.TempDir()}, &rig{name: "dev-b", logLevel: level, root: t.TempDir()}, &rig{name: "peer", logLevel: level, root: t.TempDir()}
	}
	for _, tc := range []struct {
		name string
		run  func() (controlErr, siblingErr error)
	}{
		{"file fan-out, log level", func() (error, error) {
			a, b, p := at("info")
			return assertSiblingFileMirror(a, b, p, "ap", "pa", "bp")
		}},
		{"file recovery, log level", func() (error, error) {
			a, b, p := at("info")
			return assertSiblingFileRecovery(a, b, p, "ap", "pa", "pb", "bp")
		}},
		{"sent file by gap-fill, log level", func() (error, error) {
			a, b, p := at("info")
			return assertSentFileOnSiblingByGapFill(a, b, p, "ap", "bp")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ctrl, sib error
			captureRun(t, func() { ctrl, sib = tc.run() })
			for label, err := range map[string]error{"control": ctrl, "sibling": sib} {
				if err == nil || !strings.HasPrefix(err.Error(), "not asserted: ") {
					t.Errorf("%s verdict = %v, want a \"not asserted: …\" failure", label, err)
				}
			}
		})
	}
}
