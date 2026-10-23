#pragma once

#include "media_meter.h"    
#include "playout_scaler.h"

#include <cstdint>
#include <deque>
#include <vector>

namespace haoma::streams {

struct JbConfig {
  int   start_target_ms  = 120;
  int   target_min_ms    = 60;
  int   target_max_ms    = 300;
  int   slack_ms         = 60;
  int   catchup_hold_ms  = 1000;
  int   catchup_gap_ms   = 100;
  int   catchup_gap_min_ms = 20;  
  int   quiet_wait_ms    = 1000;
  int   hard_cap_ms      = 1000;
  float quiet_rms        = 0.01f;
  bool  catchup_enabled  = true;

  bool  scale_enabled       = false;
  float scale_max_rate      = 1.125f;
  float scale_step_per_ms   = 0.0005f;
};

struct JbCounters {
  int64_t drop_catchup_ms  = 0;
  int64_t drop_overflow_ms = 0;
  int64_t queue_before_ms  = 0;
  int64_t queue_after_ms   = 0;
};

int plc_frames_for_gap(uint64_t expected, uint64_t got);

class JitterBuffer {
 public:
  JitterBuffer(JbConfig c, int64_t sample_rate);

  void push(int64_t now_ns, std::vector<float>&& pcm);

  size_t pop(int64_t now_ns, float* out, size_t n, int64_t* consumed_samples = nullptr);

  void set_jitter_ms(double j);

  void clear();

  JbCounters take_counters();

  int64_t depth_ms() const;

  int target_ms() const { return target_ms_; }

  double jitter_ms() const { return jitter_ms_; }

  float scale_rate() const { return scaler_.current_rate(); }

  bool is_prefilling() const { return prefilling_; }

 private:
  struct Frame {
    std::vector<float> pcm;
  };

  void enforce_hard_cap();
  void maybe_update_target(int64_t now_ns);
  void run_catchup(int64_t now_ns);
  void drop_one_catchup_frame(int64_t now_ns);
  size_t drop_one_frame_preferring_quiet(bool force);
  int catchup_hold_ms_for(int64_t excess_ms) const;
  int catchup_gap_ms_for(int64_t excess_ms) const;
  void fade_out_tail(std::vector<float>& pcm) const;
  void fade_in_head(std::vector<float>& pcm) const;

  void advance_front(size_t count);

  float sample_at(size_t offset) const;

  JbConfig cfg_;
  int64_t  sample_rate_;
  int      fade_samples_;

  std::deque<Frame> frames_;
  size_t             head_off_      = 0;
  size_t             total_samples_ = 0;

  bool    prefilling_ = true;

  PlayoutScaler scaler_;
  double        cursor_                = 0.0;
  int64_t       last_scale_update_ns_  = -1;

  int     target_ms_              = 0;  
  double  jitter_ms_              = 0.0;
  int64_t last_target_update_ns_  = -1;  

  bool    catching_up_        = false;
  bool    over_target_seen_   = false;
  int64_t over_target_since_ns_ = 0;
  int64_t last_drop_ns_        = 0;
  int64_t eligible_since_ns_   = -1;  

  bool catchup_period_has_drop_ = false;

  JbCounters counters_;
};

}  
