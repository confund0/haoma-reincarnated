package main

import (
	"bytes"
	"crypto/subtle"
	"errors"
	"fmt"
	"os"

	"golang.org/x/term"
)

const envPassphraseFile = "HAOMA_VAULT_PASSPHRASE_FILE"

func promptPassphrase(label string) ([]byte, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, label)
		raw, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return nil, fmt.Errorf("read passphrase: %w", err)
		}
		return bytes.TrimRight(raw, "\r\n"), nil
	}

	path := os.Getenv(envPassphraseFile)
	if path == "" {
		return nil, fmt.Errorf("stdin is not a TTY and %s is unset; cannot prompt for passphrase", envPassphraseFile)
	}
	fmt.Fprintf(os.Stderr, "haoma-text: stdin is not a TTY; reading passphrase from %s=%s\n",
		envPassphraseFile, path)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s=%s: %w", envPassphraseFile, path, err)
	}
	return bytes.TrimSpace(raw), nil
}

func promptCreatePassphrase() ([]byte, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {

		return promptPassphrase("Set master passphrase (from " + envPassphraseFile + "): ")
	}
	first, err := promptPassphrase("Set master passphrase (empty = insecure default): ")
	if err != nil {
		return nil, err
	}
	second, err := promptPassphrase("Confirm passphrase: ")
	if err != nil {

		clear(first)
		return nil, err
	}

	if subtle.ConstantTimeCompare(first, second) != 1 {
		clear(first)
		clear(second)
		return nil, errors.New("passphrases did not match")
	}
	clear(second)
	return first, nil
}
