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
}

func ingestFileOffer(ctx context.Context, d *daemon, chatID chat.ChatID, entry backendapi.InboxEntry, wrapper *msg.Wrapper, body *msg.FileOfferBody) {
	logger := slog.With(
		slog.String("envelope_id", entry.Envelope.ID),
		slog.String("peer_id", entry.PeerID),
		slog.String("msg_id", wrapper.MsgID),
		slog.String("chat_id", string(chatID)),
		slog.String("token", body.Token),
	)
	logger.Info("inbound file_offer received",
		slog.Uint64("size", body.Size),
		slog.String("name", body.Name),
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
	}
	bodyRaw, err := json.Marshal(feBody)
	if err != nil {
		logger.Error("marshal file event body failed", slog.Any("err", err))
		return
	}

	ev, err := d.events.AppendInbound(events.InboundParams{
		ChatID:        chatID,
		Kind:          events.KindFile,
		SenderTs:      wrapper.Ts,
		SenderSeq:     wrapper.Seq,
		EnvelopeID:    entry.Envelope.ID,
		MsgID:         wrapper.MsgID,
		ExpireSeconds: wrapper.ExpireSeconds,
		Status:        events.DecryptOK,
		Body:          bodyRaw,
	})
	if err != nil {
		logger.Error("persist file event failed", slog.Any("err", err))
		return
	}

	bumpChatActivity(ctx, d, chatID, time.Now().Unix())
	if !chatIsFocused(d, chatID) {
		incrementChatUnread(ctx, d, chatID)
	}

	if d.files != nil {
		meta := files.Metadata{
			MsgID:            wrapper.MsgID,
			ChatID:           chatID,
			Direction:        files.DirIn,
			Token:            body.Token,
			OriginalName:     body.Name,
			Mime:             body.Mime,
			Size:             body.Size,
			Sha256Ciphertext: body.Sha256Ciphertext,
			State:            files.StateDownloading,
			CreatedAt:        time.Now().Unix(),
			UpdatedAt:        time.Now().Unix(),
		}
		if err := d.files.PutMeta(meta); err != nil {
			logger.Warn("persist file metadata failed", slog.Any("err", err))

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
	body.State = string(state)
	if bytesReceived > 0 {
		body.BytesReceived = bytesReceived
	}
	body.LastError = lastError
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
		if meta, mErr := d.files.GetMeta(msgID); mErr == nil {
			meta.State = state
			meta.UpdatedAt = time.Now().Unix()
			if err := d.files.PutMeta(meta); err != nil {
				slog.Debug("file event state stamp: metadata sync failed",
					slog.String("msg_id", msgID),
					slog.Any("err", err),
				)
			}
		}
	}
	_ = chatID
}

func fileFetchStateHandler(ctx context.Context, d *daemon) func(backendapi.FileFetchEvent) {
	return func(ev backendapi.FileFetchEvent) {
		logger := slog.With(
			slog.String("msg_id", ev.MsgID),
			slog.String("token", ev.Token),
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

			if err := pullStagedCiphertext(ctx, d, ev.MsgID, ev.Token); err != nil {
				logger.Warn("pull staging blob failed", slog.Any("err", err))
				stampFileEventState(d, "", ev.MsgID, files.StateFailedTransient, uint64(ev.BytesReceived), err.Error())
				return
			}
			if peerID := resolveOfferPeer(d, ev.MsgID); peerID != "" {
				d.shipFileReceipt(ctx, peerID, ev.Token)
			} else {
				logger.Warn("post-pull receipt skipped: peer-id unresolved (sender will retry on next offer)")
			}
			convergeFileReady(ctx, d, ev.MsgID)
			logger.Info("file ciphertext staged locally; receipt shipped; converged")
		default:
			logger.Debug("ignoring unknown fetch state")
		}
	}
}

func fileFetchProgressHandler(d *daemon) func(backendapi.FileFetchEvent) {
	return func(ev backendapi.FileFetchEvent) {
		if d.ipcSrv == nil {
			return
		}

		var chatID string
		if d.files != nil {
			if meta, err := d.files.GetMeta(ev.MsgID); err == nil {
				chatID = string(meta.ChatID)
			}
		}
		push(d.ipcSrv, ipc.FrameFileProgress, "", ipc.FileProgressPayload{
			ChatID:        chatID,
			MsgID:         ev.MsgID,
			BytesReceived: uint64(ev.BytesReceived),
			TotalBytes:    uint64(ev.TotalBytes),
		})
	}
}

func pullStagedCiphertext(ctx context.Context, d *daemon, msgID, token string) error {
	if d.backendClient == nil {
		return errors.New("no backend client")
	}
	if d.files == nil {
		return errors.New("files manager not initialised")
	}
	pullCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	rc, _, err := d.backendClient.FetchStagingBlob(pullCtx, msgID)
	if err != nil {
		if errors.Is(err, backendapi.ErrStagingNotFound) {
			return fmt.Errorf("staging blob missing on haomad: %w", err)
		}
		return fmt.Errorf("get staging blob: %w", err)
	}
	defer rc.Close()

	bytes, err := io.ReadAll(io.LimitReader(rc, files.MaxPlaintextBytes+1<<20))
	if err != nil {
		return fmt.Errorf("read staging blob: %w", err)
	}
	if _, err := d.files.WriteStaging(msgID, bytes); err != nil {
		return fmt.Errorf("write staging: %w", err)
	}

	dropCtx, dropCancel := context.WithTimeout(ctx, 10*time.Second)
	defer dropCancel()
	if err := d.backendClient.DropStagingBlob(dropCtx, msgID, token); err != nil {

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
	return dc.PeerID
}

func (d *daemon) shipFileReceipt(ctx context.Context, peerID, token string) {
	if peerID == "" || token == "" {
		return
	}
	if d.cipher == nil || d.peerSeq == nil || d.backendClient == nil {
		slog.Warn("ship file receipt: wiring incomplete; skipping",
			slog.String("peer_id", peerID),
		)
		return
	}
	seq, err := d.peerSeq.NextSendSeq(peerID)
	if err != nil {
		slog.Warn("ship file receipt: next seq failed",
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
		return
	}
	receiptMsgID, err := msg.NewID()
	if err != nil {
		slog.Warn("ship file receipt: new id failed", slog.Any("err", err))
		return
	}
	wrapper, err := msg.BuildFileReceipt(seq, time.Now().Unix(), receiptMsgID, token, 0)
	if err != nil {
		slog.Warn("ship file receipt: build wrapper failed", slog.Any("err", err))
		return
	}
	plaintext, err := msg.Marshal(wrapper)
	if err != nil {
		slog.Warn("ship file receipt: marshal failed", slog.Any("err", err))
		return
	}
	blob, err := d.cipher.Encrypt(ctx, peerID, plaintext)
	if err != nil {
		slog.Warn("ship file receipt: encrypt failed",
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
		return
	}
	resp, err := d.backendClient.Send(ctx, backendapi.SendRequest{
		PeerID:         peerID,
		Payload:        blob,
		PresenceSource: backendapi.PresenceSourceHaoma,
	})
	if err != nil {
		slog.Warn("ship file receipt: backend send failed",
			slog.String("peer_id", peerID),
			slog.String("token", token),
			slog.Any("err", err),
		)
		return
	}
	slog.Debug("file receipt shipped",
		slog.String("peer_id", peerID),
		slog.String("token", token),
		slog.String("envelope_id", resp.EnvelopeID),
		slog.String("receipt_msg_id", receiptMsgID),
		slog.Uint64("sender_seq", seq),
	)
}

func (sd *sessionDispatcher) handleRetryFiles(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle retry_files")
	if sd.d.backendClient == nil {
		sendError(sess, f.ID, "not_ready", "backend client not wired")
		return
	}
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
