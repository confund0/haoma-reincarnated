package proxy

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"
)

const (
	frameHeaderLen = 2 + 8 + 8

	minFrameBody = 8 + 8 + 16

	maxFrameBody = 65535

	summaryPeriod = time.Second

	tickPeriod = 200 * time.Millisecond

	fetchStallAfter = time.Second
	serveStallAfter = 500 * time.Millisecond

	dropLineEvery = time.Second

	baseRiseLimit = 200 * time.Microsecond
)

var errShortFrameBody = errors.New("proxy: frame body shorter than counter+pts+tag")

type frameParseError struct {
	err      error
	consumed []byte
}

func (e *frameParseError) Error() string { return e.err.Error() }
func (e *frameParseError) Unwrap() error { return e.err }

func readFrame(r *bufio.Reader, buf []byte) (frame []byte, counter, pts uint64, err error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, 0, 0, io.EOF
		}

		return nil, 0, 0, fmt.Errorf("proxy: frame header: %w", err)
	}
	bodyLen := int(binary.BigEndian.Uint16(hdr[:]))
	if bodyLen < minFrameBody {
		return nil, 0, 0, &frameParseError{
			err:      fmt.Errorf("%w: body length %d", errShortFrameBody, bodyLen),
			consumed: append([]byte(nil), hdr[:]...),
		}
	}
	total := frameHeaderLen - 8 - 8 + bodyLen
	if cap(buf) < total {
		buf = make([]byte, total)
	}
	buf = buf[:total]
	copy(buf, hdr[:])
	if _, err := io.ReadFull(r, buf[2:]); err != nil {

		return nil, 0, 0, fmt.Errorf("proxy: frame body: %w", err)
	}
	counter = binary.BigEndian.Uint64(buf[2:10])
	pts = binary.BigEndian.Uint64(buf[10:18])
	return buf, counter, pts, nil
}

type mediaMeter struct {
	log      *slog.Logger
	statsLog *slog.Logger
	kindName string
	token    string

	mu sync.Mutex

	framesTotal           uint64
	bytesTotal            int64
	stallsTotal           int
	stallMsTotal          int64
	delayExcessMaxTotalMs int64

	periodStart      time.Time
	framesInPeriod   uint64
	bytesInPeriod    int64
	writeBlockMaxMs  int64
	haveDelay        bool
	delayMinMs       int64
	delayMaxMs       int64
	delayExcessSumMs int64
	delayExcessCount int64

	framesOutInPeriod   uint64
	dropStaleMsInPeriod int64
	haveQueueDepth      bool
	queueMinMs          int64
	queueMaxMs          int64
	queueSumMs          int64
	queueDepthCount     int64

	haveBase bool
	baseNs   int64
	baseAt   time.Time

	lastFrameAt time.Time

	writeInProgress bool
	writeStartedAt  time.Time

	inStall    bool
	stallStart time.Time

	stallEndPending bool
	stallEndMs      int64

	startedAt      time.Time
	haveDropLine   bool
	lastDropLineAt time.Time
	dropHeldMs     int64
	dropHeldAgeMs  int
	dropHeldRuns   int

	parseErrorLogged bool

	rawCopy bool

	ended bool
}

func newMediaMeter(log, statsLog *slog.Logger, kindName, tokenShort string) *mediaMeter {
	now := time.Now()
	return &mediaMeter{
		log:         log,
		statsLog:    statsLog,
		kindName:    kindName,
		token:       tokenShort,
		periodStart: now,
		startedAt:   now,
	}
}

func (mm *mediaMeter) onFrameIn(now time.Time, n int, pts uint64) int64 {
	mm.mu.Lock()
	defer mm.mu.Unlock()

	mm.framesTotal++
	mm.framesInPeriod++

	var excessMs int64
	if mm.kindName == "fetch" {
		mm.bytesTotal += int64(n)
		mm.bytesInPeriod += int64(n)
		d := now.UnixNano() - int64(pts)
		excess := mm.updateDelayExcessLocked(now, d)
		excessMs = excess / int64(time.Millisecond)
		if !mm.haveDelay {
			mm.delayMinMs, mm.delayMaxMs = excessMs, excessMs
			mm.haveDelay = true
		} else {
			if excessMs < mm.delayMinMs {
				mm.delayMinMs = excessMs
			}
			if excessMs > mm.delayMaxMs {
				mm.delayMaxMs = excessMs
			}
		}
		mm.delayExcessSumMs += excessMs
		mm.delayExcessCount++
		if excessMs > mm.delayExcessMaxTotalMs {
			mm.delayExcessMaxTotalMs = excessMs
		}

		if mm.inStall {
			stallMs := now.Sub(mm.stallStart).Milliseconds()
			mm.inStall = false
			mm.stallsTotal++
			mm.stallMsTotal += stallMs
			mm.log.Debug("call media stall end",
				slog.String("kind", "fetch"),
				slog.String("token", mm.token),
				slog.Int64("stall_ms", stallMs),
				slog.Int64("delay_excess_ms", excessMs),
			)
		}
	}
	mm.lastFrameAt = now
	return excessMs
}

func (mm *mediaMeter) updateDelayExcessLocked(now time.Time, d int64) int64 {
	if !mm.haveBase {
		mm.baseNs = d
		mm.haveBase = true
		mm.baseAt = now
		return 0
	}
	elapsed := now.Sub(mm.baseAt)
	if elapsed < 0 {
		elapsed = 0
	}
	maxRise := int64(float64(elapsed) * (float64(baseRiseLimit) / float64(time.Second)))
	allowed := mm.baseNs + maxRise
	if d < allowed {
		mm.baseNs = d
	} else {
		mm.baseNs = allowed
	}
	mm.baseAt = now
	excess := d - mm.baseNs
	if excess < 0 {
		excess = 0
	}
	return excess
}

func (mm *mediaMeter) onWriteStart(now time.Time) {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	mm.writeInProgress = true
	mm.writeStartedAt = now
}

func (mm *mediaMeter) onWriteDone(now time.Time, n int, blocked time.Duration) {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	mm.writeInProgress = false
	mm.bytesTotal += int64(n)
	mm.bytesInPeriod += int64(n)
	blockedMs := blocked.Milliseconds()
	if blockedMs > mm.writeBlockMaxMs {
		mm.writeBlockMaxMs = blockedMs
	}
	if mm.inStall {
		mm.inStall = false
		mm.stallsTotal++
		mm.stallMsTotal += blockedMs
		if mm.stallEndPending {

			mm.log.Debug("call media stall end",
				slog.String("kind", "serve"),
				slog.String("token", mm.token),
				slog.Int64("stall_ms", mm.stallEndMs),
				slog.Int("dropped_ms", 0),
				slog.String("reason", "next_stall"),
			)
		}
		mm.stallEndPending = true
		mm.stallEndMs = blockedMs
	}
}

func (mm *mediaMeter) onFrameOut() {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	mm.framesOutInPeriod++
}

func (mm *mediaMeter) onParseError(err error) {
	mm.mu.Lock()
	already := mm.parseErrorLogged
	mm.parseErrorLogged = true
	mm.rawCopy = true
	mm.mu.Unlock()
	if already {
		return
	}
	mm.log.Debug("call media parse error",
		slog.String("kind", mm.kindName),
		slog.String("token", mm.token),
		slog.String("err", err.Error()),
		slog.String("action", "raw_copy"),
	)
}

func (mm *mediaMeter) onPop(now time.Time, droppedMs, ageMs int, depthMs int64) {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	if droppedMs != 0 || ageMs != 0 {
		mm.dropStaleMsInPeriod += int64(droppedMs)
		mm.dropHeldMs += int64(droppedMs)
		if mm.dropHeldRuns == 0 || ageMs > mm.dropHeldAgeMs {
			mm.dropHeldAgeMs = ageMs
		}
		mm.dropHeldRuns++
	}
	mm.logDropLineLocked(now, false)
	if mm.stallEndPending {
		mm.stallEndPending = false
		mm.log.Debug("call media stall end",
			slog.String("kind", "serve"),
			slog.String("token", mm.token),
			slog.Int64("stall_ms", mm.stallEndMs),
			slog.Int("dropped_ms", droppedMs),
		)
	}
	if !mm.haveQueueDepth {
		mm.queueMinMs, mm.queueMaxMs = depthMs, depthMs
		mm.haveQueueDepth = true
	} else {
		if depthMs < mm.queueMinMs {
			mm.queueMinMs = depthMs
		}
		if depthMs > mm.queueMaxMs {
			mm.queueMaxMs = depthMs
		}
	}
	mm.queueSumMs += depthMs
	mm.queueDepthCount++
}

func (mm *mediaMeter) logDropLineLocked(now time.Time, force bool) {
	if mm.dropHeldRuns == 0 {
		return
	}
	last := mm.startedAt
	if mm.haveDropLine {
		last = mm.lastDropLineAt
		if !force && now.Sub(last) < dropLineEvery {
			return
		}
	}
	mm.log.Debug("call media drop",
		slog.String("kind", mm.kindName),
		slog.String("token", mm.token),
		slog.String("reason", "stale"),
		slog.Int64("dropped_ms", mm.dropHeldMs),
		slog.Int("age_ms", mm.dropHeldAgeMs),
		slog.Int64("since_ms", now.Sub(last).Milliseconds()),
	)
	mm.haveDropLine = true
	mm.lastDropLineAt = now
	mm.dropHeldMs, mm.dropHeldAgeMs, mm.dropHeldRuns = 0, 0, 0
}

func (mm *mediaMeter) tick(now time.Time) {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	if mm.ended {
		return
	}

	if mm.kindName == "fetch" && !mm.rawCopy && !mm.inStall && !mm.lastFrameAt.IsZero() && now.Sub(mm.lastFrameAt) >= fetchStallAfter {
		mm.inStall = true
		mm.stallStart = mm.lastFrameAt
		mm.log.Debug("call media stall start",
			slog.String("kind", "fetch"),
			slog.String("token", mm.token),
			slog.Int64("last_frame_ms", now.Sub(mm.lastFrameAt).Milliseconds()),
		)
	}
	if mm.kindName == "serve" && !mm.rawCopy && !mm.inStall && mm.writeInProgress && now.Sub(mm.writeStartedAt) >= serveStallAfter {
		mm.inStall = true
		mm.stallStart = mm.writeStartedAt
		mm.log.Debug("call media stall start",
			slog.String("kind", "serve"),
			slog.String("token", mm.token),
			slog.Int64("write_block_ms", now.Sub(mm.writeStartedAt).Milliseconds()),
		)
	}

	mm.logDropLineLocked(now, false)

	elapsed := now.Sub(mm.periodStart)
	if elapsed < summaryPeriod {
		return
	}
	mm.logSummaryLocked(elapsed)

	periods := elapsed / summaryPeriod
	mm.periodStart = mm.periodStart.Add(periods * summaryPeriod)
	mm.framesInPeriod = 0
	mm.bytesInPeriod = 0
	mm.writeBlockMaxMs = 0
	mm.haveDelay = false
	mm.delayExcessSumMs = 0
	mm.delayExcessCount = 0
	mm.framesOutInPeriod = 0
	mm.dropStaleMsInPeriod = 0
	mm.haveQueueDepth = false
	mm.queueSumMs = 0
	mm.queueDepthCount = 0
}

func (mm *mediaMeter) logSummaryLocked(period time.Duration) {
	if mm.statsLog == nil {
		return
	}
	periodMs := period.Milliseconds()
	if mm.kindName == "serve" {
		var queueMin, queueAvg, queueMax int64
		if mm.haveQueueDepth {
			queueMin, queueMax = mm.queueMinMs, mm.queueMaxMs
		}
		if mm.queueDepthCount > 0 {
			queueAvg = mm.queueSumMs / mm.queueDepthCount
		}
		mm.statsLog.Debug("call media summary",
			slog.String("kind", "serve"),
			slog.String("token", mm.token),
			slog.String("modality", string(ModalityAudio)),
			slog.Int64("period_ms", periodMs),
			slog.Uint64("frames_in", mm.framesInPeriod),
			slog.Uint64("frames_out", mm.framesOutInPeriod),
			slog.Int64("drop_stale_ms", mm.dropStaleMsInPeriod),
			slog.Int64("queue_min_ms", queueMin),
			slog.Int64("queue_avg_ms", queueAvg),
			slog.Int64("queue_max_ms", queueMax),
			slog.Int64("write_block_max_ms", mm.writeBlockMaxMs),
			slog.Int64("bytes_out", mm.bytesInPeriod),
			slog.Int("resumes", 0),
		)
		return
	}
	var delayMin, delayAvg, delayMax int64
	if mm.haveDelay {
		delayMin, delayMax = mm.delayMinMs, mm.delayMaxMs
	}
	if mm.delayExcessCount > 0 {
		delayAvg = mm.delayExcessSumMs / mm.delayExcessCount
	}
	mm.statsLog.Debug("call media summary",
		slog.String("kind", "fetch"),
		slog.String("token", mm.token),
		slog.String("modality", string(ModalityAudio)),
		slog.Int64("period_ms", periodMs),
		slog.Uint64("frames_in", mm.framesInPeriod),
		slog.Int64("bytes_in", mm.bytesInPeriod),
		slog.Int64("delay_excess_min_ms", delayMin),
		slog.Int64("delay_excess_avg_ms", delayAvg),
		slog.Int64("delay_excess_max_ms", delayMax),
		slog.Int64("gap_max_ms", 0),
		slog.Int("restarts", 0),
		slog.Int("tag_gen", 0),
	)
}

func (mm *mediaMeter) end(now time.Time) {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	if mm.kindName == "fetch" && mm.inStall {
		stallMs := now.Sub(mm.stallStart).Milliseconds()
		mm.inStall = false
		mm.stallsTotal++
		mm.stallMsTotal += stallMs
		mm.log.Debug("call media stall end",
			slog.String("kind", "fetch"),
			slog.String("token", mm.token),
			slog.Int64("stall_ms", stallMs),
			slog.String("reason", "stream_end"),
		)
	}

	mm.logDropLineLocked(now, true)
	if mm.kindName == "serve" && mm.stallEndPending {

		mm.stallEndPending = false
		mm.log.Debug("call media stall end",
			slog.String("kind", "serve"),
			slog.String("token", mm.token),
			slog.Int64("stall_ms", mm.stallEndMs),
			slog.Int("dropped_ms", 0),
			slog.String("reason", "stream_end"),
		)
	}
	mm.ended = true

	if mm.kindName == "serve" {

		mm.log.Debug("call media end",
			slog.String("kind", "serve"),
			slog.String("token", mm.token),
			slog.Uint64("frames_in", mm.framesTotal),
			slog.Int("stalls", mm.stallsTotal),
			slog.Int64("stall_ms_total", mm.stallMsTotal),
		)
		return
	}
	mm.log.Debug("call media end",
		slog.String("kind", "fetch"),
		slog.String("token", mm.token),
		slog.Uint64("frames_in", mm.framesTotal),
		slog.Int("stalls", mm.stallsTotal),
		slog.Int64("stall_ms_total", mm.stallMsTotal),
		slog.Int64("delay_excess_max_ms", mm.delayExcessMaxTotalMs),
		slog.Int("restarts", 0),
	)
}

func runMeterTicker(mm *mediaMeter, stop <-chan struct{}) {
	t := time.NewTicker(tickPeriod)
	defer t.Stop()
	for {
		select {
		case now := <-t.C:
			mm.tick(now)
		case <-stop:
			return
		}
	}
}

type byteMeter struct {
	log      *slog.Logger
	statsLog *slog.Logger
	kindName string
	token    string
	modality Modality

	mu            sync.Mutex
	periodStart   time.Time
	bytesInPeriod int64
	bytesTotal    int64
	ended         bool
}

func newByteMeter(log, statsLog *slog.Logger, kindName, tokenShort string, modality Modality) *byteMeter {
	return &byteMeter{
		log:         log,
		statsLog:    statsLog,
		kindName:    kindName,
		token:       tokenShort,
		modality:    modality,
		periodStart: time.Now(),
	}
}

func (bm *byteMeter) onBytes(n int) {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	bm.bytesInPeriod += int64(n)
	bm.bytesTotal += int64(n)
}

func (bm *byteMeter) tick(now time.Time) {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	if bm.ended {
		return
	}
	elapsed := now.Sub(bm.periodStart)
	if elapsed < summaryPeriod {
		return
	}
	if bm.statsLog != nil {
		bm.statsLog.Debug("call media summary",
			slog.String("kind", bm.kindName),
			slog.String("token", bm.token),
			slog.String("modality", string(bm.modality)),
			slog.Int64("period_ms", elapsed.Milliseconds()),
			slog.Int64("bytes", bm.bytesInPeriod),
		)
	}
	periods := elapsed / summaryPeriod
	bm.periodStart = bm.periodStart.Add(periods * summaryPeriod)
	bm.bytesInPeriod = 0
}

func (bm *byteMeter) end(time.Time) {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	bm.ended = true
	bm.log.Debug("call media end",
		slog.String("kind", bm.kindName),
		slog.String("token", bm.token),
		slog.Int64("bytes", bm.bytesTotal),
	)
}

func runByteMeterTicker(bm *byteMeter, stop <-chan struct{}) {
	t := time.NewTicker(tickPeriod)
	defer t.Stop()
	for {
		select {
		case now := <-t.C:
			bm.tick(now)
		case <-stop:
			return
		}
	}
}

type countingWriter struct {
	w       io.Writer
	onWrite func(int)
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if n > 0 && c.onWrite != nil {
		c.onWrite(n)
	}
	return n, err
}
