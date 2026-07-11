package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
)

const (
	invStartTimeout   = 15 * time.Second
	probeReadyTimeout = 150 * time.Second
	acceptTimeout     = 90 * time.Second
	completeTimeout   = 60 * time.Second
	textSentTimeout   = 5 * time.Second
	inboundTimeout    = 25 * time.Second
	deliveryTimeout   = 30 * time.Second
	readTimeout       = 45 * time.Second
	reflectTimeout    = 30 * time.Second
	fileSentTimeout   = 10 * time.Second
	fileReadyTimeout  = 120 * time.Second
	roundTripAttempts = 3
)

var corrCounter atomic.Uint64

func nextCorrID() string { return fmt.Sprintf("hr-%d", corrCounter.Add(1)) }

func pairOnion(inviter, joiner *rig) (invSeesJoiner, joinSeesInviter string, err error) {
	invSub, invCancel := inviter.subscribe()
	defer invCancel()

	if err := inviter.send(ipc.FramePairOnionInvite, ipc.PairOnionInviteRequest{Nick: inviter.name}); err != nil {
		return "", "", fmt.Errorf("invite send: %w", err)
	}

	f, err := awaitFrame(invSub, invStartTimeout, func(f ipc.Frame) bool {
		return f.Type == ipc.FramePairOnionStarted || f.Type == ipc.FrameError
	})
	if err != nil {
		return "", "", fmt.Errorf("await started: %w", err)
	}
	if f.Type == ipc.FrameError {
		return "", "", frameErr("invite", f)
	}
	var started ipc.PairOnionStartedResponse
	if err := json.Unmarshal(f.Payload, &started); err != nil {
		return "", "", fmt.Errorf("decode started: %w", err)
	}
	logf("[%s] onion invite handle=%s (%d words); awaiting reachability probe …", inviter.name, short(started.HandleID), len(started.Words))

	pf, err := awaitFrame(invSub, probeReadyTimeout, func(f ipc.Frame) bool {
		switch f.Type {
		case ipc.FramePairOnionFailed, ipc.FrameError:
			return true
		case ipc.FramePairOnionProbe:
			var p ipc.PairOnionProbePush
			if json.Unmarshal(f.Payload, &p) != nil {
				return false
			}
			return p.HandleID == started.HandleID && p.Ready
		}
		return false
	})
	if err != nil {
		return "", "", fmt.Errorf("await probe: %w", err)
	}
	if pf.Type == ipc.FrameError || pf.Type == ipc.FramePairOnionFailed {
		return "", "", frameErr("probe", pf)
	}
	var probe ipc.PairOnionProbePush
	_ = json.Unmarshal(pf.Payload, &probe)
	if probe.Error != "" {
		logf("[%s] probe fallback (unverified reachability): %s — proceeding anyway", inviter.name, probe.Error)
	} else {
		logf("[%s] onion reachable (probe attempt %d)", inviter.name, probe.Attempt)
	}

	joinSub, joinCancel := joiner.subscribe()
	defer joinCancel()
	if err := joiner.send(ipc.FramePairOnionAccept, ipc.PairOnionAcceptRequest{Words: started.Words, Nick: joiner.name}); err != nil {
		return "", "", fmt.Errorf("accept send: %w", err)
	}
	af, err := awaitFrame(joinSub, acceptTimeout, func(f ipc.Frame) bool {
		return f.Type == ipc.FramePairOnionAccepted || f.Type == ipc.FrameError
	})
	if err != nil {
		return "", "", fmt.Errorf("await accepted: %w", err)
	}
	if af.Type == ipc.FrameError {
		return "", "", frameErr("accept", af)
	}
	var accepted ipc.PairOnionAcceptedResponse
	if err := json.Unmarshal(af.Payload, &accepted); err != nil {
		return "", "", fmt.Errorf("decode accepted: %w", err)
	}
	joinSeesInviter = accepted.PeerID

	cf, err := awaitFrame(invSub, completeTimeout, func(f ipc.Frame) bool {
		switch f.Type {
		case ipc.FramePairOnionFailed, ipc.FrameError:
			return true
		case ipc.FramePairOnionCompleted:
			var p ipc.PairOnionCompletedPush
			if json.Unmarshal(f.Payload, &p) != nil {
				return false
			}
			return p.HandleID == started.HandleID
		}
		return false
	})
	if err != nil {
		return "", "", fmt.Errorf("await completed: %w", err)
	}
	if cf.Type == ipc.FrameError || cf.Type == ipc.FramePairOnionFailed {
		return "", "", frameErr("completed", cf)
	}
	var completed ipc.PairOnionCompletedPush
	if err := json.Unmarshal(cf.Payload, &completed); err != nil {
		return "", "", fmt.Errorf("decode completed: %w", err)
	}
	invSeesJoiner = completed.PeerID
	return invSeesJoiner, joinSeesInviter, nil
}

type delivery struct {
	msgID     string
	envID     string
	chatID    string
	delivered bool
}

func deliverText(from, to *rig, toPeerID, text string) (delivery, error) {
	toSub, toCancel := to.subscribe()
	defer toCancel()

	fromSub, fromCancel := from.subscribe()
	defer fromCancel()

	var lastErr error
	for attempt := 1; attempt <= roundTripAttempts; attempt++ {
		if err := from.send(ipc.FrameSendText, ipc.SendTextRequest{PeerID: toPeerID, Text: text}); err != nil {
			return delivery{}, fmt.Errorf("send: %w", err)
		}
		sf, err := awaitFrame(fromSub, textSentTimeout, func(f ipc.Frame) bool {
			return f.Type == ipc.FrameTextSent || f.Type == ipc.FrameError
		})
		if err == nil && sf.Type == ipc.FrameError {
			lastErr = frameErr("send", sf)
			logf("[%s->%s] send attempt %d errored: %v — retrying", from.name, to.name, attempt, lastErr)
			time.Sleep(3 * time.Second)
			continue
		}
		var resp ipc.SendTextResponse
		if err == nil {
			_ = json.Unmarshal(sf.Payload, &resp)
		}

		inf, err := awaitFrame(toSub, inboundTimeout, func(f ipc.Frame) bool {
			ev, ok := timelineEvent(f)
			return ok && ev.Direction == events.DirIn && ev.Kind == events.KindText && ev.MsgID == resp.MsgID && resp.MsgID != ""
		})
		if err != nil {
			lastErr = err
			logf("[%s->%s] inbound not seen on attempt %d — resending", from.name, to.name, attempt)
			continue
		}
		inEv, _ := timelineEvent(inf)
		d := delivery{msgID: resp.MsgID, envID: resp.EnvelopeID, chatID: string(inEv.ChatID)}

		d.delivered = awaitOK(fromSub, deliveryTimeout, func(f ipc.Frame) bool {
			if f.Type != ipc.FrameDeliveryStatus {
				return false
			}
			var p ipc.DeliveryStatusPayload
			if json.Unmarshal(f.Payload, &p) != nil {
				return false
			}
			return p.EnvelopeID == d.envID && (p.State == "sent" || p.State == "delivered" || p.State == "read")
		})
		return d, nil
	}
	return delivery{}, fmt.Errorf("no inbound after %d attempts: %w", roundTripAttempts, lastErr)
}

func assertRead(from, to *rig, d delivery) error {
	fromSub, cancel := from.subscribe()
	defer cancel()
	if err := to.send(ipc.FrameMarkRead, ipc.MarkReadRequest{ChatID: d.chatID}); err != nil {
		return fmt.Errorf("mark-read send: %w", err)
	}
	_, err := awaitFrame(fromSub, readTimeout, func(f ipc.Frame) bool {
		switch f.Type {
		case ipc.FrameDeliveryStatus:
			var p ipc.DeliveryStatusPayload
			if json.Unmarshal(f.Payload, &p) != nil {
				return false
			}
			return p.EnvelopeID == d.envID && p.State == "read"
		case ipc.FrameTimelineEvent:
			ev, ok := timelineEvent(f)
			return ok && ev.MsgID == d.msgID && ev.DeliveryState == "read"
		}
		return false
	})
	return err
}

func assertEdit(from, to *rig, toPeerID, msgID, newText string) error {
	toSub, cancel := to.subscribe()
	defer cancel()
	if err := from.send(ipc.FrameSendEdit, ipc.SendEditRequest{PeerID: toPeerID, TargetMsgID: msgID, Text: newText}); err != nil {
		return fmt.Errorf("edit send: %w", err)
	}
	_, err := awaitFrame(toSub, reflectTimeout, func(f ipc.Frame) bool {
		ev, ok := timelineEvent(f)
		if !ok || ev.MsgID != msgID || ev.EditedAt <= 0 {
			return false
		}
		var b events.TextBody
		return json.Unmarshal(ev.Body, &b) == nil && b.Text == newText
	})
	return err
}

func assertReaction(from, to *rig, toPeerID, msgID, emoji string) error {
	toSub, cancel := to.subscribe()
	defer cancel()
	if err := from.send(ipc.FrameSendReaction, ipc.SendReactionRequest{PeerID: toPeerID, TargetMsgID: msgID, Emoji: emoji}); err != nil {
		return fmt.Errorf("reaction send: %w", err)
	}
	_, err := awaitFrame(toSub, reflectTimeout, func(f ipc.Frame) bool {
		ev, ok := timelineEvent(f)
		if !ok || ev.Kind != events.KindReaction || ev.Direction != events.DirIn {
			return false
		}
		var b events.ReactionBody
		return json.Unmarshal(ev.Body, &b) == nil && b.TargetMsgID == msgID && b.Emoji == emoji
	})
	return err
}

func assertDelete(from, to *rig, toPeerID, msgID string) error {
	toSub, cancel := to.subscribe()
	defer cancel()
	if err := from.send(ipc.FrameSendDelete, ipc.SendDeleteRequest{PeerID: toPeerID, TargetMsgID: msgID}); err != nil {
		return fmt.Errorf("delete send: %w", err)
	}
	_, err := awaitFrame(toSub, reflectTimeout, func(f ipc.Frame) bool {
		ev, ok := timelineEvent(f)
		return ok && ev.MsgID == msgID && ev.DeletedAt > 0
	})
	return err
}

type rigFileBody struct {
	State   string `json:"state"`
	Caption string `json:"caption"`
}

type fileResult struct {
	msgID   string
	caption string
}

func sendFile(from, to *rig, toPeerID, caption string) (fileResult, error) {
	path := filepath.Join(from.root, "rig-send-"+nextCorrID()+".bin")
	payload := []byte("haoma-rig file payload " + nextCorrID() + " — assorted bytes for a real transfer over Tor")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		return fileResult{}, fmt.Errorf("write temp file: %w", err)
	}

	toSub, toCancel := to.subscribe()
	defer toCancel()
	fromSub, fromCancel := from.subscribe()
	defer fromCancel()

	if err := from.send(ipc.FrameSendFile, ipc.SendFileRequest{PeerID: toPeerID, Path: path, Caption: caption}); err != nil {
		return fileResult{}, fmt.Errorf("send_file: %w", err)
	}
	sf, err := awaitFrame(fromSub, fileSentTimeout, func(f ipc.Frame) bool {
		return f.Type == ipc.FrameFileSent || f.Type == ipc.FrameError
	})
	if err != nil {
		return fileResult{}, fmt.Errorf("await file_sent: %w", err)
	}
	if sf.Type == ipc.FrameError {
		return fileResult{}, frameErr("send_file", sf)
	}
	var resp ipc.SendFileResponse
	if err := json.Unmarshal(sf.Payload, &resp); err != nil {
		return fileResult{}, fmt.Errorf("decode file_sent: %w", err)
	}
	if resp.MsgID == "" {
		return fileResult{}, fmt.Errorf("file_sent carried no msg_id")
	}

	rf, err := awaitFrame(toSub, fileReadyTimeout, func(f ipc.Frame) bool {
		ev, ok := timelineEvent(f)
		if !ok || ev.Kind != events.KindFile || ev.Direction != events.DirIn || ev.MsgID != resp.MsgID {
			return false
		}
		var b rigFileBody
		return json.Unmarshal(ev.Body, &b) == nil && b.State == "ready"
	})
	if err != nil {
		return fileResult{}, fmt.Errorf("await file ready: %w", err)
	}
	rev, _ := timelineEvent(rf)
	var rb rigFileBody
	_ = json.Unmarshal(rev.Body, &rb)
	return fileResult{msgID: resp.MsgID, caption: rb.Caption}, nil
}

func assertReply(from, to *rig, toPeerID, replyToMsgID, replyText, originalText string) error {
	toSub, toCancel := to.subscribe()
	defer toCancel()
	fromSub, fromCancel := from.subscribe()
	defer fromCancel()

	var lastErr error
	for attempt := 1; attempt <= roundTripAttempts; attempt++ {
		if err := from.send(ipc.FrameSendText, ipc.SendTextRequest{PeerID: toPeerID, Text: replyText, ReplyToMsgID: replyToMsgID}); err != nil {
			return fmt.Errorf("send reply: %w", err)
		}
		sf, err := awaitFrame(fromSub, textSentTimeout, func(f ipc.Frame) bool {
			return f.Type == ipc.FrameTextSent || f.Type == ipc.FrameError
		})
		if err == nil && sf.Type == ipc.FrameError {
			return frameErr("reply send", sf)
		}
		var resp ipc.SendTextResponse
		if err == nil {
			_ = json.Unmarshal(sf.Payload, &resp)
		}
		if resp.MsgID == "" {
			lastErr = fmt.Errorf("reply send ack missing msg_id")
			continue
		}
		_, err = awaitFrame(toSub, inboundTimeout, func(f ipc.Frame) bool {
			ev, ok := timelineEvent(f)
			if !ok || ev.Direction != events.DirIn || ev.Kind != events.KindText || ev.MsgID != resp.MsgID {
				return false
			}
			var b events.TextBody
			if json.Unmarshal(ev.Body, &b) != nil || b.ReplyTo == nil {
				return false
			}
			return b.ReplyTo.MsgID == replyToMsgID && b.ReplyTo.Text == originalText
		})
		if err == nil {
			return nil
		}
		lastErr = err
		logf("[%s->%s] reply inbound not seen on attempt %d — resending", from.name, to.name, attempt)
	}
	return fmt.Errorf("no reply inbound after %d attempts: %w", roundTripAttempts, lastErr)
}

func timelineEvent(f ipc.Frame) (events.Event, bool) {
	if f.Type != ipc.FrameTimelineEvent {
		return events.Event{}, false
	}
	var tp ipc.TimelineEventPayload
	if json.Unmarshal(f.Payload, &tp) != nil {
		return events.Event{}, false
	}
	var ev events.Event
	if json.Unmarshal(tp.Event, &ev) != nil {
		return events.Event{}, false
	}
	return ev, true
}

func awaitFrame(sub <-chan ipc.Frame, timeout time.Duration, pred func(ipc.Frame) bool) (ipc.Frame, error) {
	deadline := time.After(timeout)
	for {
		select {
		case f, ok := <-sub:
			if !ok {
				return ipc.Frame{}, fmt.Errorf("subscription closed")
			}
			if pred(f) {
				return f, nil
			}
		case <-deadline:
			return ipc.Frame{}, fmt.Errorf("timeout after %s", timeout)
		}
	}
}

func awaitOK(sub <-chan ipc.Frame, timeout time.Duration, pred func(ipc.Frame) bool) bool {
	_, err := awaitFrame(sub, timeout, pred)
	return err == nil
}

func frameErr(stage string, f ipc.Frame) error {
	var e ipc.ErrorPayload
	if json.Unmarshal(f.Payload, &e) == nil && (e.Code != "" || e.Message != "") {
		return fmt.Errorf("%s: %s (%s)", stage, e.Message, e.Code)
	}
	return fmt.Errorf("%s: %s", stage, f.Type)
}
