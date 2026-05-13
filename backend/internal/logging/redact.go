package logging

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"haoma/internal/logid"
)

var sensitiveStringKeys = map[string]bool{
	"onion":       true,
	"dest":        true,
	"token":       true,
	"fingerprint": true,
}

var urlStringKeys = map[string]bool{
	"peer_url": true,
}

var opaqueStringKeys = map[string]bool{
	"nick":        true,
	"sender_nick": true,
}

type redactHandler struct {
	inner slog.Handler
}

func newRedactHandler(inner slog.Handler) *redactHandler {
	return &redactHandler{inner: inner}
}

func (h *redactHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *redactHandler) Handle(ctx context.Context, r slog.Record) error {
	nr := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		nr.AddAttrs(redactAttr(a))
		return true
	})
	return h.inner.Handle(ctx, nr)
}

func (h *redactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		redacted[i] = redactAttr(a)
	}
	return &redactHandler{inner: h.inner.WithAttrs(redacted)}
}

func (h *redactHandler) WithGroup(name string) slog.Handler {
	return &redactHandler{inner: h.inner.WithGroup(name)}
}

func redactAttr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	if v.Kind() == slog.KindString && (sensitiveStringKeys[a.Key] || strings.HasSuffix(a.Key, "_id")) {
		return slog.String(a.Key, logid.Short(v.String()))
	}
	if opaqueStringKeys[a.Key] && v.Kind() == slog.KindString {
		return slog.String(a.Key, logid.Hash(v.String()))
	}
	if urlStringKeys[a.Key] && v.Kind() == slog.KindString {
		return slog.String(a.Key, logid.RedactURLTokens(v.String()))
	}
	if a.Key == "err" {
		var s string
		switch v.Kind() {
		case slog.KindString:
			s = v.String()
		default:
			s = fmt.Sprintf("%v", v.Any())
		}
		if logid.HasOnion(s) {
			return slog.String("err", logid.RedactOnions(s))
		}
	}
	return a
}
