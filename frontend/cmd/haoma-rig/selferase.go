package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var selfEraseRigNames = []string{"keeper", "absent"}

const selfEraseRigCount = 2

const (
	selfErasePINKeeper = "1379"
	selfErasePINAbsent = "2468"
	selfErasePINWrong  = "9999"
)

const selfEraseGoneTimeout = 5 * time.Minute

const (
	armedLine  = "the rail address now answers that device with a destroy proof"
	toldLine   = "told that device it is gone"
	erasedLine = "self-erase: the other device says this device was removed"
)

const (
	refusedLine   = "destroy verify: wrong proof"
	refusedSource = `source="rail answer"`
)

func runSelfEraseFleet(keeper, absent *rig) *pairingResult {
	return runSelfEraseRun(keeper, absent, selfErasePINAbsent, true)
}

func runSelfEraseRefusedFleet(keeper, absent *rig) *pairingResult {
	return runSelfEraseRun(keeper, absent, selfErasePINWrong, false)
}

func runSelfEraseRun(keeper, absent *rig, pin string, erases bool) *pairingResult {
	res := &pairingResult{a: keeper.name, b: absent.name}

	link, err := linkDevices(keeper, absent)
	if err != nil {
		res.add("link", err)
		return res
	}
	res.add("link", nil)

	if _, err := setDestroyPIN(keeper, selfErasePINKeeper); err != nil {
		res.add("destroy pin set keeper", err)
		return res
	}
	res.add("destroy pin set keeper", nil)
	if _, err := setDestroyPIN(absent, selfErasePINAbsent); err != nil {
		res.add("destroy pin set absent", err)
		return res
	}
	res.add("destroy pin set absent", nil)

	if err := awaitSiblingSalt(keeper, link.offeringSeesJoining); err != nil {
		res.add("destroy pin salt reaches keeper", err)
		return res
	}
	res.add("destroy pin salt reaches keeper", nil)

	before, err := countTreeEntries(absent)
	if err != nil {
		res.add("the device holds something before it goes", err)
		return res
	}
	if before == 0 {
		res.add("the device holds something before it goes",
			fmt.Errorf("%s holds nothing before it is removed, so this proves nothing", absent.name))
		return res
	}
	res.add("the device holds something before it goes", nil)

	if err := absent.stop(); err != nil {
		res.add("the device is switched off", err)
		return res
	}
	res.add("the device is switched off", nil)

	res.add("a device that is switched off is removed and the answer is armed",
		assertArmedAgainstASwitchedOffDevice(keeper, link.offeringSeesJoining, pin))

	mark := time.Now()
	shadow := shadowDaemonLog(absent)
	defer shadow.stop()

	if err := absent.start(); err != nil {

		logf("[%s] came back and did not stay: %v", absent.name, err)
	}

	res.add("the device that came back is told it is gone",
		assertToldItIsGone(keeper, link.offeringSeesJoining, mark))
	if erases {
		res.add("the device that came back erases itself",
			assertTreeEmptiedWithin(absent, selfEraseGoneTimeout))
		res.add("the device that came back says why it erased itself",
			assertSaidWhyItErased(absent, shadow))
	} else {
		res.add("the device that came back refuses the proof",
			assertRefusedTheProof(absent, shadow))
		res.add("the device that came back keeps everything",
			assertTreeStillHolds(absent, before))
	}
	res.add("the device that stays keeps its own data",
		assertTreeIntact(keeper))
	res.add("the device that stays is linked to nobody",
		assertNoLinkedDevices(keeper))

	return res
}

func assertArmedAgainstASwitchedOffDevice(keeper *rig, absentPeerID, pin string) error {
	out, err := unlinkDevice(keeper, absentPeerID, pin)
	if err != nil {
		return fmt.Errorf("destroy against a switched-off device: %w", err)
	}
	if !out.Unlinked {

		return fmt.Errorf("the removal was refused: %+v", out)
	}
	if out.Sent {
		return fmt.Errorf("%s reports the command was delivered, so the other device was still listening — this run measured the ordinary destroy path", keeper.name)
	}

	lines, err := daemonLogLines(keeper)
	if err != nil {
		return err
	}
	for _, line := range lines {
		if strings.Contains(line, armedLine) && lineNamesPeer(line, absentPeerID) {
			return nil
		}
	}
	return fmt.Errorf("%s never recorded the answer for that device, so it can never be told", keeper.name)
}

func lineNamesPeer(line, peerID string) bool {
	got := lineField(line, "peer_id=")
	return got != "" && strings.HasPrefix(peerID, got)
}

func assertToldItIsGone(keeper *rig, peerID string, since time.Time) error {
	deadline := time.Now().Add(selfEraseGoneTimeout)
	for {
		lines, err := daemonLogLines(keeper)
		if err != nil {
			return err
		}
		for _, line := range lines {
			if !strings.Contains(line, toldLine) || !lineNamesPeer(line, peerID) {
				continue
			}
			if lineTime(line).Before(since) {
				continue
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s never answered the device that came back, after %s", keeper.name, selfEraseGoneTimeout)
		}
		rigSleep(5*time.Second, "waiting for the returning device to be told it is gone")
	}
}

type logShadow struct {
	mu      sync.Mutex
	content string
	done    chan struct{}
	once    sync.Once
}

const shadowLogInterval = 200 * time.Millisecond

func shadowDaemonLog(r *rig) *logShadow {
	s := &logShadow{done: make(chan struct{})}
	path := filepath.Join(r.root, "haomad.log")
	go func() {
		for {
			if b, err := os.ReadFile(path); err == nil {
				s.mu.Lock()
				if len(b) > len(s.content) {
					s.content = string(b)
				}
				s.mu.Unlock()
			}
			select {
			case <-s.done:
				return
			case <-time.After(shadowLogInterval):
			}
		}
	}()
	return s
}

func (s *logShadow) stop() { s.once.Do(func() { close(s.done) }) }

func (s *logShadow) has(want string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Contains(s.content, want)
}

func (s *logShadow) hasBoth(a, b string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, line := range strings.Split(s.content, "\n") {
		if strings.Contains(line, a) && strings.Contains(line, b) {
			return true
		}
	}
	return false
}

const shadowCatchUp = 30 * time.Second

func assertSaidWhyItErased(r *rig, shadow *logShadow) error {
	deadline := time.Now().Add(shadowCatchUp)
	for {
		if shadow.has(erasedLine) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s never said it was erasing because it had been removed, so its tree went for some other reason", r.name)
		}
		rigSleep(2*time.Second, "waiting for a returning device to say why it erased itself")
	}
}

func assertRefusedTheProof(r *rig, shadow *logShadow) error {
	deadline := time.Now().Add(selfEraseGoneTimeout)
	for {

		if shadow.has(erasedLine) {
			return fmt.Errorf("%s erased itself on a proof it should have refused", r.name)
		}
		if shadow.hasBoth(refusedLine, refusedSource) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s never said it refused the proof, so this run never reached the check", r.name)
		}
		rigSleep(5*time.Second, "waiting for a returning device to refuse a proof")
	}
}

func assertTreeStillHolds(r *rig, before int) error {
	n, err := countTreeEntries(r)
	if err != nil {
		return err
	}
	if n != before {
		return fmt.Errorf("%s went from %d entries to %d on a proof it refused", r.name, before, n)
	}
	return nil
}

func assertTreeEmptiedWithin(r *rig, within time.Duration) error {
	deadline := time.Now().Add(within)
	for {
		n, err := countTreeEntries(r)
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			names, _ := treeEntries(r)
			return fmt.Errorf("%s still holds %d entries after %s: %v", r.name, n, within, names)
		}
		rigSleep(5*time.Second, "waiting for a returning device to erase itself")
	}
}
