package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/logid"
)

const rowFirstEnvelopePushLine = "delivery_status: pushed under the row's first envelope"

const foldDeliveryTimeout = 3 * deliveryTimeout

func foldFirstDevice(chats []ipc.ChatEntry, peerSeesA, peerSeesB string) (firstIsA bool, chatPeerID string, err error) {
	var found []ipc.ChatEntry
	for _, c := range chats {
		if slices.Contains(c.Members, peerSeesA) && slices.Contains(c.Members, peerSeesB) {
			found = append(found, c)
		}
	}
	switch len(found) {
	case 0:
		return false, "", fmt.Errorf("the peer holds no conversation with both of my devices (%s, %s) — its text would go to one device, and this gate would test nothing",
			short(peerSeesA), short(peerSeesB))
	case 1:
	default:
		return false, "", fmt.Errorf("the peer holds %d conversations with both of my devices, want one", len(found))
	}
	c := found[0]
	switch c.PeerID {
	case peerSeesA:
		return true, c.PeerID, nil
	case peerSeesB:
		return false, c.PeerID, nil
	}
	return false, "", fmt.Errorf("the peer's folded conversation names %s first, which is neither of my devices (%s, %s)",
		short(c.PeerID), short(peerSeesA), short(peerSeesB))
}

type foldDeliverySeen struct {
	sentEnvID string
	row       events.Event
	framed    bool
	lines     []string
}

func foldDeliveryVerdict(s foldDeliverySeen) error {
	if s.sentEnvID == "" || s.row.EnvelopeID != s.sentEnvID {
		return fmt.Errorf("text_sent named envelope %s, but the peer's row starts with %s — the push this gate waits for is not under the row's first id",
			logid.Short(s.sentEnvID), logid.Short(s.row.EnvelopeID))
	}
	if len(s.row.EnvelopeIDs) != 2 {
		return fmt.Errorf("the peer's row holds %d envelope(s), want 2 — the text did not go to both of my devices", len(s.row.EnvelopeIDs))
	}
	if !s.framed {
		return fmt.Errorf("no delivery_status \"sent\" under the row's first envelope %s while the first device was down — the marker stays grey until the person reads it",
			logid.Short(s.sentEnvID))
	}
	want := lineWant{rowFirstEnvelopePushLine, map[string]string{
		"envelope_id":     logid.Short(s.row.EnvelopeIDs[1]),
		"row_envelope_id": logid.Short(s.sentEnvID),
		"state":           events.DeliverySent,
	}}
	if countMatching(s.lines, want) == 0 {
		return fmt.Errorf("the peer pushed \"sent\" under the first envelope, but its log holds no %s — the push did not come from the second device's ack",
			want)
	}
	if !events.DeliveryOutranks(events.DeliveryEnqueued, s.row.DeliveryState) {
		return fmt.Errorf("the peer's row is at %q, want sent or higher", s.row.DeliveryState)
	}
	return nil
}

func assertFoldDelivery(devA, devB, peer *rig, aSeesPeer, bSeesPeer, peerSeesA, peerSeesB string) (err error) {
	if !strings.EqualFold(peer.logLevel, "debug") {
		return fmt.Errorf("the peer runs at -log-level=%q; this group reads a DEBUG line", peer.logLevel)
	}
	chats, err := listChats(peer, syncConvergeTimeout)
	if err != nil {
		return fmt.Errorf("could not read %s's chat list: %w", peer.name, err)
	}
	firstIsA, chatPeerID, err := foldFirstDevice(chats, peerSeesA, peerSeesB)
	if err != nil {
		return err
	}
	first, second, secondSeesPeer := devA, devB, bSeesPeer
	if !firstIsA {
		first, second, secondSeesPeer = devB, devA, aSeesPeer
	}
	logf("[%s] %s is first in the fan-out list of the folded conversation; %s stays up", peer.name, first.name, second.name)

	if err := primePeerSessionToward(peer, second, secondSeesPeer); err != nil {
		return err
	}

	logf("[%s] going down — the peer's text must be acked by %s alone", first.name, second.name)
	if err := first.stop(); err != nil {
		return fmt.Errorf("could not take %s down: %w", first.name, err)
	}
	mark := haomaLogMark(peer)
	var envID string
	defer func() {
		logf("[%s] coming back up", first.name)
		if sErr := first.start(); sErr != nil {
			sErr = fmt.Errorf("could not bring %s back up: %w", first.name, sErr)
			logf("[%s] %v", peer.name, sErr)
			err = errors.Join(err, sErr)
			return
		}
		if envID != "" {
			awaitFirstEnvelopeSent(peer, first, mark, envID)
		}
	}()

	sub, cancel := peer.subscribe()
	defer cancel()
	msgID, envID, err := sendTextNoWait(peer, chatPeerID, "fold delivery "+nextCorrID())
	if err != nil {
		return fmt.Errorf("send from %s failed: %w", peer.name, err)
	}
	logf("[%s] sent msg_id=%s, first envelope %s — waiting for \"sent\" under it", peer.name, short(msgID), logid.Short(envID))

	framed := awaitOK(sub, foldDeliveryTimeout, func(f ipc.Frame) bool {
		if f.Type != ipc.FrameDeliveryStatus {
			return false
		}
		var p ipc.DeliveryStatusPayload
		if json.Unmarshal(f.Payload, &p) != nil {
			return false
		}
		return p.EnvelopeID == envID && p.State == events.DeliverySent
	})

	rows, err := timelineRows(peer, chatPeerID, syncConvergeTimeout)
	if err != nil {
		return fmt.Errorf("could not read %s's timeline: %w", peer.name, err)
	}
	row, ok := rows[msgID]
	if !ok {
		return fmt.Errorf("%s holds no row for msg_id=%s", peer.name, short(msgID))
	}
	lines, err := waitForLine(mark, framed)
	if err != nil {
		return fmt.Errorf("could not read %s's haoma log: %w", peer.name, err)
	}
	return foldDeliveryVerdict(foldDeliverySeen{sentEnvID: envID, row: row, framed: framed, lines: lines})
}

func waitForLine(mark logMark, framed bool) ([]string, error) {
	lines, err := mark.lines()
	if err != nil || !framed || countMatching(lines, lineWant{rowFirstEnvelopePushLine, nil}) > 0 {
		return lines, err
	}
	rigSleep(2*time.Second, "let the peer's log reach the disk")
	return mark.lines()
}

const envelopeReportLine = "delivery.state-changed via SSE"

const firstEnvelopeSentTimeout = 3 * time.Minute

func awaitFirstEnvelopeSent(peer, first *rig, mark logMark, envID string) {
	want := lineWant{envelopeReportLine, map[string]string{
		"envelope_id": logid.Short(envID),
		"state":       events.DeliverySent,
	}}
	deadline := time.Now().Add(firstEnvelopeSentTimeout)
	for {
		lines, err := mark.lines()
		if err == nil && countMatching(lines, want) > 0 {
			logf("[%s] the envelope to %s reached sent", peer.name, first.name)
			return
		}
		if time.Now().After(deadline) {
			logf("[%s] the envelope to %s did not reach sent within %s; going on (not a verdict of this group)",
				peer.name, first.name, firstEnvelopeSentTimeout)
			return
		}
		rigSleep(5*time.Second, "poll: envelope to the device that was down")
	}
}
