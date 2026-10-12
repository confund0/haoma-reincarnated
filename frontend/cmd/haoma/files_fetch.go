package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/files"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/mirror"
	"haoma-frontend/internal/msg"
)

type FileEventBody struct {
	Token            string `json:"token"`
	UrlPath          string `json:"url_path"`
	Name             string `json:"name"`
	Size             uint64 `json:"size"`
	Mime             string `json:"mime,omitempty"`
	Sha256Ciphertext string `json:"sha256_ciphertext"`
	State            string `json:"state"`
	BytesReceived    uint64 `json:"bytes_received,omitempty"`
	LastError        string `json:"last_error,omitempty"`
	Caption          string `json:"caption,omitempty"`
}

func ingestFileOffer(ctx context.Context, d *daemon, chatID chat.ChatID, entry backendapi.InboxEntry, wrapper *msg.Wrapper, body *msg.FileOfferBody) {
	logger := slog.With(
		slog.String("envelope_id", entry.Envelope.ID),
		slog.String("peer_id", entry.PeerID),
		slog.String("msg_id", wrapper.MsgID),
		slog.String("chat_id", string(chatID)),
	)

	logger.Info("inbound file_offer received",
		slog.Uint64("size", body.Size),
		slog.String("mime", body.Mime),
	)

	feBody := FileEventBody{
		Token:            body.Token,
		UrlPath:          body.UrlPath,
		Name:             body.Name,
		Size:             body.Size,
		Mime:             body.Mime,
		Sha256Ciphertext: body.Sha256Ciphertext,
		State:            string(files.StateDownloading),
		Caption:          body.Caption,
	}
	bodyRaw, err := json.Marshal(feBody)
	if err != nil {
		logger.Error("marshal file event body failed", slog.Any("err", err))
		return
	}

	ev, err := d.events.AppendInbound(events.InboundParams{
		ChatID:            chatID,
		Kind:              events.KindFile,
		SenderTs:          wrapper.Ts,
		SenderSeq:         wrapper.Seq,
		ArrivedFromPeerID: entry.PeerID,
		EnvelopeID:        entry.Envelope.ID,
		MsgID:             wrapper.MsgID,
		ExpireSeconds:     wrapper.ExpireSeconds,
		Status:            events.DecryptOK,
		Body:              bodyRaw,
	})
	switch {
	case errors.Is(err, events.ErrDuplicateMsgID) &&
		(ev.Direction != events.DirIn || ev.ChatID != chatID || ev.Kind != events.KindFile):

		logger.Debug("inbound file_offer refused: its msg id names a row that is not this contact's file",
			slog.String("outcome", "foreign_msg_id"),
			slog.String("held_direction", string(ev.Direction)),
			slog.String("held_kind", string(ev.Kind)),
			slog.Bool("held_in_this_chat", ev.ChatID == chatID),
		)
		return
	case errors.Is(err, events.ErrDuplicateMsgID):

		logger.Debug("inbound file_offer already held — the row stands, the transfer runs",
			slog.String("outcome", "duplicate_row_transfer_continues"),
		)

		if _, err := d.events.RecordArrivedFrom(wrapper.MsgID, entry.PeerID); err != nil {
			logger.Warn("recording the offering device on a held row failed (its read receipt will address the chat's stored device)",
				slog.Any("err", err),
			)
		}
	case err != nil:
		logger.Error("persist file event failed", slog.Any("err", err))
		return
	}

	maybeSuppressInboundReceipt(d, chatID, wrapper.MsgID)

	bumpChatActivity(ctx, d, chatID, time.Now().Unix())
	if !chatIsFocused(d, chatID) {
		incrementChatUnread(ctx, d, chatID)
	}

	d.autoMarkOnArrival(context.Background(), chatID)

	metaHeld := false
	if d.files != nil {
		meta := files.Metadata{
			MsgID:     wrapper.MsgID,
			ChatID:    chatID,
			Direction: files.DirIn,
			Token:     body.Token,

			OfferPeerID:      entry.PeerID,
			OriginalName:     body.Name,
			Mime:             body.Mime,
			Size:             body.Size,
			Sha256Ciphertext: body.Sha256Ciphertext,
			State:            files.StateDownloading,
			CreatedAt:        time.Now().Unix(),
			UpdatedAt:        time.Now().Unix(),
		}

		err = d.files.PutMetaIfAbsent(meta)
		switch {
		case errors.Is(err, files.ErrMetaHeld):
			logger.Debug("inbound file_offer discarded: another offer for this msg id was already taken",
				slog.String("outcome", "already_held"),
			)
			return
		case err != nil:
			logger.Warn("persist file metadata failed", slog.Any("err", err))

		default:
			metaHeld = true
			logger.Debug("inbound file metadata recorded the offering device",
				slog.String("offer_peer_id", entry.PeerID),
			)
		}
	}

	switch {
	case !metaHeld:
		logger.Debug("file receipt: early receipt skipped",
			slog.String("reason", "no_metadata_row"),
		)
	case earlyReceiptSkipped(d, logger):

	default:
		if receiptMsgID, envelopeID, ok := d.shipFileReceipt(ctx, entry.PeerID, wrapper.MsgID, body.Token); ok {
			logger.Debug("file receipt: early receipt shipped",
				slog.String("receipt_msg_id", receiptMsgID),
				slog.String("receipt_envelope_id", envelopeID),
			)
		}
	}

	if d.backendClient == nil {
		logger.Warn("file fetch skipped: no backend client configured")
		return
	}
	req := backendapi.FetchFileRequest{
		MsgID:          wrapper.MsgID,
		PeerID:         entry.PeerID,
		Token:          body.Token,
		UrlPath:        body.UrlPath,
		ExpectedSize:   int64(body.Size),
		ExpectedSha256: body.Sha256Ciphertext,
	}

	enqueueCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := d.backendClient.FetchFile(enqueueCtx, req)
	if err != nil {
		logger.Warn("file fetch enqueue failed", slog.Any("err", err))

		stampFileEventState(d, ev.ChatID, ev.MsgID, files.StateFailedTransient, 0, err.Error())
		return
	}
	logger.Debug("file fetch enqueued",
		slog.String("backend_state", resp.State),
		slog.Int64("bytes_received", resp.BytesReceived),
	)
}

func stampFileEventState(d *daemon, chatID chat.ChatID, msgID string, state files.State, bytesReceived uint64, lastError string) {
	if d.events == nil || msgID == "" {
		return
	}
	ev, err := d.events.GetByMsgID(msgID)
	if err != nil {
		if errors.Is(err, events.ErrEventNotFound) {
			slog.Debug("file event state stamp: row not found",
				slog.String("msg_id", msgID),
			)
			return
		}
		slog.Warn("file event state stamp: lookup failed",
			slog.String("msg_id", msgID),
			slog.Any("err", err),
		)
		return
	}
	if ev.Kind != events.KindFile {
		slog.Debug("file event state stamp: target is not a file row",
			slog.String("msg_id", msgID),
			slog.String("kind", string(ev.Kind)),
		)
		return
	}
	var body FileEventBody
	if len(ev.Body) > 0 {
		if err := json.Unmarshal(ev.Body, &body); err != nil {
			slog.Warn("file event state stamp: decode body failed",
				slog.String("msg_id", msgID),
				slog.Any("err", err),
			)
			return
		}
	}

	if body.State == string(files.StateReady) && !stampAllowedFromReady(state) {
		slog.Debug("file event state stamp: row is ready; not moved back",
			slog.String("msg_id", msgID),
			slog.String("refused_state", string(state)),
		)
		return
	}
	body.State = string(state)
	if bytesReceived > 0 {
		body.BytesReceived = bytesReceived
	}

	body.LastError = clientDetail("file_"+string(state), lastError)
	raw, err := json.Marshal(body)
	if err != nil {
		slog.Warn("file event state stamp: marshal failed",
			slog.String("msg_id", msgID),
			slog.Any("err", err),
		)
		return
	}
	if _, err := d.events.UpdateFileBody(msgID, raw); err != nil {
		slog.Warn("file event state stamp: persist failed",
			slog.String("msg_id", msgID),
			slog.Any("err", err),
		)
		return
	}

	if d.files != nil {
		err := d.files.UpdateMeta(msgID, func(m *files.Metadata) error {
			metaCheckpoint("stamp", msgID)
			if m.State == files.StateReady && !stampAllowedFromReady(state) {
				return errSettledMeanwhile
			}
			m.State = state
			m.UpdatedAt = time.Now().Unix()
			return nil
		})
		if err != nil && !errors.Is(err, files.ErrMetaNotFound) && !errors.Is(err, errSettledMeanwhile) {
			slog.Debug("file event state stamp: metadata sync failed",
				slog.String("msg_id", msgID),
				slog.Any("err", err),
			)
		}
	}
	_ = chatID
}

func stampAllowedFromReady(state files.State) bool {
	return state == files.StateReady || state == files.StateExpired
}

var metaCheckpoint = func(site, msgID string) {}

func fileFetchStateHandler(ctx context.Context, d *daemon) func(backendapi.FileFetchEvent) {
	return func(ev backendapi.FileFetchEvent) {

		msgID, isSibling, err := siblingFetchMsgID(d, ev.MsgID)
		if err != nil {

			slog.Warn("file fetch event dropped: the staging index could not be read",
				slog.String("msg_id", ev.MsgID),
				slog.Any("err", err),
			)
			return
		}
		if isSibling {
			handleSiblingFetchEvent(ctx, d, msgID, ev)
			return
		}

		if d.files != nil {
			if _, mErr := d.files.GetMeta(ev.MsgID); errors.Is(mErr, files.ErrMetaNotFound) {
				dropUnheldFetch(ctx, d, ev)
				return
			}
		}

		logger := slog.With(
			slog.String("msg_id", ev.MsgID),
			slog.String("state", ev.State),
			slog.Int64("bytes", ev.BytesReceived),
			slog.Int64("total", ev.TotalBytes),
		)
		logger.Debug("file fetch state event")

		switch files.State(ev.State) {
		case files.StatePending, files.StateDownloading:
			stampFileEventState(d, "", ev.MsgID, files.State(ev.State), uint64(ev.BytesReceived), "")
		case files.StateFailedTransient:
			stampFileEventState(d, "", ev.MsgID, files.StateFailedTransient, uint64(ev.BytesReceived), ev.LastError)
		case files.StateFailedPermanent:
			stampFileEventState(d, "", ev.MsgID, files.StateFailedPermanent, uint64(ev.BytesReceived), ev.LastError)
		case files.StateReady:

			postPull, converge, err := finalizeReadyFetch(ctx, d, ev.MsgID, ev.Token)
			if err != nil {
				logger.Warn("finalize ready fetch failed", slog.Any("err", err))
				stampFileEventState(d, "", ev.MsgID, files.StateFailedTransient, uint64(ev.BytesReceived), err.Error())
				return
			}

			logger.Info("file download finished",
				slog.String("post_pull_receipt", postPull),
				slog.String("converge", converge),
			)
		default:
			logger.Debug("ignoring unknown fetch state")
		}
	}
}

func finalizeReadyFetch(ctx context.Context, d *daemon, msgID, token string) (postPull, converge string, err error) {
	if err := pullStagedCiphertext(ctx, d, msgID, token); err != nil {
		return "", "", err
	}

	slog.Debug("file fetch ready: blob pulled", slog.String("msg_id", msgID))
	postPull = postPullReceipt(ctx, d, msgID, token)
	converge = convergeFileReady(ctx, d, msgID)
	return postPull, converge, nil
}

func postPullReceipt(ctx context.Context, d *daemon, msgID, token string) string {
	logger := slog.With(slog.String("msg_id", msgID))
	if meta, err := d.files.GetMeta(msgID); err == nil && len(meta.KeyBytes) > 0 && len(meta.Nonce) > 0 {
		logger.Debug("file receipt: post-pull receipt skipped",
			slog.String("reason", "key_already_here"),
		)
		return "skipped_key_here"
	}
	peerID := resolveOfferPeer(d, msgID)
	if peerID == "" {
		logger.Warn("file receipt: post-pull receipt skipped",
			slog.String("reason", "no_device_to_address"),
		)
		return "skipped_no_device"
	}
	receiptMsgID, envelopeID, ok := d.shipFileReceipt(ctx, peerID, msgID, token)
	if !ok {
		return "failed"
	}
	logger.Debug("file receipt: post-pull receipt shipped",
		slog.String("reason", "key_not_here"),
		slog.String("receipt_msg_id", receiptMsgID),
		slog.String("receipt_envelope_id", envelopeID),
	)
	return "shipped"
}

func dropUnheldFetch(ctx context.Context, d *daemon, ev backendapi.FileFetchEvent) {
	logger := slog.With(slog.String("msg_id", ev.MsgID), slog.String("state", ev.State))
	if files.State(ev.State) != files.StateReady || d.backendClient == nil {
		logger.Debug("file fetch event for a file no row holds; ignored",
			slog.String("outcome", "unheld_ignored"),
		)
		return
	}
	dropCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := d.backendClient.DropStagingBlob(dropCtx, ev.MsgID, ev.Token); err != nil {
		logger.Debug("file fetch event for a file no row holds; haomad copy drop failed", slog.Any("err", err))
		return
	}
	logger.Debug("file fetch event for a file no row holds; haomad copy dropped",
		slog.String("outcome", "unheld_dropped"),
	)
}

func sweepStagedFetches(ctx context.Context, d *daemon) {
	if d.files == nil || d.backendClient == nil {
		return
	}
	rows, err := d.files.ListInboundInFlight()
	if err != nil {
		slog.Warn("staged-fetch sweep: list failed", slog.Any("err", err))
		return
	}
	for _, m := range rows {
		if ctx.Err() != nil {
			return
		}
		postPull, converge, err := finalizeReadyFetch(ctx, d, m.MsgID, m.Token)
		switch {
		case err == nil && converge == convergeDecryptFailed:
			slog.Warn("staged-fetch sweep: pulled the blob but the key does not open it; failed permanently",
				slog.String("msg_id", m.MsgID),
				slog.String("post_pull_receipt", postPull),
				slog.String("converge", converge))
		case err == nil:
			slog.Info("staged-fetch sweep: recovered orphaned transfer",
				slog.String("msg_id", m.MsgID),
				slog.String("post_pull_receipt", postPull),
				slog.String("converge", converge))
		case errors.Is(err, backendapi.ErrStagingNotFound):
			slog.Debug("staged-fetch sweep: not staged yet; leaving for live path",
				slog.String("msg_id", m.MsgID))
		case errors.Is(err, errStagingSizeMismatch):
			slog.Warn("staged-fetch sweep: staged blob size differs from the offer; leaving row",
				slog.String("msg_id", m.MsgID), slog.Any("err", err))
		default:
			slog.Warn("staged-fetch sweep: finalize failed; leaving row",
				slog.String("msg_id", m.MsgID), slog.Any("err", err))
		}
	}
}

func fileFetchProgressHandler(d *daemon) func(backendapi.FileFetchEvent) {
	return func(ev backendapi.FileFetchEvent) {

		msgID, isSibling, err := siblingFetchMsgID(d, ev.MsgID)
		if err != nil {
			return
		}
		if isSibling {
			ev.MsgID = msgID
		}

		var chatID string
		if d.files != nil {
			if meta, err := d.files.GetMeta(ev.MsgID); err == nil {
				chatID = string(meta.ChatID)
				markDownloadingAgain(d, meta)
			}
		}
		if d.ipcSrv == nil {
			return
		}
		push(d.ipcSrv, ipc.FrameFileProgress, "", ipc.FileProgressPayload{
			ChatID:        chatID,
			MsgID:         ev.MsgID,
			BytesReceived: uint64(ev.BytesReceived),
			TotalBytes:    uint64(ev.TotalBytes),
		})
	}
}

func markDownloadingAgain(d *daemon, meta files.Metadata) {
	if meta.State != files.StateFailedTransient && meta.State != files.StatePending {
		return
	}
	slog.Debug("file progress on a failed or pending row; marking it downloading",
		slog.String("msg_id", meta.MsgID),
		slog.String("from_state", string(meta.State)),
	)
	stampFileEventState(d, "", meta.MsgID, files.StateDownloading, 0, "")
}

var errStagingSizeMismatch = errors.New("staged blob size differs from the offer")

const convergeDecryptFailed = "decrypt_failed"

func pullStagedCiphertext(ctx context.Context, d *daemon, msgID, token string) error {
	if d.files == nil {
		return errors.New("files manager not initialised")
	}
	meta, err := d.files.GetMeta(msgID)
	if err != nil {
		return fmt.Errorf("read metadata for the blob size: %w", err)
	}
	return pullStagedCiphertextAs(ctx, d, msgID, msgID, token, meta.Size)
}

func pullStagedCiphertextAs(ctx context.Context, d *daemon, haomadID, msgID, token string, wantSize uint64) error {
	if d.backendClient == nil {
		return errors.New("no backend client")
	}
	if d.files == nil {
		return errors.New("files manager not initialised")
	}
	pullCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	rc, _, err := d.backendClient.FetchStagingBlob(pullCtx, haomadID)
	if err != nil {
		if errors.Is(err, backendapi.ErrStagingNotFound) {
			return fmt.Errorf("staging blob missing on haomad: %w", err)
		}
		return fmt.Errorf("get staging blob: %w", err)
	}
	defer rc.Close()

	bytes, err := io.ReadAll(io.LimitReader(rc, files.MaxPullBytes))
	if err != nil {
		return fmt.Errorf("read staging blob: %w", err)
	}

	if uint64(len(bytes)) != wantSize {
		return fmt.Errorf("%w: got %d bytes, want %d", errStagingSizeMismatch, len(bytes), wantSize)
	}
	if _, err := d.files.WriteStaging(msgID, bytes); err != nil {
		return fmt.Errorf("write staging: %w", err)
	}

	dropCtx, dropCancel := context.WithTimeout(ctx, 10*time.Second)
	defer dropCancel()
	if err := d.backendClient.DropStagingBlob(dropCtx, haomadID, token); err != nil {

		slog.Warn("drop haomad staging after pull failed",
			slog.String("msg_id", msgID),
			slog.Any("err", err),
		)
	}
	return nil
}

var _ = http.Get

func resolveOfferPeer(d *daemon, msgID string) string {
	if d == nil || d.files == nil || d.chats == nil || msgID == "" {
		return ""
	}
	meta, err := d.files.GetMeta(msgID)
	if err != nil {
		return ""
	}
	c, err := d.chats.Get(meta.ChatID)
	if err != nil {
		return ""
	}
	dc, ok := c.(*chat.DirectChat)
	if !ok {
		return ""
	}
	if meta.OfferPeerID != "" {
		for _, pid := range dc.FanoutPeerIDs() {
			if pid == meta.OfferPeerID {
				slog.Debug("file answer addressed to the device that sent the offer",
					slog.String("msg_id", msgID),
					slog.String("peer_id", meta.OfferPeerID),
					slog.String("source", "recorded_at_ingest"),
				)
				return meta.OfferPeerID
			}
		}
		slog.Debug("file answer addressed to the chat's primary device",
			slog.String("msg_id", msgID),
			slog.String("peer_id", dc.PeerID),
			slog.String("recorded_peer_id", meta.OfferPeerID),
			slog.String("source", "recorded_device_no_longer_routes_here"),
		)
		return dc.PeerID
	}
	slog.Debug("file answer addressed to the chat's primary device",
		slog.String("msg_id", msgID),
		slog.String("peer_id", dc.PeerID),
		slog.String("source", "row_names_no_offering_device"),
	)
	return dc.PeerID
}

func (d *daemon) shipFileReceipt(ctx context.Context, peerID, offerMsgID, token string) (receiptMsgID, envelopeID string, ok bool) {
	if peerID == "" || token == "" {
		return "", "", false
	}
	if d.cipher == nil || d.peerSeq == nil || d.backendClient == nil {
		slog.Warn("ship file receipt: wiring incomplete; skipping",
			slog.String("peer_id", peerID),
		)
		return "", "", false
	}
	seq, err := d.peerSeq.NextSendSeq(peerID)
	if err != nil {
		slog.Warn("ship file receipt: next seq failed",
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
		return "", "", false
	}
	receiptMsgID, err = msg.NewID()
	if err != nil {
		slog.Warn("ship file receipt: new id failed", slog.Any("err", err))
		return "", "", false
	}
	wrapper, err := msg.BuildFileReceipt(seq, time.Now().Unix(), receiptMsgID, token, 0)
	if err != nil {
		slog.Warn("ship file receipt: build wrapper failed", slog.Any("err", err))
		return "", "", false
	}
	plaintext, err := msg.Marshal(wrapper)
	if err != nil {
		slog.Warn("ship file receipt: marshal failed", slog.Any("err", err))
		return "", "", false
	}
	blob, err := d.cipher.Encrypt(ctx, peerID, plaintext)
	if err != nil {
		slog.Warn("ship file receipt: encrypt failed",
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
		return "", "", false
	}
	result, err := mirror.Dispatch(ctx, d.backendClient, mirror.NewRail(d.railKey, d.siblings, d.peerMeta),
		mirror.SingleTarget(peerID),
		func(ctx context.Context, deviceID string) (backendapi.SendRequest, []byte, error) {
			return backendapi.SendRequest{
				PeerID:         deviceID,
				Payload:        blob,
				PresenceSource: backendapi.PresenceSourceHaoma,
			}, plaintext, nil
		},
		mirror.NewOp(wrapper.Kind, ""))
	if err != nil {
		slog.Warn("ship file receipt: backend send failed",
			slog.String("peer_id", peerID),
			slog.String("msg_id", offerMsgID),
			slog.Any("err", err),
		)
		return "", "", false
	}
	slog.Debug("file receipt shipped",
		slog.String("peer_id", peerID),
		slog.String("msg_id", offerMsgID),
		slog.String("envelope_id", result.Primary.EnvelopeID),
		slog.String("receipt_msg_id", receiptMsgID),
		slog.Uint64("sender_seq", seq),
	)
	return receiptMsgID, result.Primary.EnvelopeID, true
}

func (sd *sessionDispatcher) handleRetryFiles(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle retry_files")
	if sd.d.backendClient == nil {
		sendError(sess, f.ID, "not_ready", "backend client not wired")
		return
	}

	retryGapFiles(sd.d)
	enqueued, err := sd.d.backendClient.RetryFailedFiles(ctx)
	if err != nil {
		sendError(sess, f.ID, "internal", err.Error())
		return
	}
	resp, err := ipc.NewFrame(ipc.FrameRetryFilesResponse, f.ID, ipc.RetryFilesResponse{
		Enqueued: enqueued,
	})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send retry_files_result frame failed", slog.Any("err", err))
	}
	slog.Info("retry_files done", slog.Int("enqueued", enqueued))
}
