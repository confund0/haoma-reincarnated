package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	certFilename = "cert.pem"
	keyFilename  = "cert.key"
)

func LoadOrCreateTLS(dir string) (*tls.Config, error) {
	certPath := filepath.Join(dir, certFilename)
	keyPath := filepath.Join(dir, keyFilename)

	_, certErr := os.Stat(certPath)
	_, keyErr := os.Stat(keyPath)
	certExists := certErr == nil
	keyExists := keyErr == nil

	if certExists != keyExists {
		return nil, fmt.Errorf("auth: TLS pair mismatch — cert=%v key=%v", certExists, keyExists)
	}

	if !certExists {
		if err := generateTLS(certPath, keyPath); err != nil {
			return nil, err
		}
	}

	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("auth: load TLS keypair: %w", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{pair},
		MinVersion:   tls.VersionTLS13,
	}, nil
}

func CertPath(dir string) string {
	return filepath.Join(dir, certFilename)
}

func KeyPath(dir string) string {
	return filepath.Join(dir, keyFilename)
}

func PinnedClientTLSConfig(certPath string) (*tls.Config, error) {
	pemBytes, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("auth: read pinned cert %s: %w", certPath, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("auth: %s contains no PEM-encoded cert", certPath)
	}
	return &tls.Config{
		RootCAs:    pool,
		ServerName: "haoma-backend",
		MinVersion: tls.VersionTLS13,
	}, nil
}

func generateTLS(certPath, keyPath string) error {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("auth: generate ECDSA key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("auth: generate serial: %w", err)
	}

	notBefore := time.Now()
	notAfter := notBefore.AddDate(10, 0, 0)

	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "haoma-backend",
			Organization: []string{"Haoma"},
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost", "haoma-backend"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		return fmt.Errorf("auth: create cert: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return fmt.Errorf("auth: write cert: %w", err)
	}

	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return fmt.Errorf("auth: marshal private key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
	if err := writeSensitiveFile(keyPath, keyPEM); err != nil {
		return fmt.Errorf("auth: write private key: %w", err)
	}
	return nil
}

var ErrTLSIncomplete = errors.New("auth: TLS pair mismatch")
