package main

import (
	"encoding/json"
	"fmt"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/ipc"
)

const (
	rigDestroyPINA  = "1379"
	rigDestroyPINB  = "2468"
	rigDestroyWrong = "9999"
)

func setDestroyPIN(r *rig, pin string) (ipc.DestroyPINSetResponse, error) {
	sub, cancel := r.subscribe()
	defer cancel()
	id, err := r.sendReq(ipc.FrameSetDestroyPIN, ipc.SetDestroyPINRequest{New: pin})
	if err != nil {
		return ipc.DestroyPINSetResponse{}, fmt.Errorf("set_destroy_pin send: %w", err)
	}
	f, err := awaitFrame(sub, linkSetTimeout, func(f ipc.Frame) bool {
		return f.ID == id && (f.Type == ipc.FrameDestroyPINSet || f.Type == ipc.FrameError)
	})
	if err != nil {
		return ipc.DestroyPINSetResponse{}, fmt.Errorf("await destroy_pin_set: %w", err)
	}
	if f.Type == ipc.FrameError {
		return ipc.DestroyPINSetResponse{}, frameErr("set_destroy_pin", f)
	}
	var out ipc.DestroyPINSetResponse
	if err := json.Unmarshal(f.Payload, &out); err != nil {
		return ipc.DestroyPINSetResponse{}, fmt.Errorf("decode destroy_pin_set: %w", err)
	}
	return out, nil
}

const (
	verifyDestroyPINAwait = backendapi.DestroyVerifyWait + 30*time.Second
	unlinkAwait           = backendapi.DestroyVerifyWait + 90*time.Second
)

func verifyDestroyPIN(r *rig, siblingPeerID, pin string) (ipc.DestroyPINVerifiedResponse, error) {
	sub, cancel := r.subscribe()
	defer cancel()
	req := ipc.VerifyDestroyPINRequest{PeerID: siblingPeerID, PIN: pin}
	id, err := r.sendReq(ipc.FrameVerifyDestroyPIN, req)
	if err != nil {
		return ipc.DestroyPINVerifiedResponse{}, fmt.Errorf("verify_destroy_pin send: %w", err)
	}
	f, err := awaitFrame(sub, verifyDestroyPINAwait, func(f ipc.Frame) bool {
		return f.ID == id && (f.Type == ipc.FrameDestroyPINVerified || f.Type == ipc.FrameError)
	})
	if err != nil {
		return ipc.DestroyPINVerifiedResponse{}, fmt.Errorf("await destroy_pin_verified: %w", err)
	}
	if f.Type == ipc.FrameError {
		return ipc.DestroyPINVerifiedResponse{}, frameErr("verify_destroy_pin", f)
	}
	var out ipc.DestroyPINVerifiedResponse
	if err := json.Unmarshal(f.Payload, &out); err != nil {
		return ipc.DestroyPINVerifiedResponse{}, fmt.Errorf("decode destroy_pin_verified: %w", err)
	}
	return out, nil
}

func runDestroyPINGate(res *pairingResult, devA, devB *rig, aSeesB, bSeesA, bSeesPeer string) {
	logf("[%s<->%s] destroy PIN: setting one on each device", devA.name, devB.name)

	heal, markA, markB, healErr := startSaltHeal(devA, devB, relaunchHaoma)
	healStarted := healErr == nil
	defer func() {
		if err := heal.restore(); err != nil {
			logf("[%s] COULD NOT TAKE %s OFF AGAIN: %v", devB.name, dropFirstDestroySaltEnv, err)
		}
	}()

	setA, err := setDestroyPIN(devA, rigDestroyPINA)
	if err == nil && !setA.Accepted {
		err = fmt.Errorf("%s refused its own first destroy PIN: %q", devA.name, setA.Reason)
	}
	res.add("destroy pin set a", err)
	if err != nil {
		return
	}
	setB, err := setDestroyPIN(devB, rigDestroyPINB)
	if err == nil && !setB.Accepted {
		err = fmt.Errorf("%s refused its own first destroy PIN: %q", devB.name, setB.Reason)
	}
	res.add("destroy pin set b", err)
	if err != nil {
		return
	}

	again, err := setDestroyPIN(devA, rigDestroyWrong)
	if err == nil {
		switch {
		case again.Accepted:
			err = fmt.Errorf("%s accepted a second destroy PIN; it cannot be changed", devA.name)
		case again.Reason != ipc.DestroyPINRefusedAlreadySet:
			err = fmt.Errorf("second set refused with %q, want %q", again.Reason, ipc.DestroyPINRefusedAlreadySet)
		}
	}
	res.add("destroy pin cannot change", err)

	if healErr == nil {
		healErr = awaitSaltSentAgain(devA, devB, markA, markB, aSeesB)
	}
	res.add("destroy pin salt reaches b", awaitSiblingSalt(devB, bSeesA))
	res.add("destroy pin salt reaches a", awaitSiblingSalt(devA, aSeesB))
	res.add("destroy pin salt lost once and sent again", healErr)
	if healStarted {
		res.add("destroy pin salt not sent again without a loss", awaitNoSecondSalt(devA, devB, markB, bSeesA))
	} else {
		res.add("destroy pin salt not sent again without a loss", fmt.Errorf("not run: the fault on %s never started", devB.name))
	}

	if err := heal.restore(); err != nil {
		logf("[%s] COULD NOT TAKE %s OFF; the checks below run with it on: %v", devB.name, dropFirstDestroySaltEnv, err)
	}

	stranger, err := verifyDestroyPIN(devB, bSeesPeer, rigDestroyPINA)
	if err == nil {
		switch {
		case stranger.Reason != ipc.DestroyPINVerifyUnknownSibling:
			err = fmt.Errorf("verify against a contact refused with %q, want %q",
				stranger.Reason, ipc.DestroyPINVerifyUnknownSibling)
		case stranger.Answered || stranger.Verified:
			err = fmt.Errorf("verify against a contact reached the wire: answered=%v verified=%v",
				stranger.Answered, stranger.Verified)
		}
	}
	res.add("destroy pin refuses a contact", err)

	ok, err := verifyDestroyPIN(devB, bSeesA, rigDestroyPINA)
	if err == nil {
		switch {
		case ok.Reason != "":
			err = fmt.Errorf("%s refused to ask: %s", devB.name, ok.Reason)
		case !ok.Answered:
			err = fmt.Errorf("%s never answered %s's verify", devA.name, devB.name)
		case !ok.Verified:
			err = fmt.Errorf("%s refused its own destroy PIN", devA.name)
		}
	}
	res.add("destroy pin verifies", err)

	bad, err := verifyDestroyPIN(devB, bSeesA, rigDestroyWrong)
	if err == nil {
		switch {
		case bad.Reason != "":
			err = fmt.Errorf("%s refused to ask: %s", devB.name, bad.Reason)
		case !bad.Answered:
			err = fmt.Errorf("%s never answered a wrong-PIN verify", devA.name)
		case bad.Verified:
			err = fmt.Errorf("%s accepted a PIN that does not guard it", devA.name)
		}
	}
	res.add("destroy pin refuses a wrong one", err)

	logf("[%s] destroy PIN: spending the rest of %s's attempts", devB.name, devA.name)
	for i := 0; i < destroyRigAttemptsToLock; i++ {
		if _, vErr := verifyDestroyPIN(devB, bSeesA, rigDestroyWrong); vErr != nil {
			res.add("destroy pin cool-off", fmt.Errorf("attempt %d: %w", i+1, vErr))
			return
		}
	}

	locked, err := verifyDestroyPIN(devB, bSeesA, rigDestroyPINA)
	if err == nil {
		switch {
		case locked.Reason != "":
			err = fmt.Errorf("%s refused to ask: %s", devB.name, locked.Reason)
		case !locked.Answered:
			err = fmt.Errorf("%s stopped answering entirely; it should answer and say no", devA.name)
		case locked.Verified:
			err = fmt.Errorf("%s accepted the right PIN during its cool-off; the limit is not being checked first", devA.name)
		}
	}
	res.add("destroy pin cool-off refuses the right one", err)

	stillFine, err := verifyDestroyPIN(devA, aSeesB, rigDestroyPINB)
	if err == nil {
		switch {
		case stillFine.Reason != "":
			err = fmt.Errorf("%s refused to ask: %s", devA.name, stillFine.Reason)
		case !stillFine.Answered:
			err = fmt.Errorf("%s never answered", devB.name)
		case !stillFine.Verified:
			err = fmt.Errorf("%s is locked out too; the cool-off is not per device", devB.name)
		}
	}
	res.add("destroy pin cool-off is per device", err)
}

const destroyRigAttemptsToLock = 2

func awaitSiblingSalt(r *rig, siblingPeerID string) error {
	deadline := time.Now().Add(saltCrossTimeout)
	for {
		sub, cancel := r.subscribe()
		id, err := r.sendReq(ipc.FrameGetDestroyPIN, ipc.GetDestroyPINRequest{})
		if err != nil {
			cancel()
			return fmt.Errorf("get_destroy_pin send: %w", err)
		}
		f, err := awaitFrame(sub, linkSetTimeout, func(f ipc.Frame) bool {
			return f.ID == id && (f.Type == ipc.FrameDestroyPINListed || f.Type == ipc.FrameError)
		})
		cancel()
		if err != nil {
			return fmt.Errorf("await destroy_pin_listed: %w", err)
		}
		if f.Type == ipc.FrameError {
			return frameErr("get_destroy_pin", f)
		}
		var listed ipc.DestroyPINListedResponse
		if err := json.Unmarshal(f.Payload, &listed); err != nil {
			return fmt.Errorf("decode destroy_pin_listed: %w", err)
		}
		for _, s := range listed.Siblings {
			if s.PeerID == siblingPeerID && s.SaltHeld {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s still holds no destroy PIN salt for %s after %s. "+
				"The salt rides the outbox, so this is either a rail that never delivered or a device that never sent. "+
				"Check the sending device's outbox for that envelope before reading this as a destroy-PIN fault",
				r.name, short(siblingPeerID), saltCrossTimeout)
		}
		rigSleep(2*time.Second, "waiting for a destroy PIN salt to cross")
	}
}

const saltCrossTimeout = 5 * time.Minute
