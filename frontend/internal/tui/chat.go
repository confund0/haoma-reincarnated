package tui

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/rivo/tview"

	"haoma-frontend/internal/emoji"
)

type chatEntry struct {
	evJSON   json.RawMessage
	envID    string
	delivery string
}

type chatPage struct {
	view       *tview.TextView
	chatID     string
	peerID     string
	nickname   string
	retiredAt  int64
	entries    []chatEntry
	envIndex   map[string]int
	msgIDIndex map[string]int

	fileProgress map[string]uint64
}

func newChatPage(chatID, peerID, nickname string, retiredAt int64) *chatPage {
	view := tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true)
	view.SetBorder(true)
	cp := &chatPage{
		view:         view,
		chatID:       chatID,
		peerID:       peerID,
		nickname:     nickname,
		retiredAt:    retiredAt,
		envIndex:     map[string]int{},
		msgIDIndex:   map[string]int{},
		fileProgress: map[string]uint64{},
	}
	cp.applyTitle("unknown")
	return cp
}

func (cp *chatPage) applyTitle(presenceLabel string) {
	label := cp.nickname
	if label == "" {
		label = shortID(cp.peerID)
	}
	if cp.retiredAt != 0 {
		date := time.Unix(cp.retiredAt, 0).Format("2006-01-02")
		cp.view.SetTitle(" " + GlyphRetired + " " + label + " — retired " + date + " ")
		cp.view.SetTitleColor(ColorRetiredNick)
		return
	}
	cp.view.SetTitle(" " + presenceGlyph(presenceLabel) + " " + label + " [" + presenceLabel + "] ")
	cp.view.SetTitleColor(presenceColor(presenceLabel))
}

type rawEvent struct {
	RecvSeq       uint64          `json:"recv_seq"`
	ChatID        string          `json:"chat_id"`
	SenderPeerID  string          `json:"sender_peer_id"`
	Direction     string          `json:"direction"`
	Kind          string          `json:"kind"`
	DisplayTs     int64           `json:"display_ts"`
	SenderSeq     uint64          `json:"sender_seq"`
	EnvelopeID    string          `json:"envelope_id"`
	MsgID         string          `json:"msg_id"`
	DecryptStatus string          `json:"decrypt_status"`
	DeliveryState string          `json:"delivery_state"`
	EditedAt      int64           `json:"edited_at"`
	DeletedAt     int64           `json:"deleted_at"`
	Body          json.RawMessage `json:"body"`
}

func (cp *chatPage) renderEntry(e chatEntry) string {
	var ev rawEvent
	_ = json.Unmarshal(e.evJSON, &ev)
	arrow := ""
	switch ev.Kind {
	case "text":
		if ev.Direction == "out" {
			arrow = outboundArrow(e.delivery)
		} else {
			arrow = inboundArrow()
		}
	case "file":

		var fb fileBodyMin
		_ = json.Unmarshal(ev.Body, &fb)
		arrow = fileArrow(ev.Direction, fb.State)
	}
	return cp.renderEventJSON(e.evJSON, arrow)
}

func outboundArrow(state string) string {
	switch state {
	case "sent", "delivered":
		return "[green]<-[-]"
	case "read":
		return "[green]<<[-]"
	case "failed":
		return "[red]✗-[-]"
	default:
		return "[gray]<-[-]"
	}
}

func inboundArrow() string {
	return "[blue]->[-]"
}

type fileBodyMin struct {
	Name          string `json:"name"`
	Size          uint64 `json:"size"`
	Mime          string `json:"mime,omitempty"`
	State         string `json:"state"`
	BytesReceived uint64 `json:"bytes_received,omitempty"`
	LastError     string `json:"last_error,omitempty"`
}

func fileArrow(direction, state string) string {
	switch state {
	case "ready":
		if direction == "out" {
			return "[green]<-[-]"
		}
		return "[green]->[-]"
	case "failed_permanent", "expired":
		if direction == "out" {
			return "[red]✗-[-]"
		}
		return "[red]✗>[-]"
	default:
		if direction == "out" {
			return "[gray]<-[-]"
		}
		return "[gray]->[-]"
	}
}

func formatFileSize(b uint64) string {
	const (
		kib = 1 << 10
		mib = 1 << 20
		gib = 1 << 30
	)
	switch {
	case b >= gib:
		return fmt.Sprintf("%.2f GB", float64(b)/float64(gib))
	case b >= mib:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(mib))
	case b >= kib:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(kib))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func renderFileStateChip(fb fileBodyMin, progress uint64) string {
	bytes := fb.BytesReceived
	if progress > bytes {
		bytes = progress
	}
	switch fb.State {
	case "ready":
		return "[green]ready[-]"
	case "downloading":
		if fb.Size > 0 && bytes > 0 {
			pct := bytes * 100 / fb.Size
			if pct > 100 {
				pct = 100
			}
			return fmt.Sprintf("[gray]downloading %d%%[-]", pct)
		}
		return "[gray]downloading[-]"
	case "awaiting_key":
		return "[gray]awaiting key[-]"
	case "failed_transient":

		if fb.LastError != "" {
			return "[orange]failed (transient):[-] " + fb.LastError
		}
		return "[orange]failed (transient)[-]"
	case "failed_permanent":
		if fb.LastError != "" {
			return "[red]failed:[-] " + fb.LastError
		}
		return "[red]failed[-]"
	case "expired":
		return "[red]expired[-]"
	case "":
		return "[gray]pending[-]"
	default:
		return "[gray]" + fb.State + "[-]"
	}
}

func (cp *chatPage) renderEventJSON(evJSON json.RawMessage, arrow string) string {
	var ev rawEvent
	if err := json.Unmarshal(evJSON, &ev); err != nil {
		return "[red](decode error)[white]"
	}
	stamp := time.Unix(ev.DisplayTs, 0).Format("15:04")

	if ev.DecryptStatus == "failed" {
		return fmt.Sprintf("[gray]%s[white] [red][CAN'T DECRYPT][white] seq=%d", stamp, ev.SenderSeq)
	}

	switch ev.Kind {
	case "text":

		if ev.DeletedAt > 0 {
			return fmt.Sprintf("[gray]%s[white] %s [gray][message deleted][white]", stamp, arrow)
		}
		var body struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(ev.Body, &body)
		suffix := ""
		if ev.EditedAt > 0 {
			suffix = " [gray](edited)[white]"
		}
		return fmt.Sprintf("[gray]%s[white] %s %s%s", stamp, arrow, body.Text, suffix)
	case "file":

		if ev.DeletedAt > 0 {
			return fmt.Sprintf("[gray]%s[white] %s [gray][file deleted][white]", stamp, arrow)
		}
		var fb fileBodyMin
		_ = json.Unmarshal(ev.Body, &fb)
		name := fb.Name
		if name == "" {
			name = "(unnamed)"
		}
		chip := renderFileStateChip(fb, cp.fileProgress[ev.MsgID])
		return fmt.Sprintf("[gray]%s[white] %s file: %s (%s) %s",
			stamp, arrow, name, formatFileSize(fb.Size), chip)
	case "timer_change":
		var body struct {
			From      uint32 `json:"from"`
			To        uint32 `json:"to"`
			ChangedBy string `json:"changed_by"`
		}
		_ = json.Unmarshal(ev.Body, &body)
		who := "me"
		if body.ChangedBy != "" {

			if cp.nickname != "" {
				who = cp.nickname
			} else {
				who = shortID(body.ChangedBy)
			}
		}

		return fmt.Sprintf("%s%s %s* %s %sset disappearing messages %s → %s%s",
			StyleBreadcrumbBody, stamp,
			StyleBreadcrumbNick, who,
			StyleBreadcrumbBody,
			retentionLabel(body.From), retentionLabel(body.To),
			StyleReset)
	case "reaction":
		var body struct {
			TargetMsgID string `json:"target_msg_id"`
			Emoji       string `json:"emoji"`
			At          int64  `json:"at"`
		}
		_ = json.Unmarshal(ev.Body, &body)
		who := "me"
		if ev.SenderPeerID != "" {

			if cp.nickname != "" {
				who = cp.nickname
			} else {
				who = shortID(ev.SenderPeerID)
			}
		}

		stampTs := body.At
		if stampTs == 0 {
			stampTs = ev.DisplayTs
		}
		reactStamp := time.Unix(stampTs, 0).Format("15:04")

		shown := emoji.Render(body.Emoji)
		if body.Emoji == "" {
			shown = "[gray](removed reaction)[-]"
		}

		return fmt.Sprintf("%s%s %s* %s %s%s",
			StyleBreadcrumbBody, reactStamp,
			StyleBreadcrumbNick, who,
			shown,
			StyleReset)
	default:
		return fmt.Sprintf("[gray]%s[white] (kind=%s)", stamp, ev.Kind)
	}
}

func (cp *chatPage) rebuild() {
	cp.view.Clear()
	for _, e := range cp.entries {
		fmt.Fprintln(cp.view, cp.renderEntry(e))
	}
	cp.view.ScrollToEnd()
}

func (cp *chatPage) rebuildNoScroll() {
	cp.view.Clear()
	for _, e := range cp.entries {
		fmt.Fprintln(cp.view, cp.renderEntry(e))
	}
}

func (cp *chatPage) appendEvent(evJSON json.RawMessage) {
	var ev rawEvent
	_ = json.Unmarshal(evJSON, &ev)

	entry := chatEntry{evJSON: evJSON, delivery: ev.DeliveryState}
	if ev.Direction == "out" && ev.EnvelopeID != "" {
		entry.envID = ev.EnvelopeID
	}

	insertAt := cp.findInsertPos(ev.DisplayTs, ev.RecvSeq)
	if ev.Kind == "reaction" {
		if pos, ok := cp.reactionInsertPos(ev.Body); ok {
			insertAt = pos
		}
	}
	if insertAt == len(cp.entries) {
		cp.entries = append(cp.entries, entry)
		if entry.envID != "" {
			cp.envIndex[entry.envID] = len(cp.entries) - 1
		}
		if ev.MsgID != "" {
			cp.msgIDIndex[ev.MsgID] = len(cp.entries) - 1
		}
		fmt.Fprintln(cp.view, cp.renderEntry(entry))
		cp.view.ScrollToEnd()
		return
	}

	cp.entries = append(cp.entries[:insertAt], append([]chatEntry{entry}, cp.entries[insertAt:]...)...)
	for k, v := range cp.envIndex {
		if v >= insertAt {
			cp.envIndex[k] = v + 1
		}
	}
	for k, v := range cp.msgIDIndex {
		if v >= insertAt {
			cp.msgIDIndex[k] = v + 1
		}
	}
	if entry.envID != "" {
		cp.envIndex[entry.envID] = insertAt
	}
	if ev.MsgID != "" {
		cp.msgIDIndex[ev.MsgID] = insertAt
	}
	cp.rebuildNoScroll()
}

func (cp *chatPage) findInsertPos(displayTs int64, recvSeq uint64) int {
	for i := len(cp.entries) - 1; i >= 0; i-- {
		var prev rawEvent
		_ = json.Unmarshal(cp.entries[i].evJSON, &prev)
		if prev.DisplayTs < displayTs {
			return i + 1
		}
		if prev.DisplayTs == displayTs && prev.RecvSeq < recvSeq {
			return i + 1
		}
	}
	return 0
}

func (cp *chatPage) reactionInsertPos(body json.RawMessage) (int, bool) {
	var rb struct {
		TargetMsgID string `json:"target_msg_id"`
	}
	if err := json.Unmarshal(body, &rb); err != nil || rb.TargetMsgID == "" {
		return 0, false
	}
	targetIdx, ok := cp.msgIDIndex[rb.TargetMsgID]
	if !ok {
		return 0, false
	}
	insertAt := targetIdx + 1
	for insertAt < len(cp.entries) {
		var nxt rawEvent
		if err := json.Unmarshal(cp.entries[insertAt].evJSON, &nxt); err != nil {
			break
		}
		if nxt.Kind != "reaction" {
			break
		}
		var nrb struct {
			TargetMsgID string `json:"target_msg_id"`
		}
		if err := json.Unmarshal(nxt.Body, &nrb); err != nil || nrb.TargetMsgID != rb.TargetMsgID {
			break
		}
		insertAt++
	}
	return insertAt, true
}

func (cp *chatPage) upsertEvent(evJSON json.RawMessage) {
	var ev rawEvent
	_ = json.Unmarshal(evJSON, &ev)
	if ev.MsgID == "" {
		cp.appendEvent(evJSON)
		return
	}
	idx, known := cp.msgIDIndex[ev.MsgID]
	if !known {
		cp.appendEvent(evJSON)
		return
	}
	prev := cp.entries[idx]

	newDelivery := prev.delivery
	if ev.DeliveryState != "" {
		newDelivery = ev.DeliveryState
	}
	cp.entries[idx] = chatEntry{
		evJSON:   evJSON,
		envID:    prev.envID,
		delivery: newDelivery,
	}
	cp.rebuildNoScroll()
}

func (cp *chatPage) updateFileProgress(msgID string, bytes uint64) {
	if msgID == "" {
		return
	}
	if _, known := cp.msgIDIndex[msgID]; !known {

		cp.fileProgress[msgID] = bytes
		return
	}
	cp.fileProgress[msgID] = bytes
	cp.rebuildNoScroll()
}

func (cp *chatPage) setDeliveryState(envID, state string) bool {
	idx, ok := cp.envIndex[envID]
	if !ok {
		return false
	}
	if cp.entries[idx].delivery == "read" && state != "read" {
		return false
	}
	cp.entries[idx].delivery = state
	cp.rebuild()
	return true
}

func (cp *chatPage) deleteByRecvSeq(recvSeq uint64) bool {
	for i := range cp.entries {
		var ev rawEvent
		if err := json.Unmarshal(cp.entries[i].evJSON, &ev); err != nil {
			continue
		}
		if ev.RecvSeq != recvSeq {
			continue
		}
		if cp.entries[i].envID != "" {
			delete(cp.envIndex, cp.entries[i].envID)
		}
		var removedMsgID string
		var removedEv rawEvent
		_ = json.Unmarshal(cp.entries[i].evJSON, &removedEv)
		removedMsgID = removedEv.MsgID
		if removedMsgID != "" {
			delete(cp.msgIDIndex, removedMsgID)
		}
		cp.entries = append(cp.entries[:i], cp.entries[i+1:]...)
		for k, v := range cp.envIndex {
			if v > i {
				cp.envIndex[k] = v - 1
			}
		}
		for k, v := range cp.msgIDIndex {
			if v > i {
				cp.msgIDIndex[k] = v - 1
			}
		}
		cp.rebuild()
		return true
	}
	return false
}

func (cp *chatPage) prependEvents(evs []json.RawMessage) {
	if len(evs) == 0 {
		return
	}
	wasEmpty := len(cp.entries) == 0
	newEntries := make([]chatEntry, 0, len(evs))
	for i := len(evs) - 1; i >= 0; i-- {
		var ev rawEvent
		_ = json.Unmarshal(evs[i], &ev)
		entry := chatEntry{evJSON: evs[i], delivery: ev.DeliveryState}
		if ev.Direction == "out" && ev.EnvelopeID != "" {
			entry.envID = ev.EnvelopeID
		}
		newEntries = append(newEntries, entry)
	}
	cp.entries = regroupReactions(append(newEntries, cp.entries...))
	cp.envIndex = make(map[string]int, len(cp.entries))
	cp.msgIDIndex = make(map[string]int, len(cp.entries))
	for i, e := range cp.entries {
		if e.envID != "" {
			cp.envIndex[e.envID] = i
		}
		var ev rawEvent
		if err := json.Unmarshal(e.evJSON, &ev); err == nil && ev.MsgID != "" {
			cp.msgIDIndex[ev.MsgID] = i
		}
	}
	if wasEmpty {
		cp.rebuild()
		return
	}
	cp.rebuildNoScroll()
}

func regroupReactions(entries []chatEntry) []chatEntry {
	type reactRef struct {
		entry  chatEntry
		target string
	}
	var skeleton []chatEntry
	var reactions []reactRef
	known := make(map[string]bool)
	for _, e := range entries {
		var ev rawEvent
		_ = json.Unmarshal(e.evJSON, &ev)
		if ev.Kind == "reaction" {
			var rb struct {
				TargetMsgID string `json:"target_msg_id"`
			}
			_ = json.Unmarshal(ev.Body, &rb)
			reactions = append(reactions, reactRef{entry: e, target: rb.TargetMsgID})
			continue
		}
		skeleton = append(skeleton, e)
		if ev.MsgID != "" {
			known[ev.MsgID] = true
		}
	}
	byTarget := make(map[string][]chatEntry, len(reactions))
	var orphans []chatEntry
	for _, r := range reactions {
		if r.target != "" && known[r.target] {
			byTarget[r.target] = append(byTarget[r.target], r.entry)
			continue
		}
		orphans = append(orphans, r.entry)
	}
	out := make([]chatEntry, 0, len(entries))
	for _, e := range skeleton {
		out = append(out, e)
		var ev rawEvent
		_ = json.Unmarshal(e.evJSON, &ev)
		if ev.MsgID != "" {
			if rs, ok := byTarget[ev.MsgID]; ok {
				out = append(out, rs...)
			}
		}
	}
	out = append(out, orphans...)
	return out
}
