package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"haoma-frontend/internal/ipc"
)

const (
	vidI420Width         = 480
	vidI420Height        = 640
	vidI420BytesPerFrame = vidI420Width * vidI420Height * 3 / 2
	vidPtsHeaderBytes    = 8
)

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

func (a *App) routeVideoRawTransport(f ipc.Frame) {
	var p ipc.CallStreamRawTransportPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		slog.Warn("decode call.stream-raw-transport", slog.Any("err", err))
		return
	}
	if p.Side != "vid" || p.RawUnix == "" {
		return
	}
	if a.DataDir == "" {
		a.log("[red]vid sink[white] no DataDir — fifo write disabled")
		return
	}

	a.closeVideoSink(p.CallID)

	fifoPath := filepath.Join(a.DataDir, "vid-"+p.CallID+".yuv")
	_ = os.Remove(fifoPath)
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil && !errors.Is(err, syscall.EEXIST) {
		a.log("[red]vid sink[white] mkfifo %s: %v", fifoPath, err)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	a.winMu.Lock()
	a.videoSinks[p.CallID] = cancel
	a.winMu.Unlock()

	go a.runVideoSink(ctx, p.CallID, p.RawUnix, fifoPath)

	if autoSpawnFFplayEnabled() {
		a.log("[gray]vid sink[white] %s → %s (auto-launching ffplay)", shortCallID(p.CallID), fifoPath)
	} else {
		a.log("[gray]vid sink[white] %s → %s (open with: ffplay -f rawvideo -pixel_format yuv420p -video_size %dx%d -framerate 15 -i %s)",
			shortCallID(p.CallID), fifoPath, vidI420Width, vidI420Height, fifoPath)
	}
}

func autoSpawnFFplayEnabled() bool {
	if os.Getenv("HAOMA_NO_AUTO_FFPLAY") != "" {
		return false
	}
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return false
	}
	if _, err := exec.LookPath("ffplay"); err != nil {
		return false
	}
	return true
}

func spawnFFplayStdin(callID string) (*exec.Cmd, io.WriteCloser) {
	cmd := exec.Command("ffplay",
		"-loglevel", "warning",
		"-autoexit",
		"-f", "rawvideo",
		"-pixel_format", "yuv420p",
		"-video_size", fmt.Sprintf("%dx%d", vidI420Width, vidI420Height),
		"-framerate", "15",
		"-window_title", "haoma vid "+shortCallID(callID),
		"-i", "pipe:0",
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		slog.Debug("vid sink: ffplay stdin pipe", slog.String("call_id", callID), slog.Any("err", err))
		return nil, nil
	}
	if err := cmd.Start(); err != nil {
		slog.Debug("vid sink: ffplay spawn failed", slog.String("call_id", callID), slog.Any("err", err))
		_ = stdin.Close()
		return nil, nil
	}
	return cmd, stdin
}

func (a *App) runVideoSink(ctx context.Context, callID, rawUnix, fifoPath string) {
	auto := autoSpawnFFplayEnabled()

	var (
		ffplayCmd   *exec.Cmd
		ffplayStdin io.WriteCloser
		fifo        *os.File
	)
	if auto {
		ffplayCmd, ffplayStdin = spawnFFplayStdin(callID)
	}
	defer func() {
		if ffplayStdin != nil {
			_ = ffplayStdin.Close()
		}
		if ffplayCmd != nil && ffplayCmd.Process != nil {
			_ = ffplayCmd.Process.Kill()
			_, _ = ffplayCmd.Process.Wait()
		}
		if fifo != nil {
			_ = fifo.Close()
		}
		_ = os.Remove(fifoPath)
	}()

	conn, err := net.Dial("unix", "@"+rawUnix)
	if err != nil {
		a.log("[red]vid sink[white] dial @%s: %v", rawUnix, err)
		return
	}
	defer conn.Close()

	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	hdr := make([]byte, vidPtsHeaderBytes)
	buf := make([]byte, vidI420BytesPerFrame)

	for {
		if _, err := io.ReadFull(conn, hdr); err != nil {
			if ctx.Err() == nil {
				slog.Debug("vid sink: read pts", slog.String("call_id", callID), slog.Any("err", err))
			}
			return
		}
		if _, err := io.ReadFull(conn, buf); err != nil {
			if ctx.Err() == nil {
				slog.Debug("vid sink: read i420", slog.String("call_id", callID), slog.Any("err", err))
			}
			return
		}

		if ffplayStdin != nil {
			if _, err := ffplayStdin.Write(buf); err != nil {
				slog.Debug("vid sink: ffplay stdin write", slog.String("call_id", callID), slog.Any("err", err))
				_ = ffplayStdin.Close()
				ffplayStdin = nil
			}
		}

		if !auto {
			if fifo == nil {
				fd, err := os.OpenFile(fifoPath, os.O_WRONLY|syscall.O_NONBLOCK, 0)
				if err != nil {
					continue
				}
				fifo = fd
			}
			if _, err := fifo.Write(buf); err != nil {
				if errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.EAGAIN) {
					_ = fifo.Close()
					fifo = nil
					continue
				}
				slog.Debug("vid sink: fifo write", slog.String("call_id", callID), slog.Any("err", err))
				return
			}
		}
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
