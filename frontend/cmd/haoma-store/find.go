package main

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/dgraph-io/badger/v4"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/store"
)

const msgIDIndexPrefix = "evt-msgid:"

type findFilter struct {
	msgID    string
	text     string
	file     string
	conv     string
	delivery string
	read     string
	when     window
}

func (f findFilter) empty() bool {
	return f.msgID == "" && f.text == "" && f.file == "" && f.conv == "" &&
		f.delivery == "" && f.read == "" && f.when.unset()
}

func (f findFilter) describe() []string {
	var out []string
	if f.msgID != "" {
		out = append(out, "message id "+f.msgID)
	}
	if f.text != "" {
		out = append(out, fmt.Sprintf("text holding %q", f.text))
	}
	if f.file != "" {
		out = append(out, fmt.Sprintf("file name or caption holding %q", f.file))
	}
	if f.conv != "" {
		out = append(out, fmt.Sprintf("conversation matching %q", f.conv))
	}
	if f.delivery != "" {
		out = append(out, "delivery state "+f.delivery)
	}
	switch f.read {
	case "yes":
		out = append(out, "read")
	case "no":
		out = append(out, "not read")
	}
	if !f.when.unset() {
		out = append(out, f.when.describe())
	}
	if len(out) == 0 {
		out = append(out, "everything")
	}
	return out
}

type hit struct {
	row      msgRow
	slot     string
	sideName string

	convTitle string

	who string

	arrivedFrom string

	file *fileRow

	onlyHere bool
}

type findResult struct {
	hits    []hit
	scanned int

	siblings bool

	ownerA, ownerB ownership
}

func readMessageIDs(st *store.Store) (map[string]bool, error) {
	out := map[string]bool{}
	err := st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(msgIDIndexPrefix)
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.ValidForPrefix([]byte(msgIDIndexPrefix)); it.Next() {
			out[string(it.Item().Key()[len(msgIDIndexPrefix):])] = true
		}
		return nil
	})
	return out, err
}

func scanSide(idx *sideIndex, slot string, f findFilter, files map[string]fileRow) ([]hit, int, error) {
	var hits []hit
	scanned := 0
	err := streamMessages(idx.side.st, true, func(r msgRow) error {
		scanned++
		if !matches(r, f, idx, files) {
			return nil
		}
		h := hit{
			row:         r,
			slot:        slot,
			sideName:    idx.side.name,
			convTitle:   conversationName(r.ChatID, idx),
			who:         senderName(r, idx),
			arrivedFrom: arrivedFromName(r, idx),
		}
		if fr, held := files[r.MsgID]; held {
			copyOf := fr
			h.file = &copyOf
		}
		hits = append(hits, h)
		return nil
	})
	return hits, scanned, err
}

func matches(r msgRow, f findFilter, idx *sideIndex, files map[string]fileRow) bool {
	if f.msgID != "" && !sameID(r.MsgID, f.msgID) {
		return false
	}
	if !f.when.holds(r.DisplayTs) {
		return false
	}
	if f.text != "" && !holdsFold(r.Text, f.text) {
		return false
	}
	if f.file != "" {
		fr, held := files[r.MsgID]
		hitOnRow := held && holdsFold(fr.OriginalName, f.file)
		if !holdsFold(r.FileName, f.file) && !holdsFold(r.FileCaption, f.file) && !hitOnRow {
			return false
		}
	}
	if f.conv != "" && !conversationMatches(r.ChatID, f.conv, idx) {
		return false
	}
	if f.delivery != "" && !strings.EqualFold(stateOrUnset(r.DeliveryState), f.delivery) {
		return false
	}
	switch f.read {
	case "yes":
		if !isRead(r) {
			return false
		}
	case "no":
		if isRead(r) {
			return false
		}
	}
	return true
}

func isRead(r msgRow) bool {
	if r.Direction == "in" {
		return r.ReadAt != 0
	}
	return r.DeliveryState == "read"
}

func holdsFold(haystack, needle string) bool {
	if haystack == "" {
		return false
	}
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

func conversationMatches(id chat.ChatID, want string, idx *sideIndex) bool {
	if holdsFold(string(id), want) {
		return true
	}
	c, held := idx.chatByID[id]
	if !held {
		return false
	}
	if holdsFold(c.GroupName, want) {
		return true
	}
	if fp := idx.names.personFor(c); fp != "" {
		if holdsFold(fp, want) {
			return true
		}
		if name, ok := idx.names.byFingerprint[fp]; ok && holdsFold(name, want) {
			return true
		}
	}
	for _, peerID := range c.PeerIDs {
		if holdsFold(peerID, want) {
			return true
		}
		if name, ok := idx.names.byPeerID[peerID]; ok && holdsFold(name, want) {
			return true
		}
	}
	return false
}

func conversationName(id chat.ChatID, idx *sideIndex) string {
	c, held := idx.chatByID[id]
	if !held {
		return "conversation " + shortID(string(id))
	}
	return idx.names.nameForChat(c)
}

func arrivedFromName(r msgRow, idx *sideIndex) string {
	if r.Direction != "in" {
		return ""
	}
	if r.ArrivedFromPeerID == "" {
		return "no device recorded — answers to this row go to the conversation's stored device"
	}
	out := shortID(r.ArrivedFromPeerID)
	if name := idx.names.forPeerID(r.ArrivedFromPeerID); name != "" {
		out += " = " + name
	}
	if at, held := idx.chatByID[r.ChatID].PairedAt[r.ArrivedFromPeerID]; held {
		out += ", paired at " + pairedAtText(at)
	}
	return out
}

func pairedAtText(at int64) string {
	if at == 0 {
		return "unknown (a record from before the stamp)"
	}
	return stamp(at)
}

func senderName(r msgRow, idx *sideIndex) string {
	if r.Direction == "out" {
		return "me"
	}
	if name := idx.names.forPeerID(r.SenderPeerID); name != "" {
		return name
	}
	return "them"
}

func readFileRows(st *store.Store) (map[string]fileRow, error) {
	out := map[string]fileRow{}
	err := streamFiles(st, func(r fileRow) error {
		if r.MsgID != "" {
			out[r.MsgID] = r
		}
		return nil
	})
	return out, err
}

func runFind(sides []*side, f findFilter, idsOnly bool, out io.Writer) (*findResult, error) {
	res := &findResult{}

	indexes := make([]*sideIndex, len(sides))
	fileRows := make([]map[string]fileRow, len(sides))
	idSets := make([]map[string]bool, len(sides))
	slots := []string{"a", "b"}

	for i, s := range sides {
		idx, err := readSide(s)
		if err != nil {
			return nil, err
		}
		indexes[i] = idx
		fr, err := readFileRows(s.st)
		if err != nil {
			return nil, fmt.Errorf("%s: read file rows: %w", s.name, err)
		}
		fileRows[i] = fr
		ids, err := readMessageIDs(s.st)
		if err != nil {
			return nil, fmt.Errorf("%s: read message ids: %w", s.name, err)
		}
		idSets[i] = ids
	}

	if len(sides) == 2 {
		res.ownerA, res.ownerB = ownershipOf(indexes[0]), ownershipOf(indexes[1])
		res.siblings = sameOwner(res.ownerA, res.ownerB)
	}

	for i, idx := range indexes {
		hits, scanned, err := scanSide(idx, slots[i], f, fileRows[i])
		if err != nil {
			return nil, fmt.Errorf("%s: search messages: %w", idx.side.name, err)
		}
		res.scanned += scanned
		if len(sides) == 2 && res.siblings {
			other := idSets[1-i]
			for j := range hits {
				hits[j].onlyHere = !other[hits[j].row.MsgID]
			}
		}
		res.hits = append(res.hits, hits...)
	}

	sort.SliceStable(res.hits, func(i, j int) bool {
		if res.hits[i].row.DisplayTs != res.hits[j].row.DisplayTs {
			return res.hits[i].row.DisplayTs < res.hits[j].row.DisplayTs
		}
		if res.hits[i].row.MsgID != res.hits[j].row.MsgID {
			return res.hits[i].row.MsgID < res.hits[j].row.MsgID
		}
		return res.hits[i].slot < res.hits[j].slot
	})

	if idsOnly {
		renderIDsOnly(out, res)
		return res, nil
	}
	renderFind(out, sides, f, res)
	return res, nil
}

func renderIDsOnly(out io.Writer, res *findResult) {
	seen := map[string]bool{}
	for _, h := range res.hits {
		if h.row.MsgID == "" || seen[h.row.MsgID] {
			continue
		}
		seen[h.row.MsgID] = true
		fmt.Fprintln(out, h.row.MsgID)
	}
}

func renderFind(out io.Writer, sides []*side, f findFilter, res *findResult) {
	fmt.Fprintln(out, "store find")
	fmt.Fprintln(out)
	for i, s := range sides {
		fmt.Fprintf(out, "  %s  %-24s %s\n", []string{"a", "b"}[i], s.name, s.dataDir)
	}
	fmt.Fprintln(out)
	if len(sides) == 2 {
		if res.siblings {
			fmt.Fprintln(out, "  These two stores are two devices of one person, so a message missing")
			fmt.Fprintln(out, "  from one of them is marked.")
		} else {
			fmt.Fprintln(out, "  These two stores are NOT two devices of one person. Nothing is marked")
			fmt.Fprintln(out, "  as being on one side only: two people's stores share the message ids")
			fmt.Fprintln(out, "  of an exchange between them, so the mark would say nothing.")
			fmt.Fprintln(out, "  Use the diff for two devices of one person; it refuses this pair.")
		}
		fmt.Fprintln(out)
	}
	fmt.Fprintf(out, "  looking for: %s\n", strings.Join(f.describe(), "; "))
	fmt.Fprintf(out, "  read %d message rows\n", res.scanned)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  This prints the text of your messages.")
	fmt.Fprintln(out)

	if len(res.hits) == 0 {
		fmt.Fprintln(out, "0 hits.")
		if f.empty() {
			fmt.Fprintln(out, "No filter was given, and nothing was found, so this store holds no messages.")
		}
		return
	}

	for _, h := range res.hits {
		fmt.Fprintln(out, h.row.MsgID)
		mark := "in " + h.slot
		if h.onlyHere {
			mark = "only in " + h.slot
		}
		state := stateOrUnset(h.row.DeliveryState)
		readMark := "not read"
		if isRead(h.row) {
			readMark = "read"
		}
		fmt.Fprintf(out, "    %-10s %-16s %-4s %-6s %s   delivery %s   %s\n",
			mark, h.convTitle, h.row.Direction, h.row.Kind, stamp(h.row.DisplayTs), state, readMark)
		fmt.Fprintf(out, "    sent by %s\n", h.who)
		if h.arrivedFrom != "" {
			fmt.Fprintf(out, "    arrived from %s\n", h.arrivedFrom)
		}
		if h.row.FileName != "" {
			transfer := "no file row held"
			if h.file != nil {
				transfer = "transfer " + stateOrUnset(h.file.State)
			}
			fmt.Fprintf(out, "    file %q   %s\n", h.row.FileName, transfer)
		}
		if h.row.Text != "" {
			fmt.Fprintf(out, "    %q\n", h.row.Text)
		}
		if h.row.DeletedAt != 0 {
			fmt.Fprintln(out, "    deleted")
		}
		if h.row.EditedAt != 0 {
			fmt.Fprintln(out, "    edited")
		}
		fmt.Fprintln(out)
	}

	fmt.Fprintf(out, "%d hits.\n", len(res.hits))
	fmt.Fprintln(out, "The id above each hit is the full form. Hand it to frontend/cmd/haoma-logs")
	fmt.Fprintln(out, "to see everything that happened to that message on every device.")
}
