package control

import (
	"errors"
	"fmt"
	"log/slog"
)

type AuthMethod string

const (
	MethodSafeCookie     AuthMethod = "SAFECOOKIE"
	MethodCookie         AuthMethod = "COOKIE"
	MethodHashedPassword AuthMethod = "HASHEDPASSWORD"
	MethodNull           AuthMethod = "NULL"
)

func (c *Conn) Authenticate(password string) (AuthMethod, error) {
	info, err := c.ProtocolInfo()
	if err != nil {
		return "", fmt.Errorf("control: PROTOCOLINFO: %w", err)
	}

	var downgrades []authDowngrade

	if info.Has("SAFECOOKIE") && info.CookieFile != "" {
		err := c.AuthSafeCookie(info.CookieFile)
		if err == nil {
			return MethodSafeCookie, nil
		}
		if !canFallThrough(info, password) {
			return "", fmt.Errorf("control: SAFECOOKIE: %w", err)
		}
		downgrades = append(downgrades, authDowngrade{MethodSafeCookie, err})
	}
	if info.Has("COOKIE") && info.CookieFile != "" {
		err := c.AuthCookie(info.CookieFile)
		if err == nil {
			logDowngrade(MethodCookie, downgrades)
			return MethodCookie, nil
		}
		if !canFallThrough(info, password) {
			return "", fmt.Errorf("control: COOKIE: %w", err)
		}
		downgrades = append(downgrades, authDowngrade{MethodCookie, err})
	}
	if info.Has("HASHEDPASSWORD") {
		if password == "" {
			return "", errors.New("control: tor requires HASHEDPASSWORD but no password is configured")
		}
		if err := c.AuthPassword(password); err != nil {
			return "", fmt.Errorf("control: HASHEDPASSWORD: %w", err)
		}
		logDowngrade(MethodHashedPassword, downgrades)
		return MethodHashedPassword, nil
	}
	if info.Has("NULL") {
		if err := c.AuthNull(); err != nil {
			return "", fmt.Errorf("control: NULL auth: %w", err)
		}
		logDowngrade(MethodNull, downgrades)
		return MethodNull, nil
	}
	return "", fmt.Errorf("control: no usable auth method (offered: %v)", info.Methods)
}

type authDowngrade struct {
	method AuthMethod
	err    error
}

func logDowngrade(picked AuthMethod, swallowed []authDowngrade) {
	for _, d := range swallowed {
		slog.Info("tor control: auth method downgraded",
			slog.String("failed_method", string(d.method)),
			slog.String("failed_reason", d.err.Error()),
			slog.String("picked_method", string(picked)),
		)
	}
}

func canFallThrough(info *ProtocolInfo, password string) bool {
	return (info.Has("HASHEDPASSWORD") && password != "") || info.Has("NULL")
}
