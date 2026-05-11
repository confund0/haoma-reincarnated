package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"

	"haoma-frontend/internal/ipc"
)

func parseCommand(input string) (name, rest string, ok bool) {
	trimmed := strings.TrimLeft(input, " \t")
	if !strings.HasPrefix(trimmed, "/") {
		return "", "", false
	}
	i := strings.IndexAny(trimmed, " \t")
	if i < 0 {
		return trimmed, "", true
	}
	return trimmed[:i], strings.TrimLeft(trimmed[i+1:], " \t"), true
}

func (a *App) handleInput(_ tcell.Key) {
	text := a.input.GetText()
	a.input.SetText("")
	if text == "" {
		return
	}

	a.historyPush(text)

	cmd, rest, ok := parseCommand(text)
	if !ok {

		if active := a.activeChat(); active != "" {
			a.winMu.Lock()
			haomaUp := a.haomaConnected
			a.winMu.Unlock()
			if !haomaUp {
				a.log("[red]haoma unreachable[white] — message not sent")
				return
			}
			if a.peerRetiredAt(active) != 0 {
				a.log("[red]peer retired[white] — message not sent")
				return
			}
			a.doSendText(active, text)
			return
		}
		a.log("not a command; type [yellow]/help[white] to see what's available")
		return
	}

	switch cmd {
	case "/quit", "/q", "/panic":
		a.client.Close()
		a.app.Stop()
		return
	case "/help", "/h":
		a.showHelp()
		return
	case "/close":
		a.cmdClose()
		return
	case "/change-pass":
		a.cmdChangePass(rest)
		return
	case "/change-pin":
		a.cmdChangePIN(rest)
		return
	case "/set-idle-action":
		a.cmdSetIdleAction(rest)
		return
	case "/set-idle-timeout":
		a.cmdSetIdleTimeout(rest)
		return
	case "/set-pin-validity":
		a.cmdSetPinValidity(rest)
		return
	case "/set-tor-password":
		a.cmdSetTorPassword(rest)
		return
	case "/settings":
		a.cmdSettings()
		return
	case "/fsbrowse":
		a.cmdFsBrowse(rest)
		return
	}

	a.winMu.Lock()
	haomaUp := a.haomaConnected
	a.winMu.Unlock()
	if !haomaUp {
		a.log("[red]haoma unreachable[white] — %s requires the daemon; try /quit or wait for reconnect", cmd)
		return
	}

	switch cmd {
	case "/invite-paste":
		a.cmdInvitePaste(rest)
	case "/accept-paste":
		a.cmdAcceptPaste(rest)
	case "/invite-file":
		a.cmdInviteFile(rest)
	case "/accept-file":
		a.cmdAcceptFile(rest)
	case "/invite-dht":
		a.cmdInviteDHT(rest)
	case "/accept-dht":
		a.cmdAcceptDHT(rest)
	case "/cancel-dht":
		a.cmdCancelDHT(rest)
	case "/invite-tor":
		a.cmdInviteTor(rest)
	case "/accept-tor":
		a.cmdAcceptTor(rest)
	case "/cancel-tor":
		a.cmdCancelTor(rest)
	case "/msg":
		a.cmdMsg(rest)
	case "/send-file":
		a.cmdSendFile(rest)
	case "/attach":
		a.cmdAttach()
	case "/peers", "/contacts":
		a.cmdContacts()
	case "/chats":
		a.cmdChats()
	case "/tor-info":
		a.cmdTorInfo()
	case "/inspect":
		a.cmdInspect(rest)
	case "/edit":
		a.cmdEdit()
	case "/delete":
		a.cmdDelete()
	case "/files":
		a.cmdFiles()
	case "/retry":
		a.cmdRetry()
	case "/react":
		a.cmdReact()
	case "/status":
		a.cmdStatus(rest)
	case "/nick":
		a.cmdNick(rest)
	case "/call":
		a.cmdCall()
	case "/rotate-tor":
		a.cmdRotateTor()
	case "/answer":
		a.cmdAnswer()
	case "/decline", "/reject":
		a.cmdDecline()
	case "/hangup":
		a.cmdHangup()
	default:
		a.log("unknown command: %s (try [yellow]/help[white])", cmd)
	}
}

func (a *App) showHelp() {

	a.log("commands:")
	a.log("pairing (Tor onion rendezvous — preferred):")
	a.log("  [yellow]/invite-tor[white]                            — spin ephemeral onion; emit 7 EFF-short words (uses /nick)")
	a.log("  [yellow]/accept-tor W1 W2 W3 W4 W5 W6 W7[white]      — accept an onion invite (uses /nick)")
	a.log("  [yellow]/cancel-tor <handle-id>[white]               — tear down an in-flight onion invite")
	a.log("pairing (DHT rendezvous — fallback, leaks IP to BEP-44 nodes):")
	a.log("  [yellow]/invite-dht[white]                            — emit 3+4 word invite; share OOB (uses /nick)")
	a.log("  [yellow]/accept-dht A B C  D E F G[white]            — accept a friend's 3+4 word invite (uses /nick)")
	a.log("  [yellow]/cancel-dht <guid>[white]                    — cancel a published invite")
	a.log("pairing (paste-in / file — dev / fallback, no rendezvous):")
	a.log("  [gray]order: BOTH sides /invite-* first (each mints local keys + emits a blob); THEN exchange blobs OOB; THEN /accept-* on each side.[white]")
	a.log("  [yellow]/invite-paste[white]                          — emit a paste-in invite JSON to <DataDir>/last-invite.json (uses /nick)")
	a.log("  [yellow]/accept-paste <json>[white]                  — ingest a remote invite (one-line JSON)")
	a.log("  [yellow]/invite-file <path>[white]                   — emit an invite directly to <path> (0600) (uses /nick)")
	a.log("  [yellow]/accept-file <path>[white]                   — ingest a remote invite from a file")
	a.log("messaging:")
	a.log("  [yellow]/msg [peer-id] <text>[white]                 — send a text message (peer-id optional in chat window)")
	a.log("  [yellow]/send-file [peer-id] <path>[white]           — send a file attachment (peer-id optional in chat window; 10 MB cap)")
	a.log("  [yellow]/attach[white]                              — pick a file via dialog + send to active chat (a keybind on chat scroll)")
	a.log("  [yellow]/contacts, /peers[white]                     — open + refresh the contacts (peers) pane")
	a.log("  [yellow]/chats[white]                                — open + refresh the chats (conversations) pane")
	a.log("  [yellow]/tor-info[white]                             — show published onion addresses")
	a.log("  [yellow]/close[white]                                — close active chat window (history preserved)")
	a.log("  [yellow]/edit[white]                                 — edit one of your own messages (last 24h)")
	a.log("  [yellow]/delete[white]                               — delete one of your own messages for everyone (last 24h)")
	a.log("  [yellow]/files[white], chat-window [yellow]f[white]  — list file attachments in this chat (read-only; row actions land later)")
	a.log("  [yellow]/retry[white]                                — re-kick all stuck file downloads (failed_transient)")
	a.log("  [yellow]/react[white]                                — react to any message in this chat (empty emoji removes)")
	a.log("  [yellow]/call[white]                                — start a voice call with the active chat's peer (1a: signalling only — no audio yet)")
	a.log("  [yellow]/answer[white]                              — accept the incoming call (keyboard alternative to the ringer's [Accept] button)")
	a.log("  [yellow]/decline, /reject[white]                    — decline the incoming call")
	a.log("  [yellow]/hangup[white]                              — end the active call (works on offered, ringing, or accepted)")
	a.log("  [yellow]/rotate-tor[white]                          — rotate this chat's onion address (refuses during a call or when one is in flight)")
	a.log("  [yellow]/status [available|away|busy][white]        — set your presence; broadcasts (status pane) or targets the chat peer (chat pane). No arg = reset to available.")
	a.log("  [yellow]/nick [name][white]                          — set your self-nick (embedded in outgoing invites). No arg = show current.")
	a.log("  [yellow]/inspect <msg_id>[white]                     — dev: dump the event row for a msg_id")
	a.log("  [yellow]/help, /h[white]                             — this list")
	a.log("  [yellow]/quit, /q, /panic[white]                     — exit (Ctrl-D also works; full panic actions land later)")
	a.log("vault (require --cfg-dir flow):")
	a.log("  [yellow]/change-pass <old> <new>[white]              — re-seal vault with new master passphrase")
	a.log("  [yellow]/change-pin <old> <new>[white]               — set/change soft-lock PIN (no <old> validation if current is the insecure default)")
	a.log("  [yellow]/set-idle-action <soft-lock|safe-lock>[white] — pick what fires when idle elapses")
	a.log("  [yellow]/set-idle-timeout <seconds>[white]           — how long idle before the lock fires")
	a.log("  [yellow]/set-pin-validity <seconds>[white]           — soft-lock escalation window (0 = no escalation)")
	a.log("  [yellow]/set-tor-password <password>[white]          — tor control-port password (haomad restart required)")
	a.log("  [yellow]/settings[white]                              — open the settings dialog (one canonical home for prefs)")
	a.log("navigation: Esc+1 status  Esc+2 contacts  Esc+3 chats  Esc+N chat")
	a.log("focus: [yellow]F6[white] toggles between the input bar and the active window's main pane (table / list / scroll view)")
	a.log("input: each window keeps its own draft; switching windows preserves what you typed without leaking it into another pane")
	a.log("contacts pane: Enter opens chat, [yellow]e[white] edits contact (alias + unpair + delete)")
	a.log("chats pane: Enter opens chat, [yellow]e[white] edits conversation (disappearing messages + clear/delete)")
}

func (a *App) cmdContacts() {
	a.switchTo("contacts")
	a.sendRequest(ipc.FrameListPeers, nil, a.handlePeersListed)
}

func (a *App) cmdChats() {
	a.switchTo("chats")
	a.sendRequest(ipc.FrameListChats, nil, a.handleChatsListed)
}

func (a *App) cmdMsg(arg string) {
	var peerID, text string
	if active := a.activeChat(); active != "" {
		text = strings.TrimSpace(arg)
		if text == "" {
			a.log("[red]/msg[white] needs <text>")
			return
		}
		peerID = active
	} else {
		var ok bool
		peerID, text, ok = splitPeerArg(arg)
		if !ok {
			a.log("[red]/msg[white] needs <peer-id> <text>")
			return
		}
	}
	a.doSendText(peerID, text)
}

func (a *App) cmdSendFile(arg string) {
	var peerID, path string
	if active := a.activeChat(); active != "" {
		path = strings.TrimSpace(arg)
		if path == "" {
			a.log("[red]/send-file[white] needs <path>")
			return
		}
		peerID = active
	} else {
		var ok bool
		peerID, path, ok = splitPeerArg(arg)
		if !ok {
			a.log("[red]/send-file[white] needs <peer-id> <path>")
			return
		}
	}
	a.dispatchSendFileToPeer(peerID, path)
}

func (a *App) dispatchSendFileToPeer(peerID, path string) {
	if a.peerRetiredAt(peerID) != 0 {
		a.log("[red]peer retired[white] — file not sent")
		return
	}
	a.sendRequest(ipc.FrameSendFile, ipc.SendFileRequest{PeerID: peerID, Path: path}, func(f ipc.Frame) {
		if f.Type == ipc.FrameError {
			a.renderError(f)
			return
		}
		if f.Type != ipc.FrameFileSent {
			a.log("[red]send-file[white] unexpected response: %s", f.Type)
			return
		}
		var p ipc.SendFileResponse
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			a.log("[red]send-file[white] decode: %v", err)
			return
		}
		a.log("file sent: [yellow]%s[white] (%d bytes, %s)", p.Name, p.Size, p.Mime)
	})
}

func (a *App) doSendText(peerID, text string) {

	if a.peerRetiredAt(peerID) != 0 {
		a.log("[red]peer retired[white] — message not sent")
		return
	}
	a.sendRequest(ipc.FrameSendText, ipc.SendTextRequest{PeerID: peerID, Text: text}, func(f ipc.Frame) {
		if f.Type == ipc.FrameError {
			a.renderError(f)
			return
		}
		if f.Type != ipc.FrameTextSent {
			a.log("[red]/msg[white] unexpected response: %s", f.Type)
			return
		}
		var p ipc.SendTextResponse
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			a.log("[red]/msg[white] decode: %v", err)
			return
		}
		_ = p
	})
}

func splitPeerArg(arg string) (peerID, text string, ok bool) {
	arg = strings.TrimLeft(arg, " \t")
	if arg == "" {
		return "", "", false
	}
	i := strings.IndexAny(arg, " \t")
	if i < 0 {
		return "", "", false
	}
	peerID = arg[:i]
	text = strings.TrimLeft(arg[i+1:], " \t")
	if peerID == "" || text == "" {
		return "", "", false
	}
	return peerID, text, true
}

func (a *App) cmdInvitePaste(arg string) {
	if strings.TrimSpace(arg) != "" {
		a.log("[yellow]/invite-paste[white] takes no arguments — your self-nick comes from [yellow]/nick[white]")
		return
	}
	a.warnDefaultNick("/invite-paste")
	a.log("requesting paste-in invite (your nick=[yellow]%s[white])…", a.currentNick())

	a.requestInvite(func(invJSON string) {
		path, err := a.writeInviteFile([]byte(invJSON))
		if err != nil {
			a.log("[red]invite-paste[white] write file: %v", err)
			return
		}
		a.log("[green]invite ready[white] — %d bytes at [yellow]%s", len(invJSON), path)
		a.log("copy with: [yellow]cat %s[white] in another terminal, send OOB", path)
	})
}

func (a *App) cmdInviteFile(arg string) {
	path := strings.TrimSpace(arg)
	if path == "" {
		a.log("[red]/invite-file[white] needs a path: /invite-file <path>")
		return
	}
	a.warnDefaultNick("/invite-file")
	a.log("requesting invite to %s (your nick=[yellow]%s[white])…", path, a.currentNick())

	a.requestInvite(func(invJSON string) {
		if err := atomicWrite(path, []byte(invJSON), 0o600); err != nil {
			a.log("[red]invite-file[white] write %s: %v", path, err)
			return
		}
		a.log("[green]invite ready[white] — %d bytes at [yellow]%s", len(invJSON), path)
	})
}

func (a *App) currentNick() string {
	a.winMu.Lock()
	nick := a.selfNick
	a.winMu.Unlock()
	if nick == "" {
		return "mynick"
	}
	return nick
}

func (a *App) warnDefaultNick(cmd string) {
	a.winMu.Lock()
	nick := a.selfNick
	isDefault := a.selfNickIsDefault
	a.winMu.Unlock()
	if nick == "" || isDefault {
		shown := nick
		if shown == "" {
			shown = "mynick"
		}
		a.log("[yellow]heads-up:[white] your nick is still '[yellow]%s[white]' — set yours via [yellow]/nick <name>[white] before pairing for real (cmd=%s)", shown, cmd)
	}
}

func (a *App) requestInvite(onJSON func(invJSON string)) {
	a.sendRequest(ipc.FrameInviteCreate, ipc.InviteCreateRequest{}, func(f ipc.Frame) {
		if f.Type == ipc.FrameError {
			a.renderError(f)
			return
		}
		if f.Type != ipc.FrameInviteCreated {
			a.log("[red]invite[white] unexpected response type: %s", f.Type)
			return
		}
		var p ipc.InviteCreatedResponse
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			a.log("[red]invite[white] decode response: %v", err)
			return
		}
		onJSON(p.InviteJSON)
	})
}

func (a *App) writeInviteFile(data []byte) (string, error) {
	var path string
	if a.DataDir != "" {
		path = filepath.Join(a.DataDir, "last-invite.json")
	} else {
		f, err := os.CreateTemp("", "haoma-invite-*.json")
		if err != nil {
			return "", err
		}
		_ = f.Close()
		path = f.Name()
	}
	return path, atomicWrite(path, data, 0o600)
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func (a *App) cmdAcceptPaste(arg string) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		a.log("[red]/accept-paste[white] needs JSON as its argument (or use /accept-file)")
		return
	}
	a.issueAccept(arg)
}

func (a *App) cmdAcceptFile(arg string) {
	path := strings.TrimSpace(arg)
	if path == "" {
		a.log("[red]/accept-file[white] needs a path as its argument")
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		a.log("[red]/accept-file[white] read %s: %v", path, err)
		return
	}
	a.issueAccept(string(data))
}

func (a *App) issueAccept(inviteJSON string) {
	a.log("pairing…")
	a.sendRequest(ipc.FrameInviteAccept, ipc.InviteAcceptRequest{InviteJSON: inviteJSON}, func(f ipc.Frame) {
		if f.Type == ipc.FrameError {
			a.renderError(f)
			return
		}
		if f.Type != ipc.FrameInviteAccepted {
			a.log("[red]accept[white] unexpected response type: %s", f.Type)
			return
		}
		var p ipc.InviteAcceptedResponse
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			a.log("[red]accept[white] decode response: %v", err)
			return
		}
		label := p.Nick
		if label == "" {
			label = "(no nick)"
		}

		a.log("[green]paired[white] %s — peer [yellow]%s", label, p.PeerID)
		if p.IdentityFingerprint != "" {
			a.log("[yellow]verify[white] fingerprint OOB: %s", p.IdentityFingerprint)
		}

	})
}

func (a *App) cmdInspect(arg string) {
	id := strings.TrimSpace(arg)
	if id == "" {
		a.log("[red]inspect[white] usage: /inspect <msg_id>")
		return
	}
	a.sendRequest(ipc.FrameInspectEvent, ipc.InspectEventRequest{MsgID: id}, func(f ipc.Frame) {
		if f.Type == ipc.FrameError {
			a.renderError(f)
			return
		}
		if f.Type != ipc.FrameEventInspected {
			a.log("[red]inspect[white] unexpected response: %s", f.Type)
			return
		}
		var resp ipc.EventInspectedResponse
		if err := json.Unmarshal(f.Payload, &resp); err != nil {
			a.log("[red]inspect[white] decode: %v", err)
			return
		}

		var row struct {
			RecvSeq       uint64 `json:"recv_seq"`
			PeerID        string `json:"peer_id"`
			Direction     string `json:"direction"`
			Kind          string `json:"kind"`
			DisplayTs     int64  `json:"display_ts"`
			EnvelopeID    string `json:"envelope_id"`
			MsgID         string `json:"msg_id"`
			DecryptStatus string `json:"decrypt_status,omitempty"`
			DeliveryState string `json:"delivery_state,omitempty"`
		}
		if err := json.Unmarshal(resp.Event, &row); err != nil {
			a.log("[red]inspect[white] decode event: %v", err)
			return
		}
		a.log("[green]event[white] msg_id=%s peer=%s dir=%s kind=%s seq=%d",
			row.MsgID, shortID(row.PeerID), row.Direction, row.Kind, row.RecvSeq)
		a.log("  envelope=%s display_ts=%d", row.EnvelopeID, row.DisplayTs)
		if row.DecryptStatus != "" || row.DeliveryState != "" {
			a.log("  decrypt=%s delivery=%s", row.DecryptStatus, row.DeliveryState)
		}
	})
}

func (a *App) cmdClose() {
	front, _ := a.pages.GetFrontPage()
	switch {
	case front == pageNameSettings:
		a.cmdCloseSettings()
	case strings.HasPrefix(front, "chat:"):
		a.closeChatByChat(strings.TrimPrefix(front, "chat:"))
	}
}

func (a *App) closeChatByChat(chatID string) {
	pageName := "chat:" + chatID

	a.winMu.Lock()
	idx := -1
	for i, name := range a.winOrder {
		if name == pageName {
			idx = i
			break
		}
	}
	if idx < 0 {
		a.winMu.Unlock()
		return
	}
	front, _ := a.pages.GetFrontPage()
	var next string
	a.winOrder, next = winOrderAfterClose(a.winOrder, idx)
	delete(a.chatPages, chatID)
	delete(a.chatOldest, chatID)
	delete(a.chatLoading, chatID)
	delete(a.chatBadge, pageName)
	for envID, c := range a.envToChat {
		if c == chatID {
			delete(a.envToChat, envID)
		}
	}
	a.winMu.Unlock()

	a.pages.RemovePage(pageName)

	if front == pageName && next != "" {
		a.switchTo(next)
		return
	}
	a.winBar.SetText(a.winBarText())
}

func winOrderAfterClose(order []string, idx int) (newOrder []string, next string) {
	if idx < 0 || idx >= len(order) {
		return order, ""
	}
	newOrder = append(append([]string{}, order[:idx]...), order[idx+1:]...)
	switch {
	case idx < len(newOrder):
		next = newOrder[idx]
	case idx > 0:
		next = newOrder[idx-1]
	}
	return newOrder, next
}

func (a *App) cmdRetry() {
	a.log("retrying stuck file downloads…")
	a.sendRequest(ipc.FrameRetryFiles, nil, func(f ipc.Frame) {
		if f.Type == ipc.FrameError {
			a.renderError(f)
			return
		}
		if f.Type != ipc.FrameRetryFilesResponse {
			a.log("[red]/retry[white] unexpected response type: %s", f.Type)
			return
		}
		var p ipc.RetryFilesResponse
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			a.log("[red]/retry[white] decode response: %v", err)
			return
		}
		switch {
		case p.Enqueued == 0:
			a.log("/retry: nothing to retry")
		case p.Enqueued == 1:
			a.log("/retry: 1 file re-queued")
		default:
			a.log("/retry: %d files re-queued", p.Enqueued)
		}
	})
}

func (a *App) cmdTorInfo() {
	a.log("querying tor info…")
	a.sendRequest(ipc.FrameTorInfo, nil, func(f ipc.Frame) {
		if f.Type == ipc.FrameError {
			a.renderError(f)
			return
		}
		if f.Type != ipc.FrameTorInfoResponse {
			a.log("[red]tor-info[white] unexpected response type: %s", f.Type)
			return
		}
		var p ipc.TorInfoResponsePayload
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			a.log("[red]tor-info[white] decode response: %v", err)
			return
		}
		h := p.Health
		switch {
		case h.Unreachable:
			a.log("[red]tor[white] control port unreachable")
		case !h.Ready:
			a.log("[yellow]tor[white] bootstrapping %d%%", h.Bootstrap)
		default:
			a.log("[green]tor[white] ready (bootstrap 100%%)")
		}
		if len(p.Slots) == 0 {
			a.log("[yellow]tor-info[white] no onion slots published yet")
			return
		}
		for _, s := range p.Slots {
			a.log("[green]tor slot %d[white] %s", s.Slot, s.URL)
		}
	})
}

func (a *App) renderError(f ipc.Frame) {
	var p ipc.ErrorPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		a.log("[red]error[white] (undecodable): %v", err)
		return
	}
	a.log("[red]error[white] %s: %s", p.Code, p.Message)
}

func shortID(id string) string {
	if len(id) <= 10 {
		return id
	}
	return id[:8] + "…"
}

func ifElse[T any](cond bool, a, b T) T {
	if cond {
		return a
	}
	return b
}

func (a *App) cmdInviteDHT(_ string) {
	a.log("publishing DHT invite…")
	a.sendRequest(ipc.FrameInviteDHT, ipc.InviteDHTRequest{}, func(f ipc.Frame) {
		if f.Type == ipc.FrameError {
			a.renderError(f)
			return
		}
		if f.Type != ipc.FrameInvitedDHT {
			a.log("[red]/invite-dht[white] unexpected response: %s", f.Type)
			return
		}
		var p ipc.InvitedDHTResponse
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			a.log("[red]/invite-dht[white] decode: %v", err)
			return
		}
		a.log("[green]DHT invite published[white] — share these 7 words OOB:")
		a.log("  ID  : [yellow]%s", strings.Join(p.IDWords, " "))
		a.log("  PASS: [yellow]%s", strings.Join(p.PassphraseWords, " "))
		a.log("  GUID: [gray]%s  (use /cancel-dht to revoke)", p.GUID)
		if p.ExpiresAt > 0 {
			a.log("  expires: [gray]%s", time.Unix(p.ExpiresAt, 0).Format("2006-01-02 15:04 MST"))
		}
	})
}

func (a *App) cmdAcceptDHT(arg string) {
	parts := strings.Fields(arg)
	if len(parts) < 7 {
		a.log("[red]/accept-dht[white] needs 3 id words + 4 passphrase words (7 total)")
		a.log("  example: [yellow]/accept-dht zoo fire apple  quantum bright river meadow")
		return
	}
	idWords := parts[:3]
	passWords := parts[3:7]
	a.log("accepting DHT invite [yellow]%s[white] / [yellow]%s[white]…",
		strings.Join(idWords, " "), strings.Join(passWords, " "))
	a.sendRequest(ipc.FrameAcceptDHT, ipc.AcceptDHTRequest{
		IDWords:         idWords,
		PassphraseWords: passWords,
	}, func(f ipc.Frame) {
		if f.Type == ipc.FrameError {
			a.renderError(f)
			return
		}
		if f.Type != ipc.FrameAcceptedDHT {
			a.log("[red]/accept-dht[white] unexpected response: %s", f.Type)
			return
		}
		var p ipc.AcceptedDHTResponse
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			a.log("[red]/accept-dht[white] decode: %v", err)
			return
		}
		label := p.Nick
		if label == "" {
			label = "(no nick)"
		}
		a.log("[green]paired[white] %s — peer [yellow]%s", label, p.PeerID)
		if p.IdentityFingerprint != "" {
			a.log("[yellow]verify[white] fingerprint OOB: %s", p.IdentityFingerprint)
		}

	})
}

func (a *App) cmdCancelDHT(arg string) {
	guid := strings.TrimSpace(arg)
	if guid == "" {
		a.log("[red]/cancel-dht[white] needs a guid (from /invite-dht output)")
		return
	}
	a.log("cancelling DHT invite [gray]%s[white]…", guid)
	a.sendRequest(ipc.FrameCancelDHT, ipc.CancelDHTRequest{GUID: guid}, func(f ipc.Frame) {
		if f.Type == ipc.FrameError {
			a.renderError(f)
			return
		}
		a.log("[green]cancelled[white] %s", guid)
	})
}

func (a *App) cmdInviteTor(arg string) {
	if strings.TrimSpace(arg) != "" {
		a.log("[yellow]/invite-tor[white] takes no arguments — your self-nick comes from /nick. Local-alias-for-peer is set after pair via the contacts (e)dit UX.")
	}
	a.winMu.Lock()
	nick := a.selfNick
	isDefault := a.selfNickIsDefault
	a.winMu.Unlock()
	if nick == "" {
		nick = "mynick"
		isDefault = true
	}
	if isDefault {
		a.log("[yellow]heads-up:[white] your nick is still '[yellow]%s[white]' — set yours via [yellow]/nick <name>[white] before pairing for real", nick)
	}
	a.log("creating onion invite (your nick=[yellow]%s[white])…", nick)
	a.sendRequest(ipc.FramePairOnionInvite, ipc.PairOnionInviteRequest{}, func(f ipc.Frame) {
		if f.Type == ipc.FrameError {
			a.renderError(f)
			return
		}
		if f.Type != ipc.FramePairOnionStarted {
			a.log("[red]/invite-tor[white] unexpected response: %s", f.Type)
			return
		}
		var p ipc.PairOnionStartedResponse
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			a.log("[red]/invite-tor[white] decode: %v", err)
			return
		}

		a.winMu.Lock()
		a.pendingOnionInvites[p.HandleID] = pendingOnionInvite{
			words:     p.Words,
			expiresAt: p.ExpiresAt,
		}
		a.winMu.Unlock()
		a.log("[gray]onion service created; probing reachability before sharing the words…[white]")
		a.log("  HANDLE: [gray]%s  (use /cancel-tor to abort)", p.HandleID)
		if p.ExpiresAt > 0 {
			a.log("  expires: [gray]%s", time.Unix(p.ExpiresAt, 0).Format("2006-01-02 15:04 MST"))
		}
		a.log("[gray]Tor descriptor publication typically takes 30–60s; first probe in ~10s…[white]")
	})
}

func (a *App) revealPendingOnionInvite(handleID string, fallbackTimeout bool) {
	a.winMu.Lock()
	pending, ok := a.pendingOnionInvites[handleID]
	delete(a.pendingOnionInvites, handleID)
	a.winMu.Unlock()
	if !ok {
		return
	}
	if fallbackTimeout {
		a.log("[yellow]onion not reachable yet[white] — Tor descriptor publication is slow today; sharing the words anyway. If your peer can't connect, retry in a minute.")
	} else {
		a.log("[green]ready[white] — share these %d words OOB:", len(pending.words))
	}
	a.log("  [yellow]%s", strings.Join(pending.words, " "))
	a.log("[gray]waiting for the joiner to dial…[white]")
}

func (a *App) cmdAcceptTor(arg string) {
	parts := strings.Fields(arg)
	if len(parts) < 3 {
		a.log("[red]/accept-tor[white] needs the EFF-short words from your peer's /invite-tor")
		a.log("  example: [yellow]/accept-tor acid acorn acre acts afar affix aged")
		return
	}
	words := parts
	a.winMu.Lock()
	nick := a.selfNick
	isDefault := a.selfNickIsDefault
	a.winMu.Unlock()
	if nick == "" {
		nick = "mynick"
		isDefault = true
	}
	if isDefault {
		a.log("[yellow]heads-up:[white] your nick is still '[yellow]%s[white]' — set yours via [yellow]/nick <name>[white] before pairing for real", nick)
	}
	a.log("accepting onion invite (%d words)…", len(words))
	a.log("[gray]deriving address + dialing onion via Tor (typically 5–30s — circuit setup) …[white]")
	a.sendRequest(ipc.FramePairOnionAccept, ipc.PairOnionAcceptRequest{
		Words: words,
	}, func(f ipc.Frame) {
		if f.Type == ipc.FrameError {
			a.renderError(f)
			return
		}
		if f.Type != ipc.FramePairOnionAccepted {
			a.log("[red]/accept-tor[white] unexpected response: %s", f.Type)
			return
		}
		var p ipc.PairOnionAcceptedResponse
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			a.log("[red]/accept-tor[white] decode: %v", err)
			return
		}
		label := p.Nick
		if label == "" {
			label = "(no nick)"
		}
		a.log("[green]paired (onion)[white] %s — peer [yellow]%s", label, p.PeerID)
		if p.IdentityFingerprint != "" {
			a.log("[yellow]verify[white] fingerprint OOB: %s", p.IdentityFingerprint)
		}

	})
}

func (a *App) cmdCancelTor(arg string) {
	handle := strings.TrimSpace(arg)
	if handle == "" {
		a.log("[red]/cancel-tor[white] needs a handle id (from /invite-tor output)")
		return
	}
	a.log("cancelling onion invite [gray]%s[white]…", handle)
	a.sendRequest(ipc.FramePairOnionCancel, ipc.PairOnionCancelRequest{HandleID: handle}, func(f ipc.Frame) {
		if f.Type == ipc.FrameError {
			a.renderError(f)
			return
		}
		a.log("[green]cancelled[white] %s", handle)
	})
}

var _ = fmt.Fprintf
