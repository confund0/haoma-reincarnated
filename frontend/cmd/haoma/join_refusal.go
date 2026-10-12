package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/ipc"
)

func joinRefusal(err error, dialFailed string) string {
	var se *backendapi.StatusError
	if errors.As(err, &se) {
		switch se.StatusCode {
		case http.StatusUnprocessableEntity:
			return ipc.ReasonJoinWordsInvalid
		case http.StatusGone:
			return ipc.ReasonJoinInvitationSpent
		}
	}
	return dialFailed
}

const joinReleaseTimeout = 30 * time.Second

func releaseJoinOnion(d *daemon, logger *slog.Logger, address string) {
	ctx, cancel := context.WithTimeout(context.Background(), joinReleaseTimeout)
	defer cancel()
	releaseInvitationOnion(ctx, d, logger, address)
}
