package chat_test

import (
	"errors"
	"strings"
	"testing"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/store"
)

func init() {
	store.DefaultKDFParams = store.KDFParams{
		Time: 1, Memory: 8 * 1024, Threads: 2, KeyLen: 32, SaltLen: 16,
	}
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Lock() })
	return st
}

func TestNewID_Hex128(t *testing.T) {
	a, err := chat.NewID()
	if err != nil {
		t.Fatal(err)
	}
	b, err := chat.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if string(a) == string(b) {
		t.Fatalf("two NewID calls returned equal ids: %q", a)
	}
	if len(a) != 32 {
		t.Errorf("NewID length = %d, want 32 (hex of 128 bits)", len(a))
	}
}

func TestCreateDirect_Idempotent(t *testing.T) {
	st := newStore(t)
	s := chat.NewStore(st)

	c1, err := s.CreateDirect("peer-alice")
	if err != nil {
		t.Fatal(err)
	}
	if c1.MaxMembers != 2 {
		t.Errorf("DirectChat.MaxMembers = %d, want 2", c1.MaxMembers)
	}
	if len(c1.Members) != 1 {
		t.Fatalf("DirectChat.Members = %v, want exactly 1 (remote only; self is implicit)", c1.Members)
	}
	if c1.Members[0] != "peer-alice" {
		t.Errorf("DirectChat.Members[0] = %q, want %q", c1.Members[0], "peer-alice")
	}
	if c1.PeerID != "peer-alice" {
		t.Errorf("DirectChat.PeerID = %q, want %q", c1.PeerID, "peer-alice")
	}

	c2, err := s.CreateDirect("peer-alice")
	if err != nil {
		t.Fatalf("second CreateDirect failed: %v", err)
	}
	if c1.ID != c2.ID {
		t.Errorf("second CreateDirect minted new id: %q vs %q (want idempotent)", c1.ID, c2.ID)
	}
}

func TestGetByDirectPeer_Found(t *testing.T) {
	st := newStore(t)
	s := chat.NewStore(st)

	created, err := s.CreateDirect("peer-bob")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetByDirectPeer("peer-bob")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != created.ID {
		t.Errorf("GetByDirectPeer id = %q, want %q", got.ID, created.ID)
	}
}

func TestGetByDirectPeer_NotFound(t *testing.T) {
	st := newStore(t)
	s := chat.NewStore(st)

	_, err := s.GetByDirectPeer("peer-never")
	if !errors.Is(err, chat.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestGetByDirectPeer_EmptyPeerID(t *testing.T) {
	st := newStore(t)
	s := chat.NewStore(st)

	_, err := s.GetByDirectPeer("")
	if err == nil || !strings.Contains(err.Error(), "empty peer id") {
		t.Errorf("err = %v, want empty-peer-id", err)
	}
}

func TestGet_ByID(t *testing.T) {
	st := newStore(t)
	s := chat.NewStore(st)

	created, err := s.CreateDirect("peer-carol")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ChatID() != created.ID {
		t.Errorf("Get id = %q, want %q", got.ChatID(), created.ID)
	}
	if got.Kind() != chat.KindDirect {
		t.Errorf("Kind() = %q, want %q", got.Kind(), chat.KindDirect)
	}
}

func TestGet_Missing(t *testing.T) {
	st := newStore(t)
	s := chat.NewStore(st)

	_, err := s.Get(chat.ChatID("ffffffffffffffffffffffffffffffff"))
	if !errors.Is(err, chat.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestList_ReturnsAllChats(t *testing.T) {
	st := newStore(t)
	s := chat.NewStore(st)

	peers := []string{"peer-a", "peer-b", "peer-c"}
	want := map[chat.ChatID]bool{}
	for _, p := range peers {
		c, err := s.CreateDirect(p)
		if err != nil {
			t.Fatal(err)
		}
		want[c.ID] = true
	}

	got, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("List returned %d chats, want 3", len(got))
	}
	for _, c := range got {
		id := c.ChatID()
		if !want[id] {
			t.Errorf("List returned unexpected chat id %q", id)
		}
	}
}

func TestDirectChat_NoAddMember(t *testing.T) {

	var _ chat.DirectChat
}

func TestSenderName_DirectUsesAlias(t *testing.T) {

	st := newStore(t)
	s := chat.NewStore(st)

	c, err := s.CreateDirect("peer-dave")
	if err != nil {
		t.Fatal(err)
	}
	alias := func(peerID string) string {
		if peerID == "peer-dave" {
			return "Dave"
		}
		return ""
	}

	if got := chat.SenderName(c, "in", "", alias); got != "Dave" {
		t.Errorf("SenderName(in) = %q, want %q", got, "Dave")
	}
	if got := chat.SenderName(c, "out", "", alias); got != "me" {
		t.Errorf("SenderName(out) = %q, want %q", got, "me")
	}
}

func TestSetRetentionTTL_RoundTrips(t *testing.T) {
	st := newStore(t)
	s := chat.NewStore(st)

	c, err := s.CreateDirect("peer-retention")
	if err != nil {
		t.Fatal(err)
	}
	if c.RetentionTTL != 0 {
		t.Errorf("fresh chat RetentionTTL = %d, want 0", c.RetentionTTL)
	}

	if err := s.SetRetentionTTL(c.ID, 3600); err != nil {
		t.Fatalf("SetRetentionTTL: %v", err)
	}
	got, err := s.Get(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Retention() != 3600 {
		t.Errorf("Retention() after set = %d, want 3600", got.Retention())
	}

	if err := s.SetRetentionTTL(c.ID, 0); err != nil {
		t.Fatalf("SetRetentionTTL(0): %v", err)
	}
	got, err = s.Get(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Retention() != 0 {
		t.Errorf("Retention() after zero = %d, want 0", got.Retention())
	}
}

func TestSetRetentionTTL_UnknownChat(t *testing.T) {
	st := newStore(t)
	s := chat.NewStore(st)

	err := s.SetRetentionTTL(chat.ChatID("deadbeef"), 60)
	if !errors.Is(err, chat.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestSetGroupAlias_RoundTrips(t *testing.T) {
	st := newStore(t)
	s := chat.NewStore(st)

	c, err := s.CreateDirect("peer-groupalias")
	if err != nil {
		t.Fatal(err)
	}

	got, _ := s.Get(c.ID)
	if dc := got.(*chat.DirectChat); dc.GroupAlias != "" {
		t.Errorf("fresh GroupAlias = %q, want empty", dc.GroupAlias)
	}

	if err := s.SetGroupAlias(c.ID, "useless schmucks"); err != nil {
		t.Fatalf("SetGroupAlias: %v", err)
	}
	got, _ = s.Get(c.ID)
	if dc := got.(*chat.DirectChat); dc.GroupAlias != "useless schmucks" {
		t.Errorf("after set GroupAlias = %q, want %q", dc.GroupAlias, "useless schmucks")
	}

	if err := s.SetGroupAlias(c.ID, ""); err != nil {
		t.Fatalf("SetGroupAlias clear: %v", err)
	}
	got, _ = s.Get(c.ID)
	if dc := got.(*chat.DirectChat); dc.GroupAlias != "" {
		t.Errorf("cleared GroupAlias = %q, want empty", dc.GroupAlias)
	}
}

func TestSetNotificationsMuted_RoundTrips(t *testing.T) {
	st := newStore(t)
	s := chat.NewStore(st)

	c, err := s.CreateDirect("peer-mute")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(c.ID)
	if got.IsNotificationsMuted() {
		t.Errorf("fresh chat should default unmuted")
	}

	if err := s.SetNotificationsMuted(c.ID, true); err != nil {
		t.Fatalf("SetNotificationsMuted true: %v", err)
	}
	got, _ = s.Get(c.ID)
	if !got.IsNotificationsMuted() {
		t.Errorf("after set, expected muted=true")
	}

	if err := s.SetNotificationsMuted(c.ID, false); err != nil {
		t.Fatalf("SetNotificationsMuted false: %v", err)
	}
	got, _ = s.Get(c.ID)
	if got.IsNotificationsMuted() {
		t.Errorf("after unset, expected muted=false")
	}
}

func TestSetGroupName_RoundTrips(t *testing.T) {
	st := newStore(t)
	s := chat.NewStore(st)

	c, err := s.CreateDirect("peer-groupname")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetGroupName(c.ID, "Alcoholics Anonymous"); err != nil {
		t.Fatalf("SetGroupName: %v", err)
	}
	got, _ := s.Get(c.ID)
	if dc := got.(*chat.DirectChat); dc.GroupName != "Alcoholics Anonymous" {
		t.Errorf("GroupName = %q, want %q", dc.GroupName, "Alcoholics Anonymous")
	}
}

func TestSetGroupAlias_UnknownChat(t *testing.T) {
	st := newStore(t)
	s := chat.NewStore(st)
	err := s.SetGroupAlias(chat.ChatID("deadbeef"), "x")
	if !errors.Is(err, chat.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestSetRetentionAndTimerTs_RoundTrip(t *testing.T) {
	st := newStore(t)
	s := chat.NewStore(st)

	c, err := s.CreateDirect("peer-conv")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetRetentionAndTimerTs(c.ID, 600, 1742643890); err != nil {
		t.Fatalf("SetRetentionAndTimerTs: %v", err)
	}
	got, err := s.Get(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Retention() != 600 {
		t.Errorf("Retention = %d, want 600", got.Retention())
	}
	if got.TimerChangeTs() != 1742643890 {
		t.Errorf("TimerChangeTs = %d, want 1742643890", got.TimerChangeTs())
	}
}

func TestSetRetentionTTL_EmptyID(t *testing.T) {
	st := newStore(t)
	s := chat.NewStore(st)

	err := s.SetRetentionTTL("", 60)
	if err == nil || !strings.Contains(err.Error(), "empty id") {
		t.Errorf("err = %v, want empty-id error", err)
	}
}

func TestDeleteByChat_RemovesPrimaryAndIndex(t *testing.T) {
	st := newStore(t)
	s := chat.NewStore(st)

	c, err := s.CreateDirect("peer-e")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(c.ID); !errors.Is(err, chat.ErrNotFound) {
		t.Errorf("after Delete, Get err = %v, want ErrNotFound", err)
	}
	if _, err := s.GetByDirectPeer("peer-e"); !errors.Is(err, chat.ErrNotFound) {
		t.Errorf("after Delete, GetByDirectPeer err = %v, want ErrNotFound", err)
	}
}
