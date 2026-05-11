#!/usr/bin/env bash
# streams/smoke.sh — manual loopback for Calls-1d/1e.
#
# Default path (no CMDS): same shape as the 1d smoke that worked.
# Each streamer's stdin is plain `<keyfile` redirection — 32 bytes of
# key, then EOF.  In 1e the control reader treats stdin EOF as
# non-fatal, so the streamers happily run for the rest of the call
# without control input.  Talk into the mic, hear yourself in spk.
#
# CMDS=1 swaps the stdin pipe to a FIFO so a side script can ship
# control commands (mute / unmute / bitrate / stats / exit) at the
# mic mid-run.  Streams the same audio path either way.
#
# Env knobs:
#   MIC_PORT, SPK_PORT — listen ports (defaults 15551/15552)
#   STREAM_ID          — AAD label (default "mic")
#   BAD_KEY=1          — give spk a different key, exercising the
#                        silent-drop branch.  Mic encrypts with key A,
#                        spk decrypts with key B; AEAD verify fails on
#                        every frame; spk plays silence + logs one warn.
#   CMDS=1             — drive the 1e control plane: after warm-up,
#                        ships a stats/mute/unmute/bitrate/exit sequence
#                        at mic, then exits.  Implies a FIFO on mic
#                        stdin (spk stays on plain `<keyfile`).
#   TRACE=1            — pass --trace to both streamers (200ms stats
#                        cadence + per-frame trace events).
#   EVENTS=1           — capture each streamer's stdout (the JSON-line
#                        event stream) to tmp/streams-smoke/*.jsonl.
#                        Default: stdout to /dev/null so the terminal
#                        is reserved for INFO/ERR on stderr.

set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN_DIR="$ROOT/tmp/bins"

MIC_PORT="${MIC_PORT:-15551}"
SPK_PORT="${SPK_PORT:-15552}"
STREAM_ID="${STREAM_ID:-mic}"

if ! command -v socat >/dev/null 2>&1; then
  echo "smoke: socat not installed (apk add socat)" >&2
  exit 1
fi
if [ ! -x "$BIN_DIR/haoma-mic" ] || [ ! -x "$BIN_DIR/haoma-spk" ]; then
  echo "smoke: binaries missing under $BIN_DIR — run 'make' first" >&2
  exit 1
fi

RUNDIR="$ROOT/tmp/streams-smoke"
mkdir -p "$RUNDIR"
MIC_KEY="$RUNDIR/key-mic.$$"
SPK_KEY="$RUNDIR/key-spk.$$"
MIC_FIFO="$RUNDIR/mic-in.$$"

# 32 random bytes per side.  /dev/urandom is fine for a smoke key.
dd if=/dev/urandom of="$MIC_KEY" bs=32 count=1 status=none
chmod 600 "$MIC_KEY"

if [ "${BAD_KEY:-0}" = "1" ]; then
  dd if=/dev/urandom of="$SPK_KEY" bs=32 count=1 status=none
  echo "smoke: BAD_KEY=1 — spk will fail every AEAD verify, expect silence + a warn line"
else
  cp "$MIC_KEY" "$SPK_KEY"
fi
chmod 600 "$SPK_KEY"

if [ "${EVENTS:-0}" = "1" ]; then
  MIC_EVENTS="$RUNDIR/mic-events.$$.jsonl"
  SPK_EVENTS="$RUNDIR/spk-events.$$.jsonl"
  MIC_OUT_REDIR=">$MIC_EVENTS"
  SPK_OUT_REDIR=">$SPK_EVENTS"
else
  MIC_EVENTS=
  SPK_EVENTS=
  MIC_OUT_REDIR=">/dev/null"
  SPK_OUT_REDIR=">/dev/null"
fi

cleanup() {
  [ -n "${SOCAT_PID:-}" ] && kill "$SOCAT_PID" 2>/dev/null
  [ -n "${MIC_PID:-}"   ] && kill "$MIC_PID"   2>/dev/null
  [ -n "${SPK_PID:-}"   ] && kill "$SPK_PID"   2>/dev/null
  exec 7>&- 2>/dev/null
  wait 2>/dev/null
  rm -f "$MIC_KEY" "$SPK_KEY" "$MIC_FIFO"
  if [ "${EVENTS:-0}" = "1" ]; then
    echo "smoke: events captured at:"
    echo "  $MIC_EVENTS"
    echo "  $SPK_EVENTS"
  fi
}
trap cleanup EXIT
trap 'exit 0' INT TERM

TRACE_FLAG=
[ "${TRACE:-0}" = "1" ] && TRACE_FLAG="--trace"

# spk: always plain <keyfile.  Control plane is a mic-only demo here.
eval "\"\$BIN_DIR/haoma-spk\" --port \"\$SPK_PORT\" --stream-id \"\$STREAM_ID\" \$TRACE_FLAG <\"\$SPK_KEY\" $SPK_OUT_REDIR &"
SPK_PID=$!

if [ "${CMDS:-0}" = "1" ]; then
  # FIFO so we can also feed control commands after the key.  Open
  # read+write (<>) — opening just `>` blocks until a reader, which
  # would deadlock against the streamer's <FIFO open-for-read.
  mkfifo -m 600 "$MIC_FIFO"
  exec 7<>"$MIC_FIFO"
  eval "\"\$BIN_DIR/haoma-mic\" --port \"\$MIC_PORT\" --stream-id \"\$STREAM_ID\" \$TRACE_FLAG <\"\$MIC_FIFO\" $MIC_OUT_REDIR &"
  MIC_PID=$!
  cat "$MIC_KEY" >&7
else
  eval "\"\$BIN_DIR/haoma-mic\" --port \"\$MIC_PORT\" --stream-id \"\$STREAM_ID\" \$TRACE_FLAG <\"\$MIC_KEY\" $MIC_OUT_REDIR &"
  MIC_PID=$!
fi

# Let listeners come up before socat dials.
sleep 0.3

echo "smoke: ferrying  mic:$MIC_PORT  ->  spk:$SPK_PORT   (Ctrl-C to stop)"
socat -u "TCP:127.0.0.1:$MIC_PORT" "TCP:127.0.0.1:$SPK_PORT" &
SOCAT_PID=$!

if [ "${CMDS:-0}" = "1" ]; then
  echo "smoke: CMDS=1 — driving control plane on mic"
  (
    sleep 2
    echo '{"cmd":"stats"}'              >&7
    sleep 1
    echo '{"cmd":"mute"}'               >&7
    sleep 2
    echo '{"cmd":"unmute"}'             >&7
    sleep 1
    echo '{"cmd":"bitrate","kbps":24}'  >&7
    sleep 2
    echo '{"cmd":"stats"}'              >&7
    sleep 0.5
    echo '{"cmd":"exit"}'               >&7
  ) &
fi

wait "$SOCAT_PID"
