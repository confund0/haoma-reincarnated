package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"haoma/internal/logid"
	"haoma/internal/store"
)

const (
	destroyPINRefusalAlreadySet = "already_set"
)

type destroyPINStateResponse struct {
	Set   bool   `json:"set"`
	Salt  string `json:"salt,omitempty"`
	SetAt int64  `json:"set_at,omitempty"`

	SiblingPeerID string `json:"sibling_peer_id,omitempty"`
}

type destroyPINSetRequest struct {
	Salt     string `json:"salt"`
	Verifier string `json:"verifier"`

	SiblingPeerID string `json:"sibling_peer_id"`
}

type destroyPINSetResponse struct {
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason,omitempty"`
}

func (d *daemon) handleDestroyPINState(w http.ResponseWriter, _ *http.Request) {
	rec, err := d.store.GetDestroyPIN()
	if err != nil {
		slog.Warn("destroy pin: state read failed", slog.Any("err", err))
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := destroyPINStateResponse{Set: rec.Set, SetAt: rec.SetAt}
	if rec.Set {
		out.Salt = hex.EncodeToString(rec.Salt)
		out.SiblingPeerID = rec.SiblingPeerID
	}
	slog.Debug("destroy pin: state answered",
		slog.Bool("set", out.Set),
		slog.Int64("set_at", out.SetAt),
	)
	writeJSON(w, http.StatusOK, out)
}

func (d *daemon) handleDestroyPINSet(w http.ResponseWriter, r *http.Request) {
	var body destroyPINSetRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("decode body: %w", err))
		return
	}
	salt, err := hex.DecodeString(body.Salt)
	if err != nil || len(salt) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("salt must be a non-empty hex string"))
		return
	}
	verifier, err := hex.DecodeString(body.Verifier)
	if err != nil || len(verifier) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("verifier must be a non-empty hex string"))
		return
	}
	if body.SiblingPeerID == "" {
		writeErr(w, http.StatusBadRequest, errors.New("sibling_peer_id is required"))
		return
	}
	if _, err := d.registry.Get(body.SiblingPeerID); err != nil {

		writeErr(w, http.StatusBadRequest, fmt.Errorf("sibling_peer_id %q is not a known peer: %w", logid.Short(body.SiblingPeerID), err))
		return
	}
	current, err := d.store.GetDestroyPIN()
	if err != nil {
		slog.Warn("destroy pin: set could not read the current guard", slog.Any("err", err))
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	slog.Debug("destroy pin: set observed",
		slog.Bool("guard_already_set", current.Set),
	)

	if current.Set {
		slog.Info("destroy pin: set refused",
			slog.String("reason", destroyPINRefusalAlreadySet),
		)
		writeJSON(w, http.StatusOK, destroyPINSetResponse{Reason: destroyPINRefusalAlreadySet})
		return
	}

	setAt := time.Now().Unix()
	if err := d.store.PutDestroyPIN(store.DestroyPIN{
		Salt:          salt,
		Verifier:      verifier,
		SiblingPeerID: body.SiblingPeerID,
		SetAt:         setAt,
	}); err != nil {

		if errors.Is(err, store.ErrDestroyPINAlreadySet) {
			slog.Info("destroy pin: set refused",
				slog.String("reason", destroyPINRefusalAlreadySet),
			)
			writeJSON(w, http.StatusOK, destroyPINSetResponse{Reason: destroyPINRefusalAlreadySet})
			return
		}
		slog.Warn("destroy pin: write failed", slog.Any("err", err))
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	slog.Info("destroy pin: guard stored", slog.Int64("set_at", setAt))
	writeJSON(w, http.StatusOK, destroyPINSetResponse{Accepted: true})
}

type destroyAttemptsDrainResponse struct {
	Count  int   `json:"count"`
	LastAt int64 `json:"last_at,omitempty"`
}

func (d *daemon) handleDestroyAttemptsDrain(w http.ResponseWriter, _ *http.Request) {
	count, lastAt, err := d.store.DrainDestroyAttemptNotices()
	if err != nil {
		slog.Warn("destroy pin: draining the attempt notices failed", slog.Any("err", err))
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if count > 0 {
		slog.Info("destroy pin: attempt notices handed to the client",
			slog.Int("count", count),
			slog.Int64("last_at", lastAt),
		)
	}
	writeJSON(w, http.StatusOK, destroyAttemptsDrainResponse{Count: count, LastAt: lastAt})
}

func (d *daemon) handleDestroyPINClear(w http.ResponseWriter, _ *http.Request) {
	if err := d.store.ClearDestroyPIN(); err != nil {
		slog.Warn("destroy pin: clear failed", slog.Any("err", err))
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	slog.Info("destroy pin: guard cleared")
	writeJSON(w, http.StatusOK, destroyPINSetResponse{Accepted: true})
}
