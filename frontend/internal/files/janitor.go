package files

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
)

type RemoteDropper interface {
	DropFile(ctx context.Context, msgID string) error
}

type Janitor struct {
	mgr    *Manager
	bus    *events.Bus
	log    *slog.Logger
	remote RemoteDropper
}

func NewJanitor(mgr *Manager, bus *events.Bus, log *slog.Logger, remote RemoteDropper) *Janitor {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Janitor{mgr: mgr, bus: bus, log: log, remote: remote}
}

func (j *Janitor) Run(ctx context.Context) {
	if j == nil || j.mgr == nil || j.bus == nil {
		return
	}
	const (
		eventBuf    = 64
		deletionBuf = 256
	)
	evCh, evCancel := j.bus.Subscribe(eventBuf)
	defer evCancel()
	delCh, delCancel := j.bus.SubscribeDeletions(deletionBuf)
	defer delCancel()

	j.log.Debug("files janitor started",
		slog.Int("event_buf", eventBuf),
		slog.Int("deletion_buf", deletionBuf),
	)
	defer j.log.Debug("files janitor stopped")

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-evCh:
			if !ok {
				return
			}
			if !shouldCleanup(ev) {
				continue
			}
			j.handle(ev.ChatID, ev.MsgID, "tombstone")
		case d, ok := <-delCh:
			if !ok {
				return
			}
			if d.MsgID == "" {

				j.log.Debug("deletion lacked msg_id; skipping cascade",
					slog.String("chat_id", string(d.ChatID)),
					slog.Uint64("recv_seq", d.RecvSeq),
				)
				continue
			}
			j.handle(d.ChatID, d.MsgID, "sweep")
		}
	}
}

func shouldCleanup(ev events.Event) bool {
	return ev.Kind == events.KindFile && ev.DeletedAt > 0 && ev.MsgID != ""
}

func (j *Janitor) handle(chatID chat.ChatID, msgID, trigger string) {
	logger := j.log.With(
		slog.String("chat_id", string(chatID)),
		slog.String("msg_id", msgID),
		slog.String("trigger", trigger),
	)
	logger.Debug("file cascade")

	meta, err := j.mgr.GetMeta(msgID)
	metaMissing := errors.Is(err, ErrMetaNotFound)
	if metaMissing {
		logger.Debug("no metadata row; nothing to clean")
		return
	}
	if err != nil {
		logger.Warn("metadata read failed", slog.Any("err", err))

	}

	cleanupChat := chatID
	if meta.ChatID != "" && meta.ChatID != chatID {
		logger.Debug("metadata chat differs from bus signal",
			slog.String("meta_chat_id", string(meta.ChatID)),
		)
		cleanupChat = meta.ChatID
	}

	if err := j.mgr.DeleteSealed(cleanupChat, msgID); err != nil {
		logger.Warn("delete sealed failed", slog.Any("err", err))
	}

	if err := j.mgr.DeleteStaging(msgID); err != nil {
		logger.Warn("delete staging failed", slog.Any("err", err))
	}
	if err := j.mgr.DeleteMeta(msgID); err != nil {
		logger.Warn("delete metadata failed", slog.Any("err", err))
	}

	if meta.Direction != DirOut {
		return
	}
	if j.remote == nil {
		logger.Debug("outbound cascade skipped: no remote dropper wired")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := j.remote.DropFile(ctx, msgID); err != nil {
		logger.Warn("remote drop failed", slog.Any("err", err))
		return
	}
	logger.Debug("remote drop ok")
}
