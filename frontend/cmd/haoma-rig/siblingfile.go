package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
)

func chatIDForPeer(r *rig, peerID string, timeout time.Duration) (string, error) {
	chats, err := listChats(r, timeout)
	if err != nil {
		return "", fmt.Errorf("could not read %s's chat list: %w", r.name, err)
	}
	for _, c := range chats {
		if c.PeerID == peerID {
			return c.ChatID, nil
		}
	}
	return "", fmt.Errorf("%s has no chat reaching %s", r.name, short(peerID))
}

func awaitOwnSealedCopy(r *rig, chatID, msgID string, timeout time.Duration, want []byte) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		path, err := openOwnCopy(r, chatID, msgID)
		switch {
		case err != nil:
			lastErr = err
		default:
			got, rErr := os.ReadFile(path)
			wipeOpenTransient(r, msgID)
			if rErr != nil {
				lastErr = fmt.Errorf("%s opened its own copy at %s but it could not be read: %w", r.name, path, rErr)
			} else if !bytes.Equal(got, want) {
				return fmt.Errorf("%s unsealed %d bytes for msg_id=%s, want the %d bytes the sender shipped — it holds a copy of something else",
					r.name, len(got), short(msgID), len(want))
			} else {
				logf("[%s] unsealed its own copy of msg_id=%s, %d bytes, matching what the sender shipped", r.name, short(msgID), len(got))
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s never held its own openable copy of msg_id=%s after %s: %w", r.name, short(msgID), timeout, lastErr)
		}
		time.Sleep(syncPollInterval)
	}
}

func openOwnCopy(r *rig, chatID, msgID string) (string, error) {
	sub, cancel := r.subscribe()
	defer cancel()
	id, err := r.sendReq(ipc.FrameOpenFile, ipc.OpenFileRequest{ChatID: chatID, MsgID: msgID})
	if err != nil {
		return "", fmt.Errorf("open_file send: %w", err)
	}
	f, err := awaitFrame(sub, reflectTimeout, func(f ipc.Frame) bool {
		return f.ID == id && (f.Type == ipc.FrameFileOpenReady || f.Type == ipc.FrameError)
	})
	if err != nil {
		return "", fmt.Errorf("await file_open_ready: %w", err)
	}
	if f.Type == ipc.FrameError {
		return "", frameErr("open_file", f)
	}
	var resp ipc.OpenFileReadyResponse
	if err := json.Unmarshal(f.Payload, &resp); err != nil {
		return "", fmt.Errorf("decode file_open_ready: %w", err)
	}
	if resp.FullPath == "" {
		return "", fmt.Errorf("file_open_ready carried no path")
	}
	return resp.FullPath, nil
}

func wipeOpenTransient(r *rig, msgID string) {
	if err := r.send(ipc.FrameWipeOpenTransient, ipc.WipeOpenTransientRequest{MsgID: msgID}); err != nil {
		logf("[%s] could not ask for the open transient to be wiped: %v", r.name, err)
	}
}

func outboundEnvelopeIDs(r *rig, peerID, msgID string, timeout time.Duration) ([]string, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		rows, err := timelineRows(r, peerID, syncConvergeTimeout)
		switch {
		case err != nil:
			lastErr = fmt.Errorf("could not read %s's timeline: %w", r.name, err)
		default:
			ev, ok := rows[msgID]
			switch {
			case !ok:
				lastErr = fmt.Errorf("%s shows no row for msg_id=%s", r.name, short(msgID))
			case len(ev.EnvelopeIDs) == 0:
				lastErr = fmt.Errorf("%s's row for msg_id=%s names no envelope ids", r.name, short(msgID))
			default:
				return ev.EnvelopeIDs, nil
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("after %s: %w", timeout, lastErr)
		}
		time.Sleep(syncPollInterval)
	}
}

type fileRowBody struct {
	Token   string `json:"token"`
	UrlPath string `json:"url_path"`
	State   string `json:"state"`
	Caption string `json:"caption"`
}

func readFileRowBody(r *rig, peerID, msgID string) (fileRowBody, error) {
	rows, err := timelineRows(r, peerID, syncConvergeTimeout)
	if err != nil {
		return fileRowBody{}, err
	}
	ev, ok := rows[msgID]
	if !ok {
		return fileRowBody{}, fmt.Errorf("msg_id=%s not present in %s's own timeline", short(msgID), r.name)
	}
	var b fileRowBody
	if err := json.Unmarshal(ev.Body, &b); err != nil {
		return fileRowBody{}, fmt.Errorf("decode %s's file body: %w", r.name, err)
	}
	return b, nil
}

func assertSiblingFileMirror(devA, devB, peer *rig, aSeesPeer, peerSeesA, bSeesPeer string) (controlErr, fanoutErr error) {
	if err := devicesAtDebugLevel(devB); err != nil {
		notAsserted := fmt.Errorf("not asserted: %w", err)
		return notAsserted, notAsserted
	}
	caption := "file fan-out " + nextCorrID()
	res, err := sendFile(peer, devA, peerSeesA, caption)
	if err != nil {
		return fmt.Errorf("peer send to %s failed: %w", devA.name, err),
			fmt.Errorf("not asserted: the control send did not land")
	}
	if _, err := readFileRowBody(devA, aSeesPeer, res.msgID); err != nil {
		return fmt.Errorf("could not read %s's own copy: %w", devA.name, err),
			fmt.Errorf("not asserted: the control device's own transfer is unproven")
	}

	bChatID, err := chatIDForPeer(devB, bSeesPeer, syncConvergeTimeout)
	if err != nil {
		return nil, err
	}

	logf("[%s] waiting to hold the bytes itself — the peer's offer addresses every device of mine it can reach, and nothing here asks %s to sync", devB.name, devB.name)
	if err := awaitOwnSealedCopy(devB, bChatID, res.msgID, syncConvergeTimeout, res.payload); err != nil {
		return nil, err
	}

	if rows, rErr := timelineRows(devB, bSeesPeer, syncConvergeTimeout); rErr != nil {
		return nil, fmt.Errorf("could not read %s's timeline: %w", devB.name, rErr)
	} else if _, held := rows[res.msgID]; !held {
		return nil, fmt.Errorf("%s holds the bytes for msg_id=%s but shows no row for it — the file is unreachable from the conversation", devB.name, short(res.msgID))
	}

	if err := awaitContactFetch(devB, res.msgID, pathLineTimeout); err != nil {
		return nil, err
	}
	lines, err := rigLogLines(devB)
	if err != nil {
		return nil, err
	}
	if err := noSiblingSeal(devB, lines, res.msgID); err != nil {
		return nil, err
	}
	return nil, nil
}

func contactFetched(lines []string, msgID string) bool {
	want := logMatchID("msg_id", msgID)
	for _, line := range lines {
		if hasLogMsg(line, fetchReadyLine) && strings.Contains(line, want) {
			return true
		}
	}
	return false
}

func awaitContactFetch(r *rig, msgID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		lines, err := rigLogLines(r)
		if err != nil {
			return err
		}
		if contactFetched(lines, msgID) {
			return nil
		}
		if time.Now().After(deadline) {
			routes := sealRoutes(lines, msgID)
			return fmt.Errorf("%s holds the bytes for msg_id=%s, but its log has no %q line for it after %s — the contact's own offer did not bring them (sibling seal routes seen: %d %v)",
				r.name, short(msgID), fetchReadyLine, timeout, len(routes), routes)
		}
		time.Sleep(syncPollInterval)
	}
}

func noSiblingSeal(r *rig, lines []string, msgID string) error {
	if routes := sealRoutes(lines, msgID); len(routes) > 0 {
		return fmt.Errorf("%s sealed msg_id=%s through a sibling offer (route=%s), not through the contact's own offer — this case did not measure direct delivery",
			r.name, short(msgID), strings.Join(routes, ","))
	}
	return nil
}

func assertSiblingFileRecovery(devA, devB, peer *rig, aSeesPeer, peerSeesA, peerSeesB, bSeesPeer string) (controlErr, siblingErr error) {
	if err := devicesAtDebugLevel(devA, devB); err != nil {
		notAsserted := fmt.Errorf("not asserted: %w", err)
		return notAsserted, notAsserted
	}
	logf("[%s] going down — the peer's file must reach it by no route but gap-fill, so its own offer has to dead-letter before it comes back", devB.name)
	if err := devB.stop(); err != nil {
		return fmt.Errorf("could not take %s down: %w", devB.name, err), nil
	}

	caption := "sibling recovery " + nextCorrID()
	res, sendErr := sendFile(peer, devA, peerSeesA, caption)
	if sendErr != nil {
		if err := devB.start(); err != nil {
			logf("[%s] could not bring %s back up after a failed control send: %v", devA.name, devB.name, err)
		}
		return fmt.Errorf("peer send to %s failed: %w", devA.name, sendErr),
			fmt.Errorf("not asserted: the control send did not land")
	}

	res.caption = caption
	if _, rowErr := readFileRowBody(devA, aSeesPeer, res.msgID); rowErr != nil {
		if err := devB.start(); err != nil {
			logf("[%s] could not bring %s back up after a failed read: %v", devA.name, devB.name, err)
		}
		return fmt.Errorf("could not read %s's own copy: %w", devA.name, rowErr),
			fmt.Errorf("not asserted: %s holds no row of the file to serve", devA.name)
	}

	bringBack := func() {
		if sErr := devB.start(); sErr != nil {
			logf("[%s] could not bring %s back up: %v", devA.name, devB.name, sErr)
		}
	}
	envIDs, envErr := outboundEnvelopeIDs(peer, peerSeesA, res.msgID, syncConvergeTimeout)
	if envErr != nil {
		bringBack()
		return nil, envErr
	}

	if len(envIDs) < 2 {
		bringBack()
		return nil, fmt.Errorf("%s's row for msg_id=%s names %d envelope(s), want one per device — the offer never addressed %s, so gap-fill is not what this gate would be measuring",
			peer.name, short(res.msgID), len(envIDs), devB.name)
	}
	bEnvID, bErr := enqueuedEnvelopeFor(peer, envIDs, peerSeesB)
	if bErr != nil {
		bringBack()
		return nil, bErr
	}
	logf("[%s] envelope %s is the one it enqueued for %s — that is the route this gate has to see close", peer.name, short(bEnvID), devB.name)
	if err := waitForEnvelopeGiveUp(peer, bEnvID); err != nil {
		bringBack()
		return nil, fmt.Errorf("could not close off the peer's direct offer to %s before bringing it back: %w", devB.name, err)
	}

	logf("[%s] coming back up — nobody will ask it to sync", devB.name)
	if err := devB.start(); err != nil {
		return nil, fmt.Errorf("could not bring %s back up: %w", devB.name, err)
	}

	if rows, rErr := timelineRows(devB, bSeesPeer, syncConvergeTimeout); rErr == nil {
		if _, held := rows[res.msgID]; held {
			return nil, fmt.Errorf("%s holds msg_id=%s without syncing even though it was down — this gate no longer isolates gap-fill", devB.name, short(res.msgID))
		}
	}

	return nil, awaitFileOnSibling(devA, devB, bSeesPeer, res, sealRouteGapFill, events.DirIn)
}

func sendFileNoWait(from *rig, toPeerID, caption string) (fileResult, error) {
	path := filepath.Join(from.root, "rig-send-"+nextCorrID()+".bin")
	payload := []byte("haoma-rig file payload " + nextCorrID() + " — sent while the contact is away")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		return fileResult{}, fmt.Errorf("write temp file: %w", err)
	}
	fromSub, cancel := from.subscribe()
	defer cancel()
	id, err := from.sendReq(ipc.FrameSendFile, ipc.SendFileRequest{PeerID: toPeerID, Path: path, Caption: caption})
	if err != nil {
		return fileResult{}, fmt.Errorf("send_file: %w", err)
	}
	sf, err := awaitFrame(fromSub, fileSentTimeout, func(f ipc.Frame) bool {
		return f.ID == id && (f.Type == ipc.FrameFileSent || f.Type == ipc.FrameError)
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
		return fileResult{}, fmt.Errorf("file_sent named no msg_id")
	}
	return fileResult{msgID: resp.MsgID, caption: caption, payload: payload}, nil
}

const siblingSealedLine = "sibling offer: file sealed"

const (
	sealRouteSend    = "send"
	sealRouteGapFill = "gapfill"
)

const fileServedLine = "file request: served"

const (
	fileRequestSentLine     = "file request: sent"
	fileRequestIDsLine      = "file request: files asked for"
	fileNotServedLine       = "file request: not served"
	fileRequestIgnoredLine  = "file request: ignored by the seeded fault; nothing is served"
	gapDownloadFailedPrefix = "sibling offer on gap-fill: download failed"

	siblingFetchEventLine = "sibling fetch: haomad event"
)

const (
	torSendStall = 60 * time.Second

	outboxRetryDelay = 30 * time.Second

	fileRequestSpacing = 60 * time.Second

	gapFillSendHops = 2

	fetchAllowance = 90 * time.Second

	gapFillFileTimeout = restartConvergeTimeout + fileRequestSpacing + gapFillSendHops*(torSendStall+outboxRetryDelay) + fetchAllowance

	pathLineTimeout = 30 * time.Second
)

func awaitFileOnSibling(devA, devB *rig, bSeesPeer string, res fileResult, route string, dir events.Direction) error {
	timeout := fileReadyTimeout
	if route == sealRouteGapFill {
		timeout = gapFillFileTimeout
	}
	return awaitFileOnSiblingWithin(devA, devB, bSeesPeer, res, route, dir, timeout)
}

func awaitFileOnSiblingWithin(devA, devB *rig, bSeesPeer string, res fileResult, route string, dir events.Direction, timeout time.Duration) error {
	why := "the sending device's sibling offer at send is the only route that can bring them"
	if route == sealRouteGapFill {
		why = fmt.Sprintf("its own sweep asks %s for the file, and a sibling offer on gap-fill is the only route left", devA.name)
	}
	bChatID, err := chatIDForPeer(devB, bSeesPeer, syncConvergeTimeout)
	if err != nil {
		return err
	}
	logf("[%s] waiting up to %s to hold the bytes of msg_id=%s itself — %s", devB.name, timeout, short(res.msgID), why)
	if err := awaitOwnSealedCopy(devB, bChatID, res.msgID, timeout, res.payload); err != nil {
		if route == sealRouteGapFill {
			return fmt.Errorf("%w. What the logs show: %s", err, gapFillPathSeen(devA, devB, res.msgID))
		}
		return err
	}

	if err := awaitSealRoute(devB, res.msgID, route, pathLineTimeout); err != nil {
		return err
	}
	if route == sealRouteGapFill {
		if err := awaitFileServed(devA, devB, res.msgID, pathLineTimeout); err != nil {
			return err
		}
	}

	rows, err := timelineRows(devB, bSeesPeer, syncConvergeTimeout)
	if err != nil {
		return fmt.Errorf("could not read %s's timeline: %w", devB.name, err)
	}
	ev, held := rows[res.msgID]
	if !held {
		return fmt.Errorf("%s holds the bytes for msg_id=%s but shows no row for it — the file is unreachable from the conversation", devB.name, short(res.msgID))
	}
	if ev.Direction != dir {
		return fmt.Errorf("%s's row for msg_id=%s is %s, want %s", devB.name, short(res.msgID), ev.Direction, dir)
	}
	var b fileRowBody
	if err := json.Unmarshal(ev.Body, &b); err != nil {
		return fmt.Errorf("decode %s's file body: %w", devB.name, err)
	}
	if b.State != "ready" {
		return fmt.Errorf("%s holds the bytes for msg_id=%s but its row reads %q, want ready", devB.name, short(res.msgID), b.State)
	}
	if b.Caption != res.caption {
		return fmt.Errorf("%s's row for msg_id=%s shows caption %q, want %q", devB.name, short(res.msgID), b.Caption, res.caption)
	}
	if route == sealRouteGapFill && (b.Token != "" || b.UrlPath != "") {
		return fmt.Errorf("%s's row for msg_id=%s carries a capability (token set=%t url_path set=%t) — gap-fill carried %s's own token across",
			devB.name, short(res.msgID), b.Token != "", b.UrlPath != "", devA.name)
	}
	logf("[%s] sealed msg_id=%s by route=%s, row %s at ready, caption intact", devB.name, short(res.msgID), route, dir)
	return nil
}

func sealRoutes(lines []string, msgID string) []string {
	want := logMatchID("msg_id", msgID)
	var routes []string
	for _, line := range lines {
		if !hasLogMsg(line, siblingSealedLine) || !strings.Contains(line, want) {
			continue
		}
		route, ok := logFieldValue(line, "route")
		if !ok || route == "" {
			route = "(none)"
		}
		routes = append(routes, route)
	}
	return routes
}

func checkSealRoute(r *rig, lines []string, msgID, want string) (found bool, err error) {
	routes := sealRoutes(lines, msgID)
	for _, got := range routes {
		if got == want {
			continue
		}
		var meaning string
		switch {
		case want == sealRouteGapFill:
			meaning = "the route this case held out was not closed, so it did not measure gap-fill"
		case got == sealRouteGapFill:

			meaning = "gap-fill brought the file before the sibling offer at send was taken. " +
				"With HAOMA_FAULT_IGNORE_SIBLING_OFFER off this is a race, not a fault: a gap-fill walk ended between the send and the offer's arrival. " +
				"Run again before calling it a fault; the offer at send was not measured"
		default:
			meaning = "a sibling offer that is not the offer at send brought the file, so this case did not measure the offer at send"
		}
		return true, fmt.Errorf("%s sealed msg_id=%s by route=%s, want route=%s — %s", r.name, short(msgID), got, want, meaning)
	}
	return len(routes) > 0, nil
}

func awaitSealRoute(r *rig, msgID, want string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		lines, err := rigLogLines(r)
		if err != nil {
			return err
		}
		found, err := checkSealRoute(r, lines, msgID, want)
		if err != nil {
			return err
		}
		if found {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s holds the bytes for msg_id=%s but its log has no %q line for it after %s — something other than a sibling offer brought them",
				r.name, short(msgID), siblingSealedLine, timeout)
		}
		time.Sleep(syncPollInterval)
	}
}

func fileServed(lines []string, msgID string) bool {
	want := logMatchID("msg_id", msgID)
	for _, line := range lines {
		if !hasLogMsg(line, fileServedLine) || !strings.Contains(line, want) {
			continue
		}
		if outcome, _ := logFieldValue(line, "outcome"); outcome == "served" || outcome == "resent" {
			return true
		}
	}
	return false
}

func awaitFileServed(devA, devB *rig, msgID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		lines, err := rigLogLines(devA)
		if err != nil {
			return err
		}
		if fileServed(lines, msgID) {
			logf("[%s] served the file request for msg_id=%s", devA.name, short(msgID))
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s sealed msg_id=%s by gap-fill, but %s's log has no %q line for it after %s — the responder did not serve it. %s",
				devB.name, short(msgID), devA.name, fileServedLine, timeout, gapFillPathSeen(devA, devB, msgID))
		}
		time.Sleep(syncPollInterval)
	}
}

func gapFillPathSeen(devA, devB *rig, msgID string) string {
	var seen []string
	prefix := msgID
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	want := logMatchID("msg_id", msgID)

	if lines, err := rigLogLines(devB); err != nil {
		seen = append(seen, fmt.Sprintf("%s's log could not be read: %v", devB.name, err))
	} else {
		sent, named, failed, transient := 0, false, 0, 0
		for _, line := range lines {
			switch {
			case hasLogMsg(line, fileRequestSentLine):
				sent++
			case hasLogMsg(line, fileRequestIDsLine) && strings.Contains(line, prefix):
				named = true
			case strings.Contains(line, gapDownloadFailedPrefix) && strings.Contains(line, want):
				failed++
			case hasLogMsg(line, siblingFetchEventLine) && strings.Contains(line, want):
				if state, _ := logFieldValue(line, "state"); state == "failed_transient" {
					transient++
				}
			}
		}
		seen = append(seen, fmt.Sprintf("%s sent %d file request(s) in the run, one named this file: %t, gap-fill downloads of it failed: %d",
			devB.name, sent, named, failed))
		if transient > 0 {

			seen = append(seen, fmt.Sprintf("%s's fetch of it failed_transient %d time(s) — haomad does not retry that; the fetch waits for the next backend-ready, not a fault in the take",
				devB.name, transient))
		}
		if routes := sealRoutes(lines, msgID); len(routes) > 0 {
			seen = append(seen, fmt.Sprintf("%s sealed it by route=%s", devB.name, strings.Join(routes, ",")))
		} else {
			seen = append(seen, fmt.Sprintf("%s never sealed it", devB.name))
		}
	}

	if lines, err := rigLogLines(devA); err != nil {
		seen = append(seen, fmt.Sprintf("%s's log could not be read: %v", devA.name, err))
	} else {
		var outcomes []string
		ignored := 0
		for _, line := range lines {
			switch {
			case hasLogMsg(line, fileRequestIgnoredLine):
				ignored++
			case (hasLogMsg(line, fileServedLine) || hasLogMsg(line, fileNotServedLine)) && strings.Contains(line, want):
				outcome, _ := logFieldValue(line, "outcome")
				outcomes = append(outcomes, outcome)
			}
		}
		switch {
		case len(outcomes) > 0:
			seen = append(seen, fmt.Sprintf("%s's outcomes for it: %s", devA.name, strings.Join(outcomes, ",")))
		default:
			seen = append(seen, fmt.Sprintf("%s logged no serve of it", devA.name))
		}
		if ignored > 0 {
			seen = append(seen, fmt.Sprintf("%s ignored %d file request(s) by the seeded fault", devA.name, ignored))
		}
	}
	return strings.Join(seen, "; ")
}

func hasLogMsg(line, msg string) bool {
	return strings.Contains(line, "msg="+strconv.Quote(msg))
}

const siblingOfferSentLine = "mirror: sibling offer sent"

func siblingOfferEnvelope(r *rig, msgID string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		env, err := findSiblingOfferEnvelope(r, msgID)
		if err == nil {
			return env, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("after %s: %w", timeout, err)
		}
		time.Sleep(syncPollInterval)
	}
}

func findSiblingOfferEnvelope(r *rig, msgID string) (string, error) {
	lines, err := rigLogLines(r)
	if err != nil {
		return "", err
	}
	want := logMatchID("msg_id", msgID)
	var env, sibling string
	n := 0
	for _, line := range lines {
		if !hasLogMsg(line, siblingOfferSentLine) || !strings.Contains(line, want) {
			continue
		}
		n++
		env, _ = logFieldValue(line, "envelope_id")
		sibling, _ = logFieldValue(line, "sibling_peer_id")
	}
	switch {
	case n == 0:
		return "", fmt.Errorf("%s's log has no %q line for msg_id=%s — the sibling offer at send was not handed to haomad, or the log is not at DEBUG", r.name, siblingOfferSentLine, short(msgID))
	case n > 1:
		return "", fmt.Errorf("%s's log has %d %q lines for msg_id=%s, want one — one sibling, one send", r.name, n, siblingOfferSentLine, short(msgID))
	case env == "" || sibling == "":
		return "", fmt.Errorf("%s's %q line for msg_id=%s names no envelope id or no sibling", r.name, siblingOfferSentLine, short(msgID))
	}

	dlines, err := daemonLogLines(r)
	if err != nil {
		return "", err
	}
	enqueued := 0
	var peer string
	for _, line := range dlines {
		if !strings.Contains(line, "/send enqueuing envelope") {
			continue
		}
		if got, _ := logFieldValue(line, "envelope_id"); got != env {
			continue
		}
		enqueued++
		peer, _ = logFieldValue(line, "peer_id")
	}
	switch {
	case enqueued == 0:
		return "", fmt.Errorf("%s's haomad log has no enqueue line for envelope %s — the harness cannot see what haomad did (log level, a rotation, or the id form on disk)", r.name, env)
	case enqueued > 1:
		return "", fmt.Errorf("%s's haomad log has %d enqueue lines for envelope %s — another envelope shares its short id, so this refuses to guess", r.name, enqueued, env)
	case peer != sibling:
		return "", fmt.Errorf("%s's haomad enqueued envelope %s for peer %s, but haoma sent the sibling offer to %s — the two logs disagree", r.name, env, peer, sibling)
	}
	return env, nil
}

func assertSentFileOnSibling(devA, devB, peer *rig, aSeesPeer, bSeesPeer string) (controlErr, siblingErr, keyErr error) {
	caption := "a file I sent " + nextCorrID()
	res, err := sendFile(devA, peer, aSeesPeer, caption)
	if err != nil {
		notAsserted := fmt.Errorf("not asserted: the control send did not land")
		return fmt.Errorf("send from %s to the contact failed: %w", devA.name, err), notAsserted, notAsserted
	}

	res.caption = caption
	return nil, awaitFileOnSibling(devA, devB, bSeesPeer, res, sealRouteSend, events.DirOut), res.keyWithDownload
}

func assertSentFileOnSiblingContactOffline(devA, devB, peer *rig, aSeesPeer, bSeesPeer string) (controlErr, siblingErr error) {
	logf("[%s] going down — the contact is away for this send, so nothing it could answer can help my other device", peer.name)
	if err := peer.stop(); err != nil {
		return fmt.Errorf("could not take %s down: %w", peer.name, err), fmt.Errorf("not asserted: the contact never went down")
	}
	restored := false
	restorePeer := func() {
		if restored {
			return
		}
		restored = true
		logf("[%s] coming back up", peer.name)
		if err := peer.start(); err != nil {
			logf("[%s] could not bring %s back up: %v", devA.name, peer.name, err)
		}
	}
	defer restorePeer()

	caption := "a file I sent while the contact was away " + nextCorrID()
	res, err := sendFileNoWait(devA, aSeesPeer, caption)
	if err != nil {
		return fmt.Errorf("send from %s failed locally: %w", devA.name, err),
			fmt.Errorf("not asserted: the control send did not land")
	}
	aChatID, err := chatIDForPeer(devA, aSeesPeer, syncConvergeTimeout)
	if err != nil {
		return err, fmt.Errorf("not asserted: %w", err)
	}
	if err := awaitOwnSealedCopy(devA, aChatID, res.msgID, syncConvergeTimeout, res.payload); err != nil {
		return fmt.Errorf("%s does not hold what it sent: %w", devA.name, err),
			fmt.Errorf("not asserted: the sending device's own copy is unproven")
	}

	siblingErr = awaitFileOnSibling(devA, devB, bSeesPeer, res, sealRouteSend, events.DirOut)

	envIDs, err := outboundEnvelopeIDs(devA, aSeesPeer, res.msgID, syncConvergeTimeout)
	if err != nil {
		logf("[%s] could not read the envelope ids of msg_id=%s, so its offer to the contact may still be in flight: %v", devA.name, short(res.msgID), err)
		return nil, siblingErr
	}
	for _, id := range envIDs {
		if gErr := waitForEnvelopeGiveUp(devA, id); gErr != nil {
			logf("[%s] its offer to the contact did not dead-letter while the contact was away; a later dead-letter count may include it: %v", devA.name, gErr)
		}
	}
	restorePeer()
	return nil, siblingErr
}

func assertSentFileOnSiblingByGapFill(devA, devB, peer *rig, aSeesPeer, bSeesPeer string) (controlErr, siblingErr error) {
	if err := devicesAtDebugLevel(devA, devB); err != nil {
		notAsserted := fmt.Errorf("not asserted: %w", err)
		return notAsserted, notAsserted
	}
	logf("[%s] going down — the sibling offer at send must give up on the rail, so the file can reach it by no route but gap-fill", devB.name)
	if err := devB.stop(); err != nil {
		return fmt.Errorf("could not take %s down: %w", devB.name, err), fmt.Errorf("not asserted: %s never went down", devB.name)
	}
	up := false
	bringBack := func() error {
		if up {
			return nil
		}
		up = true
		logf("[%s] coming back up — nobody will ask it to sync", devB.name)
		return devB.start()
	}
	defer func() {
		if err := bringBack(); err != nil {
			logf("[%s] could not bring %s back up: %v", devA.name, devB.name, err)
		}
	}()

	caption := "a file I sent while my other device was away " + nextCorrID()
	res, err := sendFile(devA, peer, aSeesPeer, caption)
	if err != nil {
		return fmt.Errorf("send from %s to the contact failed: %w", devA.name, err),
			fmt.Errorf("not asserted: the control send did not land")
	}

	res.caption = caption

	env, err := siblingOfferEnvelope(devA, res.msgID, syncConvergeTimeout)
	if err != nil {
		return nil, fmt.Errorf("could not name the rail envelope of the sibling offer at send: %w", err)
	}
	logf("[%s] envelope %s carries the sibling offer at send to %s — that is the route this case has to see close", devA.name, env, devB.name)
	if err := waitForLoggedEnvelopeGiveUp(devA, env); err != nil {
		return nil, fmt.Errorf("could not close off the sibling offer at send before bringing %s back: %w", devB.name, err)
	}

	if err := bringBack(); err != nil {
		return nil, fmt.Errorf("could not bring %s back up: %w", devB.name, err)
	}
	return nil, awaitFileOnSibling(devA, devB, bSeesPeer, res, sealRouteGapFill, events.DirOut)
}
