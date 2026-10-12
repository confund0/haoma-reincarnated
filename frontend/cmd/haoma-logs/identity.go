package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

const onionLen = 56

func looksLikeOnion(v string) bool {
	v = strings.TrimSuffix(v, ".onion")
	if p, ok := strings.CutSuffix(v, shortMark); ok {
		return len(p) == shortLen && isBase32(p)
	}
	return len(v) == onionLen && isBase32(v)
}

func isBase32(v string) bool {
	for i := 0; i < len(v); i++ {
		c := v[i]
		if !(c >= 'a' && c <= 'z' || c >= '2' && c <= '7') {
			return false
		}
	}
	return true
}

type pairing struct {
	id       string
	ownOnion string
	farOnion string
	sibling  bool
	farAcct  string
	farID    string
}

type identity struct {
	account     string
	dir         string
	fingerprint string
	peers       map[string]*pairing
	order       []string
}

func (id *identity) peer(rawID string) *pairing {
	c := canonicalID(rawID)
	p, ok := id.peers[c]
	if !ok {
		p = &pairing{id: rawID}
		id.peers[c] = p
		id.order = append(id.order, c)
	}
	if len(rawID) > len(p.id) {
		p.id = rawID
	}
	return p
}

type identityMap struct {
	root     string
	accounts []*identity
	byName   map[string]*identity
}

func (m *identityMap) account(name, dir string) *identity {
	if id, ok := m.byName[name]; ok {
		return id
	}
	id := &identity{account: name, dir: dir, peers: map[string]*pairing{}}
	m.byName[name] = id
	m.accounts = append(m.accounts, id)
	return id
}

func buildIdentityMap(root string, srcs []source, unknown *unknownKeys) (*identityMap, error) {
	m := &identityMap{root: root, byName: map[string]*identity{}}
	dirOf := map[string]string{}
	for _, s := range srcs {
		if _, ok := dirOf[s.account]; !ok {
			dirOf[s.account] = dirName(s.path)
		}
	}
	err := eachEntry(srcs, func(e entry) bool {
		if unknown != nil {
			unknown.note(e)
		}
		id := m.account(e.account, dirOf[e.account])

		if fp := e.get("fingerprint"); fp != "" && id.fingerprint == "" {
			id.fingerprint = fp
		}
		peerID := e.get("peer_id")
		if peerID != "" {
			if svc := e.get("service_id"); svc != "" {
				id.peer(peerID).ownOnion = svc
			}
			if from := e.get("from"); looksLikeOnion(from) {
				id.peer(peerID).farOnion = from
			}
		}
		for _, a := range e.attrs {
			if a.key == "sibling_peer_id" && a.val != "" {
				id.peer(a.val).sibling = true
			}
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	m.resolve()
	return m, nil
}

func dirName(path string) string {
	if i := strings.LastIndexByte(path, '/'); i > 0 {
		return path[:i]
	}
	return path
}

func (m *identityMap) resolve() {
	type side struct {
		acct string
		id   string
	}
	served := map[string]side{}
	for _, id := range m.accounts {
		for _, c := range id.order {
			p := id.peers[c]
			if p.ownOnion == "" {
				continue
			}
			served[canonicalID(p.ownOnion)] = side{acct: id.account, id: p.id}
		}
	}
	for _, id := range m.accounts {
		for _, c := range id.order {
			p := id.peers[c]
			if p.farOnion == "" {
				continue
			}
			if s, ok := served[canonicalID(p.farOnion)]; ok {
				p.farAcct = s.acct
				p.farID = s.id
			}
		}
	}

	for _, id := range m.accounts {
		for _, c := range id.order {
			p := id.peers[c]
			if !p.sibling || p.farAcct == "" {
				continue
			}
			if far, ok := m.byName[p.farAcct]; ok {
				far.peer(p.farID).sibling = true
			}
		}
	}
}

func (m *identityMap) describe(account, peerID string) string {
	id, ok := m.byName[account]
	if !ok {
		return "unknown account"
	}
	p, ok := id.peers[canonicalID(peerID)]
	if !ok {
		return "not in this account's logs"
	}
	switch {
	case p.sibling && p.farAcct != "":
		return "sibling link with " + p.farAcct + ", which calls it " + short(p.farID)
	case p.sibling:
		return "sibling link, far side not seen in this run"
	case p.farAcct != "":
		return "pairing with " + p.farAcct + ", which calls it " + short(p.farID)
	default:
		return "far side not seen in this run"
	}
}

func (m *identityMap) write(w io.Writer) {
	fmt.Fprintf(w, "Identity map — %d accounts under %s\n", len(m.accounts), m.root)
	names := make([]string, 0, len(m.accounts))
	for _, id := range m.accounts {
		names = append(names, id.account)
	}
	sort.Strings(names)
	for _, name := range names {
		id := m.byName[name]
		fmt.Fprintf(w, "\n%s  (%s)\n", id.account, id.dir)
		if id.fingerprint != "" {
			fmt.Fprintf(w, "  identity fingerprint %s\n", id.fingerprint)
		}
		if len(id.order) == 0 {
			fmt.Fprintln(w, "  no peer records in these logs")
			continue
		}
		keys := append([]string(nil), id.order...)
		sort.Strings(keys)
		for _, c := range keys {
			p := id.peers[c]
			onion := "onion not in these logs"
			if p.ownOnion != "" {
				onion = "serves " + short(p.ownOnion)
			}
			fmt.Fprintf(w, "  peer id %-12s %-24s %s\n",
				short(p.id), onion, m.describe(id.account, p.id))
		}
	}
	fmt.Fprintln(w)
}
