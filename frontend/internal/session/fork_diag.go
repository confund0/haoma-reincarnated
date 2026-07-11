package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"

	"go.mau.fi/libsignal/protocol"

	"haoma-frontend/internal/msg"
)

type ratchetSnap struct {
	present bool
	rootFP  string
	sendFP  string
	sendIdx uint32
	prevCtr uint32
	nPrev   int
	remReg  uint32
}

func fp(b []byte) string {
	if len(b) == 0 {
		return "-"
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:4])
}

func fpOf(get func() []byte) (out string) {
	out = "-"
	defer func() { _ = recover() }()
	return fp(get())
}

func (c *Cipher) snapshot(ctx context.Context, addr *protocol.SignalAddress) (snap ratchetSnap) {
	if !slog.Default().Enabled(ctx, slog.LevelDebug) {
		return ratchetSnap{}
	}
	defer func() { _ = recover() }()
	rec, err := c.stores.LoadSession(ctx, addr)
	if err != nil || rec == nil {
		return ratchetSnap{}
	}
	st := rec.SessionState()
	if st == nil {
		return ratchetSnap{}
	}
	snap = ratchetSnap{
		present: true,
		prevCtr: st.PreviousCounter(),
		nPrev:   len(rec.PreviousSessionStates()),
		remReg:  st.RemoteRegistrationID(),
	}
	snap.rootFP = fpOf(func() []byte { return st.RootKey().Bytes() })
	if st.HasSenderChain() {
		snap.sendFP = fpOf(func() []byte { return st.SenderRatchetKey().Serialize() })
		if ck := st.SenderChainKey(); ck != nil {
			snap.sendIdx = ck.Index()
		}
	}
	return snap
}

func (c *Cipher) logRatchetOp(ctx context.Context, dir, peerID, kind string, tag byte, before, after ratchetSnap, opErr error) {
	if !slog.Default().Enabled(ctx, slog.LevelDebug) {
		return
	}
	attrs := []any{
		slog.String("dir", dir),
		slog.String("peer", peerID),
		slog.String("kind", kind),
		slog.String("root_before", before.rootFP),
		slog.String("root_after", after.rootFP),
		slog.Bool("root_changed", before.rootFP != after.rootFP),
		slog.String("send_ratchet", after.sendFP),
		slog.Int("send_idx", int(after.sendIdx)),
		slog.Int("prev_ctr", int(after.prevCtr)),
		slog.Int("n_prev_before", before.nPrev),
		slog.Int("n_prev_after", after.nPrev),
		slog.Bool("archived", after.nPrev > before.nPrev),
		slog.Int("rem_reg", int(after.remReg)),
	}
	if tag != 0 {
		attrs = append(attrs, slog.Int("tag", int(tag)))
	}
	if opErr != nil {
		attrs = append(attrs, slog.String("op_err", opErr.Error()))
	}
	slog.Debug("ratchet.op", attrs...)
}

func peekKind(plaintext []byte) string {
	w, err := msg.Unmarshal(plaintext)
	if err != nil || w == nil {
		return ""
	}
	return string(w.Kind)
}
