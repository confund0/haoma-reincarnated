#pragma once

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
  int     int_arg = 0;  
};

ControlMsg parse_command(const std::string& line);

struct Stats {
  std::atomic<uint64_t> bytes_in{0};
  std::atomic<uint64_t> bytes_out{0};
  std::atomic<uint64_t> frames_in{0};
  std::atomic<uint64_t> frames_out{0};
  std::atomic<uint64_t> frames_dropped{0};
  std::atomic<uint32_t> jitter_ms_x100{0};
};

void emit_ready();

void emit_ready(const std::string& raw_unix);

void emit_stats(const Stats& s, double cpu_pct);

void emit_warn(const std::string& reason);
void emit_error(const std::string& reason);

void emit_trace_frame(uint64_t counter, uint32_t bytes, bool muted);

void emit_clock_sample(int64_t local_ns, int64_t sender_pts_ns);

class CpuSampler {
 public:
  double sample();
 private:
  uint64_t                              last_ticks_ = 0;
  std::chrono::steady_clock::time_point last_time_  = std::chrono::steady_clock::now();
  bool                                  primed_     = false;
};

class JitterTracker;

void control_loop(int fd,
                  std::atomic<bool>& done,
                  std::function<void(const ControlMsg&)> on_msg);

void stats_loop(Stats& s,
                JitterTracker* jt,
                std::atomic<bool>& done,
                std::atomic<bool>& trace,
                std::atomic<bool>& request_now);

class JitterTracker {
 public:
  void on_frame_arrival(std::chrono::steady_clock::time_point now);
  void store_into(Stats& s) const;
 private:
  std::chrono::steady_clock::time_point last_{};
  bool   primed_ = false;
  double j_ms_   = 0.0;
};

}  
