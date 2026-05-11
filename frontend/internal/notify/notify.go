package notify

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf16"
)

type Privacy struct {
	ShellEnabled bool
	ShowSender   bool
	ShowBody     bool
}

type Event struct {
	ChatID    string
	PeerLabel string
	Body      string
}

type Runner interface {
	Run(ctx context.Context, name string, args ...string) (output string, err error)
}

type LookPath func(name string) (string, error)

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

type Backend interface {
	Send(ctx context.Context, ev Event, title, body string) error
	Dismiss(ctx context.Context, chatID string) error
	Name() string
}

type Dispatcher struct {
	backend  atomic.Pointer[backendHolder]
	runner   Runner
	lookPath LookPath
	once     sync.Once
	osName   string
}

type backendHolder struct {
	b Backend
}

func New(runner Runner, lookPath LookPath, osName string) *Dispatcher {
	if runner == nil {
		runner = execRunner{}
	}
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if osName == "" {
		osName = runtime.GOOS
	}
	return &Dispatcher{
		runner:   runner,
		lookPath: lookPath,
		osName:   osName,
	}
}

type Decision struct {
	Title          string
	Body           string
	RedactedSender bool
	RedactedBody   bool
	Fired          bool
}

func (d *Dispatcher) Notify(ctx context.Context, ev Event, priv Privacy) Decision {
	if !priv.ShellEnabled {
		return Decision{}
	}
	title, body, redactedSender, redactedBody := renderBanner(ev, priv)
	go func() {
		b := d.ensureBackend()
		if b == nil {
			return
		}
		if err := b.Send(ctx, ev, title, body); err != nil {
			slog.Debug("notify send failed",
				slog.String("backend", b.Name()),
				slog.String("chat_id", ev.ChatID),
				slog.Any("err", err),
			)
		}
	}()
	return Decision{
		Title:          title,
		Body:           body,
		RedactedSender: redactedSender,
		RedactedBody:   redactedBody,
		Fired:          true,
	}
}

func (d *Dispatcher) Dismiss(ctx context.Context, chatID string) {
	if chatID == "" {
		return
	}
	b := d.ensureBackend()
	if b == nil {
		return
	}
	if err := b.Dismiss(ctx, chatID); err != nil {
		slog.Debug("notify dismiss failed",
			slog.String("backend", b.Name()),
			slog.String("chat_id", chatID),
			slog.Any("err", err),
		)
	}
}

func (d *Dispatcher) ensureBackend() Backend {
	d.once.Do(func() {
		b := detect(d.osName, d.runner, d.lookPath)
		if b != nil {
			d.backend.Store(&backendHolder{b: b})
			slog.Debug("notify backend detected", slog.String("name", b.Name()))
		} else {
			slog.Debug("notify: no backend detected — silently disabled",
				slog.String("os", d.osName),
			)
		}
	})
	if h := d.backend.Load(); h != nil {
		return h.b
	}
	return nil
}

func detect(osName string, runner Runner, lookPath LookPath) Backend {
	switch osName {
	case "android":
		if path, err := lookPath("termux-notification"); err == nil {
			return &termuxBackend{bin: path, runner: runner}
		}
	case "linux":
		if path, err := lookPath("notify-send"); err == nil {
			gdbus, _ := lookPath("gdbus")
			return &linuxBackend{
				bin:     path,
				gdbus:   gdbus,
				runner:  runner,
				liveIDs: make(map[string]uint32),
			}
		}
	case "darwin":
		if path, err := lookPath("osascript"); err == nil {
			return &darwinBackend{bin: path, runner: runner}
		}
	case "windows":

		if path, err := lookPath("powershell"); err == nil {
			return &windowsBackend{bin: path, runner: runner}
		}
		if path, err := lookPath("pwsh"); err == nil {
			return &windowsBackend{bin: path, runner: runner}
		}
	}
	return nil
}

func renderBanner(ev Event, priv Privacy) (title, body string, redactedSender, redactedBody bool) {
	if priv.ShowSender && ev.PeerLabel != "" {
		title = ev.PeerLabel
	} else {
		title = "Haoma"
		redactedSender = true
	}
	if priv.ShowBody && ev.Body != "" {
		body = ev.Body
	} else {
		body = "New message"
		redactedBody = true
	}
	if len(title) > 25 {

		runes := []rune(title)
		if len(runes) > 25 {
			runes = runes[:25]
		}
		title = string(runes)
	}
	return title, body, redactedSender, redactedBody
}

type linuxBackend struct {
	bin     string
	gdbus   string
	runner  Runner
	mu      sync.Mutex
	liveIDs map[string]uint32
}

func (b *linuxBackend) Name() string { return "notify-send" }

func (b *linuxBackend) Send(ctx context.Context, ev Event, title, body string) error {
	args := []string{
		"-p",
		"-a", "Haoma",
	}
	b.mu.Lock()
	prev, hasPrev := b.liveIDs[ev.ChatID]
	b.mu.Unlock()
	if hasPrev {
		args = append(args, "-r", strconv.FormatUint(uint64(prev), 10))
	}
	args = append(args, title, body)
	out, err := b.runner.Run(ctx, b.bin, args...)
	if err != nil {
		return err
	}
	id, parseErr := strconv.ParseUint(strings.TrimSpace(out), 10, 32)
	if parseErr != nil {

		return nil
	}
	b.mu.Lock()
	b.liveIDs[ev.ChatID] = uint32(id)
	b.mu.Unlock()
	return nil
}

func (b *linuxBackend) Dismiss(ctx context.Context, chatID string) error {
	b.mu.Lock()
	id, ok := b.liveIDs[chatID]
	if ok {
		delete(b.liveIDs, chatID)
	}
	b.mu.Unlock()
	if !ok {
		return nil
	}
	if b.gdbus != "" {
		_, err := b.runner.Run(ctx, b.gdbus,
			"call", "--session",
			"--dest", "org.freedesktop.Notifications",
			"--object-path", "/org/freedesktop/Notifications",
			"--method", "org.freedesktop.Notifications.CloseNotification",
			strconv.FormatUint(uint64(id), 10),
		)
		if err == nil {
			return nil
		}
		slog.Debug("notify gdbus close failed; falling back to notify-send -r",
			slog.String("chat_id", chatID),
			slog.Any("err", err),
		)
	}

	_, err := b.runner.Run(ctx, b.bin,
		"-r", strconv.FormatUint(uint64(id), 10),
		"-t", "1",
		" ", " ",
	)
	return err
}

type termuxBackend struct {
	bin    string
	runner Runner
}

func (b *termuxBackend) Name() string { return "termux-notification" }

func (b *termuxBackend) Send(ctx context.Context, ev Event, title, body string) error {
	args := []string{
		"--title", title,
		"--content", body,
		"--id", "haoma-" + ev.ChatID,
		"--group", "haoma",
	}
	_, err := b.runner.Run(ctx, b.bin, args...)
	return err
}

func (b *termuxBackend) Dismiss(ctx context.Context, chatID string) error {
	if chatID == "" {
		return nil
	}
	bin := strings.TrimSuffix(b.bin, "termux-notification") + "termux-notification-remove"
	_, err := b.runner.Run(ctx, bin, "haoma-"+chatID)
	if err != nil {

		return nil
	}
	return nil
}

type darwinBackend struct {
	bin    string
	runner Runner
}

func (b *darwinBackend) Name() string { return "osascript" }

func (b *darwinBackend) Send(ctx context.Context, _ Event, title, body string) error {
	script := fmt.Sprintf(`display notification %q with title %q`, body, title)
	_, err := b.runner.Run(ctx, b.bin, "-e", script)
	return err
}

func (b *darwinBackend) Dismiss(_ context.Context, _ string) error {

	return nil
}

type windowsBackend struct {
	bin    string
	runner Runner
}

func (b *windowsBackend) Name() string { return "powershell-toast" }

func (b *windowsBackend) Send(ctx context.Context, ev Event, title, body string) error {
	tag := "haoma-" + ev.ChatID
	script := buildToastScript(tag, "haoma", title, body)
	_, err := b.runner.Run(ctx, b.bin, "-NoProfile", "-NonInteractive", "-EncodedCommand", encodePowerShell(script))
	return err
}

func (b *windowsBackend) Dismiss(ctx context.Context, chatID string) error {
	if chatID == "" {
		return nil
	}
	tag := "haoma-" + chatID
	script := buildDismissScript(tag, "haoma")
	_, err := b.runner.Run(ctx, b.bin, "-NoProfile", "-NonInteractive", "-EncodedCommand", encodePowerShell(script))
	if err != nil {

		return nil
	}
	return nil
}

func buildToastScript(tag, group, title, body string) string {
	var b strings.Builder
	b.WriteString(`[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType=WindowsRuntime] | Out-Null` + "\n")
	b.WriteString(`[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType=WindowsRuntime] | Out-Null` + "\n")
	b.WriteString(`$xml = [Windows.Data.Xml.Dom.XmlDocument]::new()` + "\n")
	b.WriteString(`$xml.LoadXml(@"` + "\n")
	b.WriteString(`<toast><visual><binding template="ToastGeneric"><text>`)
	b.WriteString(escapeXML(title))
	b.WriteString(`</text><text>`)
	b.WriteString(escapeXML(body))
	b.WriteString(`</text></binding></visual></toast>`)
	b.WriteString("\n" + `"@)` + "\n")
	b.WriteString(`$toast = [Windows.UI.Notifications.ToastNotification]::new($xml)` + "\n")
	fmt.Fprintf(&b, "$toast.Tag = '%s'\n", escapePSSingle(tag))
	fmt.Fprintf(&b, "$toast.Group = '%s'\n", escapePSSingle(group))
	b.WriteString(`[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('Haoma').Show($toast)` + "\n")
	return b.String()
}

func buildDismissScript(tag, group string) string {
	var b strings.Builder
	b.WriteString(`[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType=WindowsRuntime] | Out-Null` + "\n")
	fmt.Fprintf(&b, "[Windows.UI.Notifications.ToastNotificationManager]::History.Remove('%s', '%s', 'Haoma')\n",
		escapePSSingle(tag), escapePSSingle(group))
	return b.String()
}

func encodePowerShell(script string) string {
	utf16Codes := utf16.Encode([]rune(script))
	bytes := make([]byte, len(utf16Codes)*2)
	for i, r := range utf16Codes {
		bytes[i*2] = byte(r)
		bytes[i*2+1] = byte(r >> 8)
	}
	return base64.StdEncoding.EncodeToString(bytes)
}

func escapeXML(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
	)
	return r.Replace(s)
}

func escapePSSingle(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}
