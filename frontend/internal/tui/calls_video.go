package tui

import (
	"os"
	"path/filepath"
)

const videoUnavailableMessage = "video calls are not available on Windows"

func (a *App) SweepVideoFifos() {
	if a.DataDir == "" {
		return
	}
	matches, err := filepath.Glob(filepath.Join(a.DataDir, "vid-*.yuv"))
	if err != nil {
		return
	}
	for _, m := range matches {
		_ = os.Remove(m)
	}
}

func (a *App) closeVideoSink(callID string) {
	a.winMu.Lock()
	cancel, ok := a.videoSinks[callID]
	if ok {
		delete(a.videoSinks, callID)
	}
	a.winMu.Unlock()
	if cancel != nil {
		cancel()
	}
}
