#pragma once

#include <cstdint>

namespace haoma::streams {

struct PlayoutScalerConfig {
  int   slack_ms          = 60;
  int   hard_cap_ms       = 1000;
  float scale_max_rate    = 1.125f;
  float scale_step_per_ms = 0.0005f;  
};

class PlayoutScaler {
 public:
  explicit PlayoutScaler(PlayoutScalerConfig c);

  void update(int64_t excess_ms, int64_t elapsed_ms);

  float current_rate() const { return current_rate_; }

 private:
  float target_rate_for(int64_t excess_ms) const;

  PlayoutScalerConfig cfg_;
  float current_rate_ = 1.0f;
};

}  
