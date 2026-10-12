package chat_test

import (
	"fmt"
	"sync"
	"testing"

	"haoma-frontend/internal/chat"
)

func TestCreateDirectEdgeless_ConcurrentAdoptionsGiveOnePersonOneChat(t *testing.T) {
	const fingerprint = "aa11bb22cc33dd44ee55ff6600778899"

	for round := 0; round < 20; round++ {
		s := chat.NewStore(newStore(t))

		const racers = 8
		var start sync.WaitGroup
		var done sync.WaitGroup
		start.Add(1)
		var mu sync.Mutex
		accepted := 0
		for i := 0; i < racers; i++ {
			done.Add(1)
			go func(i int) {
				defer done.Done()
				start.Wait()

				id := chat.ChatID(fmt.Sprintf("%032x", i+1))
				if _, err := s.CreateDirectEdgeless(id, fingerprint); err == nil {
					mu.Lock()
					accepted++
					mu.Unlock()
				}
			}(i)
		}
		start.Done()
		done.Wait()

		if accepted != 1 {
			t.Fatalf("round %d: %d adoptions were accepted, want 1", round, accepted)
		}
		all, err := s.List()
		if err != nil {
			t.Fatal(err)
		}
		if len(all) != 1 {
			ids := make([]string, 0, len(all))
			for _, c := range all {
				ids = append(ids, string(c.ChatID()))
			}
			t.Fatalf("round %d: the store holds %d conversations for one person: %v", round, len(all), ids)
		}

		byPerson, err := s.GetByFingerprint(fingerprint)
		if err != nil {
			t.Fatalf("round %d: the person has no conversation: %v", round, err)
		}
		if byPerson.ID != all[0].ChatID() {
			t.Fatalf("round %d: the person index points at %s, the surviving row is %s",
				round, byPerson.ID, all[0].ChatID())
		}
	}
}
