package pair

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"haoma/internal/dht"
)

var bep44Salt = []byte("haoma-pair-v1")

var dhtSeedContext = []byte("haoma-pair-ed25519-seed/v1")

type DHT struct {
	server *dht.Server
}

func StartDHT(ctx context.Context) (*DHT, error) {
	srv, err := dht.NewServer(slog.Default())
	if err != nil {
		return nil, fmt.Errorf("pair: dht server: %w", err)
	}
	slog.Debug("pair: dht client starting", slog.String("addr", srv.LocalAddr().String()))
	if err := srv.Bootstrap(ctx); err != nil {
		srv.Close()
		return nil, fmt.Errorf("pair: dht bootstrap: %w", err)
	}
	slog.Debug("pair: dht bootstrapped", slog.Int("nodes", srv.NodeCount()))
	return &DHT{server: srv}, nil
}

func (d *DHT) Close() {
	if d == nil || d.server == nil {
		return
	}
	_ = d.server.Close()
}

func (d *DHT) Publish(ctx context.Context, idEntropy, value []byte) ([]byte, error) {
	priv, pub, err := seedToEd25519(idEntropy)
	if err != nil {
		return nil, err
	}
	var pubArr [32]byte
	copy(pubArr[:], pub)
	item := &dht.MutableItem{
		PrivKey: priv,
		PubKey:  pubArr,
		Salt:    bep44Salt,
		Seq:     time.Now().Unix(),
		Value:   value,
	}
	if err := item.Sign(); err != nil {
		return nil, fmt.Errorf("pair: dht sign: %w", err)
	}
	target := item.Target()

	pre, err := d.server.Get(ctx, target, &pubArr, bep44Salt)
	if err != nil {
		return nil, fmt.Errorf("pair: dht pre-put get: %w", err)
	}
	if pre.Seq >= item.Seq {
		item.Seq = pre.Seq + 1
		if err := item.Sign(); err != nil {
			return nil, fmt.Errorf("pair: dht re-sign: %w", err)
		}
	}

	stored, err := d.server.Put(ctx, item, pre.Tokens)
	if err != nil {
		return nil, fmt.Errorf("pair: dht put: %w", err)
	}
	slog.Debug("pair: dht put",
		slog.String("target", fmt.Sprintf("%x", target)),
		slog.Int64("seq", item.Seq),
		slog.Int("tokens", len(pre.Tokens)),
		slog.Int("stored", stored),
	)
	return pub, nil
}

func (d *DHT) Fetch(ctx context.Context, idEntropy []byte) ([]byte, error) {
	_, pub, err := seedToEd25519(idEntropy)
	if err != nil {
		return nil, err
	}
	var pubArr [32]byte
	copy(pubArr[:], pub)
	tmp := &dht.MutableItem{PubKey: pubArr, Salt: bep44Salt}
	target := tmp.Target()

	res, err := d.server.Get(ctx, target, &pubArr, bep44Salt)
	if err != nil {
		return nil, fmt.Errorf("pair: dht get: %w", err)
	}
	if res.Value == nil {
		return nil, ErrItemNotFound
	}
	slog.Debug("pair: dht get",
		slog.String("target", fmt.Sprintf("%x", target)),
		slog.Int64("seq", res.Seq),
		slog.Int("tokens_seen", len(res.Tokens)),
		slog.Int("bytes", len(res.Value)),
	)
	return res.Value, nil
}

func (d *DHT) Revoke(ctx context.Context, idEntropy []byte) error {
	_, err := d.Publish(ctx, idEntropy, nil)
	return err
}

var ErrItemNotFound = errors.New("pair: dht item not found")

func seedToEd25519(idEntropy []byte) (priv ed25519.PrivateKey, pub ed25519.PublicKey, err error) {
	if len(idEntropy) == 0 {
		return nil, nil, errors.New("pair: empty id entropy")
	}
	h := sha256.New()
	h.Write(dhtSeedContext)
	h.Write(idEntropy)
	seed := h.Sum(nil)
	priv = ed25519.NewKeyFromSeed(seed)
	pub = priv.Public().(ed25519.PublicKey)
	return priv, pub, nil
}
