package main

import (
	"log/slog"

	"haoma-frontend/internal/logid"
)

const detailWithheld = "the daemon reported a failure; the detail stays on the daemon"

func clientDetail(code, detail string) string {
	if !logid.HasOnion(detail) {
		return detail
	}
	slog.Warn("ipc: a failure detail naming an address was withheld from the client",
		slog.String("code", code),
	)
	return detailWithheld
}
