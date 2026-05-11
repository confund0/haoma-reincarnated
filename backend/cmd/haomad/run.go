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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"haoma/internal/auth"
	"haoma/internal/peers"
	"haoma/internal/eventbus"
	"haoma/internal/files"
	"haoma/internal/ids"
	"haoma/internal/outbox"
	"haoma/internal/pair"
	"haoma/internal/proxy"
	"haoma/internal/secrets"
	"haoma/internal/store"
	"haoma/internal/tor/control"
	"haoma/internal/tor/embedded"
	"haoma/internal/tor/health"
	"haoma/internal/xport"
)

type config struct {
	storeDir      string
	secrets       secrets.Secrets
	apiAddr       string
	tokenFile     string
	certDir       string
	runtimeFile   string
	torControl    string
	torSocks      string
	manageTor     string
	torDataDir    string
	onionVirtPort int
}

type daemon struct {
	cfg      config
	store    *store.Store
	registry *peers.Registry

	xportTarget atomic.Pointer[string]
	inbox       *inbox
	worker      *outbox.Worker
	ids         *ids.IDS
	torPoller   *health.Poller

	ctrlMu   sync.Mutex
	ctrlConn *control.Conn

	verifiedSlotsMu sync.RWMutex
	verifiedSlots   []torSlot

	dhtPairCache *dhtPairCache
	pairDHTMu    sync.Mutex
	pairDHT      *pair.DHT
	torHTTP      *http.Client

	onionDriverOnce sync.Once
	onionDriver     *pair.OnionDriver
	onionInvites    *onionInviteRegistry
	bgCtx           context.Context

	pairLimiter     *rate.Limiter
	pairLimiterOnce sync.Once

	attachedHaoma atomic.Int32

	files *files.Manager

	fetchWorker atomic.Pointer[files.Worker]

	proxy *proxy.Manager

	bus *eventbus.Bus

	torPasswordMu sync.RWMutex
	torPassword   string
	torKick       chan struct{}
}

func (d *daemon) loadTorPassword() string {
	d.torPasswordMu.RLock()
	defer d.torPasswordMu.RUnlock()
	return d.torPassword
}

func (d *daemon) setTorPassword(p string) {
	d.torPasswordMu.Lock()
	d.torPassword = p
	d.torPasswordMu.Unlock()
	if d.torPoller != nil {
		d.torPoller.SetPassword(p)
	}
	select {
	case d.torKick <- struct{}{}:
	default:

	}
}

func (d *daemon) haomaAttached() bool {
	return d.attachedHaoma.Load() > 0
}

func (d *daemon) allowPair() bool {
	d.pairLimiterOnce.Do(func() {
		if d.pairLimiter == nil {
			d.pairLimiter = rate.NewLimiter(rate.Every(12*time.Second), 5)
		}
	})
	return d.pairLimiter.Allow()
}

func (d *daemon) xportPorts() []control.OnionPort {
	t := d.xportTarget.Load()
	if t == nil {
		return nil
	}
	return []control.OnionPort{{VirtPort: d.cfg.onionVirtPort, Target: *t}}
}

func (d *daemon) publishPeerOnion(p peers.Peer) error {
	if p.MyOnionPrivateKey == "" || p.RetiredAt != 0 {
		return nil
	}
	ports := d.xportPorts()
	if len(ports) == 0 {
		return errors.New("xport listener not yet bound")
	}
	d.ctrlMu.Lock()
	conn := d.ctrlConn
	d.ctrlMu.Unlock()
	if conn == nil {
		return errors.New("tor control conn not yet up")
	}
	o, err := conn.AddOnion(p.MyOnionPrivateKey, ports)
	if err != nil {
		return fmt.Errorf("ADD_ONION peer %s: %w", p.ID, err)
	}
	if o.ServiceID != p.MyOnionAddr {
		return fmt.Errorf("ADD_ONION peer %s: tor returned %s, registry has %s", p.ID, o.ServiceID, p.MyOnionAddr)
	}
	return nil
}

func (d *daemon) unpublishPeerOnion(addr string) {
	if addr == "" {
		return
	}
	d.ctrlMu.Lock()
	conn := d.ctrlConn
	d.ctrlMu.Unlock()
	if conn == nil {
		return
	}
	if err := conn.DelOnion(addr); err != nil {
		slog.Warn("DelOnion peer-pair onion failed",
			slog.String("service_id", addr),
			slog.Any("err", err),
		)
	}
}

func (d *daemon) unpublishPeerOnionsFromSnapshot(p *peers.Peer) {
	if p == nil {
		return
	}
	d.unpublishPeerOnion(p.MyOnionAddr)
	if p.PrevMyOnion != nil {
		d.unpublishPeerOnion(p.PrevMyOnion.Address)
	}
}

func (d *daemon) republishAllPeerOnions(conn *control.Conn) error {
	peerList, err := d.registry.List()
	if err != nil {
		return fmt.Errorf("registry list: %w", err)
	}
	ports := d.xportPorts()
	if len(ports) == 0 {
		return errors.New("xport listener not yet bound")
	}
	var lastErr error
	count := 0
	for _, p := range peerList {
		if p.MyOnionPrivateKey == "" || p.RetiredAt != 0 {
			continue
		}
		o, err := conn.AddOnion(p.MyOnionPrivateKey, ports)
		if err != nil {
			slog.Error("AddOnion failed during republish",
				slog.String("peer_id", p.ID),
				slog.String("service_id", p.MyOnionAddr),
				slog.Any("err", err),
			)
			lastErr = err
			continue
		}
		if o.ServiceID != p.MyOnionAddr {
			slog.Error("AddOnion returned mismatched service_id",
				slog.String("peer_id", p.ID),
				slog.String("expected", p.MyOnionAddr),
				slog.String("got", o.ServiceID),
			)
			lastErr = fmt.Errorf("service_id mismatch for peer %s", p.ID)
			continue
		}
		slog.Info("peer onion published",
			slog.String("peer_id", p.ID),
			slog.String("service_id", p.MyOnionAddr),
		)
		count++

		if p.PrevMyOnion != nil && p.PrevMyOnion.ExpiresAt > time.Now().Unix() {
			po, perr := conn.AddOnion(p.PrevMyOnion.PrivateKey, ports)
			if perr != nil {
				slog.Warn("AddOnion (prev) failed during republish",
					slog.String("peer_id", p.ID),
					slog.String("service_id", p.PrevMyOnion.Address),
					slog.Any("err", perr),
				)
			} else if po.ServiceID != p.PrevMyOnion.Address {
				slog.Warn("AddOnion (prev) returned mismatched service_id",
					slog.String("peer_id", p.ID),
					slog.String("expected", p.PrevMyOnion.Address),
					slog.String("got", po.ServiceID),
				)
			} else {
				slog.Info("peer prev-onion republished (grace)",
					slog.String("peer_id", p.ID),
					slog.String("service_id", p.PrevMyOnion.Address),
					slog.Int64("expires_at", p.PrevMyOnion.ExpiresAt),
				)
			}
		}
	}
	slog.Debug("republishAllPeerOnions complete",
		slog.Int("published", count),
		slog.Int("total_peers", len(peerList)),
	)
	return lastErr
}

func run(ctx context.Context, cfg config) error {

	s, err := store.Unlock(cfg.storeDir, cfg.secrets.HaomadStorePassphrase)
	if err != nil {
		return fmt.Errorf("unlock store: %w", err)
	}
	slog.Debug("store unlocked", slog.String("dir", cfg.storeDir))
	defer func() {
		if err := s.Lock(); err != nil {
			slog.Warn("store lock on shutdown failed", slog.Any("err", err))
		}
	}()

	idsEngine := ids.New()
	for _, r := range ids.Defaults() {
		idsEngine.AddRule(r)
	}
	bus := &eventbus.Bus{}
	filesMgr, err := files.NewManager(s, cfg.storeDir)
	if err != nil {
		return fmt.Errorf("files manager: %w", err)
	}
	d := &daemon{
		cfg:          cfg,
		store:        s,
		registry:     peers.NewRegistry(s),
		inbox:        newInbox(s, bus),
		ids:          idsEngine,
		dhtPairCache: newDHTPairCache(s),
		bus:          bus,
		files:        filesMgr,
		proxy:        proxy.NewManager(slog.Default()),
		onionInvites: newOnionInviteRegistry(),
		bgCtx:        ctx,
		torPassword:  cfg.secrets.TorPassword,
		torKick:      make(chan struct{}, 1),
	}

	go bridgeIDSToBus(ctx, idsEngine, bus)
	if n, err := d.dhtPairCache.SweepExpired(time.Now()); err != nil {
		slog.Warn("dht pair cache: startup sweep failed", slog.Any("err", err))
	} else if n > 0 {
		slog.Info("pair cache: dropped expired entries on startup", slog.Int("n", n))
	}

	xportLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen xport target: %w", err)
	}
	defer xportLn.Close()
	xportTarget := xportLn.Addr().String()
	d.xportTarget.Store(&xportTarget)

	var torInst *embedded.Instance
	if cfg.manageTor != "" {
		bootCtx, bootCancel := context.WithTimeout(ctx, 60*time.Second)
		torInst, err = embedded.Bootstrap(bootCtx, embedded.Config{
			BinPath: cfg.manageTor,
			DataDir: cfg.torDataDir,
			Logger:  slog.Default(),
		})
		bootCancel()
		if err != nil {
			return fmt.Errorf("embedded tor bootstrap: %w", err)
		}
		defer func() {
			if err := torInst.Stop(5 * time.Second); err != nil {
				slog.Warn("embedded tor: Stop returned error", slog.Any("err", err))
			}
		}()
		cfg.torControl = torInst.ControlAddr
		cfg.torSocks = torInst.SocksAddr
	}

	d.torPoller = health.New(cfg.torControl, cfg.secrets.TorPassword)
	go d.torPoller.Run(ctx)

	hc, err := xport.NewTorHTTPClient(cfg.torSocks, "haomad-outbound")
	if err != nil {
		return fmt.Errorf("tor SOCKS client: %w", err)
	}
	hc.Timeout = 60 * time.Second
	xportClient := &xport.Client{HTTP: hc}

	pairHTTP, err := xport.NewTorHTTPClient(cfg.torSocks, "haomad-pair")
	if err != nil {
		return fmt.Errorf("tor SOCKS client (pair): %w", err)
	}
	pairHTTP.Timeout = 30 * time.Second
	d.torHTTP = pairHTTP

	if n, err := outbox.Migrate(s, time.Now()); err != nil {
		slog.Warn("outbox migration failed", slog.Any("err", err))
	} else if n > 0 {
		slog.Info("outbox: migrated legacy queue entries", slog.Int("n", n))
	} else {
		slog.Debug("outbox: no legacy entries to migrate")
	}

	ackVerifier := outbox.AckVerifierFunc(func(_ context.Context, body []byte, dest string) error {
		return verifyAckBody(d, body, dest)
	})

	outboxStore := outbox.NewStore(s)
	outboxBus := &outbox.Bus{}
	d.worker = outbox.NewWorker(outboxStore, xportClient, ackVerifier, outboxBus)
	d.worker.Gate = d.torPoller.Ready

	go bridgeOutboxToBus(ctx, outboxBus, bus)

	slog.Debug("outbox worker starting")
	workerCtx, workerCancel := context.WithCancel(ctx)
	defer workerCancel()
	go func() {
		if err := d.worker.Run(workerCtx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("outbox worker exited", slog.Any("err", err))
		}
	}()

	torReady := make(chan struct{})
	torCleanup := make(chan func(), 1)
	torTierDone := make(chan struct{})
	go func() {
		defer close(torTierDone)
		bringUpTorTier(ctx, d, cfg, xportLn, xportClient, idsEngine, torReady, torCleanup)
	}()

	defer func() {
		select {
		case <-torTierDone:
		case <-time.After(5 * time.Second):
			slog.Warn("tor-tier goroutine did not exit within 5s; onion cleanup may be skipped")
		}
		select {
		case cleanup := <-torCleanup:
			cleanup()
		default:
		}
	}()

	apiLn, err := net.Listen("tcp", cfg.apiAddr)
	if err != nil {
		return fmt.Errorf("listen api: %w", err)
	}

	var token, tokenSource string
	if cfg.secrets.HaomadToken != "" {
		token = cfg.secrets.HaomadToken
		tokenSource = "secrets-stdin"
	} else {
		token, err = auth.LoadOrCreateToken(cfg.tokenFile)
		if err != nil {
			return fmt.Errorf("load token: %w", err)
		}
		tokenSource = cfg.tokenFile
	}

	tlsCfg, err := auth.LoadOrCreateTLS(cfg.certDir)
	if err != nil {
		return fmt.Errorf("load TLS: %w", err)
	}
	apiSrv := &http.Server{
		Handler:   auth.Middleware(token, []string{"/health"}, d.apiHandler()),
		TLSConfig: tlsCfg,

		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	slog.Info("local API listening (TLS)",
		slog.String("version", version),
		slog.String("addr", apiLn.Addr().String()),
		slog.String("token_source", tokenSource),
		slog.String("cert", auth.CertPath(cfg.certDir)),
	)

	if cfg.runtimeFile != "" {
		info := RuntimeInfo{
			PID:       os.Getpid(),
			APIAddr:   apiLn.Addr().String(),
			StartedAt: time.Now().UTC(),
		}
		if err := writeRuntimeFile(cfg.runtimeFile, info); err != nil {
			return fmt.Errorf("write runtime file %q: %w", cfg.runtimeFile, err)
		}
		defer removeRuntimeFile(cfg.runtimeFile)
		slog.Debug("runtime file written",
			slog.String("path", cfg.runtimeFile),
			slog.Int("pid", info.PID),
			slog.String("api_addr", info.APIAddr),
		)
	}

	if err := emitReadyLine(apiLn.Addr().String()); err != nil {

		return fmt.Errorf("ready line: %w", err)
	}

	apiErr := make(chan error, 1)
	go func() {
		apiErr <- apiSrv.Serve(tls.NewListener(apiLn, tlsCfg))
	}()
	_ = torReady

	select {
	case <-ctx.Done():
		slog.Info("shutdown requested")
	case err := <-apiErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			d.closePairDHT()
			return fmt.Errorf("api server: %w", err)
		}
	}
	d.closePairDHT()
	return shutdownHTTPServer(apiSrv)
}

func shutdownHTTPServer(srv *http.Server) error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
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

func bringUpTorTier(
	ctx context.Context,
	d *daemon,
	cfg config,
	xportLn net.Listener,
	xportClient *xport.Client,
	idsEngine *ids.IDS,
	readyClose chan<- struct{},
	cleanupOut chan<- func(),
) {
	for {
		if ctx.Err() != nil {
			return
		}
		cleanup, err := tryBringUpTorTier(ctx, d, cfg, xportLn, xportClient, idsEngine)
		if err == nil {

			select {
			case cleanupOut <- cleanup:
			default:
			}
			close(readyClose)
			return
		}
		if ctx.Err() != nil {
			return
		}
		slog.Warn("tor-tier bring-up failed; retrying in 10s", slog.Any("err", err))
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

func tryBringUpTorTier(
	ctx context.Context,
	d *daemon,
	cfg config,
	xportLn net.Listener,
	xportClient *xport.Client,
	idsEngine *ids.IDS,
) (cleanup func(), err error) {

	dialCtx, dialCancel := context.WithTimeout(ctx, 10*time.Second)
	ctrlConn, err := control.Dial(dialCtx, cfg.torControl)
	dialCancel()
	if err != nil {
		return nil, fmt.Errorf("dial tor control: %w", err)
	}
	method, err := ctrlConn.Authenticate(d.loadTorPassword())
	if err != nil {
		ctrlConn.Close()
		return nil, fmt.Errorf("tor auth: %w", err)
	}
	slog.Debug("tor control authenticated", slog.String("addr", cfg.torControl), slog.String("method", string(method)))
	d.ctrlMu.Lock()
	d.ctrlConn = ctrlConn
	d.ctrlMu.Unlock()

	if err := d.republishAllPeerOnions(ctrlConn); err != nil {
		ctrlConn.Close()
		return nil, fmt.Errorf("republish per-peer onions: %w", err)
	}

	onVerified := func(_ context.Context, peerID string) {

		peer, err := d.registry.Get(peerID)
		if err != nil {
			return
		}
		dests := make([]string, 0, len(peer.KnownAddresses))
		for _, addr := range peer.KnownAddresses {
			dests = append(dests, "http://"+addr+".onion")
		}
		go func() {
			if _, err := d.worker.KickByDests(dests); err != nil {
				slog.Warn("KickByDests failed",
					slog.String("peer_id", peerID),
					slog.Any("err", err),
				)
			}
		}()
	}
	verifier := &peers.HMACVerifier{Registry: d.registry, IDS: idsEngine, OnVerifySuccess: onVerified}
	responder := xport.StatusResponderFunc(func(rctx context.Context, req xport.Envelope) (xport.Envelope, error) {
		return buildStatusResponse(d, rctx, req)
	})
	sentAcker := xport.SentAckResponderFunc(func(rctx context.Context, req xport.Envelope) (xport.Envelope, error) {
		return buildSentAck(d, rctx, req)
	})
	receiver := xport.ReceiverFunc(func(_ context.Context, e xport.Envelope) error {
		peerID := ""
		if p, err := d.registry.ByAddress(e.From); err == nil {
			peerID = p.ID

			if terr := d.registry.TouchPresence(peerID, time.Now(), e.PresenceSource); terr != nil {
				slog.Warn("TouchPresence failed",
					slog.String("peer_id", peerID),
					slog.Any("err", terr),
				)
			}

			if e.PresenceSource != "" {
				publishPresenceChanged(d, peerID, e.PresenceSource)
			}
		}
		slog.Debug("envelope received and queued to inbox",
			slog.String("envelope_id", e.ID),
			slog.String("peer_id", peerID),
			slog.String("from", e.From),
			slog.String("kind", e.EffectiveKind()),
			slog.String("presence_source", e.PresenceSource),
			slog.Int("payload_bytes", len(e.Payload)),
		)
		return d.inbox.Put(inboxEntry{
			ArrivalAt: time.Now().UnixNano(),
			PeerID:    peerID,
			Envelope:  e,
		})
	})

	var xportSrv *http.Server
	{

		mux := http.NewServeMux()
		xportHandler := xport.NewServer(ids.SlotUnknown, receiver, verifier, responder, sentAcker)
		mux.HandleFunc("GET /pair", d.handlePairFetch)
		mux.HandleFunc("POST /pair/return", d.handlePairReturn)

		mux.HandleFunc("GET /files/{token}", d.handleFileFetch)

		mux.HandleFunc("GET /audio/{token}", d.handleProxyStreamGet)
		mux.HandleFunc("GET /video/{token}", d.handleProxyStreamGet)
		mux.HandleFunc("GET /screen/{token}", d.handleProxyStreamGet)
		mux.Handle("/", xportHandler)
		xportSrv = &http.Server{Handler: mux}
		go func() {
			if err := xportSrv.Serve(xportLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("xport server exited", slog.Any("err", err))
			}
		}()
	}

	go startOnionWatchdog(ctx, d)

	go startRetiredAddrsSweeper(ctx, d)

	resolver := files.PeerResolverFunc(func(peerID string) ([]string, error) {
		p, err := d.registry.Get(peerID)
		if err != nil {
			return nil, err
		}
		return p.KnownAddresses, nil
	})
	clientForPeer := files.HTTPClientForPeer(func(peerID string) (*http.Client, error) {
		hc, err := xport.NewTorHTTPClient(cfg.torSocks, "haomad-file-fetch:"+peerID)
		if err != nil {
			return nil, err
		}

		hc.Timeout = 0
		return hc, nil
	})
	sink := files.FetchEventSinkFunc(func(ev files.FetchEvent) {

		topic := eventbus.TopicFileFetchProgress
		if ev.State == files.FetchStateReady ||
			ev.State == files.FetchStateFailedTransient ||
			ev.State == files.FetchStateFailedPermanent ||
			ev.State == files.FetchStatePending {
			topic = eventbus.TopicFileFetchStateChanged
		}
		d.bus.Publish(topic, ev)
	})
	worker := files.NewWorker(d.files, resolver, clientForPeer, sink, slog.Default())
	d.fetchWorker.Store(worker)
	go worker.Run(ctx)

	cleanup = func() {
		d.ctrlMu.Lock()
		conn := d.ctrlConn
		d.ctrlMu.Unlock()
		if conn != nil {
			peerList, err := d.registry.List()
			if err != nil {
				slog.Warn("registry list during shutdown failed", slog.Any("err", err))
			} else {
				for _, p := range peerList {
					if p.MyOnionAddr == "" || p.RetiredAt != 0 {
						continue
					}
					if derr := conn.DelOnion(p.MyOnionAddr); derr != nil {
						slog.Warn("DelOnion on shutdown failed",
							slog.String("peer_id", p.ID),
							slog.String("service_id", p.MyOnionAddr),
							slog.Any("err", derr),
						)
					}
				}
			}
			conn.Close()
		}
		if xportSrv != nil {
			_ = shutdownHTTPServer(xportSrv)
		}
	}
	return cleanup, nil
}

func startOnionWatchdog(ctx context.Context, d *daemon) {
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.torKick:

			watchdogReconnect(ctx, d, "tor-password kick")
		case <-tick.C:
			if !d.torPoller.Ready() {
				d.clearVerifiedSlots()
				continue
			}

			d.ctrlMu.Lock()
			conn := d.ctrlConn
			d.ctrlMu.Unlock()
			if conn == nil {
				continue
			}
			_, pingErr := conn.GetInfo("version")
			if pingErr != nil {
				slog.Warn("tor control connection lost; reconnecting", slog.Any("err", pingErr))
				if !watchdogReconnect(ctx, d, "ping failed") {
					continue
				}
				d.ctrlMu.Lock()
				conn = d.ctrlConn
				d.ctrlMu.Unlock()
				if conn == nil {
					continue
				}
			}

			raw, err := conn.GetInfo("onions/current")
			if err != nil {
				d.clearVerifiedSlots()
			} else {
				d.updateVerifiedSlots(raw)
			}
		}
	}
}

func startRetiredAddrsSweeper(ctx context.Context, d *daemon) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			now := time.Now().Unix()
			if n, err := d.registry.SweepRetiredAddrs(now); err != nil {
				slog.Warn("retired-addrs sweep failed", slog.Any("err", err))
			} else if n > 0 {
				slog.Info("retired-addrs swept", slog.Int("count", n))
			}
			toDelete, err := d.registry.SweepRetiredOwnOnions(now)
			if err != nil {
				slog.Warn("retired-own-onions sweep failed", slog.Any("err", err))
				continue
			}
			for _, addr := range toDelete {
				slog.Info("retired-own-onion DEL_ONION", slog.String("addr", addr))
				d.unpublishPeerOnion(addr)
			}
		}
	}
}

func watchdogReconnect(ctx context.Context, d *daemon, reason string) bool {
	newConn, err := reconnectControl(ctx, d, d.cfg)
	if err != nil {
		slog.Warn("tor control reconnect failed",
			slog.String("reason", reason),
			slog.Any("err", err),
		)
		d.clearVerifiedSlots()
		return false
	}
	d.ctrlMu.Lock()
	old := d.ctrlConn
	d.ctrlConn = newConn
	d.ctrlMu.Unlock()
	if old != nil {
		old.Close()
	}
	if err := d.republishAllPeerOnions(newConn); err != nil {
		slog.Error("per-peer onion republish failed after reconnect",
			slog.String("reason", reason),
			slog.Any("err", err),
		)
		d.clearVerifiedSlots()

	} else {
		slog.Info("per-peer onions republished after tor reconnect", slog.String("reason", reason))
	}
	return true
}

func (d *daemon) updateVerifiedSlots(raw string) {
	published := make(map[string]bool)
	for _, line := range strings.Split(raw, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			published[s] = true
		}
	}
	var slots []torSlot
	peerList, err := d.registry.List()
	if err != nil {
		slog.Warn("verifiedSlots registry list failed", slog.Any("err", err))
		d.verifiedSlotsMu.Lock()
		d.verifiedSlots = nil
		d.verifiedSlotsMu.Unlock()
		return
	}
	now := time.Now().Unix()
	idx := 0
	for _, p := range peerList {
		if p.RetiredAt != 0 {
			continue
		}
		if p.MyOnionAddr != "" && published[p.MyOnionAddr] {
			slots = append(slots, torSlot{
				Slot:      idx,
				ServiceID: p.MyOnionAddr,
				URL:       "http://" + p.MyOnionAddr + ".onion",
			})
			idx++
		}

		if p.PrevMyOnion != nil && p.PrevMyOnion.ExpiresAt > now && published[p.PrevMyOnion.Address] {
			slots = append(slots, torSlot{
				Slot:      idx,
				ServiceID: p.PrevMyOnion.Address,
				URL:       "http://" + p.PrevMyOnion.Address + ".onion",
			})
			idx++
		}
	}
	d.verifiedSlotsMu.Lock()
	d.verifiedSlots = slots
	d.verifiedSlotsMu.Unlock()
}

func (d *daemon) clearVerifiedSlots() {
	d.verifiedSlotsMu.Lock()
	d.verifiedSlots = nil
	d.verifiedSlotsMu.Unlock()
}

func reconnectControl(ctx context.Context, d *daemon, cfg config) (*control.Conn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := control.Dial(dialCtx, cfg.torControl)
	if err != nil {
		return nil, fmt.Errorf("dial tor control: %w", err)
	}
	method, err := conn.Authenticate(d.loadTorPassword())
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("tor auth: %w", err)
	}
	slog.Debug("tor control re-authenticated", slog.String("method", string(method)))
	return conn, nil
}

func peerForVerified(d *daemon, ctx context.Context, req xport.Envelope) (*peers.Peer, error) {
	if peerID := xport.PeerIDFromContext(ctx); peerID != "" {
		peer, err := d.registry.Get(peerID)
		if err != nil {
			return nil, fmt.Errorf("stamped peer %q not in registry: %w", peerID, err)
		}
		return peer, nil
	}
	peerList, err := d.registry.List()
	if err != nil {
		return nil, err
	}
	for i := range peerList {
		if xport.Verify(req, peerList[i].InboundSecret) == nil {
			return &peerList[i], nil
		}
	}
	return nil, errors.New("no matching peer secret")
}

func buildStatusResponse(d *daemon, ctx context.Context, req xport.Envelope) (xport.Envelope, error) {
	peer, err := peerForVerified(d, ctx, req)
	if err != nil {
		return xport.Envelope{}, err
	}
	if peer.MyOnionAddr == "" {
		return xport.Envelope{}, fmt.Errorf("status responder: peer %q has no MyOnionAddr", peer.ID)
	}

	envID, err := newEnvelopeID()
	if err != nil {
		return xport.Envelope{}, err
	}
	resp := xport.Envelope{
		ID:             envID,
		Timestamp:      time.Now().Unix(),
		From:           peer.MyOnionAddr,
		Kind:           xport.KindStatus,
		PresenceSource: presenceSourceFor(d),

		Payload: nil,
	}
	if err := xport.RandomPadding(&resp); err != nil {
		return xport.Envelope{}, err
	}
	return xport.Sign(resp, peer.OutboundSecret), nil
}

func presenceSourceFor(d *daemon) string {
	if d.haomaAttached() {
		return xport.PresenceSourceHaoma
	}
	return xport.PresenceSourceHaomad
}

func buildSentAck(d *daemon, ctx context.Context, req xport.Envelope) (xport.Envelope, error) {
	peer, err := peerForVerified(d, ctx, req)
	if err != nil {
		return xport.Envelope{}, err
	}
	if peer.MyOnionAddr == "" {
		return xport.Envelope{}, fmt.Errorf("sent_ack: peer %q has no MyOnionAddr", peer.ID)
	}
	envID, err := newEnvelopeID()
	if err != nil {
		return xport.Envelope{}, err
	}
	ack := xport.Envelope{
		ID:             envID,
		Timestamp:      time.Now().Unix(),
		From:           peer.MyOnionAddr,
		Kind:           xport.KindSentAck,
		PresenceSource: presenceSourceFor(d),

		Payload: []byte(`{"acked_id":"` + req.ID + `"}`),
	}
	if err := xport.RandomPadding(&ack); err != nil {
		return xport.Envelope{}, err
	}
	return xport.Sign(ack, peer.OutboundSecret), nil
}

func verifyAckBody(d *daemon, body []byte, dest string) error {
	var env xport.Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("sent_ack parse: %w", err)
	}
	switch env.EffectiveKind() {
	case xport.KindSentAck:

	case xport.KindStatus:

	default:
		return fmt.Errorf("sent_ack: unexpected kind %q", env.EffectiveKind())
	}

	addr := destToOnion(dest)
	peer, err := d.registry.ByAddress(addr)
	if err != nil {
		return fmt.Errorf("sent_ack: peer not found for dest %q: %w", dest, err)
	}
	if err := xport.Verify(env, peer.InboundSecret); err != nil {
		return err
	}
	slog.Debug("sent_ack verified",
		slog.String("peer_id", peer.ID),
		slog.String("envelope_id", env.ID),
		slog.String("acked_kind", env.EffectiveKind()),
		slog.String("presence_source", env.PresenceSource),
	)
	if env.PresenceSource != "" && d.bus != nil {
		publishPresenceChanged(d, peer.ID, env.PresenceSource)
	}
	return nil
}

func publishPresenceChanged(d *daemon, peerID, source string) {
	if d == nil || d.bus == nil {
		return
	}
	d.bus.Publish(eventbus.TopicPeerPresenceChanged, peerPresenceObservation{
		PeerID: peerID,
		Source: source,
		At:     time.Now().Unix(),
	})
	var active, passive int64
	if d.registry != nil {
		if p, err := d.registry.Get(peerID); err == nil {
			active = p.LastActiveAt
			passive = p.LastPassiveAt
		}
	}
	d.bus.Publish(eventbus.TopicPeerLastSeenChanged, peerLastSeenObservation{
		PeerID:        peerID,
		LastActiveAt:  active,
		LastPassiveAt: passive,
	})
}

func bridgeIDSToBus(ctx context.Context, engine *ids.IDS, bus *eventbus.Bus) {
	sub, cancel := engine.Subscribe(64)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case n, ok := <-sub:
			if !ok {
				return
			}
			bus.Publish(eventbus.TopicSystemIDSEvent, n)
		}
	}
}

func bridgeOutboxToBus(ctx context.Context, src *outbox.Bus, bus *eventbus.Bus) {
	sub, cancel := src.Subscribe(64)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case ds, ok := <-sub:
			if !ok {
				return
			}
			bus.Publish(eventbus.TopicDeliveryStateChanged, ds)
		}
	}
}

func destToOnion(dest string) string {

	s := dest
	if len(s) > 7 && s[:7] == "http://" {
		s = s[7:]
	}
	if len(s) > 6 && s[len(s)-6:] == ".onion" {
		s = s[:len(s)-6]
	}
	return s
}
