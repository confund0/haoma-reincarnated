package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"path/filepath"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/calls"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/devicetag"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/files"
	"haoma-frontend/internal/gapfill"
	"haoma-frontend/internal/invitation"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/linkeddevice"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/notify"
	"haoma-frontend/internal/paths"
	"haoma-frontend/internal/peerstate"
	"haoma-frontend/internal/presence"
	"haoma-frontend/internal/rotation"
	"haoma-frontend/internal/session"
	"haoma-frontend/internal/signal"
	"haoma-frontend/internal/store"
	"haoma-frontend/internal/streamers"
)

type daemon struct {
	dataDir       string
	store         *store.Store
	signalState   *signal.State
	stores        *signal.Stores
	cipher        *session.Cipher
	peerSeq       *peerstate.Counters
	peerMeta      *peerstate.Meta
	chats         *chat.Store
	linkedDevices *linkeddevice.Store
	emptyMarker   *emptyMarker
	watermarks    *gapfill.Watermarks
	invitations   *invitation.Store

	gapfill       gapfillGate
	manualSync    manualSyncTracker
	syncTrigger   *syncTrigger
	contactsPush  *contactsPusher
	fileServer    *fileServer
	fileAsker     fileAsker
	events        *events.Log
	eventBus      *events.Bus
	files         *files.Manager
	calls         *calls.Manager
	streamers     *streamers.Manager
	backendClient *backendapi.Client
	ipcSrv        *ipc.Server

	statsLog *slog.Logger

	startedAt time.Time

	backendReachable atomic.Bool

	lastClockSkew atomic.Pointer[ipc.ClockSkew]

	latestStatus atomic.Pointer[ipc.BackendStatusPayload]

	clientFocus atomic.Pointer[ipc.ClientFocusRequest]

	presenceOverride atomic.Pointer[string]

	selfNickCache atomic.Pointer[string]

	selfNickWrite sync.Mutex

	chatFontScaleCache atomic.Pointer[float64]

	defaultRetentionCache    atomic.Pointer[uint64]
	defaultSendReceiptsCache atomic.Pointer[bool]

	presenceCache *presence.Cache

	chatPresenceMu     sync.Mutex
	chatPresencePushed map[chat.ChatID]presence.Snapshot

	settingsSnapshot atomic.Pointer[ipc.Settings]

	clientSoftLocked atomic.Bool

	fileRetrySweepOnce sync.Once

	siblingSealBusy sync.Map

	fileSealLocks keyedLocks

	seededFaults

	unplacedSweep unplacedMirrorSweep

	gapOfferTTL time.Duration

	deliveryReconcileRunning atomic.Bool

	relayCtx atomic.Value

	invitationOfferRunning atomic.Bool

	notePut func(invitation.Note) error

	invitationWaiters sync.Map

	sessionsChanged sessionSignal
	passOnsRunning  sync.Map

	sendQueue sendQueue

	passOnRetryPauses []time.Duration

	invitationWaitRetry    time.Duration
	invitationWaitRetryMax time.Duration

	backendReadyAt        atomic.Int64
	invitationRetryWindow time.Duration

	notifier *notify.Dispatcher

	rotation *rotation.Manager
}

func (d *daemon) relayContext() context.Context {
	if ctx, ok := d.relayCtx.Load().(context.Context); ok {
		return ctx
	}
	return context.Background()
}

func (d *daemon) effectivePresenceState() string {
	if p := d.presenceOverride.Load(); p != nil && *p != "" {
		return *p
	}
	return msg.PresenceAvailable
}

func run(ctx context.Context, cfg config) error {

	var (
		dataDir string
		root    string
		err     error
	)
	switch {
	case cfg.dataDir != "":
		dataDir, err = paths.BootstrapAt(cfg.dataDir)
	case cfg.cfgDir != "":
		root, err = paths.RootFromFlag(cfg.cfgDir)
		if err == nil {
			dataDir, err = paths.BootstrapAt(paths.FrontendDir(root))
		}
	default:
		root, err = paths.Root()
		if err == nil {
			dataDir, err = paths.Bootstrap(paths.FrontendSubdir)
		}
	}
	if err != nil {
		return fmt.Errorf("bootstrap data dir: %w", err)
	}
	slog.Info("data dir", slog.String("path", dataDir))

	emptyMark, err := newEmptyMarker(root, dataDir)
	if err != nil {
		return err
	}

	st, err := store.Unlock(dataDir, cfg.secrets.FrontendStorePassphrase)
	if err != nil {
		return fmt.Errorf("unlock store: %w", err)
	}
	defer func() {
		if err := st.Lock(); err != nil {
			slog.Warn("store lock on shutdown failed", slog.Any("err", err))
		}
	}()

	sigState, created, err := signal.LoadOrBootstrap(st, signal.DefaultOPKCount)
	if err != nil {
		return fmt.Errorf("signal: %w", err)
	}
	sum := sigState.Summary()
	msg := "signal: identity loaded"
	if created {
		msg = "signal: identity bootstrapped"
	}
	slog.Info(msg,
		slog.String("fingerprint", sum.IdentityFingerprint),
		slog.String("registration_id", devicetag.Tag(sum.RegistrationID)),
		slog.Int("opk_count", sum.OneTimePreKeyCount),
	)
	stores := signal.NewStores(st, sigState)

	bus := events.NewBus()
	filesMgr, err := files.NewManager(st, dataDir)
	if err != nil {
		return fmt.Errorf("files manager: %w", err)
	}
	callsMgr, err := calls.NewManager(st)
	if err != nil {
		return fmt.Errorf("calls manager: %w", err)
	}

	if n, err := callsMgr.SweepNonTerminal(calls.FailReasonDaemonRestart, 0); err != nil {
		slog.Warn("calls cold-start sweep failed", slog.Any("err", err))
	} else if n > 0 {
		slog.Info("calls cold-start sweep marked zombie rows failed", slog.Int("count", n))
	}

	var streamersMgr *streamers.Manager
	micPath, spkPath, camPath, vidPath, derr := streamers.Discover(cfg.streamerDir)
	if derr != nil {
		slog.Warn("streamer binary discovery failed; calls will fail at /call",
			slog.Any("err", derr),
			slog.String("flag_dir", cfg.streamerDir),
		)
	} else {
		streamersMgr, err = streamers.New(streamers.Config{
			Logger:  slog.Default(),
			MicPath: micPath,
			SpkPath: spkPath,
			CamPath: camPath,
			VidPath: vidPath,
			Trace:   cfg.streamerTrace,
		})
		if err != nil {
			slog.Warn("streamers manager init failed", slog.Any("err", err))
		} else {
			slog.Info("streamers ready",
				slog.String("mic", streamersMgr.MicPath()),
				slog.String("spk", streamersMgr.SpkPath()),
				slog.String("cam", streamersMgr.CamPath()),
				slog.String("vid", streamersMgr.VidPath()),
				slog.Bool("trace", cfg.streamerTrace),
			)
		}
	}
	d := &daemon{
		dataDir:       dataDir,
		store:         st,
		signalState:   sigState,
		stores:        stores,
		cipher:        session.New(stores),
		peerSeq:       peerstate.New(st),
		peerMeta:      peerstate.NewMeta(st),
		chats:         chat.NewStore(st),
		linkedDevices: linkeddevice.New(st),
		emptyMarker:   emptyMark,
		watermarks:    gapfill.NewWatermarks(st),
		invitations:   invitation.New(st),
		events:        events.New(st, bus, nil),
		eventBus:      bus,
		files:         filesMgr,
		calls:         callsMgr,
		streamers:     streamersMgr,
		presenceCache: presence.New(),
		notifier:      notify.New(nil, nil, ""),
		startedAt:     time.Now().UTC(),
		statsLog:      cfg.statsLog,

		seededFaults: seededFaultsFromEnv(),
		gapOfferTTL:  gapOfferLifetimeFromEnv(),
	}

	d.latestStatus.Store(&ipc.BackendStatusPayload{
		BackendReachable: false,
		Tor:              ipc.TorHealth{Unreachable: true},
	})

	d.settingsSnapshot.Store(defaultSettings())

	d.syncTrigger = newSyncTrigger(func(sweepCtx context.Context) sweepOutcome {
		outcome := sweepGapFill(sweepCtx, d)
		pruneJournalForSiblings(d)
		return outcome
	})
	d.events.SetJournalGrowthHook(d.syncTrigger.note)

	defer d.wireEmptyMarker()()

	if err := d.loadSelfNickInto(); err != nil {
		return fmt.Errorf("load self-nick: %w", err)
	}
	if err := d.loadChatFontScaleInto(); err != nil {
		return fmt.Errorf("load chat-font-scale: %w", err)
	}
	if err := d.loadChatDefaultsInto(); err != nil {
		return fmt.Errorf("load chat-defaults: %w", err)
	}

	dropLinkedDeviceChats(d)

	tlsCfg, err := ipc.LoadOrCreateTLS(dataDir)
	if err != nil {
		return fmt.Errorf("load TLS: %w", err)
	}

	token, err := ipc.LoadOrCreateToken(dataDir)
	if err != nil {
		return fmt.Errorf("load token: %w", err)
	}

	ipc.DaemonVersion = version
	srv := ipc.NewServer(token)
	d.ipcSrv = srv
	srv.OnSession = newSessionDispatcher(d).run
	srv.WelcomeAugment = func(wp ipc.WelcomePayload) ipc.WelcomePayload {
		wp.SelfNick = d.selfNick()
		wp.SelfNickIsDefault = d.selfNickIsDefault()
		wp.ChatFontScale = d.chatFontScale()
		wp.DefaultRetentionSec = d.defaultRetentionSec()
		wp.DefaultSendReceipts = d.defaultSendReceipts()
		return wp
	}

	d.rotation = newRotationManager(d)

	httpSrv := &http.Server{
		Handler:   srv.Handler(),
		TLSConfig: tlsCfg,

		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ln, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer ln.Close()
	slog.Info("IPC listening (TLS)",
		slog.String("addr", ln.Addr().String()),
		slog.String("cert", ipc.CertPath(dataDir)),
		slog.String("token", ipc.TokenPath(dataDir)),
	)

	if err := emitReadyLine(ln.Addr().String()); err != nil {
		return fmt.Errorf("ready line: %w", err)
	}

	relayCtx, relayCancel := context.WithCancel(ctx)
	defer relayCancel()
	d.relayCtx.Store(relayCtx)

	serveErr := make(chan error, 1)
	go func() {
		tlsLn := tls.NewListener(ln, tlsCfg)
		serveErr <- httpSrv.Serve(tlsLn)
	}()

	var startupWg sync.WaitGroup
	launch := func(fn func()) {
		startupWg.Add(1)
		go func() {
			defer startupWg.Done()
			fn()
		}()
	}

	launch(func() { pushTimelineEvents(relayCtx, d.eventBus, srv) })
	launch(func() { pushTimelineDeletions(relayCtx, d.eventBus, srv) })
	launch(func() { pushPresenceChanges(relayCtx, d) })

	launch(func() { d.syncTrigger.run(relayCtx) })

	d.contactsPush = newContactsPusher(func(fingerprints []string) {
		sendContactsPush(relayCtx, d, fingerprints)
	})

	d.fileServer = newDaemonFileServer(d)
	launch(func() { d.fileServer.run(relayCtx) })

	launch(func() { retentionSweeper(relayCtx, d) })

	launch(func() {
		files.NewJanitor(d.files, d.eventBus, slog.Default(), &daemonRemoteDropper{d: d}).Run(relayCtx)
	})

	if cfg.backendAddr != "" {

		backendRoot := root
		if backendRoot == "" {
			backendRoot, err = paths.Root()
			if err != nil {
				return fmt.Errorf("resolve haoma root: %w", err)
			}
		}
		backendTierDir := paths.BackendDir(backendRoot)

		var haomadToken, haomadTokenSource string
		if cfg.secrets.HaomadToken != "" {
			haomadToken = cfg.secrets.HaomadToken
			haomadTokenSource = "secrets-stdin"
		} else {
			haomadTokenPath := cfg.haomadTokenFile
			if haomadTokenPath == "" {
				haomadTokenPath = filepath.Join(backendTierDir, "haomad-token")
			}
			tok, terr := ipc.ReadSensitive(haomadTokenPath)
			if terr != nil {
				return fmt.Errorf("read haomad token at %s: %w", haomadTokenPath, terr)
			}
			haomadToken = tok
			haomadTokenSource = haomadTokenPath
		}

		certPath := cfg.haomadCertFile
		if certPath == "" {
			certPath = filepath.Join(backendTierDir, "cert.pem")
		}
		backendTLS, err := backendapi.PinnedTLSConfig(certPath)
		if err != nil {
			return fmt.Errorf("backend TLS pin %s: %w", certPath, err)
		}
		backendHTTP := &http.Client{
			Timeout:   2 * time.Minute,
			Transport: &http.Transport{TLSClientConfig: backendTLS},
		}
		d.backendClient = backendapi.New(cfg.backendAddr, haomadToken, backendHTTP)
		slog.Info("backend relay enabled",
			slog.String("backend_addr", cfg.backendAddr),
			slog.String("haomad_token_source", haomadTokenSource),
			slog.String("haomad_cert", certPath),
		)
		launch(func() {
			if err := relayBackend(relayCtx, d); err != nil {
				slog.Warn("backend relay exited with error", slog.Any("err", err))
			}
		})

		d.startEmptyCheck(relayCtx, launch)
	} else {
		slog.Warn("backend relay disabled — pairing and messaging unavailable until --backend-addr is set")
	}

	select {
	case <-ctx.Done():
		slog.Info("shutdown requested")
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("IPC server: %w", err)
		}
	}

	relayCancel()
	startupDone := make(chan struct{})
	go func() {
		startupWg.Wait()
		close(startupDone)
	}()
	select {
	case <-startupDone:
	case <-time.After(5 * time.Second):
		slog.Warn("startup goroutines did not exit within 5s; in-flight work may be cut off")
	}

	if d.files != nil {
		if err := d.files.WipeOpenTransient(); err != nil {
			slog.Warn("wipe transient open dir on shutdown failed", slog.Any("err", err))
		}
	}

	if d.streamers != nil {
		d.streamers.Shutdown()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdownCtx)
}

func emitReadyLine(apiAddr string) error {
	line := struct {
		Status  string `json:"status"`
		APIAddr string `json:"api_addr"`
	}{
		Status:  "ready",
		APIAddr: apiAddr,
	}
	b, err := json.Marshal(line)
	if err != nil {
		return err
	}
	if _, err := os.Stdout.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}
