package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"haoma-frontend/internal/supervisor"
)

var killProcess = syscall.Kill

type sweepResult struct {
	skipped string
	ended   []procEntry
	refused []procEntry
	changed []procEntry
	stuck   []procEntry
}

type procEntry struct {
	pid  int
	ppid int
	comm string
}

func processArgs(pid int) (args []string, ok bool) {
	argv, ok := processArgv(pid)
	if !ok || len(argv) == 0 {
		return nil, false
	}
	return argv[1:], true
}

func processArgv(pid int) ([]string, bool) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil || len(raw) == 0 {
		return nil, false
	}
	var argv []string
	for _, p := range bytes.Split(bytes.TrimRight(raw, "\x00"), []byte{0}) {
		argv = append(argv, string(p))
	}
	return argv, true
}

func parseProcStat(raw []byte) (comm string, state byte, ppid int, ok bool) {
	open := bytes.IndexByte(raw, '(')
	end := bytes.LastIndexByte(raw, ')')
	if open < 0 || end < open {
		return "", 0, 0, false
	}
	comm = string(raw[open+1 : end])

	fields := bytes.Fields(raw[end+1:])
	if len(fields) < 2 || len(fields[0]) != 1 {
		return "", 0, 0, false
	}
	ppid, err := strconv.Atoi(string(fields[1]))
	if err != nil {
		return "", 0, 0, false
	}
	return comm, fields[0][0], ppid, true
}

func liveProc(pid int) (procEntry, bool) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return procEntry{}, false
	}
	comm, state, ppid, ok := parseProcStat(raw)
	if !ok || state == 'Z' || state == 'X' {
		return procEntry{}, false
	}
	return procEntry{pid: pid, ppid: ppid, comm: comm}, true
}

func uidsAre(status []byte, uid int) bool {
	sc := bufio.NewScanner(bytes.NewReader(status))
	for sc.Scan() {
		rest, found := strings.CutPrefix(sc.Text(), "Uid:")
		if !found {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 2 {
			return false
		}
		want := strconv.Itoa(uid)
		return f[0] == want && f[1] == want
	}
	return false
}

func isTorArgv(argv []string, torrc string) bool {
	return len(argv) == 4 && argv[1] == "-f" && argv[2] == torrc && argv[3] == "--quiet"
}

type fileID struct {
	major, minor uint64
	ino          uint64
}

func fileIDOf(path string) (fileID, error) {
	st, err := os.Stat(path)
	if err != nil {
		return fileID{}, err
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return fileID{}, errors.New("no device and inode")
	}
	dev := uint64(sys.Dev)
	return fileID{
		major: ((dev >> 8) & 0xfff) | ((dev >> 32) &^ 0xfff),
		minor: (dev & 0xff) | ((dev >> 12) &^ 0xff),
		ino:   sys.Ino,
	}, nil
}

func lockHolders(locks []byte, id fileID) map[int]bool {
	out := map[int]bool{}
	sc := bufio.NewScanner(bytes.NewReader(locks))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) > 1 && f[1] == "->" {
			continue
		}

		if len(f) < 6 {
			continue
		}
		pid, err := strconv.Atoi(f[4])
		if err != nil {
			continue
		}
		parts := strings.Split(f[5], ":")
		if len(parts) != 3 {
			continue
		}
		maj, err1 := strconv.ParseUint(parts[0], 16, 64)
		mnr, err2 := strconv.ParseUint(parts[1], 16, 64)
		ino, err3 := strconv.ParseUint(parts[2], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		if (fileID{maj, mnr, ino}) == id {
			out[pid] = true
		}
	}
	return out
}

func ownedByManagingParent(ppid int) bool {
	for i := 0; i < 16 && ppid > 1; i++ {
		p, live := liveProc(ppid)
		if !live {
			return false
		}
		if args, ok := processArgs(ppid); ok && !torSetupFromArgs(args).system {
			return true
		}
		ppid = p.ppid
	}
	return false
}

type torSweep struct {
	torrc string
	lock  string
	uid   int
}

func (s torSweep) match(pid int) (procEntry, bool) {
	p, live := liveProc(pid)
	if !live {
		return procEntry{}, false
	}
	status, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil || !uidsAre(status, s.uid) {
		return procEntry{}, false
	}
	argv, ok := processArgv(pid)
	if !ok || !isTorArgv(argv, s.torrc) {
		return procEntry{}, false
	}
	id, err := fileIDOf(s.lock)
	if err != nil {
		return procEntry{}, false
	}
	locks, err := os.ReadFile("/proc/locks")
	if err != nil || !lockHolders(locks, id)[pid] {
		return procEntry{}, false
	}
	if ownedByManagingParent(p.ppid) {
		return procEntry{}, false
	}
	return p, true
}

func (s torSweep) candidates() []procEntry {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []procEntry
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		if p, ok := s.match(pid); ok {
			out = append(out, p)
		}
	}
	return out
}

func runningHaomad(runtimePath string) (int, bool) {
	info, err := supervisor.ReadRuntimeFile(runtimePath)
	if err != nil {
		return 0, false
	}
	_, live := liveProc(info.PID)
	return info.PID, live
}

func endLeftoverTors(runtimePath, torrc string, grace time.Duration) sweepResult {
	if pid, live := runningHaomad(runtimePath); live {
		return sweepResult{skipped: fmt.Sprintf("haomad pid %d from the runtime file is running", pid)}
	}
	s := torSweep{torrc: torrc, lock: filepath.Join(filepath.Dir(torrc), "lock"), uid: os.Getuid()}
	var res sweepResult
	targets := s.signal(s.candidates(), syscall.SIGTERM, &res)
	if len(targets) == 0 || waitNoneLive(targets, grace) {
		res.ended = targets
		return res
	}

	var live []procEntry
	for _, p := range targets {
		if _, ok := liveProc(p.pid); ok {
			live = append(live, p)
		} else {
			res.ended = append(res.ended, p)
		}
	}
	killed := s.signal(live, syscall.SIGKILL, &res)
	waitNoneLive(killed, grace)
	for _, p := range killed {
		if _, ok := liveProc(p.pid); ok {
			res.stuck = append(res.stuck, p)
		} else {
			res.ended = append(res.ended, p)
		}
	}
	return res
}

func (s torSweep) signal(ps []procEntry, sig syscall.Signal, res *sweepResult) []procEntry {
	var sent []procEntry
	for _, p := range ps {
		if _, ok := s.match(p.pid); !ok {
			if _, live := liveProc(p.pid); live {
				res.changed = append(res.changed, p)
			}
			continue
		}
		err := killProcess(p.pid, sig)
		switch {
		case err == nil:
			sent = append(sent, p)
		case errors.Is(err, syscall.EPERM):
			res.refused = append(res.refused, p)
		}
	}
	return sent
}

func waitNoneLive(ps []procEntry, grace time.Duration) bool {
	deadline := time.Now().Add(grace)
	for {
		live := false
		for _, p := range ps {
			if _, ok := liveProc(p.pid); ok {
				live = true
				break
			}
		}
		if !live {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func describeProcs(ps []procEntry) string {
	var b bytes.Buffer
	for i, p := range ps {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s (pid %d)", p.comm, p.pid)
	}
	return b.String()
}
