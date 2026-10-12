package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/peerstate"
)

const clockTolerance = 5

const clockToleranceName = "the clock tolerance"

var notCompared = []string{
	"chat ids, peer ids and envelope ids — each device mints its own",
	"which device of the contact a message arrived from — a peer id, and the message reached each of my devices over a different one",
	"which device a conversation arrived from — the person marker on a conversation records that, and a conversation born of an ordinary pairing carries none",
	"receive sequence numbers — each device counts its own arrivals",
	"the mirrored-from-sibling marker — true on one device and false on the other for the same message, by design",
	"file tokens, blob paths and sealed paths — each device holds its own copy of a file",
	"a conversation's group alias, per-chat nick and notification mute — carried to a sibling once, when it first adopts the conversation, and never converged after that",
	"whether read receipts are switched off for a conversation — decided to converge and not built, so the two devices are expected to differ today",
	"unread counts and last-activity times — each device derives its own from rows it holds",
}

type findingClass int

const (
	mustAgree findingClass = iota

	clockStamped

	oneSideOnly
)

type difference struct {
	subject string
	field   string
	a       string
	b       string
	class   findingClass

	gap int64
}

type tolerated struct {
	subject string
	field   string
	gap     int64
}

type onlyOne struct {
	slot        string
	subject     string
	description string
	class       findingClass
}

func oneSided(slot, subject, description string) onlyOne {
	return onlyOne{slot: slot, subject: subject, description: description, class: oneSideOnly}
}

type section struct {
	title  string
	only   []onlyOne
	differ []difference
}

type tally struct {
	aTotal  int
	bTotal  int
	matched int
	onlyA   int
	onlyB   int
	differ  int

	unjoinable int

	unnameable int
}

type joinState int

const (
	joinNothingThere joinState = iota

	joinCompared

	joinOneSided

	joinFailed
)

func (t tally) state() joinState {
	switch {
	case t.matched > 0:
		return joinCompared
	case t.onlyA > 0 || t.onlyB > 0:
		return joinOneSided
	case t.unnameable > 0 || t.unjoinable > 0:
		return joinFailed
	case t.aTotal == 0 && t.bTotal == 0:
		return joinNothingThere
	case t.aTotal == 0 || t.bTotal == 0:
		return joinOneSided
	default:
		return joinFailed
	}
}

func (t tally) line(label string) string {
	base := fmt.Sprintf("  %-18s %5d matched, %d only in a, %d only in b, %d differ",
		label, t.matched, t.onlyA, t.onlyB, t.differ)
	if t.unjoinable > 0 {
		base += fmt.Sprintf(", %d not joinable", t.unjoinable)
	}
	if t.unnameable > 0 {
		base += fmt.Sprintf(", %d could not be placed", t.unnameable)
	}
	if t.state() == joinFailed {
		base += "   <- NOT COMPARED, see below"
	}
	return base
}

type diffResult struct {
	sections []*section

	conversations tally
	messages      tally
	files         tally
	contacts      tally

	deviceReadout *deviceReadout

	unnameable []unnameableChat

	tolerated []tolerated

	ownerA, ownerB ownership
}

type unnameableChat struct {
	slot string
	row  chatRow
}

type deviceReadout struct {
	a       []string
	b       []string
	finding string
}

type namedTally struct {
	label string

	why string
	t   tally
}

func (r *diffResult) categories() []namedTally {
	return []namedTally{
		{"conversations", "a direct conversation joins on the person its pairings address, and failing that on its chat id", r.conversations},
		{"messages", "a message joins on its message id", r.messages},
		{"file rows", "a file row joins on its message id", r.files},
		{"contacts", "a contact joins on the person's identity fingerprint", r.contacts},
	}
}

func (r *diffResult) comparedAnything() bool {
	for _, c := range r.categories() {
		switch c.t.state() {
		case joinCompared, joinOneSided:
			return true
		}
	}
	return false
}

func (r *diffResult) notComparable() []namedTally {
	var out []namedTally
	for _, c := range r.categories() {
		if c.t.state() == joinFailed || c.t.unjoinable > 0 || c.t.unnameable > 0 {
			out = append(out, c)
		}
	}
	return out
}

func (r *diffResult) mustAgreeCount() int {
	n := 0
	for _, s := range r.sections {
		for _, d := range s.differ {
			if d.class == mustAgree {
				n++
			}
		}
	}
	return n
}

func (r *diffResult) skewCount() int {
	n := 0
	for _, s := range r.sections {
		for _, d := range s.differ {
			if d.class == clockStamped {
				n++
			}
		}
	}
	return n
}

func (r *diffResult) oneSidedCount() int {
	n := 0
	for _, s := range r.sections {
		for _, o := range s.only {
			if o.class == oneSideOnly {
				n++
			}
		}
	}
	return n
}

func (r *diffResult) needsReading() bool {
	return r.skewCount() > 0 || r.oneSidedCount() > 0
}

type sideIndex struct {
	side *side

	chatList []chatRow

	chatByID map[chat.ChatID]chatRow

	sectionKeyByChatID map[chat.ChatID]string

	names   *nameBook
	people  map[string]peerstate.Person
	devices []string

	identity             string
	siblingsOnMyIdentity int
}

func (idx *sideIndex) sectionFor(id chat.ChatID) string {
	if key, held := idx.sectionKeyByChatID[id]; held {
		return key
	}
	if c, held := idx.chatByID[id]; held {
		return joinKeysFor(c, idx).sectionKey()
	}
	return "chat:" + string(id)
}

func readSide(s *side) (*sideIndex, error) {
	chats, err := readChats(s.st)
	if err != nil {
		return nil, fmt.Errorf("%s: read conversations: %w", s.name, err)
	}
	idx := &sideIndex{
		side:               s,
		chatList:           chats,
		chatByID:           map[chat.ChatID]chatRow{},
		sectionKeyByChatID: map[chat.ChatID]string{},
		people:             map[string]peerstate.Person{},
	}
	for _, c := range chats {
		idx.chatByID[c.ID] = c
	}

	people, err := readContacts(s.st)
	if err != nil {
		return nil, fmt.Errorf("%s: read contacts: %w", s.name, err)
	}
	for _, p := range people {
		idx.people[p.Fingerprint] = p
	}
	idx.names = buildNameBook(people)

	set, err := readLinkedDevices(s.st)
	if err != nil {
		return nil, fmt.Errorf("%s: read linked devices: %w", s.name, err)
	}
	idx.devices = set.Members()

	idx.identity, err = readIdentity(s.st)
	if err != nil {
		return nil, fmt.Errorf("%s: read identity: %w", s.name, err)
	}
	if idx.identity != "" {
		idx.siblingsOnMyIdentity, err = countSiblingsOnMyIdentity(s.st, idx.devices, idx.identity)
		if err != nil {
			return nil, fmt.Errorf("%s: check siblings against identity: %w", s.name, err)
		}
	}

	return idx, nil
}

const (
	setContacts = "\x00contacts"
	setDevices  = "\x00devices"
)

func conversationTitle(key string, idxA, idxB *sideIndex) string {
	switch {
	case strings.HasPrefix(key, "person:"):
		fp := strings.TrimPrefix(key, "person:")
		name := idxA.names.forFingerprint(fp)
		if name == shortID(fp) {
			name = idxB.names.forFingerprint(fp)
		}
		return fmt.Sprintf("%s  (fingerprint %s)", name, shortID(fp))
	case strings.HasPrefix(key, "chat:"):
		id := chat.ChatID(strings.TrimPrefix(key, "chat:"))
		for _, idx := range []*sideIndex{idxA, idxB} {
			if c, held := idx.chatByID[id]; held {
				return fmt.Sprintf("%s  (chat id %s)", idx.names.nameForChat(c), shortID(string(id)))
			}
		}
		return "conversation " + shortID(string(id))
	case key == setContacts:
		return "contacts"
	case key == setDevices:
		return "linked devices"
	}
	return key
}

func runDiff(a, b *side, out io.Writer) (*diffResult, error) {
	idxA, err := readSide(a)
	if err != nil {
		return nil, err
	}
	idxB, err := readSide(b)
	if err != nil {
		return nil, err
	}

	ownA, ownB := ownershipOf(idxA), ownershipOf(idxB)
	if err := checkSameOwner(ownA, ownB); err != nil {
		return nil, err
	}

	res := &diffResult{ownerA: ownA, ownerB: ownB}

	byKey := map[string]*section{}
	sectionFor := func(key string) *section {
		if s, ok := byKey[key]; ok {
			return s
		}
		s := &section{title: conversationTitle(key, idxA, idxB)}
		byKey[key] = s
		res.sections = append(res.sections, s)
		return s
	}

	diffConversations(idxA, idxB, res, sectionFor)
	if err := diffMessages(idxA, idxB, res, sectionFor); err != nil {
		return nil, err
	}
	if err := diffFiles(idxA, idxB, res, sectionFor); err != nil {
		return nil, err
	}
	diffContacts(idxA, idxB, res, sectionFor)
	readOutDevices(idxA, idxB, res)

	render(out, a, b, res)
	return res, nil
}

type convJoin struct {
	row       chatRow
	personKey string
	chatKey   string
}

func (c convJoin) sectionKey() string {
	if c.personKey != "" {
		return c.personKey
	}
	return c.chatKey
}

func joinKeysFor(c chatRow, idx *sideIndex) convJoin {
	out := convJoin{row: c, chatKey: "chat:" + string(c.ID)}
	if c.Kind == string(chat.KindGroup) {

		return out
	}
	if fp := idx.names.personFor(c); fp != "" {
		out.personKey = "person:" + fp
	}
	return out
}

type conversationPairing struct {
	a, b chatRow
	how  string
}

func diffConversations(a, b *sideIndex, res *diffResult, sectionFor func(string) *section) {
	aJoins := make([]convJoin, 0, len(a.chatList))
	for _, c := range a.chatList {
		aJoins = append(aJoins, joinKeysFor(c, a))
	}
	bJoins := make([]convJoin, 0, len(b.chatList))
	for _, c := range b.chatList {
		bJoins = append(bJoins, joinKeysFor(c, b))
	}
	res.conversations.aTotal = len(aJoins)
	res.conversations.bTotal = len(bJoins)

	takenB := make([]bool, len(bJoins))
	matchedA := make([]bool, len(aJoins))
	var pairs []conversationPairing

	byPersonB := map[string]int{}
	for j, bj := range bJoins {
		if bj.personKey != "" {
			byPersonB[bj.personKey] = j
		}
	}
	for i, aj := range aJoins {
		if aj.personKey == "" {
			continue
		}
		j, held := byPersonB[aj.personKey]
		if !held || takenB[j] {
			continue
		}
		takenB[j], matchedA[i] = true, true
		pairs = append(pairs, conversationPairing{a: aj.row, b: bJoins[j].row, how: "the person"})
	}

	byChatB := map[string]int{}
	for j, bj := range bJoins {
		if !takenB[j] {
			byChatB[bj.chatKey] = j
		}
	}
	for i, aj := range aJoins {
		if matchedA[i] {
			continue
		}
		j, held := byChatB[aj.chatKey]
		if !held || takenB[j] {
			continue
		}
		takenB[j], matchedA[i] = true, true
		pairs = append(pairs, conversationPairing{a: aj.row, b: bJoins[j].row, how: "the chat id"})
	}

	for _, pair := range pairs {
		res.conversations.matched++
		key := joinKeysFor(pair.a, a).sectionKey()
		a.sectionKeyByChatID[pair.a.ID] = key
		b.sectionKeyByChatID[pair.b.ID] = key

		var diffs []difference
		if pair.a.ID != pair.b.ID {
			diffs = append(diffs, difference{
				subject: "conversation",
				field:   "chat id (the same person, two ids: backfill has not carried one across)",
				a:       string(pair.a.ID), b: string(pair.b.ID),
			})
		}
		diffs = appendIfDifferent(diffs, "conversation", "retention seconds", pair.a.RetentionTTL, pair.b.RetentionTTL)

		diffs = appendClock(diffs, res, "conversation", "read cursor", pair.a.LastReadAt, pair.b.LastReadAt)
		diffs = appendIfDifferent(diffs, "conversation", "group name", pair.a.GroupName, pair.b.GroupName)
		if len(diffs) > 0 {
			res.conversations.differ++
			sectionFor(key).differ = append(sectionFor(key).differ, diffs...)
		}
	}

	for i, aj := range aJoins {
		if matchedA[i] {
			continue
		}
		a.sectionKeyByChatID[aj.row.ID] = aj.sectionKey()
		if aj.personKey == "" {
			res.conversations.unnameable++
			res.unnameable = append(res.unnameable, unnameableChat{slot: "a", row: aj.row})
			continue
		}
		res.conversations.onlyA++
		sectionFor(aj.sectionKey()).only = append(sectionFor(aj.sectionKey()).only, oneSided("a", "conversation", describeChat(aj.row)))
	}
	for j, bj := range bJoins {
		if takenB[j] {
			continue
		}
		b.sectionKeyByChatID[bj.row.ID] = bj.sectionKey()
		if bj.personKey == "" {
			res.conversations.unnameable++
			res.unnameable = append(res.unnameable, unnameableChat{slot: "b", row: bj.row})
			continue
		}
		res.conversations.onlyB++
		sectionFor(bj.sectionKey()).only = append(sectionFor(bj.sectionKey()).only, oneSided("b", "conversation", describeChat(bj.row)))
	}
}

func describeChat(c chatRow) string {
	if c.Kind == string(chat.KindGroup) {
		name := c.GroupName
		if name == "" {
			name = "no name"
		}
		return fmt.Sprintf("group %q, chat id %s", name, shortID(string(c.ID)))
	}
	return fmt.Sprintf("chat id %s", shortID(string(c.ID)))
}

func diffMessages(a, b *sideIndex, res *diffResult, sectionFor func(string) *section) error {
	held := map[string]msgRow{}
	if err := streamMessages(a.side.st, false, func(r msgRow) error {
		if r.MsgID == "" {
			res.messages.unjoinable++
			return nil
		}
		res.messages.aTotal++
		held[r.MsgID] = r
		return nil
	}); err != nil {
		return fmt.Errorf("%s: read messages: %w", a.side.name, err)
	}

	seen := map[string]bool{}
	if err := streamMessages(b.side.st, false, func(rb msgRow) error {
		if rb.MsgID == "" {
			res.messages.unjoinable++
			return nil
		}
		res.messages.bTotal++
		ra, both := held[rb.MsgID]
		if !both {
			res.messages.onlyB++
			key := b.sectionFor(rb.ChatID)
			sectionFor(key).only = append(sectionFor(key).only, oneSided("b", "message "+rb.MsgID, describeMessage(rb, b)))
			return nil
		}
		seen[rb.MsgID] = true
		res.messages.matched++
		if diffs := compareMessages(ra, rb, res); len(diffs) > 0 {
			res.messages.differ++
			key := a.sectionFor(ra.ChatID)
			sectionFor(key).differ = append(sectionFor(key).differ, diffs...)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("%s: read messages: %w", b.side.name, err)
	}

	for id, ra := range held {
		if seen[id] {
			continue
		}
		res.messages.onlyA++
		key := a.sectionFor(ra.ChatID)
		sectionFor(key).only = append(sectionFor(key).only, oneSided("a", "message "+id, describeMessage(ra, a)))
	}
	return nil
}

func compareMessages(a, b msgRow, res *diffResult) []difference {
	subject := "message " + a.MsgID
	var diffs []difference
	diffs = appendIfDifferent(diffs, subject, "kind", a.Kind, b.Kind)
	diffs = appendIfDifferent(diffs, subject, "direction", a.Direction, b.Direction)
	diffs = appendIfDifferent(diffs, subject, "delivery state", stateOrUnset(a.DeliveryState), stateOrUnset(b.DeliveryState))

	diffs = appendClock(diffs, res, subject, "read at", a.ReadAt, b.ReadAt)
	diffs = appendClock(diffs, res, subject, "read receipt sent at", a.ReadReceiptSentAt, b.ReadReceiptSentAt)

	diffs = appendIfDifferent(diffs, subject, "edited at", a.EditedAt, b.EditedAt)
	diffs = appendIfDifferent(diffs, subject, "deleted at", a.DeletedAt, b.DeletedAt)
	diffs = appendIfDifferent(diffs, subject, "expire seconds", a.ExpireSeconds, b.ExpireSeconds)
	diffs = appendIfDifferent(diffs, subject, "decrypt status", a.DecryptStatus, b.DecryptStatus)
	if a.TextDigest != b.TextDigest {
		diffs = append(diffs, difference{
			subject: subject, field: "text",
			a: "digest " + emptyAsNone(a.TextDigest), b: "digest " + emptyAsNone(b.TextDigest),
		})
	}
	diffs = appendIfDifferent(diffs, subject, "file name", a.FileName, b.FileName)
	diffs = appendClock(diffs, res, subject, "shown at", a.DisplayTs, b.DisplayTs)
	return diffs
}

func diffFiles(a, b *sideIndex, res *diffResult, sectionFor func(string) *section) error {
	held := map[string]fileRow{}
	if err := streamFiles(a.side.st, func(r fileRow) error {
		if r.MsgID == "" {
			res.files.unjoinable++
			return nil
		}
		res.files.aTotal++
		held[r.MsgID] = r
		return nil
	}); err != nil {
		return fmt.Errorf("%s: read file rows: %w", a.side.name, err)
	}

	seen := map[string]bool{}
	if err := streamFiles(b.side.st, func(rb fileRow) error {
		if rb.MsgID == "" {
			res.files.unjoinable++
			return nil
		}
		res.files.bTotal++
		ra, both := held[rb.MsgID]
		if !both {
			res.files.onlyB++
			key := b.sectionFor(rb.ChatID)
			sectionFor(key).only = append(sectionFor(key).only, oneSided("b", "file "+rb.MsgID, describeFile(rb)))
			return nil
		}
		seen[rb.MsgID] = true
		res.files.matched++
		subject := "file " + rb.MsgID
		var diffs []difference
		diffs = appendIfDifferent(diffs, subject, "transfer state", stateOrUnset(ra.State), stateOrUnset(rb.State))
		diffs = appendIfDifferent(diffs, subject, "direction", ra.Direction, rb.Direction)
		diffs = appendIfDifferent(diffs, subject, "file name", ra.OriginalName, rb.OriginalName)
		diffs = appendIfDifferent(diffs, subject, "size", ra.Size, rb.Size)
		diffs = appendIfDifferent(diffs, subject, "ciphertext hash", ra.Sha256, rb.Sha256)
		if len(diffs) > 0 {
			res.files.differ++
			key := a.sectionFor(ra.ChatID)
			sectionFor(key).differ = append(sectionFor(key).differ, diffs...)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("%s: read file rows: %w", b.side.name, err)
	}

	for id, ra := range held {
		if seen[id] {
			continue
		}
		res.files.onlyA++
		key := a.sectionFor(ra.ChatID)
		sectionFor(key).only = append(sectionFor(key).only, oneSided("a", "file "+id, describeFile(ra)))
	}
	return nil
}

func describeFile(r fileRow) string {
	return fmt.Sprintf("%s %q, %d bytes, transfer state %s", r.Direction, r.OriginalName, r.Size, stateOrUnset(r.State))
}

func diffContacts(a, b *sideIndex, res *diffResult, sectionFor func(string) *section) {
	res.contacts.aTotal = len(a.people)
	res.contacts.bTotal = len(b.people)
	for fp, pa := range a.people {
		pb, both := b.people[fp]
		if !both {
			res.contacts.onlyA++
			sectionFor(setContacts).only = append(sectionFor(setContacts).only, oneSided("a", "contact "+shortID(fp), describeContact(pa)))
			continue
		}
		res.contacts.matched++
		subject := "contact " + shortID(fp)
		var diffs []difference
		diffs = appendIfDifferent(diffs, subject, "name they call themselves", pa.Meta.Nick, pb.Meta.Nick)
		diffs = appendIfDifferent(diffs, subject, "name I gave them", pa.Meta.Alias, pb.Meta.Alias)
		diffs = appendIfDifferent(diffs, subject, "verified", pa.Meta.Verified, pb.Meta.Verified)
		diffs = appendIfDifferent(diffs, subject, "blocked", pa.Meta.Blocked, pb.Meta.Blocked)
		if len(diffs) > 0 {
			res.contacts.differ++
			sectionFor(setContacts).differ = append(sectionFor(setContacts).differ, diffs...)
		}
	}
	for fp, pb := range b.people {
		if _, both := a.people[fp]; both {
			continue
		}
		res.contacts.onlyB++
		sectionFor(setContacts).only = append(sectionFor(setContacts).only, oneSided("b", "contact "+shortID(fp), describeContact(pb)))
	}
}

func describeContact(p peerstate.Person) string {
	name := p.Meta.Alias
	if name == "" {
		name = p.Meta.Nick
	}
	if name == "" {
		name = "no name"
	}
	return fmt.Sprintf("%s, %d pairings held", name, len(p.PeerIDs))
}

func readOutDevices(a, b *sideIndex, res *diffResult) {
	res.deviceReadout = &deviceReadout{
		a: append([]string(nil), a.devices...),
		b: append([]string(nil), b.devices...),
	}
	if len(a.devices) == 0 && len(b.devices) > 0 {
		res.deviceReadout.finding = fmt.Sprintf(
			"a records no siblings at all while b records %d. The device on side a does not know it is linked.",
			len(b.devices))
	}
	if len(b.devices) == 0 && len(a.devices) > 0 {
		res.deviceReadout.finding = fmt.Sprintf(
			"b records no siblings at all while a records %d. The device on side b does not know it is linked.",
			len(a.devices))
	}
}

func appendIfDifferent[T comparable](into []difference, subject, field string, a, b T) []difference {
	if a == b {
		return into
	}
	return append(into, difference{
		subject: subject, field: field,
		a: render1(a), b: render1(b), class: mustAgree,
	})
}

func appendClock(into []difference, res *diffResult, subject, field string, a, b int64) []difference {
	if a == b {
		return into
	}
	if a == 0 || b == 0 {
		return append(into, difference{
			subject: subject,
			field:   field + " (one side has none at all, which no clock explains)",
			a:       stamp(a), b: stamp(b), class: mustAgree,
		})
	}
	gap := abs64(a - b)
	if gap <= clockTolerance {
		res.tolerated = append(res.tolerated, tolerated{subject: subject, field: field, gap: gap})
		return into
	}
	return append(into, difference{
		subject: subject, field: field,
		a: stamp(a), b: stamp(b), class: clockStamped, gap: gap,
	})
}

func render1(v any) string {
	if s, ok := v.(string); ok {
		if s == "" {
			return "unset"
		}
		return fmt.Sprintf("%q", s)
	}
	return fmt.Sprintf("%v", v)
}

func stateOrUnset(s string) string {
	if s == "" {
		return "unset"
	}
	return s
}

func emptyAsNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func stamp(unixSeconds int64) string {
	if unixSeconds == 0 {
		return "unset"
	}
	return time.Unix(unixSeconds, 0).Format("2006-01-02 15:04:05")
}

func describeMessage(r msgRow, idx *sideIndex) string {
	who := "sent by me"
	if r.Direction == "in" {
		who = "sent by them"
		if name := idx.names.forPeerID(r.SenderPeerID); name != "" {
			who = "sent by " + name
		}
	}
	extra := ""
	if r.FileName != "" {
		extra = fmt.Sprintf(", file %q", r.FileName)
	}
	return fmt.Sprintf("%s, %s, %s, delivery %s%s",
		r.Kind, who, stamp(r.DisplayTs), stateOrUnset(r.DeliveryState), extra)
}

func render(out io.Writer, a, b *side, res *diffResult) {
	fmt.Fprintln(out, "store diff")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  a  %-24s %s\n", a.name, a.dataDir)
	fmt.Fprintf(out, "  b  %-24s %s\n", b.name, b.dataDir)
	fmt.Fprintln(out)
	renderOwnership(out, res.ownerA, res.ownerB)
	fmt.Fprintln(out, "A conversation is joined by asking the contacts set which person its")
	fmt.Fprintln(out, "pairings address, and failing that by its chat id. No single field on a")
	fmt.Fprintln(out, "conversation names the person on both devices: the one that paired")
	fmt.Fprintln(out, "directly holds only the pairing, and the one that received it from a")
	fmt.Fprintln(out, "sibling holds the person because it has no pairing yet.")
	fmt.Fprintln(out, "Messages and file rows are joined by message id. Contacts are joined by")
	fmt.Fprintln(out, "the person's identity fingerprint. Linked devices are not joined at all;")
	fmt.Fprintln(out, "they are read out, because each device holds ids only it can resolve.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Not compared, because these are local to one device by design:")
	for _, s := range notCompared {
		fmt.Fprintf(out, "  - %s\n", s)
	}
	fmt.Fprintln(out)

	sorted := append([]*section(nil), res.sections...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].title < sorted[j].title })

	renderDifferences(out, sorted, mustAgree,
		"=== differences that need attention ===",
		"  These are fields both devices are supposed to hold the same.",
		"  nothing")

	renderOneSided(out, sorted, res)

	renderDifferences(out, sorted, clockStamped,
		fmt.Sprintf("=== clock skew, wider than %s ===", clockToleranceName),
		"  Each device stamped these from its own clock. A wide gap is drift\n"+
			"  worth knowing about. It is not a store holding the wrong thing.",
		"  nothing outside the tolerance")

	renderDeviceReadout(out, res)

	fmt.Fprintln(out, "=== what was compared ===")
	fmt.Fprintln(out)
	for _, c := range res.categories() {
		fmt.Fprintln(out, c.t.line(c.label))
	}
	fmt.Fprintln(out, "  linked devices     each device holds its own ids; read out above, not compared")
	fmt.Fprintln(out)

	renderTolerated(out, res)

	gaps := res.notComparable()
	if len(gaps) > 0 {
		fmt.Fprintln(out, "=== what could NOT be compared, and why ===")
		fmt.Fprintln(out)
		for _, c := range gaps {
			if c.t.state() == joinFailed {
				fmt.Fprintf(out, "  %s: a holds %d and b holds %d, and not one of them joined.\n",
					c.label, c.t.aTotal, c.t.bTotal)
				fmt.Fprintf(out, "    %s.\n", c.why)
				fmt.Fprintln(out, "    Either the join key is missing on these rows, or the two stores")
				fmt.Fprintln(out, "    disagree about it. Nothing here says whether they converged.")
			}
			if c.t.unjoinable > 0 {
				fmt.Fprintf(out, "  %s: %d rows carry no join key at all and were skipped.\n",
					c.label, c.t.unjoinable)
				fmt.Fprintf(out, "    %s.\n", c.why)
			}
			if c.t.unnameable > 0 {
				fmt.Fprintf(out, "  %s: %d matched nothing on the other side, and this store\n",
					c.label, c.t.unnameable)
				fmt.Fprintln(out, "    cannot name the person they belong to, so nothing follows from that.")
				fmt.Fprintln(out, "    A conversation is named through the contacts set, by the pairings it")
				fmt.Fprintln(out, "    holds. One with no pairing and no person on the record cannot be")
				fmt.Fprintln(out, "    placed, and \"the other device does not have it\" would be a guess.")
				for _, u := range res.unnameable {
					fmt.Fprintf(out, "      in %s: %s\n", u.slot, describeChat(u.row))
				}
			}
		}
		fmt.Fprintln(out)
	}

	renderVerdict(out, res)
}

func renderDifferences(out io.Writer, sorted []*section, class findingClass, heading, blurb, empty string) {
	fmt.Fprintln(out, heading)
	fmt.Fprintln(out)
	fmt.Fprintln(out, blurb)
	fmt.Fprintln(out)
	wrote := false
	for _, s := range sorted {
		var rows []difference
		for _, d := range s.differ {
			if d.class == class {
				rows = append(rows, d)
			}
		}
		if len(rows) == 0 {
			continue
		}
		wrote = true
		fmt.Fprintf(out, "## %s\n\n", s.title)
		sort.SliceStable(rows, func(i, j int) bool {
			if rows[i].subject != rows[j].subject {
				return rows[i].subject < rows[j].subject
			}
			return rows[i].field < rows[j].field
		})
		for _, d := range rows {
			fmt.Fprintf(out, "  %s\n    %s", d.subject, d.field)
			if d.class == clockStamped {
				fmt.Fprintf(out, "   (%d seconds apart)", d.gap)
			}
			fmt.Fprintf(out, "\n      a  %s\n      b  %s\n", d.a, d.b)
		}
		fmt.Fprintln(out)
	}
	if !wrote {
		fmt.Fprintln(out, empty)
		fmt.Fprintln(out)
	}
}

func renderOneSided(out io.Writer, sorted []*section, res *diffResult) {
	fmt.Fprintln(out, "=== rows on one side only ===")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  One store holds these and the other does not. That may be a fault, and")
	fmt.Fprintln(out, "  it may be what the run did: a scenario that never sent a row to the")
	fmt.Fprintln(out, "  second device leaves exactly this behind. Nothing in either store says")
	fmt.Fprintln(out, "  which, so they are listed and not judged.")
	fmt.Fprintln(out)
	wrote := false
	for _, s := range sorted {
		if len(s.only) == 0 {
			continue
		}
		wrote = true
		fmt.Fprintf(out, "## %s\n\n", s.title)
		rows := append([]onlyOne(nil), s.only...)
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].subject < rows[j].subject })
		for _, r := range rows {
			fmt.Fprintf(out, "  only in %s   %s\n               %s\n", r.slot, r.subject, r.description)
		}
		fmt.Fprintln(out)
	}
	if !wrote {
		fmt.Fprintln(out, "  nothing")
		fmt.Fprintln(out)
	}
}

func renderTolerated(out io.Writer, res *diffResult) {
	fmt.Fprintf(out, "=== ignored: inside %s, which is %d seconds ===\n", clockToleranceName, clockTolerance)
	fmt.Fprintln(out)
	if len(res.tolerated) == 0 {
		fmt.Fprintln(out, "  nothing was ignored.")
		fmt.Fprintln(out)
		return
	}
	fmt.Fprintf(out, "  %d clock-stamped field(s) differed by %d seconds or less and are not\n",
		len(res.tolerated), clockTolerance)
	fmt.Fprintln(out, "  reported as differences. Two devices run independent clocks and there is")
	fmt.Fprintln(out, "  no external time source, so this much is expected.")
	fmt.Fprintln(out)
	rows := append([]tolerated(nil), res.tolerated...)
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].subject != rows[j].subject {
			return rows[i].subject < rows[j].subject
		}
		return rows[i].field < rows[j].field
	})
	shown := rows
	if len(shown) > 20 {
		shown = shown[:20]
	}
	for _, t := range shown {
		fmt.Fprintf(out, "    %s   %s   %d second(s) apart\n", t.subject, t.field, t.gap)
	}
	if len(rows) > len(shown) {
		fmt.Fprintf(out, "    and %d more\n", len(rows)-len(shown))
	}
	fmt.Fprintln(out)
}

func renderDeviceReadout(out io.Writer, res *diffResult) {
	r := res.deviceReadout
	if r == nil {
		return
	}
	fmt.Fprintln(out, "=== linked devices: read out, not compared ===")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  Each device addresses its own siblings by ids it minted itself, so")
	fmt.Fprintln(out, "  the two sets never hold the same id and cannot be matched against")
	fmt.Fprintln(out, "  each other. What each one records is printed instead.")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  a records %d sibling(s)\n", len(r.a))
	for _, id := range r.a {
		fmt.Fprintf(out, "      %s\n", shortID(id))
	}
	fmt.Fprintf(out, "  b records %d sibling(s)\n", len(r.b))
	for _, id := range r.b {
		fmt.Fprintf(out, "      %s\n", shortID(id))
	}
	if r.finding != "" {
		fmt.Fprintln(out)
		fmt.Fprintf(out, "  FINDING: %s\n", r.finding)
	}
	fmt.Fprintln(out)
}

func verdictExit(res *diffResult) int {
	switch {
	case res.mustAgreeCount() > 0:
		return exitDiffers
	case !res.comparedAnything(), len(res.notComparable()) > 0:
		return exitIncomplete
	case res.needsReading():
		return exitReview
	}
	return exitAgreed
}

func renderVerdict(out io.Writer, res *diffResult) {
	gaps := res.notComparable()
	switch {
	case res.mustAgreeCount() > 0:
		fmt.Fprintf(out, "VERDICT: these two stores have not converged. %d field(s) that must\n",
			res.mustAgreeCount())
		fmt.Fprintln(out, "agree do not. They are listed at the top.")
		if len(gaps) > 0 {
			fmt.Fprintln(out, "Some rows could not be compared at all; see the section above.")
		}
	case !res.comparedAnything():
		fmt.Fprintln(out, "VERDICT: nothing was compared, so this run says nothing about whether")
		fmt.Fprintln(out, "these two stores converged. Check that both paths point at the two")
		fmt.Fprintln(out, "devices of one person.")
	case len(gaps) > 0:
		fmt.Fprintln(out, "VERDICT: everything that could be compared agrees, but some rows could")
		fmt.Fprintln(out, "not be compared at all. This is not a clean answer. What was compared")
		fmt.Fprintln(out, "is counted above, and what was not is named beside it.")
	case res.needsReading():
		fmt.Fprintln(out, "VERDICT: nothing that must agree differs, so these two stores have not")
		fmt.Fprintln(out, "been shown to diverge.")
		if n := res.oneSidedCount(); n > 0 {
			fmt.Fprintf(out, "%d row(s) sit on one side only. That may be the run's own doing.\n", n)
		}
		if n := res.skewCount(); n > 0 {
			fmt.Fprintf(out, "%d clock-stamped field(s) sit outside the tolerance. That is drift.\n", n)
		}
		fmt.Fprintln(out, "Read those and decide; neither is divergence on its own.")
	default:
		fmt.Fprintln(out, "VERDICT: these two stores hold the same thing everywhere this tool")
		fmt.Fprintln(out, "looked.")
	}
}
