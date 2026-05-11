package secrets

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const MaxBlobSize = 64 * 1024

type Secrets struct {
	HaomadStorePassphrase   string `json:"haomad_store_passphrase"`
	FrontendStorePassphrase string `json:"frontend_store_passphrase"`
	HaomadToken             string `json:"haomad_token"`
	TorPassword             string `json:"tor_password,omitempty"`
	HaomadURL               string `json:"haomad_url,omitempty"`
	IdleTimeoutSeconds      int    `json:"idle_timeout_seconds,omitempty"`
}

func Parse(r io.Reader) (Secrets, error) {
	b, err := io.ReadAll(io.LimitReader(r, MaxBlobSize))
	if err != nil {
		return Secrets{}, fmt.Errorf("secrets: read: %w", err)
	}
	if len(b) == 0 {
		return Secrets{}, errors.New("secrets: empty input")
	}
	if int64(len(b)) >= int64(MaxBlobSize) {
		return Secrets{}, fmt.Errorf("secrets: input exceeds %d bytes", MaxBlobSize)
	}
	var s Secrets
	if err := json.Unmarshal(b, &s); err != nil {
		return Secrets{}, fmt.Errorf("secrets: decode: %w", err)
	}
	return s, nil
}

func (s Secrets) Validate() error {
	if s.HaomadStorePassphrase == "" {
		return errors.New("secrets: haomad_store_passphrase is empty")
	}
	if s.FrontendStorePassphrase == "" {
		return errors.New("secrets: frontend_store_passphrase is empty")
	}
	if s.HaomadToken == "" {
		return errors.New("secrets: haomad_token is empty")
	}
	return nil
}

func (s Secrets) Marshal() ([]byte, error) {
	return json.Marshal(s)
}
