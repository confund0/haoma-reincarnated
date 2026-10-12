package chat_test

import (
	"errors"
	"testing"

	"haoma-frontend/internal/chat"
)

func TestBeforeGrowRunsBeforeAChatIsWritten(t *testing.T) {
	for _, c := range []struct {
		name   string
		create func(s *chat.Store) error
	}{
		{"CreateDirect", func(s *chat.Store) error { _, err := s.CreateDirect("peer-a"); return err }},
		{"CreateDirectEdgeless", func(s *chat.Store) error {
			id, err := chat.NewID()
			if err != nil {
				return err
			}
			_, err = s.CreateDirectEdgeless(id, "fp-a")
			return err
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := chat.NewStore(newStore(t))
			calls := 0
			s.SetBeforeGrow(func() error {
				calls++
				chats, err := s.List()
				if err != nil {
					t.Fatalf("List: %v", err)
				}
				if len(chats) != 0 {
					t.Errorf("the hook ran after a chat was written: %d chat(s)", len(chats))
				}
				return nil
			})
			if err := c.create(s); err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			if calls != 1 {
				t.Errorf("hook ran %d times, want 1", calls)
			}
		})
	}
}

func TestBeforeGrowErrorRefusesTheChat(t *testing.T) {
	s := chat.NewStore(newStore(t))
	s.SetBeforeGrow(func() error { return errors.New("seeded") })
	if _, err := s.CreateDirect("peer-a"); err == nil {
		t.Error("CreateDirect went on after the hook failed")
	}
	id, err := chat.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateDirectEdgeless(id, "fp-a"); err == nil {
		t.Error("CreateDirectEdgeless went on after the hook failed")
	}
	if chats, err := s.List(); err != nil || len(chats) != 0 {
		t.Errorf("List = %d chat(s), %v; want none", len(chats), err)
	}
}
