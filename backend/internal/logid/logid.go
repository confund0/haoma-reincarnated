package logid

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"regexp"
)

const shortPrefix = 8

func Short(s string) string {
	if len(s) <= shortPrefix {
		return s
	}
	return s[:shortPrefix] + "…"
}

var onionRe = regexp.MustCompile(`[a-z2-7]{56}\.onion`)

func RedactOnions(s string) string {
	return onionRe.ReplaceAllStringFunc(s, func(m string) string {
		return m[:shortPrefix] + "…" + ".onion"
	})
}

const OnionWithheld = "<address>"

func WithholdOnions(s string) string {
	return onionRe.ReplaceAllString(s, OnionWithheld)
}

func HasOnion(s string) bool {
	return onionRe.MatchString(s)
}

var longTokenRe = regexp.MustCompile(`[A-Za-z0-9_\-]{12,}`)

func Hash(s string) string {
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return "h:" + hex.EncodeToString(sum[:4])
}

func RedactURLTokens(s string) string {
	s = RedactOnions(s)
	return longTokenRe.ReplaceAllStringFunc(s, func(m string) string {
		if len(m) <= shortPrefix {
			return m
		}
		return m[:shortPrefix] + "…"
	})
}

func RedactURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		ue.URL = RedactURLTokens(ue.URL)
	}
	return err
}
