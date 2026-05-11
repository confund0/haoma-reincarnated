#pragma once

// Calls-1e — streamer ↔ haoma control plane (ADR-040 Decision 5).
//
// Data plane lives on the localhost TCP socket; stdin/stdout is the
// JSON-line control channel.  After read_key() consumes the first 32
// bytes of stdin, the rest of stdin carries one command per line:
//
//   {"cmd":"mute"}
//   {"cmd":"unmute"}
//   {"cmd":"stats"}
//   {"cmd":"exit"}
//   {"cmd":"bitrate","kbps":N}
//
// stdout carries one event per line:
//
//   {"type":"ready"}
//   {"type":"stats", ...}            // every ~2 s by default
//   {"type":"warn",  "reason":"..."}
//   {"type":"error", "reason":"..."}
//   {"type":"trace", ...}            // only with --trace
//
// All emit_*() are thread-safe (mutex + fflush).  Parsing is a tiny
// hand-rolled extractor — the command vocabulary is closed.

#include <atomic>
#include <chrono>
#include <cstdint>
#include <functional>
#include <string>

namespace haoma::streams {

enum class Command {
  Unknown,
  Mute,
  Unmute,
  Stats,
  Exit,
  Bitrate,
};

struct ControlMsg {
  Command cmd     = Command::Unknown;
  int     int_arg = 0;  // bitrate kbps
};

// Parse one JSON line.  Whitespace-tolerant, lenient on key order.
// Returns Command::Unknown on any parse failure (malformed JSON, missing
// "cmd", unknown verb, missing required arg).
ControlMsg parse_command(const std::string& line);

// Lock-free counters; bumped from capture/reader threads, sampled by the
// stats emitter.  jitter_ms_x100 stores jitter in 0.01 ms units (so a
// uint32 holds a ~42-second envelope, plenty for sane calls).
struct Stats {
  std::atomic<uint64_t> bytes_in{0};
  std::atomic<uint64_t> bytes_out{0};
  std::atomic<uint64_t> frames_in{0};
  std::atomic<uint64_t> frames_out{0};
  std::atomic<uint64_t> frames_dropped{0};
  std::atomic<uint32_t> jitter_ms_x100{0};
};

// One-shot startup banner.  Emit exactly once after the streamer is
// ready to push or pull frames.
void emit_ready();

// Periodic stats event.  cpu_pct comes from CpuSampler::sample().
void emit_stats(const Stats& s, double cpu_pct);

// Non-fatal warning / fatal error.  Streamer exits after emit_error.
void emit_warn(const std::string& reason);
void emit_error(const std::string& reason);

// Per-frame trace line, only when --trace is passed.  Encode/decode
// hot path; keep field set minimal.
void emit_trace_frame(uint64_t counter, uint32_t bytes, bool muted);

// /proc/self/stat sampler.  Returns process CPU% over the interval
// since the previous sample().  First call returns 0.  Returns 0 on
// non-Linux or parse failure.
class CpuSampler {
 public:
  double sample();
 private:
  uint64_t                              last_ticks_ = 0;
  std::chrono::steady_clock::time_point last_time_  = std::chrono::steady_clock::now();
  bool                                  primed_     = false;
};

class JitterTracker;

// Long-running readers/emitters for streamer mains.

// Read JSON-line commands off `fd` (typically STDIN_FILENO) until
// `done` flips true, EOF, or fd error.  Each parsed line fires
// `on_msg`.  Polls in 100 ms slices so shutdown is prompt.  EOF is
// non-fatal — the streamer keeps running, just no more commands.
void control_loop(int fd,
                  std::atomic<bool>& done,
                  std::function<void(const ControlMsg&)> on_msg);

// Periodic stats emitter.  Default cadence 2 s; with `trace` true,
// 200 ms.  `request_now` flips true to fire one stats event ASAP
// (bound to the {"cmd":"stats"} handler).  Wakes promptly on `done`.
// Pulls jitter into `s` if `jt` is non-null (spk-only).
void stats_loop(Stats& s,
                JitterTracker* jt,
                std::atomic<bool>& done,
                std::atomic<bool>& trace,
                std::atomic<bool>& request_now);

// RFC3550-style smoothed inter-arrival jitter.  Sender pacing is a
// fixed 20 ms per frame, so we treat the deviation from 20 ms as the
// transit-time delta.  Result is exposed in 0.01 ms ticks via the
// shared Stats struct (call store_into).
class JitterTracker {
 public:
  void on_frame_arrival(std::chrono::steady_clock::time_point now);
  void store_into(Stats& s) const;
 private:
  std::chrono::steady_clock::time_point last_{};
  bool   primed_ = false;
  double j_ms_   = 0.0;
};

}  // namespace haoma::streams
