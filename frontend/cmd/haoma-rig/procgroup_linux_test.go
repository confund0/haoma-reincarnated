package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSweepGroupEndsWhatOutlivedTheLeader(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 60 & echo $!; exit 0")
	ownGroup(cmd, false)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	child, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	pgid := cmd.Process.Pid

	left := groupMembers(pgid)
	if len(left) != 1 || left[0].pid != child {
		t.Fatalf("before the sweep: %v, want the child %d alone", left, child)
	}

	sweepGroup("test/haomad", cmd)

	if left := groupMembers(pgid); len(left) != 0 {
		t.Fatalf("after the sweep the group still holds %s", describeMembers(left))
	}
}

func TestSweepGroupLeavesOtherGroups(t *testing.T) {
	other := exec.Command("sleep", "60")
	ownGroup(other, false)
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Process.Kill(); _ = other.Wait() }()

	done := exec.Command("true")
	ownGroup(done, false)
	if err := done.Run(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	sweepGroup("test/haomad", done)
	if time.Since(start) > time.Second {
		t.Fatal("an empty group took the sweep's grace")
	}
	if len(groupMembers(other.Process.Pid)) != 1 {
		t.Fatal("the sweep ended a process of another group")
	}
}

func TestParseProcStat(t *testing.T) {
	comm, state, pgrp, ok := parseProcStat([]byte("1234 (tor (x) y) S 1 4321 4321 0 -1 4194560"))
	if !ok || comm != "tor (x) y" || state != 'S' || pgrp != 4321 {
		t.Fatalf("got %q %c %d %v", comm, state, pgrp, ok)
	}
	if _, _, _, ok := parseProcStat([]byte("garbage")); ok {
		t.Fatal("parsed garbage")
	}
}

const deathHelperEnv = "HAOMA_RIG_TEST_DEATH_HELPER"

func startChildAndDie(mode string) {
	child := exec.Command("sleep", "60")
	switch mode {
	case "haomad":
		ownGroup(child, true)
	case "haomad-keep":
		ownGroup(child, false)
	case "haoma":

		signalOnRigDeath(child)
		child.SysProcAttr.Setpgid = true
	}
	if err := child.Start(); err != nil {
		os.Exit(3)
	}
	fmt.Println(child.Process.Pid)

	_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	select {}
}

func childOfDeadRig(t *testing.T, mode string) int {
	t.Helper()
	helper := exec.Command(os.Args[0], "-test.run", "^TestChildOfADeadRig$")
	helper.Env = append(os.Environ(), deathHelperEnv+"="+mode)
	out, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	child, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("helper said %q", line)
	}
	_ = helper.Wait()
	t.Cleanup(func() { _ = syscall.Kill(-child, syscall.SIGKILL) })
	return child
}

func TestChildOfADeadRig(t *testing.T) {
	if mode := os.Getenv(deathHelperEnv); mode != "" {
		startChildAndDie(mode)
	}
	for _, mode := range []string{"haomad", "haoma"} {
		child := childOfDeadRig(t, mode)
		if !waitGroupEmpty(child, 5*time.Second) {
			t.Errorf("%s: the child %d still runs after the rig that started it died", mode, child)
		}
	}
	child := childOfDeadRig(t, "haomad-keep")
	time.Sleep(500 * time.Millisecond)
	if !processLive(child) {
		t.Errorf("-keep: the child %d was stopped when the rig died", child)
	}
}

func fakeTorOn(t *testing.T, torrc string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sh", "-c", "sleep 60; :", "fake-tor", "-f", torrc)
	ownGroup(cmd, false)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() })
	deadline := time.Now().Add(2 * time.Second)
	for len(torsOnTorrc(torrc)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the fake tor never showed its torrc in /proc")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cmd
}

func TestPrepareTorDirEndsALeftoverTor(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "rig-tor", "dev-a")
	other := filepath.Join(base, "rig-tor", "dev-b")
	leftover := fakeTorOn(t, filepath.Join(dir, "torrc"))
	unrelated := fakeTorOn(t, filepath.Join(other, "torrc"))

	var buf bytes.Buffer
	prev := logOut
	logOut = &buf
	defer func() { logOut = prev }()

	if err := prepareTorDir("dev-a", dir, filepath.Join(base, "rig")); err != nil {
		t.Fatal(err)
	}
	if processLive(leftover.Process.Pid) {
		t.Fatalf("the leftover tor %d still runs", leftover.Process.Pid)
	}
	if !strings.Contains(buf.String(), "pid "+strconv.Itoa(leftover.Process.Pid)) {
		t.Fatalf("the log does not name the pid it ended:\n%s", buf.String())
	}
	if !processLive(unrelated.Process.Pid) {
		t.Fatal("a tor on another dir was ended")
	}
}

func TestCmdlineHasTorrc(t *testing.T) {
	raw := []byte("/usr/bin/tor\x00-f\x00/t/dev-a/torrc\x00--quiet\x00")
	if !cmdlineHasTorrc(raw, "/t/dev-a/torrc") {
		t.Fatal("missed the torrc")
	}
	for _, torrc := range []string{"/t/dev-a", "/t/dev-a/torrc2", "/t/dev-b/torrc"} {
		if cmdlineHasTorrc(raw, torrc) {
			t.Fatalf("matched %s", torrc)
		}
	}
	if cmdlineHasTorrc([]byte("cat\x00/t/dev-a/torrc\x00"), "/t/dev-a/torrc") {
		t.Fatal("matched a torrc without -f before it")
	}
}
