package main

import (
	"errors"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"sync/atomic"
	"time"

	"haoma/internal/eventbus"
	"haoma/internal/wipe"
)

const wipeClientGrace = 2 * time.Second

var wipeClientGraceForTest = wipeClientGrace

type wipeAnnouncement struct {
	Reason  string `json:"reason"`
	EraseID string `json:"erase_id,omitempty"`
}

func newEraseID() string {
	id, err := newEnvelopeID()
	if err != nil {
		slog.Warn("wipe: could not mint an erase id; the erasure runs without one",
			slog.Any("err", err),
		)
		return ""
	}
	return id
}

type wipeResponse struct {
	Started bool `json:"started"`
}

func (d *daemon) handleWipe(w http.ResponseWriter, _ *http.Request) {
	if d.cfg.dataRoot == "" {

		slog.Warn("wipe: refused, this daemon was not told its data root")
		writeErr(w, http.StatusPreconditionFailed,
			errors.New("this daemon was not started with a data root, so it cannot erase the device"))
		return
	}

	eraseID := newEraseID()
	slog.Info("wipe: erasing this device",
		slog.String("reason", "the operator asked"),
		slog.String("erase_id", eraseID),
	)
	writeJSON(w, http.StatusOK, wipeResponse{Started: true})

	go d.eraseAndExit("the operator asked", eraseID)
}

const eraseShutdownTimeout = 30 * time.Second

func (d *daemon) eraseAndExit(reason, eraseID string) {
	first := false
	d.eraseOnce.Do(func() { first = true })
	if !first {
		slog.Info("wipe: refused, an erasure is already running",
			slog.String("reason", reason),
			slog.String("erase_id", eraseID),
		)
		return
	}
	d.eraseNow(reason, eraseID)
}

func (d *daemon) eraseNow(reason, eraseID string) {

	d.announceWipe(reason, eraseID)
	time.Sleep(wipeClientGraceForTest)

	d.stopAndWait(reason, eraseID)

	if eraseRoots(d.cfg.dataRoot, runtime.GOOS, eraseID) {
		slog.Info("wipe: this device is erased", slog.String("erase_id", eraseID))
	}
	exitAfterWipe(0)
}

func eraseRoots(dataRoot, goos, eraseID string) bool {
	roots, err := wipe.Roots(dataRoot, goos)
	if err != nil {
		slog.Error("wipe: the app's directories cannot be worked out; erasing the data root alone",
			slog.String("erase_id", eraseID),
			slog.Any("err", err),
		)
		roots = []wipe.Root{{Path: dataRoot}}
	}

	all := err == nil
	for _, r := range roots {
		slog.Debug("wipe: emptying a directory",
			slog.String("erase_id", eraseID),
			slog.String("root", r.Path),
			slog.Any("kept", r.Keep),
		)
		if rootErr := wipe.Device(r.Path, r.Keep...); rootErr != nil {
			all = false
			slog.Error("wipe: a directory did not empty fully",
				slog.String("erase_id", eraseID),
				slog.String("root", r.Path),
				slog.Any("err", rootErr),
			)
			continue
		}
		slog.Info("wipe: a directory is empty",
			slog.String("erase_id", eraseID),
			slog.String("root", r.Path),
		)
	}
	return all
}

func (d *daemon) stopAndWait(reason, eraseID string) {
	if d.stopRun == nil || d.runDone == nil {

		return
	}
	slog.Info("wipe: stopping this daemon before the files go",
		slog.String("reason", reason),
		slog.String("erase_id", eraseID),
	)

	erasePending.Store(true)
	d.stopRun()
	select {
	case <-d.runDone:
		slog.Info("wipe: this daemon has stopped; erasing",
			slog.String("erase_id", eraseID),
		)
	case <-time.After(eraseShutdownTimeoutForTest):
		slog.Warn("wipe: the shutdown did not finish; erasing anyway",
			slog.String("erase_id", eraseID),
			slog.Duration("waited", eraseShutdownTimeoutForTest),
		)
	}
}

var eraseShutdownTimeoutForTest = eraseShutdownTimeout

var erasePending atomic.Bool

func awaitErase() {
	if !erasePending.Load() {
		return
	}
	slog.Info("wipe: shutdown finished; the erasure ends this process")
	<-make(chan struct{})
}

var exitAfterWipe = func(code int) { os.Exit(code) }

func (d *daemon) announceWipe(reason, eraseID string) {
	if d.bus == nil {
		return
	}
	told, dropped := d.bus.Publish(eventbus.TopicDestroyDevice,
		wipeAnnouncement{Reason: reason, EraseID: eraseID})
	if dropped > 0 {
		slog.Warn("wipe: a client was not told to stop",
			slog.String("reason", reason),
			slog.String("erase_id", eraseID),
			slog.Int("told", told),
			slog.Int("dropped", dropped),
		)
		return
	}
	slog.Info("wipe: attached clients told to stop",
		slog.String("reason", reason),
		slog.String("erase_id", eraseID),
		slog.Int("told", told),
	)
}
