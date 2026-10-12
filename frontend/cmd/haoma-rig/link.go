package main

import (
	"encoding/json"
	"fmt"
	"time"

	"haoma-frontend/internal/ipc"
)

const (
	linkStartTimeout  = 15 * time.Second
	linkProbeTimeout  = 150 * time.Second
	linkAcceptTimeout = 390 * time.Second
	linkCompleteTime  = 60 * time.Second
	linkRetryDelay    = 10 * time.Second
	linkSetTimeout    = 10 * time.Second
	linkAcceptTries   = 2
	linkSettleDelay   = 5 * time.Second
)

type linkResult struct {
	offeringSeesJoining string
	joiningSeesOffering string
	adoptedFingerprint  string
}

func linkDevices(offering, joining *rig) (linkResult, error) {
	var out linkResult

	offSub, offCancel := offering.subscribe()
	defer offCancel()

	if err := offering.send(ipc.FrameDeviceLinkOffer, ipc.DeviceLinkOfferRequest{}); err != nil {
		return out, fmt.Errorf("offer send: %w", err)
	}

	sf, err := awaitFrame(offSub, linkStartTimeout, func(f ipc.Frame) bool {
		switch f.Type {
		case ipc.FrameDeviceLinkStarted, ipc.FrameDeviceLinkFailed, ipc.FrameError:
			return true
		}
		return false
	})
	if err != nil {
		return out, fmt.Errorf("await devicelink started: %w", err)
	}
	if sf.Type != ipc.FrameDeviceLinkStarted {
		return out, linkFrameErr("offer", sf)
	}
	var started ipc.DeviceLinkStartedResponse
	if err := json.Unmarshal(sf.Payload, &started); err != nil {
		return out, fmt.Errorf("decode devicelink started: %w", err)
	}
	if len(started.Words) == 0 {
		return out, fmt.Errorf("devicelink started carried no words")
	}
	logf("[%s] devicelink offer handle=%s (%d words); expires %s — awaiting rendezvous reachability …",
		offering.name, short(started.HandleID), len(started.Words),
		time.Unix(started.ExpiresAt, 0).Format(time.TimeOnly))

	pf, err := awaitFrame(offSub, linkProbeTimeout, func(f ipc.Frame) bool {
		switch f.Type {
		case ipc.FrameDeviceLinkFailed, ipc.FrameError:
			return true
		case ipc.FrameDeviceLinkProbe:
			var p ipc.DeviceLinkProbePush
			if json.Unmarshal(f.Payload, &p) != nil {
				return false
			}
			return p.HandleID == started.HandleID && p.Ready
		}
		return false
	})
	switch {
	case err != nil:

		logf("[%s] no reachability report within %s — falling back to blind attempts", offering.name, linkProbeTimeout)
	case pf.Type != ipc.FrameDeviceLinkProbe:
		return out, linkFrameErr("reachability", pf)
	default:
		var probe ipc.DeviceLinkProbePush
		if err := json.Unmarshal(pf.Payload, &probe); err != nil {
			return out, fmt.Errorf("decode devicelink reachability: %w", err)
		}
		if probe.Error != "" {
			logf("[%s] reachability unverified (%s) — proceeding anyway", offering.name, probe.Error)
		} else {
			logf("[%s] rendezvous reachable (attempt %d)", offering.name, probe.Attempt)
		}
	}

	joinSub, joinCancel := joining.subscribe()
	defer joinCancel()

	var accepted ipc.DeviceLinkAcceptedResponse
	var lastErr error

	for attempt := 1; attempt <= linkAcceptTries; attempt++ {
		reqID, err := joining.sendReq(ipc.FrameDeviceLinkAccept, ipc.DeviceLinkAcceptRequest{Words: started.Words})
		if err != nil {
			return out, fmt.Errorf("accept send: %w", err)
		}
		af, err := awaitFrame(joinSub, linkAcceptTimeout, func(f ipc.Frame) bool {
			switch f.Type {
			case ipc.FrameDeviceLinkAccepted, ipc.FrameError:
				return f.ID == reqID
			case ipc.FrameDeviceLinkFailed:
				return true
			}
			return false
		})
		if err != nil {
			lastErr = fmt.Errorf("await devicelink accepted: %w", err)
		} else if af.Type != ipc.FrameDeviceLinkAccepted {
			lastErr = linkFrameErr("accept", af)
		} else if err := json.Unmarshal(af.Payload, &accepted); err != nil {
			return out, fmt.Errorf("decode devicelink accepted: %w", err)
		} else {
			lastErr = nil
			break
		}
		if attempt < linkAcceptTries {
			logf("[%s] devicelink accept attempt %d failed (%v) — retrying in %s", joining.name, attempt, lastErr, linkRetryDelay)
			time.Sleep(linkRetryDelay)
		}
	}
	if lastErr != nil {
		return out, fmt.Errorf("devicelink accept exhausted after %d attempts: %w", linkAcceptTries, lastErr)
	}
	out.joiningSeesOffering = accepted.PeerID
	out.adoptedFingerprint = accepted.AdoptedFingerprint
	logf("[%s] adopted identity fp=%s (contacts cleared: %t)", joining.name, short(accepted.AdoptedFingerprint), accepted.ContactsCleared)

	cf, err := awaitFrame(offSub, linkCompleteTime, func(f ipc.Frame) bool {
		switch f.Type {
		case ipc.FrameDeviceLinkFailed, ipc.FrameError:
			return true
		case ipc.FrameDeviceLinkCompleted:
			var p ipc.DeviceLinkCompletedPush
			if json.Unmarshal(f.Payload, &p) != nil {
				return false
			}
			return p.HandleID == started.HandleID
		}
		return false
	})
	if err != nil {
		return out, fmt.Errorf("await devicelink completed: %w", err)
	}
	if cf.Type != ipc.FrameDeviceLinkCompleted {
		return out, linkFrameErr("completed", cf)
	}
	var completed ipc.DeviceLinkCompletedPush
	if err := json.Unmarshal(cf.Payload, &completed); err != nil {
		return out, fmt.Errorf("decode devicelink completed: %w", err)
	}
	out.offeringSeesJoining = completed.PeerID

	if out.offeringSeesJoining == "" || out.joiningSeesOffering == "" {
		return out, fmt.Errorf("devicelink completed without a routing id on both ends (offering sees %q, joining sees %q)",
			out.offeringSeesJoining, out.joiningSeesOffering)
	}
	return out, nil
}

func assertLinkedSet(r *rig, wantPeerID string) error {
	sub, cancel := r.subscribe()
	defer cancel()
	if err := r.send(ipc.FrameDeviceLinksList, ipc.DeviceLinksListRequest{}); err != nil {
		return fmt.Errorf("devicelinks_list send: %w", err)
	}
	f, err := awaitFrame(sub, linkSetTimeout, func(f ipc.Frame) bool {
		return f.Type == ipc.FrameDeviceLinksListed || f.Type == ipc.FrameError
	})
	if err != nil {
		return fmt.Errorf("await devicelinks_listed: %w", err)
	}
	if f.Type == ipc.FrameError {
		return frameErr("devicelinks_list", f)
	}
	var listed ipc.DeviceLinksListedResponse
	if err := json.Unmarshal(f.Payload, &listed); err != nil {
		return fmt.Errorf("decode devicelinks_listed: %w", err)
	}
	for _, d := range listed.Devices {
		if d.PeerID == wantPeerID {
			return nil
		}
	}
	return fmt.Errorf("linked-device set on %s does not name %s (%d device(s) listed)", r.name, short(wantPeerID), len(listed.Devices))
}

func linkFrameErr(stage string, f ipc.Frame) error {
	if f.Type == ipc.FrameDeviceLinkFailed {
		var p ipc.DeviceLinkFailedPush
		if json.Unmarshal(f.Payload, &p) == nil && (p.Reason != "" || p.Detail != "") {
			return fmt.Errorf("%s: devicelink failed: %s (%s)", stage, p.Reason, p.Detail)
		}
	}
	return frameErr(stage, f)
}
