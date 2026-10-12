package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"haoma-frontend/internal/backuparchive"
	"haoma-frontend/internal/lockfile"
	"haoma-frontend/internal/logging"
	"haoma-frontend/internal/paths"
	"haoma-frontend/internal/vault"
)

var version = "dev"

const (
	vaultFileName   = "vault.enc"
	lockfileName    = "vault.lock"
	runtimeFileName = "haomad.runtime.json"
)

func main() {
	var (
		cfgDir        string
		haomadBin     string
		haomaBin      string
		haomaVaultBin string
		logLevel      string
		logFile       string
		logFormat     string
		showVersion   bool
		restoreBackup string
		torBin        string
		systemTor     bool
	)
	flag.StringVar(&cfgDir, "cfg-dir", "", "data root: anchors vault.enc + vault.lock + daemon tier dirs. Empty = platform per-user dir (Linux ~/.haoma, Windows %AppData%/haoma). Tilde + relative paths resolved against CWD.")
	flag.StringVar(&haomadBin, "haomad-bin", "", "path to the haomad binary; default = same directory as haoma-text.")
	flag.StringVar(&haomaBin, "haoma-bin", "", "path to the haoma binary; default = same directory as haoma-text.")
	flag.StringVar(&haomaVaultBin, "haoma-vault-bin", "", "path to the haoma-vault binary used for vault writes; default = same directory as haoma-text.")
	flag.StringVar(&logLevel, "log-level", "warn", "log level: debug|info|warn|error. Passed through to spawned haomad + haoma.")
	flag.StringVar(&logFile, "log-file", "", "log destination: empty = <cfg-dir>/haoma-text.log (privacy default — never leak to stdio while the TUI owns the screen), \"-\" = stderr (foreground dev only), else a file path (created with 0600).")
	flag.StringVar(&logFormat, "log-format", "text", "log format: text|json")
	flag.StringVar(&restoreBackup, "restore-backup", "", "before unlock, restore an archive made by /backup onto an empty device (no contact, no chat, no linked device; any other device is refused before anything is staged): extracts the archive into <cfg-dir>/.staging-<ts>/, asks for the passphrase of the backed-up device, stops a running haomad, then deletes the empty device's vault and stores and moves the backup in. tor/ and the logs stay. A restore refused before the swap leaves this device as it was; one that fails during the swap says to delete <cfg-dir> and restore again.")
	idleOverride := flag.Int("idle-timeout-seconds", 0, "override the vault's IdleTimeoutSeconds for this run (dev convenience). 0 = use vault value.")
	flag.StringVar(&torBin, "tor-bin", defaultTorBin, "tor binary haomad starts as a tor of its own, with its data dir in <cfg-dir>/tor; a bare name is looked up on PATH. No tor control password is needed with it. Not used with --system-tor.")
	flag.BoolVar(&systemTor, "system-tor", false, "use the tor already running on 127.0.0.1:9050 (SOCKS) and 9051 (control) instead of a tor of haomad's own. Its control password is the vault's tor password (/set-tor-password), needed only if that tor asks for one.")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.Parse()

	if showVersion {
		fmt.Println("haoma-text", version)
		return
	}

	torBinSet := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "tor-bin" {
			torBinSet = true
		}
	})
	tor, err := torFlags(systemTor, torBin, torBinSet)
	if err != nil {
		fmt.Fprintf(os.Stderr, "haoma-text: %v\n", err)
		os.Exit(2)
	}

	runVaultBootflow(cfgDir, spawnOpts{
		haomadBin:     haomadBin,
		haomaBin:      haomaBin,
		haomaVaultBin: haomaVaultBin,
		logLevel:      logLevel,
		logFile:       logFile,
		logFormat:     logFormat,
		idleOverride:  *idleOverride,
		restoreBackup: restoreBackup,
		tor:           tor,
	})
}

func initLogging(level, file, format string) func() error {
	logger, closeLog, err := logging.New(logging.Config{
		Level: level, File: file, Format: format, Service: "haoma-text",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "haoma-text: logging: %v\n", err)
		os.Exit(2)
	}
	slog.SetDefault(logger)
	return closeLog
}

func fatal(msg string, args ...any) {
	slog.Error(msg, args...)
	os.Exit(1)
}

func runVaultBootflow(cfgDirFlag string, opts spawnOpts) {
	root, err := paths.RootFromFlag(cfgDirFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "haoma-text: resolve --cfg-dir: %v\n", err)
		os.Exit(1)
	}
	if _, err := paths.BootstrapAt(root); err != nil {
		fmt.Fprintf(os.Stderr, "haoma-text: bootstrap cfg-dir %s: %v\n", root, err)
		os.Exit(1)
	}

	opts.tor = opts.tor.withDir(root)

	logFile := opts.logFile
	if logFile == "" {
		logFile = filepath.Join(root, "haoma-text.log")
	}
	closeLog := initLogging(opts.logLevel, logFile, opts.logFormat)
	defer closeLog()

	lockPath := filepath.Join(root, lockfileName)
	lk, err := lockfile.Acquire(lockPath)
	if err != nil {
		if errors.Is(err, lockfile.ErrInUse) {
			fatal("vault is in use by another haoma-text process; refusing to start",
				slog.String("lock", lockPath))
		}
		fatal("acquire vault lock",
			slog.String("lock", lockPath), slog.Any("err", err))
	}
	defer func() {
		if err := lk.Release(); err != nil {
			slog.Warn("release vault lock", slog.Any("err", err))
		}
	}()

	opts = resolveDaemonBins(opts)

	cleanRestoreLeftovers(opts.haomaVaultBin, root)

	vaultPath := filepath.Join(root, vaultFileName)
	var unlocked vaultUnlock
	if opts.restoreBackup != "" {

		var report restoreReport
		unlocked, report, err = runRestoreBackup(root, opts.haomaVaultBin, opts.restoreBackup, promptPassphrase)
		if err != nil {

			var sentence sentenceError
			if errors.As(err, &sentence) {
				fmt.Fprintln(os.Stderr, "haoma-text: "+sentence.text)
			} else {
				fmt.Fprintf(os.Stderr, "haoma-text: restore: %v\n", err)
			}
			fatal("restore-backup", slog.Any("err", err))
		}
		fmt.Fprintln(os.Stderr, "haoma-text: backup restored.")
		for _, line := range report.Lines() {
			fmt.Fprintln(os.Stderr, "haoma-text: "+line)
		}
	} else {
		unlocked, err = openOrCreateVault(vaultPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "haoma-text: %v\n", err)
			fatal("vault", slog.Any("err", err))
		}
	}
	slog.Info("vault unlocked",
		slog.String("cfg_dir", root),
		slog.String("threat_profile", unlocked.payload.ThreatProfile),
		slog.String("idle_action", unlocked.payload.IdleAction),
		slog.Int("idle_timeout_sec", unlocked.payload.IdleTimeoutSeconds),
	)

	vc := newVaultController(vaultPath, unlocked.passphrase, unlocked.payload, unlocked.params, opts.haomaVaultBin)

	defer vc.Zero()
	if err := spawnAndRun(root, vc, opts); err != nil {
		fatal("supervisor", slog.Any("err", err))
	}
}

type vaultUnlock struct {
	payload    vault.Payload
	passphrase []byte
	params     vault.KDFParams
}

func openOrCreateVault(path string) (vaultUnlock, error) {

	root := filepath.Dir(path)
	if pending, err := backuparchive.PendingCommit(root); err != nil {
		return vaultUnlock{}, err
	} else if pending != nil {
		return vaultUnlock{}, errors.New(pending.Message(root))
	}
	if _, err := os.Stat(path); err == nil {
		return unlockExisting(path)
	} else if !os.IsNotExist(err) {
		return vaultUnlock{}, fmt.Errorf("stat %s: %w", path, err)
	}
	return createFresh(path)
}

func unlockExisting(path string) (vaultUnlock, error) {
	pass, err := promptPassphrase("Master passphrase: ")
	if err != nil {
		return vaultUnlock{}, err
	}
	payload, params, err := vault.Open(path, pass)
	if err != nil {

		clear(pass)
		return vaultUnlock{}, fmt.Errorf("open %s: %w", path, err)
	}
	if vault.IsInsecureDefaultPassphrase(pass) {
		slog.Warn("vault sealed with the insecure default passphrase — run /change-pass before sharing anything sensitive")
	}
	return vaultUnlock{payload: payload, passphrase: pass, params: params}, nil
}

func createFresh(path string) (vaultUnlock, error) {
	slog.Info("no vault at path — first-run setup", slog.String("path", path))
	pass, err := promptCreatePassphrase()
	if err != nil {
		return vaultUnlock{}, err
	}
	payload, err := vault.MintFreshPayload()
	if err != nil {
		return vaultUnlock{}, fmt.Errorf("mint fresh secrets: %w", err)
	}
	if len(pass) == 0 {
		pass = []byte(vault.InsecureDefaultPassphrase)
		slog.Warn("vault created with the insecure default passphrase — run /change-pass before sharing anything sensitive",
			slog.String("path", path))
	}
	if err := vault.Create(path, pass, payload, vault.DefaultKDFParams); err != nil {
		clear(pass)
		return vaultUnlock{}, fmt.Errorf("create %s: %w", path, err)
	}
	slog.Info("vault minted", slog.String("path", path))
	return vaultUnlock{payload: payload, passphrase: pass, params: vault.DefaultKDFParams}, nil
}
