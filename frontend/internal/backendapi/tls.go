package backendapi

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

func PinnedTLSConfig(certPath string) (*tls.Config, error) {
	pemBytes, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("backendapi: read pinned cert %s: %w", certPath, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("backendapi: %s contains no PEM-encoded cert", certPath)
	}
	return &tls.Config{
		RootCAs:    pool,
		ServerName: "haoma-backend",
		MinVersion: tls.VersionTLS13,
	}, nil
}
