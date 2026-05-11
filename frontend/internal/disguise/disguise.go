package disguise

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

const (
	FileName = "disguise.enc"

	CurrentVersion uint8 = 1

	headerLen = 8 + 1 + 4 + 4 + 1 + 1 + 16 + 24
	saltLen   = 16
	nonceLen  = 24
	keyLen    = 32
	fileMode  = 0o600
)

var magic = [8]byte{'H', 'A', 'O', 'M', 'A', 'D', 'G', 'S'}

var Marker = []byte("haoma-disguise-ok\n")

var LightKDFParams = KDFParams{
	Time:    1,
	Memory:  32 * 1024,
	Threads: 4,
	KeyLen:  32,
}

type KDFParams struct {
	Time    uint32
	Memory  uint32
	Threads uint8
	KeyLen  uint8
}

var (
	ErrBadMagic           = errors.New("disguise: not a Haoma disguise sidecar (magic mismatch)")
	ErrUnsupportedVersion = errors.New("disguise: unsupported version")
	ErrTruncated          = errors.New("disguise: file truncated")
	ErrPatternMismatch    = errors.New("disguise: pattern does not match (or sidecar tampered)")
	ErrEmpty              = errors.New("disguise: file exists but is empty")
)

func Path(cfgDir string) string { return filepath.Join(cfgDir, FileName) }

func Init(path, pattern string) error {
	if pattern == "" {
		return errors.New("disguise: empty pattern")
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("disguise: %s already exists; refusing to overwrite", path)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("disguise: stat %s: %w", path, err)
	}
	return seal(path, pattern, LightKDFParams)
}

func Verify(path, pattern string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("disguise: read: %w", err)
	}
	if len(raw) == 0 {
		return ErrEmpty
	}
	plaintext, err := openBytes(raw, pattern)
	if err != nil {
		return err
	}
	defer zero(plaintext)
	if !bytes.Equal(plaintext, Marker) {
		return ErrPatternMismatch
	}
	return nil
}

func Rekey(path, oldPattern, newPattern string) error {
	if newPattern == "" {
		return errors.New("disguise: empty new pattern")
	}
	if err := Verify(path, oldPattern); err != nil {
		return err
	}
	return seal(path, newPattern, LightKDFParams)
}

func seal(path, pattern string, params KDFParams) error {
	if params.KeyLen != keyLen {
		return fmt.Errorf("disguise: KeyLen must be %d, got %d", keyLen, params.KeyLen)
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("disguise: salt: %w", err)
	}
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("disguise: nonce: %w", err)
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

	key := argon2.IDKey([]byte(pattern), salt, params.Time, params.Memory, params.Threads, uint32(params.KeyLen))
	defer zero(key)

	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return fmt.Errorf("disguise: aead init: %w", err)
	}
	ciphertext := aead.Seal(nil, nonce, Marker, header)

	full := append(header, ciphertext...)
	return writeAtomic(path, full)
}

func openBytes(raw []byte, pattern string) ([]byte, error) {
	if len(raw) < headerLen+chacha20poly1305.Overhead {
		return nil, ErrTruncated
	}
	header := raw[:headerLen]
	body := raw[headerLen:]

	if [8]byte(header[:8]) != magic {
		return nil, ErrBadMagic
	}
	version := header[8]
	if version != CurrentVersion {
		return nil, fmt.Errorf("%w: got %d, supported %d", ErrUnsupportedVersion, version, CurrentVersion)
	}
	params := KDFParams{
		Time:    binary.BigEndian.Uint32(header[9:13]),
		Memory:  binary.BigEndian.Uint32(header[13:17]),
		Threads: header[17],
		KeyLen:  header[18],
	}
	if params.KeyLen != keyLen {
		return nil, fmt.Errorf("disguise: unsupported KeyLen %d (want %d)", params.KeyLen, keyLen)
	}
	salt := header[19 : 19+saltLen]
	nonce := header[19+saltLen : 19+saltLen+nonceLen]

	key := argon2.IDKey([]byte(pattern), salt, params.Time, params.Memory, params.Threads, uint32(params.KeyLen))
	defer zero(key)

	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("disguise: aead init: %w", err)
	}
	plaintext, err := aead.Open(nil, nonce, body, header)
	if err != nil {
		return nil, ErrPatternMismatch
	}
	return plaintext, nil
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("disguise: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(fileMode); err != nil {
		tmp.Close()
		return fmt.Errorf("disguise: chmod temp: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("disguise: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("disguise: close temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("disguise: rename: %w", err)
	}
	return nil
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
