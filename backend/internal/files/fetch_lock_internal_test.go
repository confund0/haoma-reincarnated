package files

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"haoma/internal/store"
)

func TestRunOne_DropBeforeFirstWriteLeavesNoRow(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Lock() })
	mgr, err := NewManager(st, dir)
	if err != nil {
		t.Fatal(err)
	}
	const msgID = "0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d"
	const tok = "tok-race"
	row := Fetch{Token: tok, MsgID: msgID, PeerID: "p", UrlPath: "/files/" + tok, ExpectedSize: 4, ExpectedSha256: "x", State: FetchStatePending}
	if err := mgr.PutFetch(row); err != nil {
		t.Fatal(err)
	}
	row.State = FetchStateFailedTransient
	if err := mgr.UpdateFetch(row); err != nil {
		t.Fatal(err)
	}

	var requests atomic.Int32
	w := NewWorker(mgr,
		PeerResolverFunc(func(string) ([]string, error) { return []string{"any"}, nil }),
		HTTPClientForPeer(func(string) (*http.Client, error) {
			requests.Add(1)
			return nil, errors.New("no client in this test")
		}),
		nil, nil)

	unlock := mgr.fetchLocks.lock(msgID)
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.runOne(context.Background(), tok)
	}()
	time.Sleep(100 * time.Millisecond)
	if err := mgr.DeleteStaging(msgID); err != nil {
		t.Fatal(err)
	}
	if err := mgr.DeleteFetch(tok); err != nil {
		t.Fatal(err)
	}
	unlock()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runOne did not return")
	}

	if got, err := mgr.GetFetch(tok); !errors.Is(err, ErrFetchNotFound) {
		t.Fatalf("fetch row after the drop = %+v, err %v; want ErrFetchNotFound", got, err)
	}
	if n := requests.Load(); n != 0 {
		t.Fatalf("worker went on to fetch a dropped row (%d client calls)", n)
	}
}
