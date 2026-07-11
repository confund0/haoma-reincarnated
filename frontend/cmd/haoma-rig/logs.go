package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var denylist = []string{
	"no valid session",
	"no session record",
	"old counter",
	"panic",
	"decrypt failed",
	"corrupt",
}

type logScan struct {
	errors      int
	warns       int
	errSamples  []string
	denySamples []string
	warnLines   []string
}

func (s logScan) fatal() bool { return s.errors > 0 || len(s.denySamples) > 0 }

func scanRigLogs(r *rig) logScan {
	var s logScan
	for _, name := range []string{"haomad.log", "haoma.log", "haomad.stderr", "haoma.stderr"} {
		f, err := os.Open(filepath.Join(r.root, name))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			tagged := name + ": " + trimTime(line)
			switch {
			case strings.Contains(line, "level=ERROR"):
				s.errors++
				if len(s.errSamples) < 8 {
					s.errSamples = append(s.errSamples, tagged)
				}
			case strings.Contains(line, "level=WARN"):
				s.warns++
				if len(s.warnLines) < 5 {
					s.warnLines = append(s.warnLines, tagged)
				}
			}
			low := strings.ToLower(line)
			for _, sig := range denylist {
				if strings.Contains(low, sig) {
					if len(s.denySamples) < 8 {
						s.denySamples = append(s.denySamples, "["+sig+"] "+tagged)
					}
					break
				}
			}
		}
		f.Close()
	}
	return s
}

func reviewLogs(base, stamp string, rigs []*rig) (checks []check, trace string) {
	for _, r := range rigs {
		s := scanRigLogs(r)
		c := check{name: "logs:" + r.name, ok: !s.fatal()}
		switch {
		case s.fatal():
			c.err = fmt.Sprintf("%d ERROR, %d denylist hit — %s", s.errors, len(s.denySamples), firstSample(s))
		case s.warns > 0:
			c.err = fmt.Sprintf("%d WARN (non-fatal)", s.warns)
		}
		checks = append(checks, c)
		for _, w := range s.warnLines {
			logf("[%s] WARN: %s", r.name, w)
		}
		for _, e := range s.errSamples {
			logf("[%s] ERROR: %s", r.name, e)
		}
		for _, d := range s.denySamples {
			logf("[%s] DENYLIST: %s", r.name, d)
		}
	}

	trace = filepath.Join(base, "trace-"+stamp+".log")
	if err := writeMergedTrace(rigs, trace); err != nil {
		logf("merged trace write failed: %v", err)
		trace = ""
	}
	return checks, trace
}

func writeMergedTrace(rigs []*rig, path string) error {
	type entry struct{ ts, text string }
	var entries []entry
	for _, r := range rigs {
		for _, name := range []string{"haomad.log", "haoma.log"} {
			f, err := os.Open(filepath.Join(r.root, name))
			if err != nil {
				continue
			}
			src := strings.TrimSuffix(name, ".log")
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for sc.Scan() {
				t := sc.Text()
				entries = append(entries, entry{
					ts:   timeToken(t),
					text: fmt.Sprintf("%-12s %s", r.name+"/"+src, t),
				})
			}
			f.Close()
		}
	}

	sort.SliceStable(entries, func(i, j int) bool { return entries[i].ts < entries[j].ts })
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.text)
		b.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

func preserveLogs(base, stamp string, rigs []*rig) string {
	dst := filepath.Join(base, "fail-"+stamp)
	for _, r := range rigs {
		rd := filepath.Join(dst, r.name)
		if err := os.MkdirAll(rd, 0o700); err != nil {
			continue
		}
		for _, name := range []string{"haomad.log", "haoma.log", "haomad.stderr", "haoma.stderr"} {
			copyFile(filepath.Join(r.root, name), filepath.Join(rd, name))
		}
	}
	return dst
}

func copyFile(src, dst string) {
	in, err := os.Open(src)
	if err != nil {
		return
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return
	}
	defer out.Close()
	io.Copy(out, in)
}

func trimTime(line string) string {
	if strings.HasPrefix(line, "time=") {
		if sp := strings.IndexByte(line, ' '); sp > 0 {
			return line[sp+1:]
		}
	}
	return line
}

func timeToken(line string) string {
	if strings.HasPrefix(line, "time=") {
		if sp := strings.IndexByte(line, ' '); sp > 5 {
			return line[5:sp]
		}
	}
	return ""
}

func firstSample(s logScan) string {
	if len(s.denySamples) > 0 {
		return s.denySamples[0]
	}
	if len(s.errSamples) > 0 {
		return s.errSamples[0]
	}
	return ""
}
