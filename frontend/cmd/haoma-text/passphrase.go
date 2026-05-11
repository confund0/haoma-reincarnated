package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

const envPassphraseFile = "HAOMA_VAULT_PASSPHRASE_FILE"

func promptPassphrase(label string) (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, label)
		raw, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", fmt.Errorf("read passphrase: %w", err)
		}
		return strings.TrimRight(string(raw), "\r\n"), nil
	}

	path := os.Getenv(envPassphraseFile)
	if path == "" {
		return "", fmt.Errorf("stdin is not a TTY and %s is unset; cannot prompt for passphrase", envPassphraseFile)
	}
	fmt.Fprintf(os.Stderr, "haoma-text: stdin is not a TTY; reading passphrase from %s=%s\n",
		envPassphraseFile, path)
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s=%s: %w", envPassphraseFile, path, err)
	}
	return strings.TrimSpace(string(raw)), nil
}

func promptCreatePassphrase() (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {

		return promptPassphrase("Set master passphrase (from " + envPassphraseFile + "): ")
	}
	first, err := promptPassphrase("Set master passphrase (empty = insecure default): ")
	if err != nil {
		return "", err
	}
	second, err := promptPassphrase("Confirm passphrase: ")
	if err != nil {
		return "", err
	}
	if first != second {
		return "", errors.New("passphrases did not match")
	}
	return first, nil
}
