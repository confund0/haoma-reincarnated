#pragma once

#include <chrono>
#include <cstdint>
#include <string>

namespace haoma::streams {

inline int64_t steady_now_ns() {
  return std::chrono::duration_cast<std::chrono::nanoseconds>(
      std::chrono::steady_clock::now().time_since_epoch()).count();
}

inline int64_t samples_to_ms(int64_t samples, int64_t sample_rate) {
  return samples * 1000 / sample_rate;
}

inline int32_t xrun_delta(int32_t xr, int32_t last_xrun) {
  if (xr > last_xrun) return xr - last_xrun;
  if (xr < last_xrun) return xr;
  return 0;
}

class DelayExcess {
 public:
  int64_t on_frame(int64_t now_ns, int64_t pts_ns);

 private:
  bool    have_base_ = false;
  int64_t base_ns_   = 0;
  int64_t base_at_ns_ = 0;  
};

class MinAvgMax {
 public:
  void reset();
  void add(int64_t v);

  int64_t count() const { return count_; }
  int64_t min() const { return have_ ? min_ : 0; }
  int64_t avg() const { return count_ > 0 ? sum_ / count_ : 0; }
  int64_t max() const { return have_ ? max_ : 0; }

 private:
  bool    have_ = false;
  int64_t min_ = 0;
  int64_t max_ = 0;
  int64_t sum_ = 0;
  int64_t count_ = 0;
};

class PeriodClock {
 public:
  explicit PeriodClock(int64_t start_ns) : period_start_ns_(start_ns) {}

  bool tick(int64_t now_ns, int64_t* period_ms);

 private:
  int64_t period_start_ns_;
};

struct SpkPeriod {
  int64_t period_ms            = 0;
  uint64_t frames_in           = 0;
  int64_t played_ms            = 0;
  int64_t underrun_ms          = 0;
  int64_t gap_ms               = 0;
  int64_t drop_overflow_ms     = 0;
  int64_t drop_catchup_ms      = 0;  
  int64_t drop_replay_ms       = 0;
  int64_t drop_aead_ms         = 0;
  int64_t drop_mute_ms         = 0;
  int64_t drop_decode_ms       = 0;
  int64_t plc_ms               = 0;  
  int64_t queue_min_ms         = 0;
  int64_t queue_avg_ms         = 0;
  int64_t queue_max_ms         = 0;
  int     target_ms            = 0;  
  double  jitter_ms            = 0.0;
  float   scale_rate           = 1.0f;  
  int64_t delay_excess_min_ms  = 0;
  int64_t delay_excess_avg_ms  = 0;
  int64_t delay_excess_max_ms  = 0;
  uint64_t bytes_in            = 0;
};

struct MicPeriod {
  int64_t  period_ms          = 0;
  uint64_t frames_captured    = 0;
  uint64_t frames_out         = 0;
  int64_t  write_block_max_ms = 0;
  int32_t  xrun_count         = -1;
  int      bitrate_kbps       = 0;
  uint64_t bytes_out          = 0;
  bool     muted              = false;
};

std::string format_spk_summary(const SpkPeriod& p);
std::string format_mic_summary(const MicPeriod& p);

}  
