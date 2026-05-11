package vault

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"

	"haoma-frontend/internal/paths"
	"haoma-frontend/internal/secrets"
)

var magic = [8]byte{'H', 'A', 'O', 'M', 'A', 'V', 'L', 'T'}

const CurrentVersion uint8 = 1

const (
	headerLen = 8 + 1 + 4 + 4 + 1 + 1 + 16 + 24
	saltLen   = 16
	nonceLen  = 24
	keyLen    = 32
	fileMode  = 0o600
)

const (
	InsecureDefaultPassphrase = "good-girls-go-to-heaven"
	InsecureDefaultPIN        = "0000"
)

func IsInsecureDefaultPassphrase(p string) bool { return p == InsecureDefaultPassphrase }

func IsInsecureDefaultPIN(p string) bool { return p == InsecureDefaultPIN }

const (
	DefaultIdleTimeoutSec   = 1800
	DefaultIdleAction       = "safe-lock"
	DefaultPinValiditySec   = 0
	DefaultPanicAction      = ""
	DefaultRotationInterval = 0

	DefaultRetentionSec     = 0
	DefaultSendReceipts     = true
	DefaultNotifyShell      = true
	DefaultNotifyShowSender = false
	DefaultNotifyShowBody   = false
)

const (
	PresetDomestic = "domestic"
	PresetPrivacy  = "privacy"
	PresetActivist = "activist"
)

type ThreatPresetBundle struct {
	IdleAction         string
	IdleTimeoutSeconds int
	PinValiditySec     int
	PanicAction        string
}

var ThreatPresetBundles = map[string]ThreatPresetBundle{
	PresetDomestic: {
		IdleAction:         "soft-lock",
		IdleTimeoutSeconds: 300,
		PinValiditySec:     86400,
		PanicAction:        "safe-lock",
	},
	PresetPrivacy: {
		IdleAction:         "soft-lock",
		IdleTimeoutSeconds: 60,
		PinValiditySec:     300,
		PanicAction:        "hard-lock",
	},
}

type KDFParams struct {
	Time    uint32
	Memory  uint32
	Threads uint8
	KeyLen  uint8
}

var DefaultKDFParams = KDFParams{
	Time:    4,
	Memory:  256 * 1024,
	Threads: 4,
	KeyLen:  32,
}

type Payload struct {
	secrets.Secrets

	ThreatProfile string `json:"threat_profile,omitempty"`

	PIN string `json:"pin,omitempty"`

	IdleAction string `json:"idle_action,omitempty"`

	PinValiditySec int `json:"pin_validity_sec,omitempty"`

	PanicAction string `json:"panic_action,omitempty"`

	NotificationsOnLock bool `json:"notifications_on_lock"`

	RotationIntervalSec int `json:"rotation_interval_sec,omitempty"`

	SecurityWarnings []string `json:"security_warnings,omitempty"`

	DefaultRetentionSec uint64 `json:"default_retention_sec,omitempty"`

	DefaultSendReceipts bool `json:"default_send_receipts"`

	NotifyShellEnabled bool `json:"notify_shell_enabled,omitempty"`

	NotifyShowSender bool `json:"notify_show_sender,omitempty"`

	NotifyShowBody bool `json:"notify_show_body,omitempty"`

	NotifyDisguiseEnabled bool `json:"notify_disguise_enabled,omitempty"`

	DefaultSaveDir        string `json:"default_save_dir,omitempty"`
	DefaultAttachStartDir string `json:"default_attach_start_dir,omitempty"`
}

func (p Payload) Validate() error {
	if err := p.Secrets.Validate(); err != nil {
		return err
	}
	switch p.IdleAction {
	case "", "soft-lock", "safe-lock", "hard-lock":
	default:
		return fmt.Errorf("vault: invalid idle_action %q", p.IdleAction)
	}
	switch p.PanicAction {
	case "", "safe-lock", "hard-lock", "self-destruct":
	default:
		return fmt.Errorf("vault: invalid panic_action %q", p.PanicAction)
	}
	switch p.ThreatProfile {
	case "", PresetDomestic, PresetPrivacy, PresetActivist:
	default:
		return fmt.Errorf("vault: invalid threat_profile %q", p.ThreatProfile)
	}
	if p.PinValiditySec < 0 {
		return fmt.Errorf("vault: pin_validity_sec must be >= 0, got %d", p.PinValiditySec)
	}
	if p.RotationIntervalSec < 0 {
		return fmt.Errorf("vault: rotation_interval_sec must be >= 0, got %d", p.RotationIntervalSec)
	}
	return nil
}

var (
	ErrBadMagic           = errors.New("vault: not a Haoma vault file (magic mismatch)")
	ErrUnsupportedVersion = errors.New("vault: unsupported version")
	ErrTruncated          = errors.New("vault: file truncated")
	ErrUnseal             = errors.New("vault: AEAD unseal failed (wrong passphrase or tampered ciphertext)")
	ErrEmpty              = errors.New("vault: file exists but is empty")
)

func PeekParams(path string) (KDFParams, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return KDFParams{}, fmt.Errorf("vault: read: %w", err)
	}
	if len(raw) < headerLen {
		return KDFParams{}, ErrTruncated
	}
	if [8]byte(raw[:8]) != magic {
		return KDFParams{}, ErrBadMagic
	}
	if raw[8] != CurrentVersion {
		return KDFParams{}, fmt.Errorf("%w: got %d, supported %d", ErrUnsupportedVersion, raw[8], CurrentVersion)
	}
	return KDFParams{
		Time:    binary.BigEndian.Uint32(raw[9:13]),
		Memory:  binary.BigEndian.Uint32(raw[13:17]),
		Threads: raw[17],
		KeyLen:  raw[18],
	}, nil
}

func Open(path, passphrase string) (Payload, KDFParams, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Payload{}, KDFParams{}, fmt.Errorf("vault: read: %w", err)
	}
	if len(raw) == 0 {
		return Payload{}, KDFParams{}, ErrEmpty
	}
	return openBytes(raw, passphrase)
}

func openBytes(raw []byte, passphrase string) (Payload, KDFParams, error) {
	if len(raw) < headerLen+chacha20poly1305.Overhead {
		return Payload{}, KDFParams{}, ErrTruncated
	}
	header := raw[:headerLen]
	body := raw[headerLen:]

	if [8]byte(header[:8]) != magic {
		return Payload{}, KDFParams{}, ErrBadMagic
	}
	version := header[8]
	if version != CurrentVersion {
		return Payload{}, KDFParams{}, fmt.Errorf("%w: got %d, supported %d", ErrUnsupportedVersion, version, CurrentVersion)
	}
	params := KDFParams{
		Time:    binary.BigEndian.Uint32(header[9:13]),
		Memory:  binary.BigEndian.Uint32(header[13:17]),
		Threads: header[17],
		KeyLen:  header[18],
	}
	if params.KeyLen != keyLen {
		return Payload{}, KDFParams{}, fmt.Errorf("vault: unsupported KeyLen %d (want %d)", params.KeyLen, keyLen)
	}
	salt := header[19 : 19+saltLen]
	nonce := header[19+saltLen : 19+saltLen+nonceLen]

	key := argon2.IDKey([]byte(passphrase), salt, params.Time, params.Memory, params.Threads, uint32(params.KeyLen))
	defer zero(key)

	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return Payload{}, KDFParams{}, fmt.Errorf("vault: aead init: %w", err)
	}
	plaintext, err := aead.Open(nil, nonce, body, header)
	if err != nil {
		return Payload{}, KDFParams{}, ErrUnseal
	}
	defer zero(plaintext)

	var p Payload
	if err := json.Unmarshal(plaintext, &p); err != nil {
		return Payload{}, KDFParams{}, fmt.Errorf("vault: decode payload: %w", err)
	}
	return p, params, nil
}

func Create(path, passphrase string, payload Payload, params KDFParams) error {
	if err := payload.Validate(); err != nil {
		return fmt.Errorf("vault: payload: %w", err)
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("vault: %s already exists; refusing to overwrite", path)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("vault: stat %s: %w", path, err)
	}
	return saveTo(path, passphrase, payload, params)
}

func CreateInsecure(path string, payload Payload, params KDFParams) error {
	return Create(path, InsecureDefaultPassphrase, payload, params)
}

func Save(path, passphrase string, payload Payload, params KDFParams) error {
	if err := payload.Validate(); err != nil {
		return fmt.Errorf("vault: payload: %w", err)
	}
	return saveTo(path, passphrase, payload, params)
}

func ChangePassphrase(path, oldPass, newPass string) error {
	payload, params, err := Open(path, oldPass)
	if err != nil {
		return err
	}
	return Save(path, newPass, payload, params)
}

func saveTo(path, passphrase string, payload Payload, params KDFParams) error {
	if params.KeyLen != keyLen {
		return fmt.Errorf("vault: KeyLen must be %d, got %d", keyLen, params.KeyLen)
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("vault: salt: %w", err)
	}
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("vault: nonce: %w", err)
	}

	header := make([]byte, headerLen)
	copy(header[0:8], magic[:])
	header[8] = CurrentVersion
	binary.BigEndian.PutUint32(header[9:13], params.Time)
	binary.BigEndian.PutUint32(header[13:17], params.Memory)
	header[17] = params.Threads
	header[18] = params.KeyLen
	copy(header[19:19+saltLen], salt)
	copy(header[19+saltLen:19+saltLen+nonceLen], nonce)

	plaintext, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("vault: marshal payload: %w", err)
	}
	defer zero(plaintext)

	key := argon2.IDKey([]byte(passphrase), salt, params.Time, params.Memory, params.Threads, uint32(params.KeyLen))
	defer zero(key)

	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return fmt.Errorf("vault: aead init: %w", err)
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, header)

	full := append(header, ciphertext...)
	return writeAtomic(path, full)
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("vault: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(fileMode); err != nil {
		tmp.Close()
		return fmt.Errorf("vault: chmod temp: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("vault: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("vault: close temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("vault: rename: %w", err)
	}
	return nil
}

func MintFreshSecrets() (secrets.Secrets, error) {
	storeBack, err := randB64(32)
	if err != nil {
		return secrets.Secrets{}, err
	}
	storeFront, err := randB64(32)
	if err != nil {
		return secrets.Secrets{}, err
	}
	tok, err := randHex(32)
	if err != nil {
		return secrets.Secrets{}, err
	}
	return secrets.Secrets{
		HaomadStorePassphrase:   storeBack,
		FrontendStorePassphrase: storeFront,
		HaomadToken:             tok,
	}, nil
}

func MintFreshPayload() (Payload, error) {
	s, err := MintFreshSecrets()
	if err != nil {
		return Payload{}, err
	}
	s.IdleTimeoutSeconds = DefaultIdleTimeoutSec

	saveDir, err := paths.DefaultDownloadsDir()
	if err != nil {
		return Payload{}, fmt.Errorf("vault: resolve default save dir: %w", err)
	}
	attachDir, err := paths.DefaultAttachStartDir()
	if err != nil {
		return Payload{}, fmt.Errorf("vault: resolve default attach dir: %w", err)
	}
	return Payload{
		Secrets:             s,
		PIN:                 InsecureDefaultPIN,
		IdleAction:          DefaultIdleAction,
		PinValiditySec:      DefaultPinValiditySec,
		PanicAction:         DefaultPanicAction,
		NotificationsOnLock: true,
		RotationIntervalSec: DefaultRotationInterval,

		DefaultRetentionSec:   DefaultRetentionSec,
		DefaultSendReceipts:   DefaultSendReceipts,
		NotifyShellEnabled:    DefaultNotifyShell,
		NotifyShowSender:      DefaultNotifyShowSender,
		NotifyShowBody:        DefaultNotifyShowBody,
		DefaultSaveDir:        saveDir,
		DefaultAttachStartDir: attachDir,
	}, nil
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

func randB64(n int) (string, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", fmt.Errorf("vault: rand: %w", err)
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", fmt.Errorf("vault: rand: %w", err)
	}
	return hex.EncodeToString(b), nil
}
