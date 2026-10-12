package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"haoma-frontend/internal/ipc"
)

const (
	deadLetterWait = 4 * time.Minute

	republishWait = 240 * time.Second
)

func assertReceiptRetry(sender, reader *rig, senderSeesReader string) error {

	d, err := deliverText(sender, reader, senderSeesReader, fmt.Sprintf("receipt-retry %s->%s", sender.name, reader.name))
	if err != nil {
		return fmt.Errorf("setup text: %w", err)
	}

	logf("[%s] stopping haomad so the receipt has a real failure to hit", sender.name)
	sender.stopHaomad()

	readerSub, readerCancel := reader.subscribe()
	defer readerCancel()
	if err := reader.send(ipc.FrameMarkRead, ipc.MarkReadRequest{ChatID: d.chatID}); err != nil {
		return fmt.Errorf("mark-read (doomed): %w", err)
	}
	shipped, err := awaitFrame(readerSub, readTimeout, func(f ipc.Frame) bool {
		return f.Type == ipc.FrameReadSent
	})
	if err != nil {
		return fmt.Errorf("receipt never shipped: %w", err)
	}
	var sentPayload ipc.ReadSentPayload
	if err := json.Unmarshal(shipped.Payload, &sentPayload); err != nil {
		return fmt.Errorf("read-sent payload: %w", err)
	}
	logf("[%s] receipt shipped into a dead end (envelope %s)", reader.name, short(sentPayload.EnvelopeID))

	cleared, err := awaitClearedRows(reader, sentPayload.EnvelopeID, deadLetterWait)
	if err != nil {
		return err
	}
	if cleared < 1 {
		return fmt.Errorf("give-up was observed but cleared %d rows — the receipt is still marked as sent", cleared)
	}
	logf("[%s] give-up seen, %d row(s) handed back to pending", reader.name, cleared)

	logf("[%s] restarting haomad on %s", sender.name, sender.haomadAddr)
	if err := sender.restartHaomad(); err != nil {
		return fmt.Errorf("restart sender backend: %w", err)
	}

	time.Sleep(settleDelay)

	senderSub, senderCancel := sender.subscribe()
	defer senderCancel()
	deadline := time.Now().Add(republishWait)
	for {
		if err := reader.send(ipc.FrameMarkRead, ipc.MarkReadRequest{ChatID: d.chatID}); err != nil {
			return fmt.Errorf("mark-read (retry): %w", err)
		}
		_, err := awaitFrame(senderSub, 30*time.Second, func(f ipc.Frame) bool {
			switch f.Type {
			case ipc.FrameDeliveryStatus:
				var p ipc.DeliveryStatusPayload
				if json.Unmarshal(f.Payload, &p) != nil {
					return false
				}
				return p.EnvelopeID == d.envID && p.State == "read"
			case ipc.FrameTimelineEvent:
				ev, ok := timelineEvent(f)
				return ok && ev.MsgID == d.msgID && ev.DeliveryState == "read"
			}
			return false
		})
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("re-shipped receipt never reached the sender: %w", err)
		}
		logf("[%s->%s] retry not observed yet — re-opening the chat", reader.name, sender.name)
	}
}

func awaitClearedRows(reader *rig, envelopeID string, timeout time.Duration) (int, error) {
	const marker = "stamps withdrawn"
	logPath := filepath.Join(reader.root, "haoma.log")
	deadline := time.Now().Add(timeout)
	for {
		f, err := os.Open(logPath)
		if err == nil {
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for sc.Scan() {
				line := sc.Text()
				if !strings.Contains(line, marker) || !strings.Contains(line, short(envelopeID)) {
					continue
				}
				f.Close()
				return parseLoggedInt(line, "rows_cleared"), nil
			}
			f.Close()
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("no give-up recorded for envelope %s within %s — the outbox never dead-lettered, or the report was discarded again", short(envelopeID), timeout)
		}
		time.Sleep(2 * time.Second)
	}
}

func parseLoggedInt(line, key string) int {
	idx := strings.Index(line, key+"=")
	if idx < 0 {
		return -1
	}
	rest := line[idx+len(key)+1:]
	if sp := strings.IndexByte(rest, ' '); sp >= 0 {
		rest = rest[:sp]
	}
	n := 0
	if _, err := fmt.Sscanf(rest, "%d", &n); err != nil {
		return -1
	}
	return n
}
