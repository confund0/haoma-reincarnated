package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"haoma/internal/auth"
	"haoma/internal/logging"
	"haoma/internal/secrets"
)

var version = "0.0.0-dev"

const backendSubdir = "backend"

func main() {

	showVersion := flag.Bool("version", false, "print version and exit")
	cfgDir := flag.String("cfg-dir", "", "data root (e.g. ~/.haoma); when set, --store/--cert-dir/--token-file default to <cfg-dir>/"+backendSubdir+"/. Per-file flags below override.")
	storeDir := flag.String("store", "", "encrypted store directory (default <cfg-dir>/"+backendSubdir+")")
	apiAddr := flag.String("api-addr", "127.0.0.1:7890", "local API listen address (use :0 for ephemeral; the chosen address is published on the stdout ready line)")
	tokenFile := flag.String("token-file", "", "bearer-token file for the local API (default <store>/haomad-token); 0600 perms enforced; ignored under --secrets-stdin (token comes from the vault)")
	certDir := flag.String("cert-dir", "", "directory holding the API listener's TLS cert.pem + cert.key (default same as --store); minted on first run; haoma reads cert.pem from here for cert pinning")
	passphraseFile := flag.String("passphrase-file", "", "file containing the store passphrase (preferred over $HAOMA_PASSPHRASE); 0600 perms enforced; see ADR-035")
	torPasswordFile := flag.String("tor-password-file", "", "file containing the tor control-port password (preferred over $HAOMA_TOR_PASSWORD); 0600 perms enforced; see ADR-035")
	secretsStdin := flag.Bool("secrets-stdin", false, "read the secrets bundle (ADR-036) as a single JSON blob from stdin, EOF-terminated; mutually exclusive with --passphrase-file/--tor-password-file/--token-file")
	runtimeFile := flag.String("runtime-file", "", "path to runtime metadata file (ADR-036): atomically written after the API listener binds, removed on graceful shutdown; consumed by haoma-text supervisor for attach-vs-spawn. Empty = skip both.")
	torControl := flag.String("tor-control", "127.0.0.1:9051", "tor control-port address (ignored under --manage-tor; the spawned tor's auto-allocated control port is used instead)")
	torSocks := flag.String("tor-socks", "127.0.0.1:9050", "tor SOCKS5 address for outbound peer traffic (ignored under --manage-tor)")
	manageTor := flag.String("manage-tor", "", "path to a tor binary; when set, haomad spawns and supervises tor itself with auto-allocated control + SOCKS ports (Android / no-system-tor environments). On graceful shutdown the spawned tor is SIGTERM'd then SIGKILL'd.")
	torDataDir := flag.String("tor-data-dir", "", "directory for the spawned tor's per-instance state (torrc, control-port file, cookie, descriptors, log). Required when --manage-tor is set; default <store>/tor")
	onionVirtPort := flag.Int("onion-virt-port", 80, "external port exposed on the .onion")
	logLevel := flag.String("log-level", "warn", "log level: debug|info|warn|error")
	logFile := flag.String("log-file", "", "log destination: empty = silent (production), \"-\" = stderr (dev), else a file path (created with 0600)")
	logFormat := flag.String("log-format", "text", "log format: text|json")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	if *storeDir == "" {
		if *cfgDir == "" {
			fmt.Fprintln(os.Stderr, "haomad: --store or --cfg-dir required")
			os.Exit(2)
		}
		*storeDir = filepath.Join(*cfgDir, backendSubdir)
	}

	sec, err := loadSecrets(*secretsStdin, *passphraseFile, *torPasswordFile, *tokenFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "haomad: secrets: %v\n", err)
		os.Exit(2)
	}

	logger, closeLog, errLog := logging.New(logging.Config{
		Level: *logLevel, File: *logFile, Format: *logFormat, Service: "haomad",
	})
	if errLog != nil {
		fmt.Fprintf(os.Stderr, "haomad: logging: %v\n", errLog)
		os.Exit(2)
	}
	defer closeLog()
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	resolvedTokenFile := *tokenFile
	if resolvedTokenFile == "" && !*secretsStdin {

		resolvedTokenFile = filepath.Join(*storeDir, "haomad-token")
	}

	resolvedCertDir := *certDir
	if resolvedCertDir == "" {

		resolvedCertDir = *storeDir
	}

	resolvedTorDataDir := *torDataDir
	if *manageTor != "" && resolvedTorDataDir == "" {
		resolvedTorDataDir = filepath.Join(*storeDir, "tor")
	}

	cfg := config{
		storeDir:      *storeDir,
		secrets:       sec,
		apiAddr:       *apiAddr,
		tokenFile:     resolvedTokenFile,
		certDir:       resolvedCertDir,
		runtimeFile:   *runtimeFile,
		torControl:    *torControl,
		torSocks:      *torSocks,
		manageTor:     *manageTor,
		torDataDir:    resolvedTorDataDir,
		onionVirtPort: *onionVirtPort,
	}
	if err := run(ctx, cfg); err != nil {
		slog.Error("fatal", slog.Any("err", err))
		os.Exit(1)
	}
}

func loadSecrets(useStdin bool, passphraseFile, torPasswordFile, tokenFile string) (secrets.Secrets, error) {
	if useStdin {
		if passphraseFile != "" || torPasswordFile != "" || tokenFile != "" {
			return secrets.Secrets{}, fmt.Errorf("--secrets-stdin is mutually exclusive with --passphrase-file/--tor-password-file/--token-file")
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
	pass, err := loadFromFileOrEnv(passphraseFile, "--passphrase-file", "HAOMA_PASSPHRASE")
	if err != nil {
		return secrets.Secrets{}, fmt.Errorf("passphrase: %w", err)
	}
	tor, err := loadFromFileOrEnv(torPasswordFile, "--tor-password-file", "HAOMA_TOR_PASSWORD")
	if err != nil {
		return secrets.Secrets{}, fmt.Errorf("tor password: %w", err)
	}
	return secrets.Secrets{
		HaomadStorePassphrase: pass,
		TorPassword:           tor,
	}, nil
}

func loadFromFileOrEnv(file, fileFlag, envName string) (string, error) {
	if file != "" {
		return auth.ReadSensitive(file)
	}
	if v := os.Getenv(envName); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("set %s (preferred), --secrets-stdin, or $%s", fileFlag, envName)
}
