package main

import (
	"context"
	"errors"
	"sync"

	"haoma-frontend/internal/backendapi"
)

var errRendezvousWatchEnded = errors.New("the event stream ended before the rendezvous answered")

type rendezvousWatch struct {
	stop  context.CancelFunc
	ended chan struct{}
	more  chan struct{}

	mu       sync.Mutex
	attempts []backendapi.PairOnionProbe
	err      error
}

func openRendezvousWatch(ctx context.Context, d *daemon) (*rendezvousWatch, error) {
	if d.backendClient == nil {
		return nil, errors.New("no backend client to watch")
	}
	watchCtx, stop := context.WithCancel(ctx)
	w := &rendezvousWatch{
		stop:  stop,
		ended: make(chan struct{}),
		more:  make(chan struct{}, 1),
	}
	opened := make(chan struct{})
	go func() {
		err := d.backendClient.Events(watchCtx, backendapi.EventsOpts{
			OnReady: func() { close(opened) },
			OnPairOnionProbe: func(p backendapi.PairOnionProbe) {
				w.mu.Lock()
				w.attempts = append(w.attempts, p)
				w.mu.Unlock()
				select {
				case w.more <- struct{}{}:
				default:
				}
			},
		})
		w.mu.Lock()
		w.err = err
		w.mu.Unlock()
		close(w.ended)
	}()
	select {
	case <-opened:
		return w, nil
	case <-w.ended:
		stop()
		w.mu.Lock()
		err := w.err
		w.mu.Unlock()
		if err == nil {
			err = errRendezvousWatchEnded
		}
		return nil, err
	case <-ctx.Done():
		stop()
		return nil, ctx.Err()
	}
}

func (w *rendezvousWatch) close() { w.stop() }

func (w *rendezvousWatch) await(ctx context.Context, handleID string, onAttempt func(backendapi.PairOnionProbe)) (backendapi.PairOnionProbe, error) {
	for {
		w.mu.Lock()
		kept := w.attempts
		w.attempts = nil
		w.mu.Unlock()
		for _, p := range kept {
			if p.HandleID != handleID {
				continue
			}
			if onAttempt != nil {
				onAttempt(p)
			}
			if p.Ready {
				return p, nil
			}
		}
		select {
		case <-w.more:
		case <-w.ended:

			w.mu.Lock()
			left := len(w.attempts)
			err := w.err
			w.mu.Unlock()
			if left > 0 {
				continue
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				return backendapi.PairOnionProbe{}, ctxErr
			}
			if err == nil {
				err = errRendezvousWatchEnded
			}
			return backendapi.PairOnionProbe{}, err
		case <-ctx.Done():
			return backendapi.PairOnionProbe{}, ctx.Err()
		}
	}
}

func awaitRendezvousReady(ctx context.Context, d *daemon, handleID string, onAttempt func(backendapi.PairOnionProbe)) (backendapi.PairOnionProbe, error) {
	if handleID == "" {
		return backendapi.PairOnionProbe{}, errors.New("no handle to watch")
	}
	w, err := openRendezvousWatch(ctx, d)
	if err != nil {
		return backendapi.PairOnionProbe{}, err
	}
	defer w.close()
	return w.await(ctx, handleID, onAttempt)
}
