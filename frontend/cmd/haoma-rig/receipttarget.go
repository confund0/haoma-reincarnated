package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/logid"
)

const (
	receiptGroupedLine  = "read receipt: targets grouped by the device that sent them"
	receiptShippedLine  = "read receipt shipped"
	receiptAckLine      = "read receipt acknowledges this message"
	receiptRecordedLine = "read receipt addressed to the device that sent the message"
	receiptFallbackLine = "read receipt addressed to the chat's stored device"
	inboxDecryptedLine  = "inbox decrypted"
)

const receiptSourceRecorded = "recorded_at_ingest"

const (
	fieldReceiptMsgID   = "receipt_msg_id"
	fieldReceiptGroups  = "groups"
	fieldRoutingDevices = "routing_devices"
	fieldSource         = "source"
	fieldPeerID         = "peer_id"
	fieldMsgID          = "msg_id"
	fieldChatID         = "chat_id"
)

const receiptShipTimeout = 60 * time.Second

const receiptShipPollInterval = 2 * time.Second

const receiptReadTimeout = 90 * time.Second

type receiptSender struct {
	device string
	peerID string
	msgID  string
}

var receiptTargetVerdicts = []string{
	"receipt target premise",
	"receipt one envelope per sending device",
	"receipt target recorded at ingest",
	"receipt read state control",
	"receipt read state second device",
}

func assertReceiptTargets(devA, devB, peer *rig, aSeesPeer, bSeesPeer, peerSeesA, peerSeesB string) map[string]error {
	out := map[string]error{}
	failAll := func(err error) map[string]error {
		for _, n := range receiptTargetVerdicts {
			out[n] = err
		}
		return out
	}

	if err := devicesAtDebugLevel(peer); err != nil {
		return failAll(err)
	}
	if peerSeesA == peerSeesB {
		return failAll(fmt.Errorf("%s holds one routing id (%s) for both of my devices — one envelope would then be the right answer, so this gate cannot measure the target choice",
			peer.name, short(peerSeesA)))
	}

	if err := primePeerSessionToward(peer, devB, bSeesPeer); err != nil {
		return failAll(err)
	}

	dA, err := deliverText(devA, peer, aSeesPeer, "receipt target from "+devA.name+" "+nextCorrID())
	if err != nil {
		return failAll(fmt.Errorf("%s could not deliver its own text to %s: %w", devA.name, peer.name, err))
	}
	dB, err := deliverText(devB, peer, bSeesPeer, "receipt target from "+devB.name+" "+nextCorrID())
	if err != nil {
		return failAll(fmt.Errorf("%s could not deliver its own text to %s: %w", devB.name, peer.name, err))
	}
	if dA.chatID != dB.chatID {
		return failAll(fmt.Errorf("%s put the two texts in two conversations (%s and %s) — one reading cannot cover both, so two envelopes would say nothing about the target choice",
			peer.name, short(dA.chatID), short(dB.chatID)))
	}
	chatID := dA.chatID
	senders := []receiptSender{
		{device: devA.name, peerID: peerSeesA, msgID: dA.msgID},
		{device: devB.name, peerID: peerSeesB, msgID: dB.msgID},
	}

	before, err := rigLogLines(peer)
	if err != nil {
		return failAll(err)
	}
	if err := assertTwoSendingDevices(peer.name, before, senders); err != nil {
		return failAll(err)
	}
	out["receipt target premise"] = nil
	logf("[%s] holds one conversation %s carrying a message from each of my devices — %s from %s, %s from %s",
		peer.name, short(chatID), short(dA.msgID), devA.name, short(dB.msgID), devB.name)

	notAsserted := func(err error) map[string]error {
		for _, n := range receiptTargetVerdicts[1:] {
			out[n] = err
		}
		return completeReceiptVerdicts(out)
	}

	beforeAcks := len(parseReceiptAcks(before))
	beforeShipped := len(parseReceiptShipments(before))
	beforeGrouped := len(receiptGroupedLines(before, chatID))

	subA, cancelA := devA.subscribe()
	defer cancelA()
	subB, cancelB := devB.subscribe()
	defer cancelB()

	marked, err := deviceReadsRow(peer, chatID, readTimeout)
	if err != nil {
		return notAsserted(fmt.Errorf("mark-read on %s: %w", peer.name, err))
	}
	if marked < len(senders) {
		return notAsserted(fmt.Errorf("%s stamped %d row(s) read, want at least %d — the two texts this driver sent were not both waiting to be read, so the reading did not cover them",
			peer.name, marked, len(senders)))
	}
	logf("[%s] read conversation %s once, stamping %d row(s)", peer.name, short(chatID), marked)

	after, acks, awaitErr := awaitReceiptAcks(peer, beforeAcks, senders, receiptShipTimeout)

	out["receipt one envelope per sending device"] = func() error {
		if awaitErr != nil {
			return awaitErr
		}
		shipments := freshShipments(after, beforeShipped)
		if err := assertOneEnvelopePerSender(acks, shipments, senders); err != nil {
			return err
		}

		if err := assertReceiptGrouping(peer.name, beforeGrouped, receiptGroupedLines(after, chatID), len(senders)); err != nil {
			return err
		}

		logf("[%s] shipped one receipt envelope per sending device: %s (only the two messages this driver sent are judged; the rest of the reading is counted, not checked)",
			peer.name, describeShipments(shipments))
		return nil
	}()

	out["receipt target recorded at ingest"] = func() error {
		if awaitErr != nil {
			return awaitErr
		}
		if err := assertReceiptTargetRecorded(peer.name, after, senders); err != nil {
			return err
		}
		logf("[%s] picked both receipt targets from the sending device recorded on the row (%s=%s)", peer.name, fieldSource, receiptSourceRecorded)
		return nil
	}()

	out["receipt read state control"] = awaitOwnRowRead(subA, devA, dA.msgID)
	out["receipt read state second device"] = awaitOwnRowRead(subB, devB, dB.msgID)
	return completeReceiptVerdicts(out)
}

func completeReceiptVerdicts(out map[string]error) map[string]error {
	for _, n := range receiptTargetVerdicts {
		if _, ok := out[n]; !ok {
			out[n] = fmt.Errorf("assertReceiptTargets: verdict %q was never set — a defect in the driver itself", n)
		}
	}
	return out
}

func awaitOwnRowRead(sub <-chan ipc.Frame, r *rig, msgID string) error {
	_, err := awaitFrame(sub, receiptReadTimeout, func(f ipc.Frame) bool {
		ev, ok := timelineEvent(f)
		return ok && ev.MsgID == msgID && ev.Direction == events.DirOut && ev.DeliveryState == events.DeliveryRead
	})
	if err != nil {
		return fmt.Errorf("%s never saw its own row msg_id=%s reach %q — it sent that message and only a receipt addressed to %s moves its copy: %w",
			r.name, short(msgID), events.DeliveryRead, r.name, err)
	}
	return nil
}

type receiptAck struct {
	line         string
	msgID        string
	peerID       string
	receiptMsgID string
}

type receiptShipment struct {
	line         string
	peerID       string
	receiptMsgID string
	targets      string
}

func parseReceiptAcks(lines []string) []receiptAck {
	var out []receiptAck
	for _, line := range lines {
		if !strings.Contains(line, receiptAckLine) {
			continue
		}
		a := receiptAck{line: line}
		a.msgID, _ = logAttrValue(line, fieldMsgID)
		a.peerID, _ = logAttrValue(line, fieldPeerID)
		a.receiptMsgID, _ = logAttrValue(line, fieldReceiptMsgID)
		out = append(out, a)
	}
	return out
}

func parseReceiptShipments(lines []string) []receiptShipment {
	var out []receiptShipment
	for _, line := range lines {
		if !strings.Contains(line, receiptShippedLine) {
			continue
		}
		s := receiptShipment{line: line}
		s.peerID, _ = logAttrValue(line, fieldPeerID)
		s.receiptMsgID, _ = logAttrValue(line, fieldReceiptMsgID)
		s.targets, _ = logAttrValue(line, "targets")
		out = append(out, s)
	}
	return out
}

func freshShipments(lines []string, baseline int) []receiptShipment {
	all := parseReceiptShipments(lines)
	if baseline <= len(all) {
		return all[baseline:]
	}
	return all
}

func describeShipments(shipments []receiptShipment) string {
	if len(shipments) == 0 {
		return "no receipt envelope left the reader"
	}
	parts := make([]string, 0, len(shipments))
	for _, s := range shipments {
		parts = append(parts, fmt.Sprintf("receipt %s to %s covering %s message(s)", s.receiptMsgID, s.peerID, s.targets))
	}
	return strings.Join(parts, "; ")
}

func describeAcks(acks []receiptAck) string {
	if len(acks) == 0 {
		return "no message was acknowledged at all"
	}
	parts := make([]string, 0, len(acks))
	for _, a := range acks {
		parts = append(parts, fmt.Sprintf("%s acked to %s by receipt %s", a.msgID, a.peerID, a.receiptMsgID))
	}
	return strings.Join(parts, "; ")
}

func awaitReceiptAcks(reader *rig, baseline int, senders []receiptSender, timeout time.Duration) ([]string, []receiptAck, error) {
	deadline := time.Now().Add(timeout)
	var lines []string
	var fresh []receiptAck
	for {
		var err error
		lines, err = rigLogLines(reader)
		if err != nil {
			return nil, nil, err
		}
		all := parseReceiptAcks(lines)
		fresh = all
		if baseline <= len(all) {
			fresh = all[baseline:]
		}
		if namesEverySender(fresh, senders) {
			return lines, fresh, nil
		}
		if !time.Now().Before(deadline) {
			return lines, fresh, fmt.Errorf("%s acknowledged %d message(s) in %s and they do not cover every message the reading named (%s): %s",
				reader.name, len(fresh), timeout, describeSenders(senders), describeAcks(fresh))
		}
		time.Sleep(receiptShipPollInterval)
	}
}

func namesEverySender(acks []receiptAck, senders []receiptSender) bool {
	for _, s := range senders {
		found := false
		for _, a := range acks {
			if a.msgID == logid.Short(s.msgID) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func describeSenders(senders []receiptSender) string {
	parts := make([]string, 0, len(senders))
	for _, s := range senders {
		parts = append(parts, fmt.Sprintf("%s sent msg_id=%s", s.device, short(s.msgID)))
	}
	return strings.Join(parts, ", ")
}

func assertOneEnvelopePerSender(acks []receiptAck, shipments []receiptShipment, senders []receiptSender) error {
	picked := make([]receiptAck, len(senders))
	for i, s := range senders {
		var hits []receiptAck
		for _, a := range acks {
			if a.msgID == logid.Short(s.msgID) {
				hits = append(hits, a)
			}
		}
		if len(hits) != 1 {
			return fmt.Errorf("%d receipt(s) acknowledged %s's message msg_id=%s, want exactly 1: %s",
				len(hits), s.device, short(s.msgID), describeAcks(acks))
		}
		if hits[0].receiptMsgID == "" {
			return fmt.Errorf("the line acknowledging %s's message msg_id=%s carries no %s, so the envelope that covered it cannot be told from any other: %s",
				s.device, short(s.msgID), fieldReceiptMsgID, hits[0].line)
		}
		picked[i] = hits[0]
	}
	for i := range picked {
		for j := i + 1; j < len(picked); j++ {
			if picked[i].receiptMsgID == picked[j].receiptMsgID {
				return fmt.Errorf("one envelope (receipt %s) acknowledged both %s's and %s's messages and was addressed to %s — the whole batch went to a single device, which leaves the other device's messages unacknowledged: %s",
					picked[i].receiptMsgID, senders[i].device, senders[j].device, picked[i].peerID, picked[i].line)
			}
		}
	}
	for i, s := range senders {
		if want := logid.Short(s.peerID); picked[i].peerID != want {
			return fmt.Errorf("the receipt covering %s's message msg_id=%s was addressed to %s, want %s — %s sent that message and holds the session it arrived on",
				s.device, short(s.msgID), picked[i].peerID, want, s.device)
		}
	}

	for i, s := range senders {
		var found *receiptShipment
		for j := range shipments {
			if shipments[j].receiptMsgID == picked[i].receiptMsgID {
				found = &shipments[j]
				break
			}
		}
		if found == nil {
			return fmt.Errorf("the receipt %s covering %s's message msg_id=%s matches no envelope the reader shipped: %s",
				picked[i].receiptMsgID, s.device, short(s.msgID), describeShipments(shipments))
		}
		if found.peerID != picked[i].peerID {
			return fmt.Errorf("receipt %s was acknowledged to %s and shipped to %s — the two records of one envelope disagree: %s",
				picked[i].receiptMsgID, picked[i].peerID, found.peerID, found.line)
		}
	}
	if len(shipments) != len(senders) {
		return fmt.Errorf("the reading produced %d receipt envelope(s), want %d — one per sending device: %s",
			len(shipments), len(senders), describeShipments(shipments))
	}
	return nil
}

func receiptGroupedLines(lines []string, chatID string) []string {
	var out []string
	for _, line := range lines {
		if !strings.Contains(line, receiptGroupedLine) {
			continue
		}
		if v, ok := logAttrValue(line, fieldChatID); ok && v == logid.Short(chatID) {
			out = append(out, line)
		}
	}
	return out
}

func assertReceiptGrouping(readerName string, before int, after []string, want int) error {
	if len(after) != before+1 {
		return fmt.Errorf("%s logged %d summary line(s) for this conversation, want exactly one more than the %d it held before the reading — the reading this gate drove cannot be told from another one",
			readerName, len(after), before)
	}
	line := after[len(after)-1]
	groups, ok := logAttrValue(line, fieldReceiptGroups)
	if !ok {
		return fmt.Errorf("%s's summary line carries no %s count: %s", readerName, fieldReceiptGroups, line)
	}
	if groups != fmt.Sprint(want) {
		return fmt.Errorf("%s split the reading into %s group(s), want %d — one per sending device: %s", readerName, groups, want, line)
	}
	routing, ok := logAttrValue(line, fieldRoutingDevices)
	if !ok {
		return fmt.Errorf("%s's summary line carries no %s count: %s", readerName, fieldRoutingDevices, line)
	}
	if routing != fmt.Sprint(want) {
		return fmt.Errorf("%s routes that conversation to %s device(s), want %d — the fleet is not the two-device shape this gate measures: %s",
			readerName, routing, want, line)
	}
	return nil
}

func assertReceiptTargetRecorded(readerName string, lines []string, senders []receiptSender) error {
	for _, s := range senders {
		n := countTargetChoice(lines, receiptRecordedLine, s.msgID, receiptSourceRecorded)
		switch {
		case n == 1:
			continue
		case n > 1:
			return fmt.Errorf("%s picked a target for %s's message msg_id=%s %d times, want once — the reading was shipped more than once",
				readerName, s.device, short(s.msgID), n)
		}
		if fallback := fallbackSources(lines, s.msgID); len(fallback) > 0 {
			return fmt.Errorf("%s addressed the receipt for %s's message msg_id=%s to the conversation's stored device (%s=%s), not to the device that sent it — this run took the old path and proves nothing about the new one",
				readerName, s.device, short(s.msgID), fieldSource, strings.Join(fallback, ","))
		}
		return fmt.Errorf("%s wrote no line saying which device it addressed the receipt for %s's message msg_id=%s to — the target choice cannot be read at all",
			readerName, s.device, short(s.msgID))
	}
	return nil
}

func countTargetChoice(lines []string, marker, msgID, source string) int {
	n := 0
	for _, line := range lines {
		if !strings.Contains(line, marker) {
			continue
		}
		if v, ok := logAttrValue(line, fieldMsgID); !ok || v != logid.Short(msgID) {
			continue
		}
		if v, ok := logAttrValue(line, fieldSource); ok && v == source {
			n++
		}
	}
	return n
}

func fallbackSources(lines []string, msgID string) []string {
	seen := map[string]bool{}
	for _, line := range lines {
		if !strings.Contains(line, receiptFallbackLine) {
			continue
		}
		if v, ok := logAttrValue(line, fieldMsgID); !ok || v != logid.Short(msgID) {
			continue
		}
		if v, ok := logAttrValue(line, fieldSource); ok {
			seen[v] = true
		}
	}
	return sortedKeys(seen)
}

func assertTwoSendingDevices(readerName string, lines []string, senders []receiptSender) error {
	used := map[string]string{}
	for _, s := range senders {
		got := decryptedFrom(lines, s.msgID)
		want := logid.Short(s.peerID)
		if len(got) != 1 || got[0] != want {
			return fmt.Errorf("%s's own record says %s's message msg_id=%s arrived from %v, want exactly [%s] — until the two messages come from two devices, one envelope is a correct answer and this gate measures nothing",
				readerName, s.device, short(s.msgID), got, want)
		}
		if other, held := used[want]; held {
			return fmt.Errorf("%s says both %s's and %s's messages arrived from the same device %s — the two pairings folded onto one routing id, so this gate cannot measure the target choice",
				readerName, other, s.device, want)
		}
		used[want] = s.device
	}
	return nil
}

func decryptedFrom(lines []string, msgID string) []string {
	seen := map[string]bool{}
	for _, line := range lines {
		if !strings.Contains(line, inboxDecryptedLine) {
			continue
		}
		if v, ok := logAttrValue(line, fieldMsgID); !ok || v != logid.Short(msgID) {
			continue
		}
		if v, ok := logAttrValue(line, fieldPeerID); ok {
			seen[v] = true
		}
	}
	return sortedKeys(seen)
}

func logAttrValue(line, key string) (string, bool) {
	needle := key + "="
	from := 0
	for {
		i := strings.Index(line[from:], needle)
		if i < 0 {
			return "", false
		}
		at := from + i
		if at == 0 || line[at-1] == ' ' {
			rest := line[at+len(needle):]
			if sp := strings.IndexByte(rest, ' '); sp >= 0 {
				rest = rest[:sp]
			}
			return rest, true
		}
		from = at + len(needle)
	}
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
