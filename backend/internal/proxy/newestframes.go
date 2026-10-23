package proxy

import (
	"io"
	"sync"
	"time"
)

var staleFrameMaxAge = 200 * time.Millisecond

const queueCapSpan = 2 * time.Second

type queuedFrame struct {
	b       []byte
	at      time.Time
	counter uint64
	pts     uint64
}

type frameQueue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	frames []queuedFrame
	closed bool

	evicted    bool
	evictedPts uint64
	evictedAt  time.Time

	rawConsumed []byte
	rawRest     io.Reader

	waitHook func()
}

func newFrameQueue() *frameQueue {
	q := &frameQueue{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *frameQueue) push(f queuedFrame) {
	q.mu.Lock()
	q.frames = append(q.frames, f)
	for len(q.frames) > 1 && q.frames[len(q.frames)-1].at.Sub(q.frames[0].at) > queueCapSpan {
		if !q.evicted {
			q.evicted = true
			q.evictedPts = q.frames[0].pts
			q.evictedAt = q.frames[0].at
		}
		q.frames[0] = queuedFrame{}
		q.frames = q.frames[1:]
	}
	q.mu.Unlock()
	q.cond.Signal()
}

func (q *frameQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.cond.Broadcast()
}

func (q *frameQueue) closeRaw(consumed []byte, rest io.Reader) {
	q.mu.Lock()
	q.rawConsumed = consumed
	q.rawRest = rest
	q.closed = true
	q.mu.Unlock()
	q.cond.Broadcast()
}

func (q *frameQueue) rawTail() (consumed []byte, rest io.Reader) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.rawConsumed, q.rawRest
}

func (q *frameQueue) depthMs(now time.Time) int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.frames) == 0 {
		return 0
	}
	return int64(now.Sub(q.frames[0].at) / time.Millisecond)
}

func (q *frameQueue) popBatch(now time.Time, maxAge, window time.Duration, maxFrames int) (out []queuedFrame, droppedMs, ageMs int, ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var haveDrop bool
	var firstDroppedPts uint64
	var firstDroppedAt time.Time
	for {
		if q.evicted {
			if !haveDrop {
				firstDroppedPts = q.evictedPts
				firstDroppedAt = q.evictedAt
				haveDrop = true
			}
			q.evicted = false
		}
		for len(q.frames) > 0 && now.Sub(q.frames[0].at) > maxAge {
			if !haveDrop {
				firstDroppedPts = q.frames[0].pts
				firstDroppedAt = q.frames[0].at
				haveDrop = true
			}
			q.frames[0] = queuedFrame{}
			q.frames = q.frames[1:]
		}
		if len(q.frames) > 0 {
			if haveDrop {
				droppedMs = int((int64(q.frames[0].pts) - int64(firstDroppedPts)) / int64(time.Millisecond))
				ageMs = int(now.Sub(firstDroppedAt) / time.Millisecond)
			}
			end := 1
			for end < len(q.frames) && end < maxFrames && q.frames[end].at.Sub(q.frames[0].at) <= window {
				end++
			}
			out = append(out, q.frames[:end]...)
			for i := 0; i < end; i++ {
				q.frames[i] = queuedFrame{}
			}
			q.frames = q.frames[end:]
			return out, droppedMs, ageMs, true
		}
		if q.closed {
			return nil, 0, 0, false
		}
		if q.waitHook != nil {
			q.waitHook()
		}
		q.cond.Wait()
	}
}
