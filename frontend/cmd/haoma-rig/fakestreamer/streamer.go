package main

import (
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	micName = "haoma-mic"
	spkName = "haoma-spk"
)

const (
	exitClean    = 0
	exitArgs     = 1
	exitKey      = 2
	exitListen   = 3
	exitNoClient = 4
)

const (
	keyBytes      = 32
	frameHeader   = 2 + 8 + 8
	frameTag      = 16
	frameOverhead = frameHeader + frameTag
	maxPayload    = 65503
	frameInterval = 20 * time.Millisecond
)

var silencePacket = func() []byte {
	p := []byte{0xFB, 0x41, 0x4B, 0xFF, 0xFE}
	return append(p, make([]byte, 75)...)
}()

type counters struct {
	bytesIn, bytesOut   atomic.Uint64
	framesIn, framesOut atomic.Uint64
	dropped             atomic.Uint64
	jitterX100          atomic.Uint32
}

type streamer struct {
	tag      string
	mic      bool
	port     int
	streamID string
	trace    atomic.Bool

	stdin          io.Reader
	outMu          sync.Mutex
	stdout, stderr io.Writer

	aead      cipher.AEAD
	st        counters
	aeadFails atomic.Uint64

	muted       atomic.Bool
	statsReq    atomic.Bool
	bitrateKbps atomic.Int32

	doneOnce sync.Once
	done     chan struct{}

	origin time.Time
}

func (s *streamer) finish() { s.doneOnce.Do(func() { close(s.done) }) }

func (s *streamer) isDone() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *streamer) logInfo(format string, args ...any) { s.logLine("INFO", format, args...) }
func (s *streamer) logErr(format string, args ...any)  { s.logLine("ERR ", format, args...) }

func (s *streamer) logLine(level, format string, args ...any) {
	line := fmt.Sprintf("[%s] %s: %s\n", s.tag, level, fmt.Sprintf(format, args...))
	s.outMu.Lock()
	defer s.outMu.Unlock()
	_, _ = io.WriteString(s.stderr, line)
}

func (s *streamer) emit(line string) {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	_, _ = io.WriteString(s.stdout, line+"\n")
}

func (s *streamer) emitReady()             { s.emit(`{"type":"ready"}`) }
func (s *streamer) emitWarn(reason string) { s.emit(`{"type":"warn","reason":"` + esc(reason) + `"}`) }

func (s *streamer) emitStats() {
	s.emit(fmt.Sprintf(`{"type":"stats","bytes_in":%d,"bytes_out":%d,"frames_in":%d,"frames_out":%d,"frames_dropped":%d,"jitter_ms":%.2f,"cpu_pct":%.1f}`,
		s.st.bytesIn.Load(), s.st.bytesOut.Load(), s.st.framesIn.Load(), s.st.framesOut.Load(),
		s.st.dropped.Load(), float64(s.st.jitterX100.Load())/100.0, 0.0))
}

func (s *streamer) emitTrace(counter uint64, bytes int, muted bool) {
	s.emit(fmt.Sprintf(`{"type":"trace","counter":%d,"bytes":%d,"muted":%t}`, counter, bytes, muted))
}

func esc(in string) string {
	var b strings.Builder
	for _, c := range []byte(in) {
		if c == '"' || c == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(c)
	}
	return b.String()
}

func usageLine(tag string) string {
	return "usage: " + tag + " --port N --stream-id ID [--trace]\n"
}

func helpText(tag string) string {
	if tag == micName {
		return usageLine(tag) +
			"  binds 127.0.0.1:N, accepts ONE client, captures mic,\n" +
			"  encodes Opus 48k/mono/20ms/32kbps CBR/VOIP, DTX off,\n" +
			"  AEAD-seals each frame (ChaCha20-Poly1305).\n" +
			"  Reads 32-byte key from the first 32 bytes of stdin;\n" +
			"  remainder of stdin is JSON-line control input.\n" +
			"  --stream-id ID is one of: mic | cam | screen.\n" +
			"  --trace ups stats cadence + emits per-frame trace lines.\n"
	}
	return usageLine(tag) +
		"  binds 127.0.0.1:N, accepts ONE client, AEAD-opens incoming\n" +
		"  framed Opus, plays back at 48k/mono. Bad tag = silent drop.\n" +
		"  Reads 32-byte key from the first 32 bytes of stdin;\n" +
		"  remainder of stdin is JSON-line control input.\n" +
		"  --stream-id ID is one of: mic | cam | screen.\n" +
		"  --trace ups stats cadence + emits per-frame trace lines.\n"
}

func roleOf(arg0 string) string {
	return strings.TrimSuffix(filepath.Base(arg0), ".exe")
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer, stop <-chan struct{}) int {
	tag := roleOf(args[0])
	if tag != micName && tag != spkName {
		fmt.Fprintf(stderr, "fakestreamer: start it as %s or %s, not %s\n", micName, spkName, tag)
		return exitArgs
	}
	s := &streamer{
		tag:    tag,
		mic:    tag == micName,
		stdin:  stdin,
		stdout: stdout,
		stderr: stderr,
		done:   make(chan struct{}),
		origin: time.Now(),
	}
	s.bitrateKbps.Store(32)
	ok, help := s.parseArgs(args)
	if help {
		_, _ = io.WriteString(stderr, helpText(tag))
		return exitClean
	}
	if !ok {
		_, _ = io.WriteString(stderr, usageLine(tag))
		return exitArgs
	}

	key := make([]byte, keyBytes)
	if n, err := io.ReadFull(stdin, key); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			s.logErr("key fd 0: short read (%d/%d before EOF)", n, keyBytes)
		} else {
			s.logErr("key fd 0: read: %v", err)
		}
		return exitKey
	}
	aead, err := chacha20poly1305.New(key)
	clear(key)
	if err != nil {
		s.logErr("aead: %v", err)
		return exitKey
	}
	s.aead = aead

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.port))
	if err != nil {
		s.logErr("bind 127.0.0.1:%d: %v", s.port, err)
		return exitListen
	}
	s.logInfo("listening on 127.0.0.1:%d (stream=%s)", s.port, s.streamID)
	if s.mic {
		s.logInfo("fake streamer of the rig: no sound device, sends Opus silence")
	} else {
		s.logInfo("fake streamer of the rig: no sound device, plays nothing")
	}

	s.emitReady()

	conn, err := acceptOne(ln, stop)
	if err != nil {
		if errors.Is(err, errStopped) {
			s.logInfo("accept interrupted (shutdown)")
		} else {
			s.logErr("accept: %v", err)
		}
		return exitNoClient
	}
	s.logInfo("client connected")

	go func() {
		select {
		case <-stop:
			s.finish()
		case <-s.done:
		}
	}()
	if s.mic {
		go s.sendLoop(conn)
		s.logInfo("streaming...")
	} else {
		go s.readLoop(conn)
		s.logInfo("playing...")
	}

	go s.controlLoop()
	go s.statsLoop()
	go s.summaryLoop()

	<-s.done
	_ = conn.Close()
	if n := s.aeadFails.Load(); n > 0 {
		s.logInfo("AEAD failures over the call: %d", n)
	}
	s.logInfo("frames in %d, out %d, dropped %d", s.st.framesIn.Load(), s.st.framesOut.Load(), s.st.dropped.Load())
	s.logInfo("clean exit")
	return exitClean
}

func (s *streamer) parseArgs(args []string) (ok, help bool) {
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch {
		case (a == "--port" || a == "-p") && i+1 < len(args):
			i++
			p := atoi(args[i])
			if p < 1 || p > 65535 {
				s.logErr("invalid --port: %d", p)
				return false, false
			}
			s.port = p
		case a == "--stream-id" && i+1 < len(args):
			i++
			s.streamID = args[i]
		case a == "--trace":
			s.trace.Store(true)
		case a == "--help" || a == "-h":
			return true, true
		default:
			s.logErr("unknown arg: %s", a)
			return false, false
		}
	}
	if s.port == 0 {
		s.logErr("--port required")
		return false, false
	}
	if s.streamID == "" {
		s.logErr("--stream-id required")
		return false, false
	}
	return true, false
}

func atoi(s string) int {
	s = strings.TrimLeft(s, " \t\n\v\f\r")
	end := 0
	if end < len(s) && (s[end] == '-' || s[end] == '+') {
		end++
	}
	digits := end
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == digits {
		return 0
	}
	v, err := strconv.ParseInt(s[:end], 10, 32)
	if err != nil {
		return 0
	}
	return int(v)
}

var errStopped = errors.New("stopped")

func acceptOne(ln net.Listener, stop <-chan struct{}) (net.Conn, error) {
	var stopped atomic.Bool
	accepted := make(chan struct{})
	go func() {
		select {
		case <-stop:
			stopped.Store(true)
			_ = ln.Close()
		case <-accepted:
		}
	}()
	conn, err := ln.Accept()
	close(accepted)
	_ = ln.Close()
	if err != nil {
		if stopped.Load() {
			return nil, errStopped
		}
		return nil, err
	}
	return conn, nil
}

func nonce(counter uint64) []byte {
	n := make([]byte, chacha20poly1305.NonceSize)
	binary.BigEndian.PutUint64(n[4:], counter)
	return n
}

func aad(streamID string, counter, pts uint64) []byte {
	a := make([]byte, 0, len(streamID)+16)
	a = append(a, streamID...)
	a = binary.BigEndian.AppendUint64(a, counter)
	return binary.BigEndian.AppendUint64(a, pts)
}

func sealFrame(a cipher.AEAD, streamID string, counter, pts uint64, plain []byte) ([]byte, error) {
	if len(plain) > maxPayload {
		return nil, fmt.Errorf("payload too big (%d)", len(plain))
	}
	frame := make([]byte, frameHeader, frameOverhead+len(plain))
	binary.BigEndian.PutUint16(frame[0:], uint16(8+8+len(plain)+frameTag))
	binary.BigEndian.PutUint64(frame[2:], counter)
	binary.BigEndian.PutUint64(frame[10:], pts)

	return a.Seal(frame, nonce(counter), plain, aad(streamID, counter, pts)), nil
}

var errFrame = errors.New("bad frame")

func readFrame(r io.Reader) (counter, pts uint64, sealed []byte, err error) {
	var hdr [2]byte
	if n, err := io.ReadFull(r, hdr[:]); err != nil {
		if n == 0 && errors.Is(err, io.EOF) {
			return 0, 0, nil, io.EOF
		}
		return 0, 0, nil, errFrame
	}
	bodyLen := int(binary.BigEndian.Uint16(hdr[:]))
	if bodyLen < 8+8+frameTag {
		return 0, 0, nil, errFrame
	}
	cipherLen := bodyLen - 8 - 8 - frameTag
	if cipherLen > maxPayload {
		return 0, 0, nil, errFrame
	}
	body := make([]byte, bodyLen)
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, 0, nil, errFrame
	}
	counter = binary.BigEndian.Uint64(body[0:])
	pts = binary.BigEndian.Uint64(body[8:])
	return counter, pts, body[16:], nil
}

func (s *streamer) pts() uint64 { return uint64(time.Since(s.origin).Nanoseconds()) + 1 }

func (s *streamer) sendLoop(conn net.Conn) {
	t := time.NewTicker(frameInterval)
	defer t.Stop()
	var counter uint64
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
		}

		f, err := sealFrame(s.aead, s.streamID, counter, s.pts(), silencePacket)
		if err != nil {
			s.logErr("encode_frame: %v", err)
			continue
		}
		this := counter
		counter++
		if _, err := conn.Write(f); err != nil {
			s.logInfo("peer closed (write failed)")
			s.st.dropped.Add(1)
			s.finish()
			return
		}
		s.st.framesOut.Add(1)
		s.st.bytesOut.Add(uint64(len(f)))
		if s.trace.Load() {
			s.emitTrace(this, len(f), s.muted.Load())
		}
	}
}

func (s *streamer) readLoop(conn net.Conn) {
	var (
		expected uint64
		first    = true
		jt       jitter
	)
	for !s.isDone() {
		counter, pts, sealed, err := readFrame(conn)
		if errors.Is(err, io.EOF) {
			s.logInfo("peer EOF")
			s.finish()
			return
		}
		if err != nil {
			if !s.isDone() {
				s.logErr("read_frame error")
			}
			s.finish()
			return
		}
		cipherLen := len(sealed) - frameTag
		s.st.bytesIn.Add(uint64(frameOverhead + cipherLen))
		jt.arrival(time.Now())
		s.st.jitterX100.Store(jt.x100())

		if _, err := s.aead.Open(nil, nonce(counter), sealed, aad(s.streamID, counter, pts)); err != nil {
			if s.aeadFails.Load() == 0 {
				s.logInfo("AEAD verify failed at counter %d — wrong key or tampered frame; silently dropping", counter)
			}
			s.aeadFails.Add(1)
			s.st.dropped.Add(1)
			continue
		}
		switch {
		case first:
			first = false
		case counter < expected:
			s.st.dropped.Add(1)
			continue
		}
		expected = counter + 1

		s.st.framesIn.Add(1)
		if s.trace.Load() {
			s.emitTrace(counter, frameOverhead+cipherLen, s.muted.Load())
		}
		if s.muted.Load() {
			s.st.dropped.Add(1)
		}
	}
}

type jitter struct {
	last   time.Time
	primed bool
	ms     float64
}

func (j *jitter) arrival(now time.Time) {
	if !j.primed {
		j.last, j.primed = now, true
		return
	}
	dev := float64(now.Sub(j.last))/float64(time.Millisecond) - 20.0
	if dev < 0 {
		dev = -dev
	}
	j.ms += (dev - j.ms) / 16.0
	j.last = now
}

func (j *jitter) x100() uint32 {
	v := j.ms * 100.0
	if v < 0 {
		v = 0
	}
	if v > float64(^uint32(0)) {
		v = float64(^uint32(0))
	}
	return uint32(v)
}

type command struct {
	cmd  string
	kbps int
}

func parseCommand(line string) command {
	v, ok := stringField(line, "cmd")
	if !ok {
		return command{}
	}
	switch v {
	case "mute", "unmute", "stats", "exit":
		return command{cmd: v}
	case "bitrate":
		k, ok := intField(line, "kbps")
		if !ok {
			return command{}
		}
		return command{cmd: v, kbps: int(int32(k))}
	}
	return command{}
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\v' || c == '\f' || c == '\r'
}

func afterColon(s, key string) (int, bool) {
	k := strings.Index(s, `"`+key+`"`)
	if k < 0 {
		return 0, false
	}
	pos := k + len(key) + 2
	for pos < len(s) && isSpace(s[pos]) {
		pos++
	}
	if pos >= len(s) || s[pos] != ':' {
		return 0, false
	}
	pos++
	for pos < len(s) && isSpace(s[pos]) {
		pos++
	}
	return pos, pos < len(s)
}

func stringField(s, key string) (string, bool) {
	pos, ok := afterColon(s, key)
	if !ok || s[pos] != '"' {
		return "", false
	}
	end := strings.IndexByte(s[pos+1:], '"')
	if end < 0 {
		return "", false
	}
	return s[pos+1 : pos+1+end], true
}

func intField(s, key string) (int64, bool) {
	pos, ok := afterColon(s, key)
	if !ok {
		return 0, false
	}
	end := pos
	if s[end] == '-' || s[end] == '+' {
		end++
	}
	digits := end
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == digits {
		return 0, false
	}
	v, err := strconv.ParseInt(s[pos:end], 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func (s *streamer) controlLoop() {
	buf := make([]byte, 256)
	var line []byte
	for !s.isDone() {
		n, err := s.stdin.Read(buf)
		for _, c := range buf[:n] {
			switch {
			case c == '\n':
				if len(line) > 0 {
					s.apply(parseCommand(string(line)))
					line = line[:0]
				}
			case c == '\r':
			case len(line) < 1024:
				line = append(line, c)
			default:
				line = line[:0]
			}
		}
		if err != nil {
			return
		}
	}
}

func (s *streamer) apply(c command) {
	switch c.cmd {
	case "mute":
		s.muted.Store(true)
		s.logInfo("muted")
	case "unmute":
		s.muted.Store(false)
		s.logInfo("unmuted")
	case "bitrate":
		if !s.mic {
			s.emitWarn("bitrate_not_supported_on_decoder")
			return
		}
		if c.kbps < 6 || c.kbps > 128 {
			s.emitWarn("bitrate_out_of_range")
			s.logInfo("rejected bitrate %d kbps (allowed 6..128)", c.kbps)
			return
		}
		s.bitrateKbps.Store(int32(c.kbps))
		s.logInfo("bitrate -> %d kbps", c.kbps)
	case "stats":
		s.statsReq.Store(true)
	case "exit":
		s.logInfo("exit command")
		s.finish()
	default:
		s.emitWarn("unknown_command")
	}
}

func (s *streamer) statsLoop() {
	for !s.isDone() {
		target := 2000 * time.Millisecond
		if s.trace.Load() {
			target = 200 * time.Millisecond
		}
		for slept := time.Duration(0); slept < target && !s.isDone(); slept += 100 * time.Millisecond {
			if s.statsReq.Load() {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if s.isDone() {
			return
		}
		s.statsReq.Store(false)
		s.emitStats()
	}
}

const summaryInterval = 1 * time.Second

func (s *streamer) summaryLoop() {
	var lastFramesIn, lastFramesOut, lastBytesIn, lastBytesOut, lastAeadFails uint64
	t := time.NewTicker(summaryInterval)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
		}
		if s.mic {
			framesOut := s.st.framesOut.Load()
			bytesOut := s.st.bytesOut.Load()
			s.emitMicSummary(framesOut-lastFramesOut, bytesOut-lastBytesOut)
			lastFramesOut, lastBytesOut = framesOut, bytesOut
			continue
		}
		framesIn := s.st.framesIn.Load()
		bytesIn := s.st.bytesIn.Load()
		aeadFails := s.aeadFails.Load()
		dropAeadMs := (aeadFails - lastAeadFails) * 20
		s.emitSpkSummary(framesIn-lastFramesIn, bytesIn-lastBytesIn, dropAeadMs)
		lastFramesIn, lastBytesIn, lastAeadFails = framesIn, bytesIn, aeadFails
	}
}

func (s *streamer) emitMicSummary(framesOut, bytesOut uint64) {
	s.emit(fmt.Sprintf(
		`{"type":"summary","period_ms":%d,"frames_captured":%d,"frames_out":%d,"write_block_max_ms":0,"bitrate_kbps":%d,"bytes_out":%d,"muted":%t}`,
		summaryInterval.Milliseconds(), framesOut, framesOut, s.bitrateKbps.Load(), bytesOut, s.muted.Load(),
	))
}

func (s *streamer) emitSpkSummary(framesIn, bytesIn, dropAeadMs uint64) {
	s.emit(fmt.Sprintf(
		`{"type":"summary","period_ms":%d,"frames_in":%d,"played_ms":0,"underrun_ms":0,"gap_ms":0,`+
			`"drop_overflow_ms":0,"drop_replay_ms":0,"drop_aead_ms":%d,"drop_mute_ms":0,"drop_decode_ms":0,`+
			`"queue_min_ms":0,"queue_avg_ms":0,"queue_max_ms":0,"jitter_ms":0,`+
			`"delay_excess_min_ms":0,"delay_excess_avg_ms":0,"delay_excess_max_ms":0,"bytes_in":%d}`,
		summaryInterval.Milliseconds(), framesIn, dropAeadMs, bytesIn,
	))
}
