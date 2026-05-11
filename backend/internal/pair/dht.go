package pair

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/anacrolix/dht/v2"
	"github.com/anacrolix/dht/v2/bep44"
	"github.com/anacrolix/dht/v2/exts/getput"
	"github.com/anacrolix/dht/v2/krpc"
	"github.com/anacrolix/torrent/bencode"
)

var bep44Salt = []byte("haoma-pair-v1")

var dhtSeedContext = []byte("haoma-pair-ed25519-seed/v1")

type DHT struct {
	server *dht.Server
	conn   net.PacketConn
}

func StartDHT(ctx context.Context) (*DHT, error) {
	conn, err := net.ListenPacket("udp", ":0")
	if err != nil {
		return nil, fmt.Errorf("pair: dht listen: %w", err)
	}
	cfg := dht.NewDefaultServerConfig()
	cfg.Conn = conn
	cfg.StartingNodes = func() ([]dht.Addr, error) {
		return dht.GlobalBootstrapAddrs("udp")
	}
	srv, err := dht.NewServer(cfg)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("pair: dht server: %w", err)
	}
	slog.Debug("pair: dht client starting", slog.String("addr", srv.Addr().String()))
	if _, err := srv.BootstrapContext(ctx); err != nil {
		srv.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("pair: dht bootstrap: %w", err)
	}
	slog.Debug("pair: dht bootstrapped", slog.Int("nodes", srv.NumNodes()))
	return &DHT{server: srv, conn: conn}, nil
}

func (d *DHT) Close() {
	if d == nil {
		return
	}
	if d.server != nil {
		d.server.Close()
	}
	if d.conn != nil {
		_ = d.conn.Close()
	}
}

func (d *DHT) Publish(ctx context.Context, idEntropy, value []byte) ([]byte, error) {
	priv, pub, err := seedToEd25519(idEntropy)
	if err != nil {
		return nil, err
	}
	var pubArr [32]byte
	copy(pubArr[:], pub)
	target := bep44.MakeMutableTarget(pubArr, bep44Salt)
	seq := time.Now().Unix()
	seqToPut := func(existing int64) bep44.Put {
		if existing >= seq {
			seq = existing + 1
		}
		put := bep44.Put{
			V:    value,
			K:    &pubArr,
			Salt: bep44Salt,
			Seq:  seq,
		}
		put.Sign(priv)
		return put
	}
	stats, err := getput.Put(ctx, krpc.ID(target), d.server, bep44Salt, seqToPut)
	if err != nil {
		return nil, fmt.Errorf("pair: dht put: %w", err)
	}
	slog.Debug("pair: dht put",
		slog.String("target", fmt.Sprintf("%x", target[:])),
		slog.Int64("seq", seq),
		slog.Uint64("contacted", uint64(stats.NumAddrsTried)),
		slog.Uint64("responded", uint64(stats.NumResponses)),
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
	target := bep44.MakeMutableTarget(pubArr, bep44Salt)
	res, stats, err := getput.Get(ctx, target, d.server, nil, bep44Salt)
	if err != nil {
		if err.Error() == "value not found" {
			return nil, ErrItemNotFound
		}
		return nil, fmt.Errorf("pair: dht get: %w", err)
	}
	slog.Debug("pair: dht get",
		slog.String("target", fmt.Sprintf("%x", target[:])),
		slog.Int64("seq", res.Seq),
		slog.Uint64("contacted", uint64(stats.NumAddrsTried)),
		slog.Uint64("responded", uint64(stats.NumResponses)),
	)

	var out []byte
	if err := bencode.Unmarshal(res.V, &out); err != nil {
		return nil, fmt.Errorf("pair: bencode unmarshal value: %w", err)
	}
	return out, nil
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
