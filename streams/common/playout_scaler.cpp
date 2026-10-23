#include "playout_scaler.h"

#include <algorithm>

namespace haoma::streams {

PlayoutScaler::PlayoutScaler(PlayoutScalerConfig c) : cfg_(c) {}

float PlayoutScaler::target_rate_for(int64_t excess_ms) const {
  if (excess_ms <= cfg_.slack_ms) return 1.0f;
  if (excess_ms >= cfg_.hard_cap_ms) return cfg_.scale_max_rate;
  double frac = (double)(excess_ms - cfg_.slack_ms) /
                (double)(cfg_.hard_cap_ms - cfg_.slack_ms);
  return (float)(1.0 + frac * (double)(cfg_.scale_max_rate - 1.0f));
}

void PlayoutScaler::update(int64_t excess_ms, int64_t elapsed_ms) {
  float target = target_rate_for(excess_ms);
  float max_step = cfg_.scale_step_per_ms * (float)std::max((int64_t)0, elapsed_ms);
  if (target > current_rate_) {
    current_rate_ = std::min(target, current_rate_ + max_step);
  } else if (target < current_rate_) {
    current_rate_ = std::max(target, current_rate_ - max_step);
  }
}

}  
