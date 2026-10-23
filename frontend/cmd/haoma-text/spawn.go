package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/ipcclient"
	"haoma-frontend/internal/paths"
	"haoma-frontend/internal/supervisor"
	"haoma-frontend/internal/tui"
	"haoma-frontend/internal/vault"
)

const (
	haomadLogName = "haomad.log"
	haomaLogName  = "haoma.log"

	haomadStderrName = "haomad.stderr"
	haomaStderrName  = "haoma.stderr"

	readyTimeout = 30 * time.Second
	stopTimeout  = 10 * time.Second
	healthDial   = 3 * time.Second
)

func daemonLogPaths(root, logName, stderrName string) (logPath, stderrPath string) {
	return filepath.Join(root, logName), filepath.Join(root, stderrName)
}

type spawnOpts struct {
	haomadBin     string
	haomaBin      string
	haomaVaultBin string
	logLevel      string
	logFile       string
	logFormat     string
	idleOverride  int
	restoreBackup string
	tor           torSetup
	callStats     bool
}

func spawnAndRun(root string, vc *vaultController, opts spawnOpts) error {
	payload := vc.snapshot()
	backendDir := paths.BackendDir(root)
	frontendDir := paths.FrontendDir(root)
	runtimePath := filepath.Join(root, runtimeFileName)
	haomadLog, haomadStderr := daemonLogPaths(root, haomadLogName, haomadStderrName)
	haomaLog, haomaStderr := daemonLogPaths(root, haomaLogName, haomaStderrName)

	if _, err := paths.BootstrapAt(backendDir); err != nil {
		return fmt.Errorf("bootstrap %s: %w", backendDir, err)
	}
	if _, err := paths.BootstrapAt(frontendDir); err != nil {
		return fmt.Errorf("bootstrap %s: %w", frontendDir, err)
	}
	textDataDir, err := paths.BootstrapAt(paths.TextDir(root))
	if err != nil {
		return fmt.Errorf("bootstrap text dir: %w", err)
	}

	secretsBlob, err := secretsForSpawn(payload.Secrets, opts.tor).Marshal()
	if err != nil {
		return fmt.Errorf("marshal secrets: %w", err)
	}

	defer wipe(secretsBlob)

	bearer := payload.HaomadToken

	haomadCertPath := filepath.Join(backendDir, "cert.pem")

	haomadArgs, err := buildHaomadArgs(root, runtimePath, opts.logLevel, haomadLog, opts.tor, opts.callStats)
	if err != nil {
		return fmt.Errorf("haomad: %w", err)
	}

	torrc := filepath.Join(root, torDirName, "torrc")
	sweep := endLeftoverTors(runtimePath, torrc, 5*time.Second)
	switch {
	case sweep.skipped != "":
		slog.Debug("no sweep for a leftover tor", slog.String("why", sweep.skipped))
	case len(sweep.ended)+len(sweep.refused)+len(sweep.changed)+len(sweep.stuck) > 0:
		slog.Warn("sweep for a tor left by an earlier haomad",
			slog.String("torrc", torrc),
			slog.String("ended", describeProcs(sweep.ended)),
			slog.String("refused", describeProcs(sweep.refused)),
			slog.String("changed", describeProcs(sweep.changed)),
			slog.String("still_running", describeProcs(sweep.stuck)))
	}
	haomadCtx, haomadCancel := context.WithTimeout(context.Background(), readyTimeout)
	defer haomadCancel()
	haomad, err := supervisor.AttachOrSpawn(
		haomadCtx,
		"haomad",
		opts.haomadBin,
		haomadArgs,
		secretsBlob,
		runtimePath,
		haomadStderr,
		haomadHealthCheck(bearer, haomadCertPath),
	)
	if err != nil {
		return fmt.Errorf("haomad: %w", err)
	}
	slog.Info("haomad up",
		slog.Int("pid", haomad.PID),
		slog.String("api_addr", haomad.APIAddr),
		slog.Time("started_at", haomad.StartedAt),
		slog.Bool("attached", haomad.Attached()),
	)

	haomadTor := opts.tor
	var torStatus string
	if haomad.Attached() {

		running := torSetupFromRuntime(haomad.ManageTor, haomad.TorDataDir)
		haomadTor = running
		if running == opts.tor {
			slog.Info("haomad was already running, with the tor asked for",
				slog.Int("pid", haomad.PID), slog.String("tor", running.String()))
		} else {
			slog.Warn("haomad was already running with another tor, and keeps it until it restarts",
				slog.Int("pid", haomad.PID), slog.String("tor", running.String()),
				slog.String("tor_asked", opts.tor.String()))
			torStatus = attachedTorStatus(haomad.PID, running, opts.tor)
		}
	} else {
		slog.Info("haomad started", slog.String("tor", opts.tor.String()))
	}

	haomaArgs := []string{
		"--cfg-dir", root,
		"--secrets-stdin",
		"--addr", "127.0.0.1:0",
		"--backend-addr", "https://" + haomad.APIAddr,
		"--log-level", opts.logLevel,
		"--log-file", haomaLog,
	}
	if opts.callStats {
		haomaArgs = append(haomaArgs, "--call-stats")
	}
	haoma, err := supervisor.Spawn("haoma", opts.haomaBin, haomaArgs, secretsBlob, haomaStderr)
	if err != nil {

		return fmt.Errorf("haoma: %w", err)
	}

	readyCtx, readyCancel := context.WithTimeout(context.Background(), readyTimeout)
	haomaAddr, err := haoma.WaitReady(readyCtx)
	readyCancel()
	if err != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), stopTimeout)
		_ = haoma.Stop(stopCtx)
		stopCancel()
		return fmt.Errorf("haoma: WaitReady: %w", err)
	}
	slog.Info("haoma up", slog.String("api_addr", haomaAddr))

	wipe(secretsBlob)

	client, err := ipcclient.New(ipcclient.Config{
		FrontendDir:   frontendDir,
		Addr:          haomaAddr,
		ClientName:    "haoma-text",
		ClientVersion: version,
	})
	if err != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), stopTimeout)
		_ = haoma.Stop(stopCtx)
		stopCancel()
		return fmt.Errorf("ipc client: %w", err)
	}
	go func() {
		if err := client.Run(); err != nil {
			slog.Warn("client run exited with error", slog.Any("err", err))
		}
	}()

	app := tui.New(client)
	app.DataDir = textDataDir
	app.Version = version
	app.SweepVideoFifos()
	app.VaultCtl = vc
	app.BackupCtl = newBackupController(root, opts.haomaVaultBin, client, haomad, haoma)
	app.SystemTor = haomadTor.system
	if torStatus != "" {
		app.PostStatus("%s", torStatus)
	}

	isDefault, saysSo := vc.IsInsecureDefaultPassphrase(), vc.SaysPassphraseIsDefault()
	slog.Debug("status: the default-passphrase warning was decided",
		slog.Bool("passphrase_is_default", isDefault),
		slog.Bool("says_passphrase_is_default", saysSo),
		slog.String("tier", vc.snapshot().ThreatProfile),
	)
	if isDefault && saysSo {
		app.PostStatus("[red]WARNING:[white] vault sealed with the insecure default passphrase. Run [yellow]/change-pass <old> <new>[white] before sharing anything sensitive.")
	}
	if vc.IsInsecureDefaultPIN() {
		app.PostStatus("[red]WARNING:[white] PIN is the insecure default %q. Run [yellow]/change-pin <old> <new>[white] before relying on the soft-lock.", "0000")
	}

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	stopBackground := make(chan struct{})
	go func() {
		select {
		case sig := <-sigCh:
			slog.Info("signal received; stopping TUI (haomad stays alive)",
				slog.String("signal", sig.String()))
			app.Stop()
		case <-stopBackground:
		}
	}()
	defer func() {
		signal.Stop(sigCh)
		close(stopBackground)
	}()

	idleTimeout := opts.idleOverride
	if idleTimeout == 0 {
		idleTimeout = payload.IdleTimeoutSeconds
	}

	if idleTimeout > 0 {
		slog.Info("idle timer enabled",
			slog.Int("timeout_sec", idleTimeout),
			slog.String("action", payload.IdleAction),
			slog.Int("pin_validity_sec", payload.PinValiditySec),
		)
	} else {
		slog.Info("idle timer disabled at boot (IdleTimeoutSeconds <= 0); watcher will activate when value flips positive")
	}
	go runIdleWatcher(stopBackground, app, vc.snapshot)

	runErr := app.Run()

	slog.Info("TUI exited; stopping haoma (haomad stays running)")
	stopCtx, stopCancel := context.WithTimeout(context.Background(), stopTimeout)
	defer stopCancel()
	if err := haoma.Stop(stopCtx); err != nil {
		slog.Warn("haoma stop", slog.Any("err", err))
	}
	return runErr
}

func haomadHealthCheck(bearer, certPath string) supervisor.HealthCheckFunc {
	return func(ctx context.Context, addr string) error {
		tlsCfg, err := backendapi.PinnedTLSConfig(certPath)
		if err != nil {
			return fmt.Errorf("pin haomad cert: %w", err)
		}
		dialCtx, cancel := context.WithTimeout(ctx, healthDial)
		defer cancel()
		req, err := http.NewRequestWithContext(dialCtx, http.MethodGet, "https://"+addr+"/tor", nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+bearer)
		client := &http.Client{
			Timeout:   healthDial,
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode == http.StatusUnauthorized {
			return errors.New("attached daemon rejected our bearer token")
		}
		return nil
	}
}

func resolveDaemonBins(opts spawnOpts) spawnOpts {
	selfDir := ""
	if exe, err := os.Executable(); err == nil {
		selfDir = filepath.Dir(exe)
	}
	if opts.haomadBin == "" {
		opts.haomadBin = joinIfDir(selfDir, "haomad")
	}
	if opts.haomaBin == "" {
		opts.haomaBin = joinIfDir(selfDir, "haoma")
	}
	if opts.haomaVaultBin == "" {
		opts.haomaVaultBin = joinIfDir(selfDir, "haoma-vault")
	}
	return opts
}

func joinIfDir(dir, name string) string {
	if dir == "" {
		return name
	}
	return filepath.Join(dir, name)
}

func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

const idlePollInterval = 1 * time.Second

func runIdleWatcher(stopCh <-chan struct{}, app *tui.App, snap func() vault.Payload) {
	tick := time.NewTicker(idlePollInterval)
	defer tick.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-tick.C:
			cur := snap()

			timeoutSec := cur.IdleTimeoutSeconds
			if timeoutSec <= 0 {
				continue
			}
			elapsed := time.Now().Unix() - app.LastActivityUnix()
			if elapsed < int64(timeoutSec) {
				continue
			}
			action := cur.IdleAction
			pin := cur.PIN
			pinValiditySec := cur.PinValiditySec
			if action == "soft-lock" && pin != "" {
				slog.Info("idle elapsed; showing PIN gate",
					slog.Int64("elapsed_sec", elapsed),
					slog.String("action", "soft-lock"),
					slog.Int("pin_validity_sec", pinValiditySec),
				)
				if app.ShowPINGate(pin, pinValiditySec) {
					slog.Info("PIN gate cleared; resuming")
					continue
				}
				slog.Info("PIN gate escalated to safe-lock; stopping TUI (haomad stays alive)")
				app.Stop()
				return
			}
			if action == "soft-lock" && pin == "" {
				slog.Warn("idle elapsed (action=soft-lock) but PIN is unset; falling back to safe-lock",
					slog.Int64("elapsed_sec", elapsed))
			} else {
				slog.Info("idle elapsed; safe-locking; stopping TUI (haomad stays alive)",
					slog.Int64("elapsed_sec", elapsed),
					slog.String("action", action),
				)
			}
			app.Stop()
			return
		}
	}
}
