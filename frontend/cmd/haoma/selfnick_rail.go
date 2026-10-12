package main

import (
	"context"
	"log/slog"

	"haoma-frontend/internal/syncrail"
)

func pushSelfNickToSiblings(ctx context.Context, d *daemon) {
	if d == nil {
		return
	}
	sibs := d.siblings()
	if len(sibs) == 0 {

		return
	}
	snap, ok := selfNickSnapshot(d)
	if !ok {
		slog.Debug("self-nick: this device is still on the placeholder; nothing is pushed",
			slog.Int("siblings", len(sibs)),
		)
		return
	}
	go func() {
		for _, sib := range sibs {
			if ctx.Err() != nil {
				return
			}

			if err := sendGapEnvelope(ctx, d, sib, syncrail.KindSelfNick, syncrail.SelfNickBody{Self: snap}); err != nil {

				slog.Debug("self-nick: push failed; the sweep will take it",
					slog.String("sibling_peer_id", sib),
					slog.Any("err", err),
				)
				continue
			}
			slog.Debug("self-nick: pushed to sibling",
				slog.String("sibling_peer_id", sib),
				slog.Int64("nick_at", snap.NickAt),
			)
		}
	}()
}

func selfNickSnapshot(d *daemon) (syncrail.SelfSnapshot, bool) {
	nick, at, ok := d.selfNickToSend()
	if !ok {
		return syncrail.SelfSnapshot{}, false
	}
	return syncrail.SelfSnapshot{Nick: nick, NickAt: at}, true
}

func handleSelfNickPush(_ context.Context, d *daemon, siblingPeerID string, body syncrail.SelfNickBody) {
	logger := slog.With(slog.String("sibling_peer_id", siblingPeerID))
	logger.Debug("self-nick: a sibling offered a name",
		slog.Int64("offered_at", body.Self.NickAt),
	)
	applySelfNick(d, logger, body.Self)
}

func applySelfNick(d *daemon, logger *slog.Logger, snap syncrail.SelfSnapshot) (moved bool) {
	if d == nil || snap.Nick == "" {
		return false
	}
	moved, err := d.mergeSelfNick(logger, snap.Nick, snap.NickAt)
	if err != nil {
		logger.Warn("self-nick: a sibling's name was not merged; this device keeps its own",
			slog.Any("err", err),
		)
		return false
	}
	if !moved {
		return false
	}

	d.pushSelfNick()
	return true
}
