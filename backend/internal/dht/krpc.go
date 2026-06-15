package dht

import (
	"encoding/binary"
	"fmt"
	"net/netip"

	"haoma/internal/bencode"
)

const (
	yQuery    = "q"
	yResponse = "r"
	yError    = "e"
)

const (
	qPing     = "ping"
	qFindNode = "find_node"
	qGet      = "get"
	qPut      = "put"
)

type krpcMsg struct {
	T  string        `bencode:"t"`
	Y  string        `bencode:"y"`
	Q  string        `bencode:"q,omitempty"`
	A  *krpcArgs     `bencode:"a,omitempty"`
	R  *krpcReturn   `bencode:"r,omitempty"`
	E  bencode.Bytes `bencode:"e,omitempty"`
	V  string        `bencode:"v,omitempty"`
	RO int           `bencode:"ro,omitempty"`
	IP []byte        `bencode:"ip,omitempty"`
}

type krpcArgs struct {
	ID     ID     `bencode:"id"`
	Target ID     `bencode:"target,omitempty"`
	Token  string `bencode:"token,omitempty"`

	V    []byte `bencode:"v,omitempty"`
	Seq  *int64 `bencode:"seq,omitempty"`
	Cas  int64  `bencode:"cas,omitempty"`
	K    []byte `bencode:"k,omitempty"`
	Salt []byte `bencode:"salt,omitempty"`
	Sig  []byte `bencode:"sig,omitempty"`
}

type krpcReturn struct {
	ID    ID            `bencode:"id"`
	Nodes []byte        `bencode:"nodes,omitempty"`
	Token string        `bencode:"token,omitempty"`
	V     bencode.Bytes `bencode:"v,omitempty"`
	K     []byte        `bencode:"k,omitempty"`
	Sig   []byte        `bencode:"sig,omitempty"`
	Seq   *int64        `bencode:"seq,omitempty"`
}

const compactNodeSize = 26

func parseCompactNodes(b []byte) ([]struct {
	ID   ID
	Addr netip.AddrPort
}, error,
) {
	if len(b)%compactNodeSize != 0 {
		return nil, fmt.Errorf("dht: compact nodes blob %d not multiple of %d", len(b), compactNodeSize)
	}
	out := make([]struct {
		ID   ID
		Addr netip.AddrPort
	}, 0, len(b)/compactNodeSize)
	for off := 0; off < len(b); off += compactNodeSize {
		rec := b[off : off+compactNodeSize]
		var id ID
		copy(id[:], rec[:20])
		ip := netip.AddrFrom4([4]byte{rec[20], rec[21], rec[22], rec[23]})
		port := binary.BigEndian.Uint16(rec[24:26])

		if port == 0 || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLoopback() {
			continue
		}
		out = append(out, struct {
			ID   ID
			Addr netip.AddrPort
		}{id, netip.AddrPortFrom(ip, port)})
	}
	return out, nil
}

func errorFromBencode(b bencode.Bytes) (int, string) {
	if len(b) == 0 {
		return 0, ""
	}
	var raw any
	if err := bencode.Unmarshal(b, &raw); err != nil {
		return 0, fmt.Sprintf("undecodable error: %v", err)
	}
	switch v := raw.(type) {
	case []any:
		if len(v) < 2 {
			return 0, fmt.Sprintf("malformed error list: %v", v)
		}
		code, _ := v[0].(int64)
		msg, _ := v[1].(string)
		return int(code), msg
	case string:
		return 0, v
	default:
		return 0, fmt.Sprintf("unexpected error shape: %T", raw)
	}
}
