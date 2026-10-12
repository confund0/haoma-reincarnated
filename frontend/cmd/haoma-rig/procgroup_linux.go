package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

func ownGroup(cmd *exec.Cmd, deathSignal bool) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if deathSignal {
		cmd.SysProcAttr.Pdeathsig = syscall.SIGTERM
	}
}

func signalOnRigDeath(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}

func sweepGroup(label string, cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil || cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		return
	}
	sweepGroupID(label, cmd.Process.Pid, 5*time.Second)
}

func sweepGroupID(label string, pgid int, grace time.Duration) {
	left := groupMembers(pgid)
	if len(left) == 0 {
		return
	}
	logf("[%s] still running after it exited: %s — SIGTERM", label, describeMembers(left))
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	if waitGroupEmpty(pgid, grace) {
		return
	}
	logf("[%s] its group did not exit on SIGTERM — killing: %s", label, describeMembers(groupMembers(pgid)))
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	if !waitGroupEmpty(pgid, grace) {
		logf("[%s] its group is still running after SIGKILL: %s", label, describeMembers(groupMembers(pgid)))
	}
}

func waitGroupEmpty(pgid int, grace time.Duration) bool {
	deadline := time.Now().Add(grace)
	for {
		if len(groupMembers(pgid)) == 0 {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

type groupMember struct {
	pid  int
	comm string
}

func groupMembers(pgid int) []groupMember {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []groupMember
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		raw, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		comm, state, pgrp, ok := parseProcStat(raw)
		if !ok || pgrp != pgid || state == 'Z' || state == 'X' {
			continue
		}
		out = append(out, groupMember{pid: pid, comm: comm})
	}
	return out
}

func parseProcStat(raw []byte) (comm string, state byte, pgrp int, ok bool) {
	open := bytes.IndexByte(raw, '(')
	end := bytes.LastIndexByte(raw, ')')
	if open < 0 || end < open {
		return "", 0, 0, false
	}
	comm = string(raw[open+1 : end])

	fields := bytes.Fields(raw[end+1:])
	if len(fields) < 3 || len(fields[0]) != 1 {
		return "", 0, 0, false
	}
	pgrp, err := strconv.Atoi(string(fields[2]))
	if err != nil {
		return "", 0, 0, false
	}
	return comm, fields[0][0], pgrp, true
}

func describeMembers(ms []groupMember) string {
	if len(ms) == 0 {
		return "nothing"
	}
	var b bytes.Buffer
	for i, m := range ms {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s (pid %d)", m.comm, m.pid)
	}
	return b.String()
}

func torsOnTorrc(torrc string) []groupMember {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []groupMember
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		raw, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err != nil || !cmdlineHasTorrc(raw, torrc) {
			continue
		}
		if !processLive(pid) {
			continue
		}
		comm, _, _, _ := readProcStat(pid)
		out = append(out, groupMember{pid: pid, comm: comm})
	}
	return out
}

func cmdlineHasTorrc(raw []byte, torrc string) bool {
	args := bytes.Split(bytes.TrimRight(raw, "\x00"), []byte{0})
	for i := 0; i+1 < len(args); i++ {
		if string(args[i]) == "-f" && string(args[i+1]) == torrc {
			return true
		}
	}
	return false
}

func endLeftoverTors(label, torrc string, grace time.Duration) error {
	left := torsOnTorrc(torrc)
	if len(left) == 0 {
		return nil
	}
	logf("[%s] a tor from an earlier run still holds %s: %s — SIGTERM", label, torrc, describeMembers(left))
	signalAll(left, syscall.SIGTERM)
	if waitNoneLive(left, grace) {
		return nil
	}
	logf("[%s] the leftover tor did not exit on SIGTERM — killing: %s", label, describeMembers(left))
	signalAll(left, syscall.SIGKILL)
	if waitNoneLive(left, grace) {
		return nil
	}
	return fmt.Errorf("a tor from an earlier run still holds %s after SIGKILL: %s", torrc, describeMembers(left))
}

func signalAll(ms []groupMember, sig syscall.Signal) {
	for _, m := range ms {
		_ = syscall.Kill(m.pid, sig)
	}
}

func waitNoneLive(ms []groupMember, grace time.Duration) bool {
	deadline := time.Now().Add(grace)
	for {
		live := false
		for _, m := range ms {
			if processLive(m.pid) {
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

func processLive(pid int) bool {
	_, state, _, ok := readProcStat(pid)
	return ok && state != 'Z' && state != 'X'
}

func readProcStat(pid int) (comm string, state byte, pgrp int, ok bool) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", 0, 0, false
	}
	return parseProcStat(raw)
}
