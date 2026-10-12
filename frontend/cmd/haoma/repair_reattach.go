package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/invitation"
)

const (
	invitationWaitRetryDefault    = 2 * time.Second
	invitationWaitRetryMaxDefault = 5 * time.Minute
)

const invitationRetryWindowDefault = 15 * time.Minute

const invitationRetireTimeout = 30 * time.Second

func (d *daemon) attachInvitationWaiter(ctx context.Context, handleID, fingerprint, invitationID string) bool {
	if handleID == "" {
		return false
	}
	if _, running := d.invitationWaiters.LoadOrStore(handleID, invitationID); running {
		return false
	}
	go func() {
		defer d.invitationWaiters.Delete(handleID)
		d.awaitInvitationJoiner(ctx, handleID, fingerprint, invitationID)
	}()
	return true
}

func reattachInvitationWaiters(ctx context.Context, d *daemon) {
	if d == nil || d.invitations == nil || d.backendClient == nil {
		return
	}

	d.backendReadyAt.Store(time.Now().UnixNano())
	notes, err := d.invitations.ListNotes()
	if err != nil {
		slog.Warn("invitation: the invitations this device offered could not be read; no wait was attached again",
			slog.Any("err", err),
		)
		return
	}
	now := time.Now().Unix()
	attached := 0
	for _, n := range notes {
		logger := slog.With(slog.Any("note", n))
		if n.HandleID == "" || n.Fingerprint == "" {
			logger.Debug("invitation: note skipped — it names no rendezvous or no person")
			continue
		}
		if n.ExpiresAt <= now {

			logger.Debug("invitation: note skipped — the invitation has lapsed")
			continue
		}
		if reason, needed := reattachNeeded(ctx, d, n); !needed {

			if reason == reasonNotReady {
				logger.Debug("invitation: note skipped — the contact could not be read",
					slog.String("reason", reason),
				)
				continue
			}
			retireSkippedInvitation(ctx, d, logger, n.HandleID, n.Fingerprint, n.MsgID, reason)
			continue
		}
		if !d.attachInvitationWaiter(ctx, n.HandleID, n.Fingerprint, n.MsgID) {
			logger.Debug("invitation: note skipped — a wait for it is already running")
			continue
		}
		attached++
		slog.Info("invitation: the wait for the person was attached again",
			slog.String("invitation_id", n.MsgID),
			slog.String("handle_id", n.HandleID),
			slog.String("fingerprint", n.Fingerprint),
			slog.Int64("expires_at", n.ExpiresAt),
		)
	}
	slog.Debug("invitation: re-attach pass done",
		slog.Int("notes", len(notes)),
		slog.Int("attached", attached),
	)
}

func reattachNeeded(ctx context.Context, d *daemon, n invitation.Note) (reason string, needed bool) {
	_, plan, reason, _, ok := pairingOfferPlan(ctx, d, n.Fingerprint, true)
	if !ok && reason != reasonInvitationFromContact {
		return reason, false
	}
	if !stillMissing(plan, n.Device) {
		return reasonAlreadyPaired, false
	}
	return "", true
}

func (d *daemon) waitInvitationJoiner(ctx context.Context, logger *slog.Logger, handleID, fingerprint string) (backendapi.PairOnionWaitResponse, error) {
	pause := d.invitationWaitRetry
	if pause <= 0 {
		pause = invitationWaitRetryDefault
	}
	maxPause := d.invitationWaitRetryMax
	if maxPause <= 0 {
		maxPause = invitationWaitRetryMaxDefault
	}
	for try := 1; ; try++ {
		resp, err := d.backendClient.PairOnionWait(ctx, handleID)
		if err == nil {
			return resp, nil
		}
		if ctx.Err() != nil || !invitationWaitRetryable(err) {
			return resp, err
		}
		if invitationHandleUnknown(err) && !d.withinBackendReadyWindow(time.Now()) {
			logger.Debug("invitation: haomad does not know the handle, and the window after the last backend-ready transition has passed; the next transition attaches the wait again")
			return resp, err
		}
		if !invitationStillWaited(d, handleID, fingerprint, time.Now().Unix()) {
			logger.Debug("invitation: the wait is not tried again — the note no longer names this rendezvous, or it lapsed")
			return resp, err
		}
		logger.Debug("invitation: haomad could not take the wait yet; trying again",
			slog.Int("try", try),
			slog.Duration("pause", pause),
			slog.Any("err", err),
		)
		timer := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			timer.Stop()
			return resp, ctx.Err()
		case <-timer.C:
		}
		pause *= 2
		if pause > maxPause {
			pause = maxPause
		}
	}
}

func invitationWaitRetryable(err error) bool {
	var se *backendapi.StatusError
	if !errors.As(err, &se) {
		return true
	}
	switch se.StatusCode {
	case http.StatusNotFound, http.StatusRequestTimeout:
		return true
	default:
		return false
	}
}

func invitationStillWaited(d *daemon, handleID, fingerprint string, now int64) bool {
	if d == nil || d.invitations == nil {
		return false
	}
	note, found, err := noteForHandle(d, fingerprint, handleID)
	if err != nil || !found {
		return false
	}
	return note.ExpiresAt > now
}

func invitationHandleUnknown(err error) bool {
	var se *backendapi.StatusError
	return errors.As(err, &se) && se.StatusCode == http.StatusNotFound
}

func (d *daemon) withinBackendReadyWindow(now time.Time) bool {
	at := d.backendReadyAt.Load()
	if at == 0 {
		return false
	}
	window := d.invitationRetryWindow
	if window <= 0 {
		window = invitationRetryWindowDefault
	}
	return now.Sub(time.Unix(0, at)) < window
}

func retireSkippedInvitation(ctx context.Context, d *daemon, logger *slog.Logger, handleID, fingerprint, invitationID, reason string) {
	cancelCtx, cancel := context.WithTimeout(ctx, invitationRetireTimeout)
	defer cancel()
	if err := d.backendClient.PairOnionCancel(cancelCtx, handleID); err != nil {
		logger.Warn("invitation: an invitation that gets no wait was not cancelled; the next backend-ready transition tries again",
			slog.String("reason", reason),
			slog.Any("err", err),
		)
		return
	}
	state := events.InvitationFailed
	if reason == reasonAlreadyPaired {
		state = events.InvitationSuperseded
	}
	stampInvitationOutcome(d, logger, invitationID, state, invitationNoteDeadline(d, handleID, fingerprint), false)
	dropInvitationAttempt(d, logger, handleID, fingerprint)
	slog.Info("invitation: an invitation that gets no wait was taken back",
		slog.String("invitation_id", invitationID),
		slog.String("handle_id", handleID),
		slog.String("reason", reason),
		slog.String("state", state),
	)
}
