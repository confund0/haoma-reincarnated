package main

import (
	"fmt"
	"time"

	"haoma-frontend/internal/logid"
)

const (
	dropFirstDestroySaltOnLine = "seeded fault on: this device drops the first destroy PIN salt my other device sends, so only the sweep can bring it"

	destroySaltDroppedLine = "seeded fault: the first destroy PIN salt from my other device was dropped"

	saltSentAgainLine = "destroy pin: the other device does not hold this device's salt; sending it again"

	saltHeldLine = "destroy pin: the other device holds this device's salt"
)

var saltHealTimeout = saltCrossTimeout

type saltHealSettings struct {
	devB     *rig
	relaunch relaunchFunc
	on       bool
}

func (s *saltHealSettings) faultOnB() error {
	s.on = true
	return s.relaunch(s.devB, func(r *rig) { r.dropFirstDestroySalt = true })
}

func (s *saltHealSettings) restore() error {
	if !s.on {
		return nil
	}
	logf("[%s] starting haoma again without %s", s.devB.name, dropFirstDestroySaltEnv)
	if err := s.relaunch(s.devB, func(r *rig) { r.dropFirstDestroySalt = false }); err != nil {
		return err
	}
	s.on = false
	return nil
}

func awaitMarkedLine(r *rig, mark logMark, timeout time.Duration, want lineWant) error {
	deadline := time.Now().Add(timeout)
	for {
		lines, err := mark.lines()
		if err != nil {
			return err
		}
		if countMatching(lines, want) > 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s's haoma log has no %s line since the check began, after %s", r.name, want, timeout)
		}
		time.Sleep(syncPollInterval)
	}
}

func startSaltHeal(devA, devB *rig, relaunch relaunchFunc) (*saltHealSettings, logMark, logMark, error) {
	s := &saltHealSettings{devB: devB, relaunch: relaunch}
	if err := devicesAtDebugLevel(devA, devB); err != nil {
		return s, logMark{}, logMark{}, err
	}
	markA := haomaLogMark(devA)
	markB := haomaLogMark(devB)
	logf("[%s] starting haoma again with %s=1", devB.name, dropFirstDestroySaltEnv)
	if err := s.faultOnB(); err != nil {
		return s, markA, markB, fmt.Errorf("could not start %s's haoma again: %w", devB.name, err)
	}
	if err := awaitStartLines(devB, markB, startLine{dropFirstDestroySaltOnLine, ""}); err != nil {
		return s, markA, markB, err
	}
	return s, markA, markB, nil
}

func awaitSaltSentAgain(devA, devB *rig, markA, markB logMark, aSeesB string) error {
	if err := awaitMarkedLine(devB, markB, saltHealTimeout, lineWant{destroySaltDroppedLine, nil}); err != nil {
		return fmt.Errorf("the fault never dropped %s's salt, so nothing was lost for the sweep to bring: %w", devA.name, err)
	}
	logf("[%s] dropped %s's destroy PIN salt; asking it to sync, so its report reaches %s", devB.name, devA.name, devA.name)
	if err := driveSync(devB, false); err != nil {

		logf("[%s] the sync verb failed (%v); waiting for the local trigger instead", devB.name, err)
	}
	want := lineWant{saltSentAgainLine, map[string]string{"sibling_peer_id": logid.Short(aSeesB)}}
	if err := awaitMarkedLine(devA, markA, saltHealTimeout, want); err != nil {
		return fmt.Errorf("%s never sent its salt again after %s lost it: %w", devA.name, devB.name, err)
	}
	return nil
}

func awaitNoSecondSalt(devA, devB *rig, markB logMark, bSeesA string) error {
	if err := driveSync(devA, false); err != nil {
		logf("[%s] the sync verb failed (%v); waiting for the local trigger instead", devA.name, err)
	}
	held := lineWant{saltHeldLine, map[string]string{"sibling_peer_id": logid.Short(bSeesA)}}
	if err := awaitMarkedLine(devB, markB, saltHealTimeout, held); err != nil {
		return fmt.Errorf("no report from %s that matched %s's salt reached %s, so the check measured nothing: %w", devA.name, devB.name, devB.name, err)
	}
	lines, err := markB.lines()
	if err != nil {
		return err
	}
	if n := countMatching(lines, lineWant{saltSentAgainLine, nil}); n > 0 {
		return fmt.Errorf("%s sent its salt again %d time(s) although %s never lost it", devB.name, n, devA.name)
	}
	return nil
}
