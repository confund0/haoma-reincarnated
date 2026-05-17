package tui

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/ipcclient"
)

type App struct {
	app    *tview.Application
	client *ipcclient.Client

	pages   *tview.Pages
	winBar  *tview.TextView
	hintBar *tview.TextView
	sysBar  *tview.TextView
	input   *tview.InputField

	haomaConnected  bool
	haomadReachable bool
	torBootstrap    int
	torReady        bool
	torUnreachable  bool
	torOnionCount   int

	lastLoggedHaoma   int
	lastLoggedBackend int

	winOrder []string
	winMu    sync.Mutex

	statusView *tview.TextView

	peers        []ipc.PeerEntry
	contactTable *tview.Table

	chats      []ipc.ChatEntry
	chatsTable *tview.Table

	chatPages   map[string]*chatPage
	chatOldest  map[string]int64
	chatLoading map[string]bool
	chatBadge   map[string]bool

	envToChat map[string]string

	DataDir string

	escPending bool

	selfPresence string
	peerPresence map[string]string

	selfNick          string
	selfNickIsDefault bool

	pendingOnionInvites map[string]pendingOnionInvite

	activeCalls map[string]ipc.CallEntry

	liveCalls map[string]*liveCallPage

	rotations map[string]rotationView

	rotationCooldowns map[string]rotationCooldown

	history    map[string][]string
	historyIdx map[string]int

	drafts map[string]string

	pendingMu sync.Mutex
	pending   map[string]responseHandler
	corrSeq   atomic.Uint64

	lastActivity atomic.Int64

	mainRoot tview.Primitive
	locked   atomic.Bool

	VaultCtl VaultController

	settings *settingsPage
}

type responseHandler func(f ipc.Frame)

type pendingOnionInvite struct {
	words     []string
	expiresAt int64
}

func New(client *ipcclient.Client) *App {
	a := &App{
		client:              client,
		pending:             map[string]responseHandler{},
		chatPages:           map[string]*chatPage{},
		chatOldest:          map[string]int64{},
		chatLoading:         map[string]bool{},
		chatBadge:           map[string]bool{},
		envToChat:           map[string]string{},
		history:             map[string][]string{},
		historyIdx:          map[string]int{},
		drafts:              map[string]string{},
		peerPresence:        map[string]string{},
		pendingOnionInvites: map[string]pendingOnionInvite{},
		activeCalls:         map[string]ipc.CallEntry{},
		liveCalls:           map[string]*liveCallPage{},
		rotations:           map[string]rotationView{},
		rotationCooldowns:   map[string]rotationCooldown{},
		lastLoggedHaoma:     -1,
		lastLoggedBackend:   -1,
	}
	a.lastActivity.Store(time.Now().Unix())

	a.app = tview.NewApplication()

	a.statusView = newStatusView()
	a.statusView.SetChangedFunc(func() { a.app.Draw() })

	a.contactTable = buildContactsTable(nil, a.peerPresenceLabel, a.peerRotationCell, a.openChat)

	a.chatsTable = buildChatsTable(nil, a.peerLabelForChat, a.peerPresenceLabel, a.peerRotationCell, a.openChat)

	a.pages = tview.NewPages()
	a.pages.AddPage("status", a.statusView, true, true)
	a.pages.AddPage("contacts", a.contactTable, true, false)
	a.pages.AddPage("chats", a.chatsTable, true, false)
	a.winOrder = []string{"status", "contacts", "chats"}

	a.winBar = tview.NewTextView().
		SetDynamicColors(true).
		SetText(a.winBarText())
	a.winBar.SetChangedFunc(func() { a.app.Draw() })

	a.hintBar = tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignRight).
		SetText(a.hintBarText("status"))
	a.hintBar.SetChangedFunc(func() { a.app.Draw() })

	a.sysBar = tview.NewTextView().
		SetDynamicColors(true).
		SetText(a.sysBarText())
	a.sysBar.SetBackgroundColor(tcell.NewRGBColor(0x20, 0x20, 0x20))
	a.sysBar.SetChangedFunc(func() { a.app.Draw() })

	a.input = tview.NewInputField().
		SetLabel("> ").
		SetFieldBackgroundColor(tcell.ColorDefault)
	a.input.SetDoneFunc(a.handleInput)

	a.input.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Key() {
		case tcell.KeyEscape:
			a.escPending = true
			return nil
		case tcell.KeyUp:
			a.historyStep(-1)
			return nil
		case tcell.KeyDown:
			a.historyStep(+1)
			return nil
		default:

			a.historyResetCursor()
		}
		return ev
	})
	a.installContactsKeys(a.contactTable)
	a.installChatsKeys(a.chatsTable)

	topBar := tview.NewFlex().
		AddItem(a.winBar, 0, 1, false).
		AddItem(a.hintBar, 30, 0, false)

	root := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(a.pages, 0, 1, false).
		AddItem(topBar, 1, 0, false).
		AddItem(a.sysBar, 1, 0, false).
		AddItem(a.input, 1, 0, true)

	a.mainRoot = root
	a.app.SetRoot(root, true).SetFocus(a.input)
	a.app.SetInputCapture(a.globalKeys)

	appendStatus(a.statusView, "[yellow]connecting to haoma…[white]")

	go a.pumpIncoming()
	go a.pumpConnection()
	return a
}

func (a *App) Run() error {
	defer a.client.Close()
	return a.app.Run()
}

func (a *App) Stop() {
	a.client.Close()
	a.app.Stop()
}

func (a *App) LastActivityUnix() int64 {
	return a.lastActivity.Load()
}

func (a *App) winBarText() string {
	a.winMu.Lock()
	defer a.winMu.Unlock()

	chatsHasUnread := false
	for _, c := range a.chats {
		if c.UnreadCount > 0 {
			chatsHasUnread = true
			break
		}
	}

	chatsInCall := map[string]bool{}
	for _, c := range a.activeCalls {
		if c.ChatID != "" {
			chatsInCall[c.ChatID] = true
		}
	}
	var sb strings.Builder
	front, _ := a.pages.GetFrontPage()
	for i, name := range a.winOrder {
		n := i + 1
		unread := a.chatBadge[name] || (name == "chats" && chatsHasUnread)
		inCall := strings.HasPrefix(name, "chat:") && chatsInCall[strings.TrimPrefix(name, "chat:")]
		switch {
		case inCall:
			fmt.Fprintf(&sb, " %s[%d:%s %s]%s", StyleWinBarInCall, n, a.winName(name), GlyphInCall, StyleReset)
		case name == front:
			fmt.Fprintf(&sb, " %s[%d:%s]%s", StyleWinBarActive, n, a.winName(name), StyleReset)
		case unread:
			fmt.Fprintf(&sb, " %s[%d:%s %s]%s", StyleWinBarUnread, n, a.winName(name), GlyphUnread, StyleReset)
		default:
			fmt.Fprintf(&sb, " %s[%d]%s", StyleWinBarIdle, n, StyleReset)
		}
	}
	return sb.String()
}

func (a *App) hintBarText(name string) string {
	var hint string
	switch {
	case name == "contacts":
		hint = "Keys: e:edit f:fp"
	case name == "chats":
		hint = "Keys: e:edit"
	case strings.HasPrefix(name, "chat:"):
		hint = "Keys: PgUp:older"
	default:

		return ""
	}

	return StyleHintText + hint + " " + StyleReset
}

func (a *App) sysBarText() string {
	a.winMu.Lock()
	haoma := a.haomaConnected
	haomad := a.haomadReachable
	bootstrap := a.torBootstrap
	ready := a.torReady
	unreachable := a.torUnreachable
	onionCount := a.torOnionCount
	selfState := a.selfPresence
	selfNick := a.selfNick
	activeRotations := len(a.rotations)
	now := time.Now().Unix()
	cooldownPeers := 0
	for peerID, cd := range a.rotationCooldowns {
		if cd.ExpiresAt <= now {
			continue
		}
		if _, inflight := a.rotations[peerID]; inflight {
			continue
		}
		cooldownPeers++
	}
	a.winMu.Unlock()

	uiStyle := StylePipelineOK

	feStyle := StylePipelineBad
	if haoma {
		feStyle = StylePipelineOK
	}
	feArrow := feStyle

	beStyle := StylePipelineIdle
	if haoma {
		if haomad {
			beStyle = StylePipelineOK
		} else {
			beStyle = StylePipelineBad
		}
	}
	beArrow := beStyle

	torStyle := StylePipelineIdle
	bootStr := "--"
	onionStr := "--"
	switch {
	case !haoma || !haomad:

	case unreachable:
		torStyle = StylePipelineBad
	case ready && onionCount > 0:
		torStyle = StylePipelineOK
		bootStr = "100%"
		onionStr = "OK"
	case ready:

		torStyle = StylePipelineWarn
		bootStr = "100%"
	case bootstrap > 0:
		torStyle = StylePipelineWarn
		bootStr = fmt.Sprintf("%d%%", bootstrap)
	default:

		torStyle = StylePipelineBad
	}
	torArrow := torStyle

	chip := func(letters string, bracketStyle string) string {
		return bracketStyle + "[" + StylePipelineText + letters + bracketStyle + "]" + StyleReset
	}
	arrow := func(style string) string {
		return " " + style + "->" + StyleReset + " "
	}

	torSuffix := chip(bootStr+" | "+onionStr, torStyle)

	selfLabel := effectiveSelfLabel(selfState)
	nickText := selfNick
	if nickText == "" {
		nickText = "mynick"
	}
	meSuffix := "  " + presenceTag(selfLabel) + "[" + StylePipelineText + "me: " + nickText + presenceTag(selfLabel) + "]" + StyleReset

	rotSuffix := ""
	totalRot := activeRotations + cooldownPeers
	if totalRot > 0 {
		rotStyle := StyleRotationCooldown
		if activeRotations > 0 {
			rotStyle = StyleRotationActive
		}
		rotSuffix = "  " + chip(fmt.Sprintf("R:%d", totalRot), rotStyle)
	}

	return " " + chip("UI", uiStyle) +
		arrow(feArrow) + chip("FE", feStyle) +
		arrow(beArrow) + chip("BE", beStyle) +
		arrow(torArrow) + chip("Tor", torStyle) + torSuffix +
		rotSuffix +
		meSuffix
}

func (a *App) winName(name string) string {
	switch name {
	case "status":
		return "status"
	case "contacts":
		return "contacts"
	case "chats":
		return "chats"
	default:
		chatID := strings.TrimPrefix(name, "chat:")
		cp := a.chatPages[chatID]
		if cp != nil && cp.nickname != "" {
			return cp.nickname
		}
		if cp != nil {
			return shortID(cp.peerID)
		}
		return shortID(chatID)
	}
}

func (a *App) peerNickLocked(peerID string) string {
	for _, p := range a.peers {
		if p.ID == peerID {
			return p.Label
		}
	}
	return ""
}

func (a *App) peerByIDLocked(peerID string) *ipc.PeerEntry {
	for i := range a.peers {
		if a.peers[i].ID == peerID {
			return &a.peers[i]
		}
	}
	return nil
}

func (a *App) peerRetiredAt(peerID string) int64 {
	a.winMu.Lock()
	defer a.winMu.Unlock()
	if p := a.peerByIDLocked(peerID); p != nil {
		return p.RetiredAt
	}
	return 0
}

func (a *App) switchTo(name string) {

	front, _ := a.pages.GetFrontPage()
	a.winMu.Lock()
	if front != "" && front != name {
		a.drafts[draftKey(front)] = a.input.GetText()
	}
	incoming := a.drafts[draftKey(name)]

	a.chatBadge[name] = false
	a.winMu.Unlock()

	a.pages.SwitchToPage(name)
	a.input.SetText(incoming)

	a.historyResetCursor()
	a.winBar.SetText(a.winBarText())
	a.hintBar.SetText(a.hintBarText(name))

	switch name {
	case "contacts":
		a.app.SetFocus(a.contactTable)
	case "chats":
		a.app.SetFocus(a.chatsTable)
	case pageNameSettings:
		a.winMu.Lock()
		sp := a.settings
		a.winMu.Unlock()
		if sp != nil {
			a.app.SetFocus(sp.list)
		} else {
			a.app.SetFocus(a.input)
		}
	default:
		a.app.SetFocus(a.input)
	}
	if strings.HasPrefix(name, "chat:") {
		chatID := strings.TrimPrefix(name, "chat:")

		a.sendRequest(ipc.FrameMarkRead, ipc.MarkReadRequest{ChatID: chatID}, func(resp ipc.Frame) {
			if resp.Type == ipc.FrameError {
				a.renderError(resp)
			}
		})

		a.sendRequest(ipc.FrameClientFocus, ipc.ClientFocusRequest{
			ChatID:         chatID,
			ScrollPosition: 0,
		}, nil)
	} else {

		a.sendRequest(ipc.FrameClientFocus, ipc.ClientFocusRequest{
			ChatID: "",
		}, nil)
	}
}

func (a *App) switchToN(n int) {
	a.winMu.Lock()
	if n < 1 || n > len(a.winOrder) {
		a.winMu.Unlock()
		return
	}
	name := a.winOrder[n-1]
	a.winMu.Unlock()
	a.switchTo(name)
}

func (a *App) openChat(peerID string) {
	a.winMu.Lock()
	var (
		nick      string
		retiredAt int64
		chatID    string
	)
	if p := a.peerByIDLocked(peerID); p != nil {
		nick = p.Label
		retiredAt = p.RetiredAt
		chatID = p.ChatID
	}
	a.winMu.Unlock()

	if chatID == "" {

		a.sendRequest(ipc.FrameEnsureChat, ipc.EnsureChatRequest{PeerID: peerID}, func(f ipc.Frame) {
			if f.Type == ipc.FrameError {
				a.renderError(f)
				return
			}
			if f.Type != ipc.FrameChatEnsured {
				a.log("[red]open chat[white] unexpected response: %s", f.Type)
				return
			}
			var p ipc.ChatEnsuredResponse
			if err := json.Unmarshal(f.Payload, &p); err != nil {
				a.log("[red]open chat[white] decode: %v", err)
				return
			}
			if p.Peer.ID == "" || p.Chat.ChatID == "" {
				a.log("[red]open chat[white] empty ensure_chat reply")
				return
			}
			a.winMu.Lock()
			patched := false
			for i := range a.peers {
				if a.peers[i].ID == p.Peer.ID {
					a.peers[i] = p.Peer
					patched = true
					break
				}
			}
			if !patched {
				a.peers = append(a.peers, p.Peer)
			}
			chatPatched := false
			for i := range a.chats {
				if a.chats[i].ChatID == p.Chat.ChatID {
					a.chats[i] = p.Chat
					chatPatched = true
					break
				}
			}
			if !chatPatched {
				a.chats = append(a.chats, p.Chat)
			}
			a.winMu.Unlock()
			a.app.QueueUpdateDraw(func() { a.openChat(p.Peer.ID) })
		})
		return
	}
	pageName := "chat:" + chatID

	a.winMu.Lock()
	_, alreadyOpen := a.chatPages[chatID]
	var cp *chatPage
	if !alreadyOpen {
		cp = newChatPage(chatID, peerID, nick, retiredAt)
		cp.applyTitle(a.peerEffectiveLabel(peerID))
		a.chatPages[chatID] = cp
		a.winOrder = append(a.winOrder, pageName)
	}
	a.winMu.Unlock()

	if !alreadyOpen {
		cp.view.SetChangedFunc(func() { a.app.Draw() })

		cp.view.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
			if ev.Key() == tcell.KeyPgUp {
				a.loadOlderHistory(chatID, peerID)
				return nil
			}
			if ev.Key() == tcell.KeyRune && ev.Rune() == 'f' {
				a.cmdFiles()
				return nil
			}
			if ev.Key() == tcell.KeyRune && ev.Rune() == 'a' {
				a.cmdAttach()
				return nil
			}
			return ev
		})

		a.pages.AddPage(pageName, cp.view, true, false)
		a.loadOlderHistory(chatID, peerID)
	}

	a.switchTo(pageName)
}

func (a *App) loadOlderHistory(chatID, peerID string) {
	a.winMu.Lock()
	if a.chatLoading[chatID] {
		a.winMu.Unlock()
		return
	}
	a.chatLoading[chatID] = true
	before := a.chatOldest[chatID]
	a.winMu.Unlock()

	a.sendRequest(ipc.FrameListTimeline, ipc.ListTimelineRequest{
		PeerID:          peerID,
		Limit:           50,
		BeforeDisplayTs: before,
	}, func(f ipc.Frame) {
		a.winMu.Lock()
		a.chatLoading[chatID] = false
		a.winMu.Unlock()

		if f.Type == ipc.FrameError {
			a.renderError(f)
			return
		}
		if f.Type != ipc.FrameTimelinePage {
			return
		}
		var p ipc.TimelinePageResponse
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			return
		}
		if len(p.Events) == 0 {
			return
		}

		a.winMu.Lock()
		cp := a.chatPages[chatID]
		a.winMu.Unlock()
		if cp == nil {
			return
		}

		var oldest rawEvent
		if err := json.Unmarshal(p.Events[len(p.Events)-1], &oldest); err == nil {
			a.winMu.Lock()
			if a.chatOldest[chatID] == 0 || oldest.DisplayTs < a.chatOldest[chatID] {
				a.chatOldest[chatID] = oldest.DisplayTs
			}
			a.winMu.Unlock()
		}

		a.winMu.Lock()
		for _, rawEv := range p.Events {
			var ev rawEvent
			if err := json.Unmarshal(rawEv, &ev); err != nil {
				continue
			}
			if ev.Direction == "out" && ev.EnvelopeID != "" {
				a.envToChat[ev.EnvelopeID] = chatID
			}
		}
		a.winMu.Unlock()

		a.app.QueueUpdateDraw(func() {
			cp.prependEvents(p.Events)
		})
	})
}

func (a *App) globalKeys(ev *tcell.EventKey) *tcell.EventKey {

	a.lastActivity.Store(time.Now().Unix())

	if a.locked.Load() {
		return ev
	}

	if ev.Key() == tcell.KeyTab {
		if front, prim := a.pages.GetFrontPage(); prim != nil &&
			(strings.HasPrefix(front, "call-ringer:") || strings.HasPrefix(front, "call-live:")) {
			focus := a.app.GetFocus()
			if _, onInput := focus.(*tview.InputField); onInput || focus == nil {
				a.app.SetFocus(prim)
				return nil
			}
		}
	}

	if ev.Key() == tcell.KeyCtrlD {
		a.client.Close()
		a.app.Stop()
		return nil
	}

	if a.escPending {
		a.escPending = false
		if ev.Key() == tcell.KeyRune {
			r := ev.Rune()
			if r >= '1' && r <= '9' {
				a.switchToN(int(r - '0'))
				return nil
			}
		}

	}

	if ev.Key() == tcell.KeyF6 {
		front, _ := a.pages.GetFrontPage()
		if !a.isMainWindow(front) {
			return ev
		}
		if _, onInput := a.app.GetFocus().(*tview.InputField); onInput {
			if pane := a.mainPaneForFront(front); pane != nil {
				a.app.SetFocus(pane)
			}
		} else {
			a.app.SetFocus(a.input)
		}
		return nil
	}

	if ev.Key() == tcell.KeyRune && ev.Rune() == '/' {
		if _, isInput := a.app.GetFocus().(*tview.InputField); !isInput {
			a.input.SetText("/")
			a.app.SetFocus(a.input)
			return nil
		}
	}
	return ev
}

func (a *App) pumpConnection() {
	for up := range a.client.Connection() {
		a.winMu.Lock()
		a.haomaConnected = up
		if !up {

			a.haomadReachable = false
			a.torBootstrap = 0
			a.torReady = false
			a.torUnreachable = true
			a.torOnionCount = 0
		}
		shouldLog := -1
		switch {
		case up && a.lastLoggedHaoma != 1:
			a.lastLoggedHaoma = 1
			shouldLog = 1
		case !up && a.lastLoggedHaoma != 0:
			a.lastLoggedHaoma = 0
			shouldLog = 0
		}
		a.winMu.Unlock()

		a.app.QueueUpdateDraw(func() {
			a.sysBar.SetText(a.sysBarText())
			switch shouldLog {
			case 1:
				appendStatus(a.statusView, "[green]haoma connected[white] — waiting for backend status")
			case 0:
				appendStatus(a.statusView, "[red]haoma disconnected[white] — input disabled, reconnecting…")
			}
		})
	}
}

func (a *App) pumpIncoming() {
	for f := range a.client.Incoming() {
		if h := a.takePending(f.ID); h != nil {
			h(f)
			continue
		}
		a.routeFrame(f)
	}
	a.app.QueueUpdateDraw(func() {
		appendStatus(a.statusView, "(disconnected from daemon)")
	})
}

func (a *App) routeFrame(f ipc.Frame) {
	switch f.Type {
	case ipc.FrameWelcome:

		var p ipc.WelcomePayload
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			slog.Warn("decode welcome", slog.Any("err", err))
			a.app.QueueUpdateDraw(func() {
				appendStatus(a.statusView, "[red]malformed welcome frame from daemon[white] — %v", err)
			})
			break
		}
		a.winMu.Lock()
		a.selfNick = p.SelfNick
		a.selfNickIsDefault = p.SelfNickIsDefault
		a.winMu.Unlock()
		a.app.QueueUpdateDraw(func() {
			appendStatus(a.statusView, "[green]connected[white] — daemon %s (protocol v%d)", p.DaemonVersion, p.ProtocolVersion)
			appendStatus(a.statusView, "Esc+1 status  Esc+2 contacts  Esc+N chat  /help for commands")
			if p.SelfNickIsDefault {
				appendStatus(a.statusView, "[yellow]your nick is '%s'[white] — set yours via [yellow]/nick <name>[white]", p.SelfNick)
			}
			a.sysBar.SetText(a.sysBarText())
		})

		a.sendRequest(ipc.FrameSubscribe, ipc.SubscribeRequest{}, a.handleSubscribed)

	case ipc.FrameNick:
		var p ipc.NickPayload
		if err := json.Unmarshal(f.Payload, &p); err == nil {
			a.winMu.Lock()
			a.selfNick = p.Nick
			a.selfNickIsDefault = p.IsDefault
			a.winMu.Unlock()
			a.app.QueueUpdateDraw(func() {
				appendStatus(a.statusView, "[green]nick set:[white] [yellow]%s[white]", p.Nick)
				a.sysBar.SetText(a.sysBarText())
			})
		}

	case ipc.FramePairOnionProbe:
		var p ipc.PairOnionProbePush
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			break
		}
		a.app.QueueUpdateDraw(func() {
			if p.Ready {
				a.revealPendingOnionInvite(p.HandleID, p.Error == "probe-timeout")
				return
			}
			detail := p.Error
			if detail == "" {
				detail = "no response yet"
			}
			a.log("[gray]probe attempt %d:[white] %s", p.Attempt, detail)
		})

	case ipc.FramePing:
		pong, _ := ipc.NewFrame(ipc.FramePong, f.ID, nil)
		a.client.Send(pong)

	case ipc.FrameTimelineEvent:
		a.routeTimelineEvent(f)

	case ipc.FrameTimelineEventDeleted:
		a.routeTimelineEventDeleted(f)

	case ipc.FrameFileProgress:
		a.routeFileProgress(f)

	case ipc.FrameChatSettings:

		a.sendRequest(ipc.FrameListChats, nil, a.handleChatsListed)

	case ipc.FrameBackendStatus:
		a.routeBackendStatus(f)

	case ipc.FramePresenceChanged:
		a.routePresenceChanged(f)

	case ipc.FramePeerLastSeenChanged:
		a.routePeerLastSeenChanged(f)

	case ipc.FramePeerUpdated:
		a.routePeerUpdated(f)

	case ipc.FrameChatUpdated:
		a.routeChatUpdated(f)

	case ipc.FrameChatActivityChanged:
		a.routeChatActivityChanged(f)

	case ipc.FrameChatUnreadChanged:
		a.routeChatUnreadChanged(f)

	case ipc.FramePeerDeleted:
		a.routePeerDeleted(f)

	case ipc.FrameChatCleared:
		a.routeChatCleared(f)

	case ipc.FrameChatDeleted:
		a.routeChatDeleted(f)

	case ipc.FrameAcceptedDHT:

		var p ipc.AcceptedDHTResponse
		if err := json.Unmarshal(f.Payload, &p); err == nil {
			label := p.Nick
			if label == "" {
				label = "(no nick)"
			}
			a.app.QueueUpdateDraw(func() {
				appendStatus(a.statusView, "[green]paired[white] %s — peer [yellow]%s", label, p.PeerID)
				if p.IdentityFingerprint != "" {
					appendStatus(a.statusView, "[yellow]verify[white] fingerprint OOB: %s", p.IdentityFingerprint)
				}
			})
		}

	case ipc.FramePeerPaired:

	case ipc.FramePairOnionCompleted:

		var p ipc.PairOnionCompletedPush
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			slog.Warn("decode pair_onion_completed", slog.Any("err", err))
			break
		}
		label := p.Nick
		if label == "" {
			label = "(no nick)"
		}
		a.app.QueueUpdateDraw(func() {
			appendStatus(a.statusView, "[green]paired (onion)[white] %s — peer [yellow]%s", label, p.PeerID)
			if p.IdentityFingerprint != "" {
				appendStatus(a.statusView, "[yellow]verify[white] fingerprint OOB: %s", p.IdentityFingerprint)
			}
			appendStatus(a.statusView, "[gray]ephemeral onion torn down; contact persisted[white]")
		})

	case ipc.FramePairOnionFailed:

		var p ipc.PairOnionFailedPush
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			slog.Warn("decode pair_onion_failed", slog.Any("err", err))
			break
		}
		a.app.QueueUpdateDraw(func() {
			appendStatus(a.statusView, "[red]onion invite failed[white] (%s) %s", p.Reason, p.Detail)
		})

	case ipc.FrameStatusEvent, ipc.FrameError:
		a.app.QueueUpdateDraw(func() {
			renderToStatus(a.statusView, f)
		})

	case ipc.FrameCallStateChanged:
		a.routeCallStateChanged(f)

	case ipc.FrameCallStreamEvent:
		a.routeCallStreamEvent(f)

	case ipc.FrameCallStreamRawTransport:

	case ipc.FrameRotateRequested:
		a.routeRotateRequested(f)

	case ipc.FrameRotateLifecycle:
		a.routeRotateLifecycle(f)

	case ipc.FrameDeliveryStatus:
		var p ipc.DeliveryStatusPayload
		_ = json.Unmarshal(f.Payload, &p)
		a.winMu.Lock()
		chatID := a.envToChat[p.EnvelopeID]
		cp := a.chatPages[chatID]
		a.winMu.Unlock()
		if cp != nil {
			a.app.QueueUpdateDraw(func() {
				cp.setDeliveryState(p.EnvelopeID, p.State)
			})
		} else {
			a.app.QueueUpdateDraw(func() {
				renderToStatus(a.statusView, f)
			})
		}

	default:
		a.app.QueueUpdateDraw(func() {
			appendStatus(a.statusView, "frame: %s", f.Type)
		})
	}
}

func (a *App) routePeerUpdated(f ipc.Frame) {
	var p ipc.PeerUpdatedPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		slog.Warn("tui: peer_updated decode failed", slog.Any("err", err))
		return
	}
	if p.Peer.ID == "" {
		return
	}
	slog.Debug("tui: peer_updated received",
		slog.String("peer_id", p.Peer.ID),
		slog.String("label", p.Peer.Label),
		slog.String("nick", p.Peer.Nick),
		slog.String("alias", p.Peer.Alias),
		slog.Int64("retired_at", p.Peer.RetiredAt),
	)
	a.winMu.Lock()
	patched := false
	for i := range a.peers {
		if a.peers[i].ID == p.Peer.ID {
			a.peers[i] = p.Peer
			patched = true
			break
		}
	}
	if !patched {
		a.peers = append(a.peers, p.Peer)
	}
	a.winMu.Unlock()

	a.app.QueueUpdateDraw(func() {
		a.winMu.Lock()
		peers := a.peers
		a.winMu.Unlock()
		front, _ := a.pages.GetFrontPage()
		a.contactTable = buildContactsTable(peers, a.peerPresenceLabel, a.peerRotationCell, a.openChat)
		a.installContactsKeys(a.contactTable)
		a.pages.RemovePage("contacts")
		a.pages.AddPage("contacts", a.contactTable, true, false)
		if front == "contacts" {
			a.pages.SwitchToPage("contacts")
			a.app.SetFocus(a.contactTable)
		}

		a.winMu.Lock()
		defer a.winMu.Unlock()
		for _, cp := range a.chatPages {
			if cp.peerID == p.Peer.ID {
				if p.Peer.Label != "" {
					cp.nickname = p.Peer.Label
				}
				cp.applyTitle(a.peerEffectiveLabel(p.Peer.ID))
			}
		}
	})
}

func (a *App) routeChatUpdated(f ipc.Frame) {
	var p ipc.ChatUpdatedPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		slog.Warn("tui: chat_updated decode failed", slog.Any("err", err))
		return
	}
	if p.Chat.ChatID == "" {
		return
	}
	slog.Debug("tui: chat_updated received",
		slog.String("chat_id", p.Chat.ChatID),
		slog.String("peer_id", p.Chat.PeerID),
		slog.String("label", p.Chat.Label),
	)
	a.winMu.Lock()
	patched := false
	for i := range a.chats {
		if a.chats[i].ChatID == p.Chat.ChatID {
			a.chats[i] = p.Chat
			patched = true
			break
		}
	}
	if !patched {
		a.chats = append(a.chats, p.Chat)
	}
	a.winMu.Unlock()

	a.app.QueueUpdateDraw(func() {
		a.winMu.Lock()
		chats := a.chats
		a.winMu.Unlock()
		front, _ := a.pages.GetFrontPage()
		a.chatsTable = buildChatsTable(chats, a.peerLabelForChat, a.peerPresenceLabel, a.peerRotationCell, a.openChat)
		a.installChatsKeys(a.chatsTable)
		a.pages.RemovePage("chats")
		a.pages.AddPage("chats", a.chatsTable, true, false)
		if front == "chats" {
			a.pages.SwitchToPage("chats")
			a.app.SetFocus(a.chatsTable)
		}
	})
}

func (a *App) routeChatActivityChanged(f ipc.Frame) {
	var p ipc.ChatActivityChangedPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		slog.Warn("tui: chat.activity-changed decode failed", slog.Any("err", err))
		return
	}
	if p.ChatID == "" {
		return
	}
	a.winMu.Lock()
	patched := false
	for i := range a.chats {
		if a.chats[i].ChatID == p.ChatID {
			a.chats[i].LastActivityAt = p.LastActivityAt
			patched = true
			break
		}
	}
	a.winMu.Unlock()
	if !patched {
		slog.Debug("tui: chat.activity-changed for unknown chat (dropped)",
			slog.String("chat_id", p.ChatID),
		)
		return
	}
	a.rebuildChatsTable()
}

func (a *App) routeChatUnreadChanged(f ipc.Frame) {
	var p ipc.ChatUnreadChangedPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		slog.Warn("tui: chat.unread-changed decode failed", slog.Any("err", err))
		return
	}
	if p.ChatID == "" {
		return
	}
	a.winMu.Lock()
	patched := false
	for i := range a.chats {
		if a.chats[i].ChatID == p.ChatID {
			a.chats[i].UnreadCount = p.UnreadCount
			patched = true
			break
		}
	}
	a.winMu.Unlock()
	if !patched {
		slog.Debug("tui: chat.unread-changed for unknown chat (dropped)",
			slog.String("chat_id", p.ChatID),
		)
		return
	}
	a.rebuildChatsTable()
}

func (a *App) rebuildChatsTable() {
	a.app.QueueUpdateDraw(func() {
		a.winMu.Lock()
		chats := a.chats
		a.winMu.Unlock()
		front, _ := a.pages.GetFrontPage()
		a.chatsTable = buildChatsTable(chats, a.peerLabelForChat, a.peerPresenceLabel, a.peerRotationCell, a.openChat)
		a.installChatsKeys(a.chatsTable)
		a.pages.RemovePage("chats")
		a.pages.AddPage("chats", a.chatsTable, true, false)
		if front == "chats" {
			a.pages.SwitchToPage("chats")
			a.app.SetFocus(a.chatsTable)
		}
		a.winBar.SetText(a.winBarText())
	})
}

func (a *App) routePeerDeleted(f ipc.Frame) {
	var p ipc.PeerDeletedPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		slog.Warn("tui: peer_deleted decode failed", slog.Any("err", err))
		return
	}
	if p.PeerID == "" {
		return
	}
	a.winMu.Lock()
	peers := a.peers[:0]
	for _, peer := range a.peers {
		if peer.ID != p.PeerID {
			peers = append(peers, peer)
		}
	}
	a.peers = peers
	chats := a.chats[:0]
	var droppedChatIDs []string
	for _, c := range a.chats {
		if c.Kind == ipc.ChatKindDirect && c.PeerID == p.PeerID {
			droppedChatIDs = append(droppedChatIDs, c.ChatID)
			continue
		}
		chats = append(chats, c)
	}
	a.chats = chats
	a.winMu.Unlock()
	slog.Debug("tui: peer_deleted received",
		slog.String("peer_id", p.PeerID),
		slog.Int("dropped_chats", len(droppedChatIDs)),
	)
	a.app.QueueUpdateDraw(func() {
		for _, id := range droppedChatIDs {
			a.closeChatByChat(id)
		}
		a.winMu.Lock()
		peers := a.peers
		chats := a.chats
		a.winMu.Unlock()
		front, _ := a.pages.GetFrontPage()
		a.contactTable = buildContactsTable(peers, a.peerPresenceLabel, a.peerRotationCell, a.openChat)
		a.installContactsKeys(a.contactTable)
		a.pages.RemovePage("contacts")
		a.pages.AddPage("contacts", a.contactTable, true, false)
		a.chatsTable = buildChatsTable(chats, a.peerLabelForChat, a.peerPresenceLabel, a.peerRotationCell, a.openChat)
		a.installChatsKeys(a.chatsTable)
		a.pages.RemovePage("chats")
		a.pages.AddPage("chats", a.chatsTable, true, false)
		switch front {
		case "contacts":
			a.pages.SwitchToPage("contacts")
			a.app.SetFocus(a.contactTable)
		case "chats":
			a.pages.SwitchToPage("chats")
			a.app.SetFocus(a.chatsTable)
		}
	})
}

func (a *App) routeChatCleared(f ipc.Frame) {
	var p ipc.ChatClearedPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		slog.Warn("tui: chat_cleared decode failed", slog.Any("err", err))
		return
	}
	if p.ChatID == "" {
		return
	}
	slog.Debug("tui: chat_cleared received",
		slog.String("chat_id", p.ChatID),
		slog.Int("deleted_count", p.DeletedCount),
	)
	a.app.QueueUpdateDraw(func() {
		a.closeChatByChat(p.ChatID)
	})
}

func (a *App) routeChatDeleted(f ipc.Frame) {
	var p ipc.ChatDeletedPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		slog.Warn("tui: chat_deleted decode failed", slog.Any("err", err))
		return
	}
	if p.ChatID == "" {
		return
	}
	a.winMu.Lock()
	chats := a.chats[:0]
	for _, c := range a.chats {
		if c.ChatID != p.ChatID {
			chats = append(chats, c)
		}
	}
	a.chats = chats
	a.winMu.Unlock()
	slog.Debug("tui: chat_deleted received",
		slog.String("chat_id", p.ChatID),
		slog.Int("deleted_count", p.DeletedCount),
	)
	a.app.QueueUpdateDraw(func() {
		a.closeChatByChat(p.ChatID)
		a.winMu.Lock()
		chats := a.chats
		a.winMu.Unlock()
		front, _ := a.pages.GetFrontPage()
		a.chatsTable = buildChatsTable(chats, a.peerLabelForChat, a.peerPresenceLabel, a.peerRotationCell, a.openChat)
		a.installChatsKeys(a.chatsTable)
		a.pages.RemovePage("chats")
		a.pages.AddPage("chats", a.chatsTable, true, false)
		if front == "chats" {
			a.pages.SwitchToPage("chats")
			a.app.SetFocus(a.chatsTable)
		}
	})
}

func (a *App) routePresenceChanged(f ipc.Frame) {
	var p ipc.PresenceChangedPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		slog.Warn("tui: presence_changed decode failed", slog.Any("err", err))
		return
	}
	if p.PeerID == "" {
		return
	}
	label := p.Effective
	if label == "" {
		label = "unknown"
	}
	a.winMu.Lock()
	prev := a.peerPresence[p.PeerID]
	a.peerPresence[p.PeerID] = label
	var pages []*chatPage
	for _, cp := range a.chatPages {
		if cp.peerID == p.PeerID {
			pages = append(pages, cp)
		}
	}
	a.winMu.Unlock()
	slog.Debug("tui: presence_changed received",
		slog.String("peer_id", p.PeerID),
		slog.String("effective", label),
		slog.String("prev", prev),
		slog.Bool("accepting", p.Accepting),
		slog.String("chatty", p.Chatty),
		slog.Int("open_chat_pages", len(pages)),
	)
	a.app.QueueUpdateDraw(func() {
		for _, cp := range pages {
			cp.applyTitle(label)
		}

		a.winMu.Lock()
		peers := a.peers
		chats := a.chats
		a.winMu.Unlock()
		front, _ := a.pages.GetFrontPage()
		a.contactTable = buildContactsTable(peers, a.peerPresenceLabel, a.peerRotationCell, a.openChat)
		a.installContactsKeys(a.contactTable)
		a.pages.RemovePage("contacts")
		a.pages.AddPage("contacts", a.contactTable, true, false)
		a.chatsTable = buildChatsTable(chats, a.peerLabelForChat, a.peerPresenceLabel, a.peerRotationCell, a.openChat)
		a.installChatsKeys(a.chatsTable)
		a.pages.RemovePage("chats")
		a.pages.AddPage("chats", a.chatsTable, true, false)
		switch front {
		case "contacts":
			a.pages.SwitchToPage("contacts")
			a.app.SetFocus(a.contactTable)
		case "chats":
			a.pages.SwitchToPage("chats")
			a.app.SetFocus(a.chatsTable)
		}
	})
}

func (a *App) routePeerLastSeenChanged(f ipc.Frame) {
	var p ipc.PeerLastSeenChangedPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		slog.Warn("tui: peer.last-seen-changed decode failed", slog.Any("err", err))
		return
	}
	if p.PeerID == "" {
		return
	}
	a.winMu.Lock()
	patched := false
	for i := range a.peers {
		if a.peers[i].ID == p.PeerID {
			a.peers[i].LastActiveAt = p.LastActiveAt
			a.peers[i].LastPassiveAt = p.LastPassiveAt
			patched = true
			break
		}
	}
	a.winMu.Unlock()
	if !patched {
		slog.Debug("tui: peer.last-seen-changed for unknown peer (dropped)",
			slog.String("peer_id", p.PeerID),
		)
		return
	}
	a.app.QueueUpdateDraw(func() {
		a.winMu.Lock()
		peers := a.peers
		a.winMu.Unlock()
		front, _ := a.pages.GetFrontPage()
		a.contactTable = buildContactsTable(peers, a.peerPresenceLabel, a.peerRotationCell, a.openChat)
		a.installContactsKeys(a.contactTable)
		a.pages.RemovePage("contacts")
		a.pages.AddPage("contacts", a.contactTable, true, false)
		if front == "contacts" {
			a.pages.SwitchToPage("contacts")
			a.app.SetFocus(a.contactTable)
		}
	})
}

func (a *App) routeBackendStatus(f ipc.Frame) {
	var p ipc.BackendStatusPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		return
	}
	a.winMu.Lock()
	a.haomadReachable = p.BackendReachable
	a.torBootstrap = p.Tor.Bootstrap
	a.torReady = p.Tor.Ready
	a.torUnreachable = p.Tor.Unreachable
	a.torOnionCount = p.OnionCount

	shouldLog := -1
	switch {
	case p.BackendReachable && a.lastLoggedBackend != 1:
		a.lastLoggedBackend = 1
		shouldLog = 1
	case !p.BackendReachable && a.lastLoggedBackend != 0:
		a.lastLoggedBackend = 0
		shouldLog = 0
	}
	a.winMu.Unlock()

	a.app.QueueUpdateDraw(func() {
		a.sysBar.SetText(a.sysBarText())
		switch shouldLog {
		case 1:
			appendStatus(a.statusView, "[green]haomad reachable[white]")
		case 0:
			appendStatus(a.statusView, "[yellow]haomad unreachable[white] — messages will queue")
		}
	})
}

func (a *App) handleSubscribed(f ipc.Frame) {
	if f.Type == ipc.FrameError {

		a.app.QueueUpdateDraw(func() {
			appendStatus(a.statusView, "[yellow]subscribe unsupported[white] — fetching snapshots directly (older daemon?)")
		})
	}
	a.sendRequest(ipc.FrameListPeers, nil, a.handlePeersListed)
	a.sendRequest(ipc.FrameListChats, nil, a.handleChatsListed)

	a.pushSettingsSync()
}

func (a *App) handlePeersListed(f ipc.Frame) {
	if f.Type == ipc.FrameError {
		return
	}
	var p ipc.PeersListedResponse
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		return
	}
	a.winMu.Lock()
	a.peers = p.Peers
	a.winMu.Unlock()
	a.app.QueueUpdateDraw(func() {
		a.winMu.Lock()
		peers := a.peers
		a.winMu.Unlock()
		front, _ := a.pages.GetFrontPage()
		a.contactTable = buildContactsTable(peers, a.peerPresenceLabel, a.peerRotationCell, a.openChat)
		a.installContactsKeys(a.contactTable)
		a.pages.RemovePage("contacts")
		a.pages.AddPage("contacts", a.contactTable, true, false)
		if front == "contacts" {
			a.pages.SwitchToPage("contacts")
			a.app.SetFocus(a.contactTable)
		}

		a.winMu.Lock()
		defer a.winMu.Unlock()
		for _, peer := range peers {
			if peer.Effective != "" {
				a.peerPresence[peer.ID] = peer.Effective
			}
			cp := a.chatPages[peer.ChatID]
			if cp == nil {
				continue
			}
			if peer.Label != "" && cp.nickname != peer.Label {
				cp.nickname = peer.Label
			}
			cp.retiredAt = peer.RetiredAt
			cp.applyTitle(a.peerEffectiveLabel(peer.ID))
		}
	})
}

func (a *App) routeTimelineEventDeleted(f ipc.Frame) {
	var p ipc.TimelineEventDeletedPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		return
	}
	a.winMu.Lock()
	cp := a.chatPages[p.ChatID]
	a.winMu.Unlock()
	if cp == nil {
		return
	}
	a.app.QueueUpdateDraw(func() {
		cp.deleteByRecvSeq(p.RecvSeq)
	})
}

func (a *App) routeFileProgress(f ipc.Frame) {
	var p ipc.FileProgressPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		return
	}
	if p.ChatID == "" || p.MsgID == "" {
		return
	}
	a.winMu.Lock()
	cp := a.chatPages[p.ChatID]
	a.winMu.Unlock()
	if cp == nil {
		return
	}
	a.app.QueueUpdateDraw(func() {
		cp.updateFileProgress(p.MsgID, p.BytesReceived)
	})
}

func (a *App) routeTimelineEvent(f ipc.Frame) {
	var wrapper ipc.TimelineEventPayload
	if err := json.Unmarshal(f.Payload, &wrapper); err != nil {
		return
	}
	var ev rawEvent
	if err := json.Unmarshal(wrapper.Event, &ev); err != nil {
		return
	}
	chatID := ev.ChatID
	if chatID == "" {
		return
	}
	pageName := "chat:" + chatID

	if ev.Direction == "out" && ev.EnvelopeID != "" {
		a.winMu.Lock()
		a.envToChat[ev.EnvelopeID] = chatID
		a.winMu.Unlock()
	}

	a.winMu.Lock()
	cp := a.chatPages[chatID]
	front, _ := a.pages.GetFrontPage()
	a.winMu.Unlock()

	if cp == nil {
		a.winMu.Lock()
		a.chatBadge[pageName] = true
		a.winMu.Unlock()
		a.app.QueueUpdateDraw(func() {
			a.winBar.SetText(a.winBarText())
		})
		return
	}

	a.app.QueueUpdateDraw(func() {
		cp.upsertEvent(wrapper.Event)
		if front != pageName {
			a.winMu.Lock()
			a.chatBadge[pageName] = true
			a.winMu.Unlock()
			a.winBar.SetText(a.winBarText())
		}
	})
}

func (a *App) sendRequest(t ipc.FrameType, payload any, handler responseHandler) {
	id := a.nextCorrID()
	if handler != nil {
		a.pendingMu.Lock()
		a.pending[id] = handler
		a.pendingMu.Unlock()
	}
	f, err := ipc.NewFrame(t, id, payload)
	if err != nil {
		a.log("[red]send error[white] build %s: %v", t, err)
		a.clearPending(id)
		return
	}
	a.client.Send(f)
}

func (a *App) nextCorrID() string {
	n := a.corrSeq.Add(1)
	return "tui-" + strconv.FormatUint(n, 10)
}

func (a *App) takePending(id string) responseHandler {
	if id == "" {
		return nil
	}
	a.pendingMu.Lock()
	h := a.pending[id]
	if h != nil {
		delete(a.pending, id)
	}
	a.pendingMu.Unlock()
	return h
}

func (a *App) clearPending(id string) {
	a.pendingMu.Lock()
	delete(a.pending, id)
	a.pendingMu.Unlock()
}

func (a *App) log(format string, args ...any) {
	stamp := time.Now().Format("15:04:05")
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(a.statusView, "[gray]%s[white] %s\n", stamp, msg)
	a.statusView.ScrollToEnd()
}

func (a *App) PostStatus(format string, args ...any) {
	a.log(format, args...)
}

const historyCap = 100

func (a *App) historyKey() string {
	front, _ := a.pages.GetFrontPage()
	if strings.HasPrefix(front, "chat:") {
		return front
	}
	return "cmd"
}

var sensitiveHistoryCommands = []string{
	"/set-tor-password",
	"/change-pass",
	"/change-pin",
}

func isSensitiveHistoryInput(text string) bool {
	for _, cmd := range sensitiveHistoryCommands {
		if !strings.HasPrefix(text, cmd) {
			continue
		}
		if len(text) == len(cmd) {
			return true
		}
		next := text[len(cmd)]
		if next == ' ' || next == '\t' {
			return true
		}
	}
	return false
}

func (a *App) historyPush(text string) {
	if text == "" {
		return
	}
	if isSensitiveHistoryInput(text) {

		a.historyIdx[a.historyKey()] = len(a.history[a.historyKey()])
		return
	}
	key := a.historyKey()
	buf := a.history[key]
	if n := len(buf); n > 0 && buf[n-1] == text {
		a.historyIdx[key] = len(buf)
		return
	}
	buf = append(buf, text)
	if len(buf) > historyCap {
		buf = buf[len(buf)-historyCap:]
	}
	a.history[key] = buf
	a.historyIdx[key] = len(buf)
}

func (a *App) historyStep(delta int) {
	key := a.historyKey()
	buf := a.history[key]
	if len(buf) == 0 {
		return
	}
	i, ok := a.historyIdx[key]
	if !ok {
		i = len(buf)
	}
	i += delta
	if i < 0 {
		i = 0
	}
	if i > len(buf) {
		i = len(buf)
	}
	a.historyIdx[key] = i
	if i == len(buf) {
		a.input.SetText("")
	} else {
		a.input.SetText(buf[i])
	}
}

func (a *App) historyResetCursor() {
	key := a.historyKey()
	a.historyIdx[key] = len(a.history[key])
}

func draftKey(pageName string) string {
	if strings.HasPrefix(pageName, "chat:") {
		return strings.TrimPrefix(pageName, "chat:")
	}
	return pageName
}

func (a *App) isMainWindow(name string) bool {
	if name == "" {
		return false
	}
	a.winMu.Lock()
	defer a.winMu.Unlock()
	for _, w := range a.winOrder {
		if w == name {
			return true
		}
	}
	return false
}

func (a *App) mainPaneForFront(front string) tview.Primitive {
	if strings.HasPrefix(front, "chat:") {
		chatID := strings.TrimPrefix(front, "chat:")
		a.winMu.Lock()
		defer a.winMu.Unlock()
		if cp, ok := a.chatPages[chatID]; ok {
			return cp.view
		}
		return nil
	}
	switch front {
	case "status":
		return a.statusView
	case "contacts":
		return a.contactTable
	case "chats":
		return a.chatsTable
	case pageNameSettings:
		a.winMu.Lock()
		defer a.winMu.Unlock()
		if a.settings != nil {
			return a.settings.list
		}
	}
	return nil
}

func (a *App) activeChat() string {
	front, _ := a.pages.GetFrontPage()
	if !strings.HasPrefix(front, "chat:") {
		return ""
	}
	chatID := strings.TrimPrefix(front, "chat:")
	a.winMu.Lock()
	defer a.winMu.Unlock()
	if cp, ok := a.chatPages[chatID]; ok {
		return cp.peerID
	}
	return ""
}

func hexPreview(b []byte, n int) string {
	if len(b) == 0 {
		return "empty"
	}
	end := n
	if end > len(b) {
		end = len(b)
	}
	const hx = "0123456789abcdef"
	out := make([]byte, 0, end*2+3)
	for _, x := range b[:end] {
		out = append(out, hx[x>>4], hx[x&0x0f])
	}
	s := string(out)
	if end < len(b) {
		s += "…"
	}
	return s
}

func coalesce(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
