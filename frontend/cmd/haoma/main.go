package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/logging"
	"haoma-frontend/internal/secrets"
)

var version = "dev"

func main() {

	var cfg config
	flag.StringVar(&cfg.cfgDir, "cfg-dir", "", "data root (e.g. ~/.haoma); when set, --data-dir/--haomad-token-file/--haomad-cert-file default to <cfg-dir>/{frontend,backend}/. Per-file flags below override.")
	flag.StringVar(&cfg.dataDir, "data-dir", "", "daemon data directory (default <cfg-dir>/frontend or platform per-user dir)")
	flag.StringVar(&cfg.addr, "addr", "127.0.0.1:0", "IPC listen address; :0 = ephemeral (the chosen address is published on the stdout ready line)")
	flag.StringVar(&cfg.backendAddr, "backend-addr", "", "haomad local API URL (e.g. https://127.0.0.1:8731); disables backend relay if empty")
	flag.StringVar(&cfg.haomadTokenFile, "haomad-token-file", "", "path to haomad's bearer-token file (default <cfg-dir>/backend/haomad-token); 0600 perms enforced; ignored under --secrets-stdin (token comes from the vault)")
	flag.StringVar(&cfg.haomadCertFile, "haomad-cert-file", "", "path to haomad's TLS cert.pem for cert pinning (default <cfg-dir>/backend/cert.pem); world-readable")
	flag.StringVar(&cfg.passphraseFile, "passphrase-file", "", "file containing the store passphrase (preferred over $HAOMA_FRONTEND_PASSPHRASE); 0600 perms enforced; see ADR-035")
	flag.StringVar(&cfg.passphraseEnv, "passphrase-env", "HAOMA_FRONTEND_PASSPHRASE", "env var holding the store passphrase (fallback when --passphrase-file is unset)")
	flag.BoolVar(&cfg.secretsStdin, "secrets-stdin", false, "read the secrets bundle (ADR-036) as a single JSON blob from stdin, EOF-terminated; mutually exclusive with --passphrase-file/--passphrase-env/--haomad-token-file")
	flag.StringVar(&cfg.streamerDir, "streamer-dir", "", "directory containing haoma-mic / haoma-spk binaries (overrides $HAOMA_STREAMER_DIR; default: dir of haoma binary, then $PATH)")
	flag.BoolVar(&cfg.streamerTrace, "streamer-trace", false, "spawn streamers with --trace (per-frame stdout events for call debugging)")
	flag.StringVar(&cfg.logLevel, "log-level", "warn", "log level: debug|info|warn|error")
	flag.StringVar(&cfg.logFile, "log-file", "", "log destination: empty = silent (production), \"-\" = stderr (dev), else a file path (created with 0600)")
	flag.StringVar(&cfg.logFormat, "log-format", "text", "log format: text|json")
	flag.Int64Var(&cfg.logMaxBytes, "log-max-bytes", 0, "rotate the log file when it would exceed this size (0 = 4 MiB default); ignored when --log-file is empty or \"-\"")
	flag.BoolVar(&cfg.showVersion, "version", false, "print version and exit")
	flag.Parse()

	if cfg.showVersion {
		fmt.Println("haoma", version)
		return
	}

	sec, err := loadSecrets(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "haoma: secrets: %v\n", err)
		os.Exit(2)
	}
	cfg.secrets = sec

	logger, closeLog, err := logging.New(logging.Config{
		Level: cfg.logLevel, File: cfg.logFile, Format: cfg.logFormat, Service: "haoma", MaxBytes: cfg.logMaxBytes,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "haoma: logging: %v\n", err)
		os.Exit(2)
	}
	defer closeLog()
	slog.SetDefault(logger)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := run(ctx, cfg); err != nil {
		slog.Error("fatal", slog.Any("err", err))
		os.Exit(1)
	}
}

type config struct {
	cfgDir          string
	dataDir         string
	addr            string
	backendAddr     string
	haomadTokenFile string
	haomadCertFile  string
	passphraseFile  string
	passphraseEnv   string
	secretsStdin    bool
	streamerDir     string
	streamerTrace   bool
	logLevel        string
	logFile         string
	logFormat       string
	logMaxBytes     int64
	showVersion     bool

	secrets secrets.Secrets
}

func loadSecrets(cfg config) (secrets.Secrets, error) {
	if cfg.secretsStdin {
		if cfg.passphraseFile != "" || cfg.haomadTokenFile != "" {
			return secrets.Secrets{}, fmt.Errorf("--secrets-stdin is mutually exclusive with --passphrase-file/--haomad-token-file")
		}

		s, err := secrets.Parse(os.Stdin)
		if err != nil {
			return secrets.Secrets{}, err
		}
		if err := s.Validate(); err != nil {
			return secrets.Secrets{}, err
		}
		return s, nil
	}

	var pass string
	if cfg.passphraseFile != "" {
		p, err := ipc.ReadSensitive(cfg.passphraseFile)
		if err != nil {
			return secrets.Secrets{}, fmt.Errorf("passphrase: %w", err)
		}
		pass = p
	} else {
		pass = os.Getenv(cfg.passphraseEnv)
		if pass == "" {
			return secrets.Secrets{}, fmt.Errorf("passphrase: set --passphrase-file (preferred), --secrets-stdin, or $%s", cfg.passphraseEnv)
		}
	}
	return secrets.Secrets{
		FrontendStorePassphrase: pass,
	}, nil
}
