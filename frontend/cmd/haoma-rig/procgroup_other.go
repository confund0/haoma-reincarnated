//go:build !linux

package main

import (
	"os/exec"
	"time"
)

func ownGroup(*exec.Cmd, bool) {}

func signalOnRigDeath(*exec.Cmd) {}

func endLeftoverTors(string, string, time.Duration) error { return nil }

func sweepGroup(string, *exec.Cmd) {}
