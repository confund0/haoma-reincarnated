package tui

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/rivo/tview"

	"haoma-frontend/internal/calls"
	"haoma-frontend/internal/emoji"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
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

	searchQuery    string
	searchCurMsgID string

	systemLine string

	pending []pendingText
}

type pendingText struct {
	corrID string
	text   string
	at     int64
	msgID  string
	failed string
}

func newChatPage(chatID, peerID, nickname string, retiredAt int64) *chatPage {
	view := tview.NewTextView().
		SetDynamicColors(true).
		SetRegions(true).
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
	cp.applyTitle(presenceView{})
	return cp
}

func (cp *chatPage) applyTitle(v presenceView) {
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

	name := tview.Escape(label)
	if tag := presenceNameTag(v.chatty); tag != "" {
		name = tag + name + StyleReset
	}
	cp.view.SetTitle(" " + presenceArrowsTag(v) + " " + name + " ")
	cp.view.SetTitleColor(tview.Styles.TitleColor)
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
	FailReason    string          `json:"fail_reason"`
	DeliveryState string          `json:"delivery_state"`
	Mirrored      bool            `json:"mirrored_from_sibling"`
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
			arrow = outboundArrow(e.delivery, ev.Mirrored)
		} else {
			arrow = inboundArrow()
		}
	case "file":

		var fb fileBodyMin
		_ = json.Unmarshal(ev.Body, &fb)
		arrow = fileArrow(ev.Direction, fb.State, e.delivery, ev.Mirrored)
	}
	return cp.renderEventJSON(e.evJSON, arrow)
}

func outboundArrow(state string, mirrored bool) string {
	colour, head, tail := "gray", "<", "-"
	switch state {
	case "sent", "delivered":
		colour = "green"
	case "read":
		colour, tail = "green", "<"
	case "failed":
		colour, head = "red", "✗"
	}

	tailColour := colour

	if mirrored && colour == "green" {
		colour = "purple"
	}

	if tailColour != colour {
		return "[" + colour + "]" + head + "[" + tailColour + "]" + tail + "[-]"
	}
	return "[" + colour + "]" + head + tail + "[-]"
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
	Caption       string `json:"caption,omitempty"`
}

func fileArrow(direction, transferState, deliveryState string, mirrored bool) string {
	if direction == "out" {

		switch deliveryState {
		case "read":
			if mirrored {
				return "[green]<[purple]<[-]"
			}
			return "[green]<<[-]"
		case "delivered":
			if mirrored {
				return "[green]<[purple]-[-]"
			}
			return "[green]<-[-]"
		case "sent":
			if mirrored {
				return "[gray]<[purple]-[-]"
			}
			return "[gray]<[green]-[-]"
		case "failed":
			if mirrored {
				return "[red]✗[purple]-[-]"
			}
			return "[red]✗-[-]"
		default:
			if mirrored {
				return "[gray]<[purple]-[-]"
			}
			return "[gray]<-[-]"
		}
	}
	switch transferState {
	case "ready":
		return "[green]->[-]"
	case "failed_permanent", "expired":
		return "[red]✗>[-]"
	default:
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
		if ev.FailReason == string(events.DecryptReasonVersionMismatch) {
			return fmt.Sprintf("[gray]%s[white] [yellow][peer too old, please upgrade][white] seq=%d", stamp, ev.SenderSeq)
		}
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

		text := body.Text
		regionOpen, regionClose := "", ""
		if cp.searchQuery != "" {
			openTag := StyleSearchHighlight
			if ev.MsgID != "" && ev.MsgID == cp.searchCurMsgID {
				openTag = StyleSearchHighlightCurrent
			}
			text = highlightAllInsensitiveStyle(text, cp.searchQuery, openTag, StyleSearchHighlightOff)
			if ev.MsgID != "" {
				regionOpen = "[\"msg:" + ev.MsgID + "\"]"
				regionClose = "[\"\"]"
			}
		}
		return fmt.Sprintf("[gray]%s[white] %s %s%s%s%s", stamp, arrow, regionOpen, text, suffix, regionClose)
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
		row := fmt.Sprintf("[gray]%s[white] %s file: %s (%s) %s",
			stamp, arrow, name, formatFileSize(fb.Size), chip)

		if fb.Caption != "" {
			row += "\n            " + fb.Caption
		}
		return row
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
	case "call_summary":
		var body struct {
			CallID          string   `json:"call_id"`
			Direction       string   `json:"direction"`
			Outcome         string   `json:"outcome"`
			DurationSeconds int64    `json:"duration_seconds"`
			Modalities      []string `json:"modalities"`
			FailReason      string   `json:"fail_reason"`
		}
		_ = json.Unmarshal(ev.Body, &body)

		noAnswer := body.Outcome == "failed" && body.FailReason == calls.FailReasonNoAnswer
		accent := StyleCallSummaryBad
		switch {
		case body.Outcome == "completed" || body.Outcome == "answered_elsewhere":
			accent = StyleCallSummaryOK
		case noAnswer:
			accent = StyleCallSummaryNeutral
		}
		modality := "audio"
		for _, m := range body.Modalities {
			if m == "video" {
				modality = "video"
				break
			}
		}

		dirGlyph := "↑"
		if body.Direction == "in" {
			dirGlyph = "↓"
		}
		tail := ""
		switch body.Outcome {
		case "completed":
			tail = formatCallSummaryDuration(body.DurationSeconds)
		case "missed":
			tail = "missed"
		case "cancelled":
			tail = "cancelled"
		case "rejected":
			tail = "declined"
		case "failed":
			tail = "failed"
			if noAnswer {
				tail = "no answer"
			}
		case "disrupted":
			tail = "disrupted"
		case "answered_elsewhere":
			tail = "answered on another device"
		}
		summary := dirGlyph + " " + modality
		if tail != "" {
			summary += " · " + tail
		}

		return fmt.Sprintf("%s%s %s* %s%s",
			StyleBreadcrumbBody, stamp,
			accent, summary,
			StyleReset)
	case "onion_rotation":
		var body struct {
			RotationID string `json:"rotation_id"`
			Stage      string `json:"stage"`
			Reason     string `json:"reason"`
		}
		_ = json.Unmarshal(ev.Body, &body)

		dirGlyph := "↑"
		if ev.Direction == "in" {
			dirGlyph = "↓"
		}
		var summary string
		accent := StyleOnionRotationOK
		switch body.Stage {
		case "started":
			if ev.Direction == "in" {
				summary = dirGlyph + " they're rotating their Tor address"
			} else {
				summary = dirGlyph + " rotating our Tor address"
			}
		case "succeeded":
			if ev.Direction == "in" {
				summary = dirGlyph + " they rotated their Tor address"
			} else {
				summary = dirGlyph + " rotated our Tor address"
			}
		case "failed":
			accent = StyleOnionRotationBad
			reason := body.Reason
			if reason == "" {
				reason = "unknown"
			}
			summary = dirGlyph + " rotation failed: " + reason
		default:
			summary = dirGlyph + " rotation " + body.Stage
		}
		return fmt.Sprintf("%s%s %s* %s%s",
			StyleBreadcrumbBody, stamp,
			accent, summary,
			StyleReset)
	case "invitation":

		var body struct {
			ExpiresAt int64  `json:"expires_at"`
			State     string `json:"state"`
		}
		_ = json.Unmarshal(ev.Body, &body)
		look := invitationLookFor(body.State, body.ExpiresAt, time.Now())
		lead := "invitation you offered"
		if ev.Direction == "in" {
			lead = "invitation they offered"
		}

		return fmt.Sprintf("%s%s %s* %s - %s%s",
			StyleBreadcrumbBody, stamp,
			look.tag, lead, look.phrase,
			StyleReset)
	default:
		return fmt.Sprintf("[gray]%s[white] (kind=%s)", stamp, ev.Kind)
	}
}

func formatCallSummaryDuration(secs int64) string {
	if secs <= 0 {
		return "0s"
	}
	h := secs / 3600
	m := (secs % 3600) / 60
	s := secs % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%dm%ds", h, m, s)
	case m > 0:
		return fmt.Sprintf("%dm%ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

func (cp *chatPage) rebuild() {
	cp.redraw()
	cp.view.ScrollToEnd()
}

func (cp *chatPage) rebuildNoScroll() {
	cp.redraw()
}

func (cp *chatPage) redraw() {
	cp.view.Clear()
	if cp.systemLine != "" {
		fmt.Fprintln(cp.view, StyleBreadcrumbBody+"* "+cp.systemLine+StyleReset)
	}
	for _, e := range cp.entries {
		fmt.Fprintln(cp.view, cp.renderEntry(e))
	}
	for _, p := range cp.pending {
		fmt.Fprintln(cp.view, renderPendingText(p))
	}
}

const (
	systemLineNoSession = "Waiting for a secure session. Your messages go out when it is up."
	systemLineNoAnswer  = "Waiting for the contact to answer."
	systemLineStartedAt = "Chat started "
)

func chatSystemLine(c ipc.ChatEntry) string {
	switch {
	case c.WaitingForSession:
		return systemLineNoSession
	case c.WaitingForContact:
		return systemLineNoAnswer
	case c.ChatStartedAt > 0:
		return systemLineStartedAt + time.Unix(c.ChatStartedAt, 0).Format("2006-01-02 15:04:05")
	}
	return ""
}

func (cp *chatPage) setSystemLine(line string) {
	if line == cp.systemLine {
		return
	}
	cp.systemLine = line
	cp.rebuildNoScroll()
}

func renderPendingText(p pendingText) string {
	stamp := StyleRowStamp + time.Unix(p.at, 0).Format("15:04") + StyleRowText
	if p.failed != "" {
		return fmt.Sprintf("%s %s %s %s(failed: %s)%s",
			stamp, outboundArrow("failed", false), p.text, StylePendingFailed, tview.Escape(p.failed), StyleRowText)
	}
	return fmt.Sprintf("%s %s %s %s(sending)%s",
		stamp, outboundArrow("", false), p.text, StylePendingSending, StyleRowText)
}

func (cp *chatPage) addPending(corrID, text string, at int64) {
	cp.pending = append(cp.pending, pendingText{corrID: corrID, text: text, at: at})
	cp.rebuild()
}

func (cp *chatPage) pendingSent(corrID, msgID string) {
	i := cp.pendingIndex(corrID)
	if i < 0 {
		return
	}
	if _, isRow := cp.msgIDIndex[msgID]; isRow || msgID == "" {
		cp.pending = append(cp.pending[:i], cp.pending[i+1:]...)
		cp.rebuildNoScroll()
		return
	}
	cp.pending[i].msgID = msgID
}

func (cp *chatPage) pendingFailed(corrID, reason string) {
	i := cp.pendingIndex(corrID)
	if i < 0 {
		return
	}
	if reason == "" {
		reason = "not sent"
	}
	cp.pending[i].failed = reason
	cp.rebuildNoScroll()
}

func (cp *chatPage) pendingIndex(corrID string) int {
	for i, p := range cp.pending {
		if p.corrID == corrID {
			return i
		}
	}
	return -1
}

func (cp *chatPage) dropPendingForRow(msgID string) bool {
	if msgID == "" {
		return false
	}
	for i, p := range cp.pending {
		if p.msgID == msgID {
			cp.pending = append(cp.pending[:i], cp.pending[i+1:]...)
			return true
		}
	}
	return false
}

func (cp *chatPage) setSearchHighlight(query, curMsgID string) {
	cp.searchQuery = query
	cp.searchCurMsgID = curMsgID
}

func (cp *chatPage) clearSearchHighlight() {
	cp.searchQuery = ""
	cp.searchCurMsgID = ""
	cp.view.Highlight()
}

func (cp *chatPage) scrollToMsgID(msgID string) {
	if msgID == "" {
		return
	}
	if idx, ok := cp.msgIDIndex[msgID]; ok {
		target := idx - 5
		if target < 0 {
			target = 0
		}
		cp.view.ScrollTo(target, 0)
	}
	cp.view.Highlight("msg:" + msgID)
}

func highlightAllInsensitive(s, query string) string {
	return highlightAllInsensitiveStyle(s, query, StyleSearchHighlight, StyleSearchHighlightOff)
}

func highlightAllInsensitiveStyle(s, query, openTag, closeTag string) string {
	if query == "" || s == "" {
		return s
	}
	lo := strings.ToLower(s)
	q := strings.ToLower(query)
	var b strings.Builder
	i := 0
	for i < len(s) {
		rel := strings.Index(lo[i:], q)
		if rel < 0 {
			b.WriteString(s[i:])
			break
		}
		b.WriteString(s[i : i+rel])
		b.WriteString(openTag)
		b.WriteString(s[i+rel : i+rel+len(q)])
		b.WriteString(closeTag)
		i = i + rel + len(q)
	}
	return b.String()
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

	replaced := cp.dropPendingForRow(ev.MsgID)
	if insertAt == len(cp.entries) {
		cp.entries = append(cp.entries, entry)
		if entry.envID != "" {
			cp.envIndex[entry.envID] = len(cp.entries) - 1
		}
		if ev.MsgID != "" {
			cp.msgIDIndex[ev.MsgID] = len(cp.entries) - 1
		}

		if len(cp.pending) > 0 || replaced {
			cp.rebuild()
			return
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
	cur := cp.entries[idx].delivery
	if !events.DeliveryAdvances(cur, state) {

		slog.Debug("chat: delivery report refused; the row is not below it",
			slog.String("envelope_id", envID),
			slog.String("row_state", cur),
			slog.String("report_state", state),
		)
		return false
	}
	slog.Debug("chat: delivery marker moved",
		slog.String("envelope_id", envID),
		slog.String("from_state", cur),
		slog.String("to", state),
	)
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

			if removedMsgID == cp.searchCurMsgID {
				cp.clearSearchHighlight()
			}
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
