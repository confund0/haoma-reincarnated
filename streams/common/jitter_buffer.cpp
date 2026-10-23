#include "jitter_buffer.h"

#include <algorithm>
#include <cmath>

namespace haoma::streams {

namespace {
constexpr int64_t kMsToNs           = 1'000'000;
constexpr int64_t kTargetUpdateNs   = 10'000 * kMsToNs;  

double frame_rms(const std::vector<float>& pcm) {
  if (pcm.empty()) return 0.0;
  double sum_sq = 0.0;
  for (float s : pcm) sum_sq += (double)s * (double)s;
  return std::sqrt(sum_sq / (double)pcm.size());
}
}  

int plc_frames_for_gap(uint64_t expected, uint64_t got) {
  if (got <= expected) return 0;
  uint64_t gap = got - expected;
  return gap < 3 ? (int)gap : 3;
}

JitterBuffer::JitterBuffer(JbConfig c, int64_t sample_rate)
    : cfg_(c), sample_rate_(sample_rate),
      scaler_(PlayoutScalerConfig{c.slack_ms, c.hard_cap_ms,
                                   c.scale_max_rate, c.scale_step_per_ms}) {
  target_ms_    = cfg_.start_target_ms;
  fade_samples_ = (int)(sample_rate_ * 5 / 1000);  
  last_drop_ns_ = 0;
}

int64_t JitterBuffer::depth_ms() const {
  return samples_to_ms((int64_t)total_samples_, sample_rate_);
}

void JitterBuffer::fade_out_tail(std::vector<float>& pcm) const {
  size_t n = pcm.size();
  size_t len = std::min((size_t)fade_samples_, n);
  size_t start = n - len;
  for (size_t i = 0; i < len; ++i) {
    float gain = 1.0f - (float)(i + 1) / (float)len;
    pcm[start + i] *= gain;
  }
}

void JitterBuffer::fade_in_head(std::vector<float>& pcm) const {
  size_t len = std::min((size_t)fade_samples_, pcm.size());
  for (size_t i = 0; i < len; ++i) {
    float gain = (float)(i + 1) / (float)len;
    pcm[i] *= gain;
  }
}

void JitterBuffer::maybe_update_target(int64_t now_ns) {
  if (last_target_update_ns_ < 0) {
    last_target_update_ns_ = now_ns;
    return;
  }
  if (now_ns - last_target_update_ns_ < kTargetUpdateNs) return;
  last_target_update_ns_ = now_ns;
  int t = (int)(80 + 2 * jitter_ms_);
  target_ms_ = std::clamp(t, cfg_.target_min_ms, cfg_.target_max_ms);
}

size_t JitterBuffer::drop_one_frame_preferring_quiet(bool force) {
  size_t drop_idx = 0;
  bool   found_quiet = false;
  for (size_t i = 0; i < frames_.size(); ++i) {
    if (frame_rms(frames_[i].pcm) < cfg_.quiet_rms) {
      drop_idx = i;
      found_quiet = true;
      break;
    }
  }
  if (!found_quiet && !force) return 0;

  size_t dropped_samples;
  if (drop_idx == 0) {
    dropped_samples = frames_.front().pcm.size() - head_off_;
    if (frames_.size() > 1) fade_in_head(frames_[1].pcm);
    frames_.pop_front();
    head_off_ = 0;
  } else {
    dropped_samples = frames_[drop_idx].pcm.size();
    fade_out_tail(frames_[drop_idx - 1].pcm);
    if (drop_idx + 1 < frames_.size()) fade_in_head(frames_[drop_idx + 1].pcm);
    frames_.erase(frames_.begin() + (ptrdiff_t)drop_idx);
  }
  total_samples_ -= dropped_samples;
  return dropped_samples;
}

void JitterBuffer::enforce_hard_cap() {
  while (depth_ms() > cfg_.hard_cap_ms && !frames_.empty()) {
    size_t dropped = drop_one_frame_preferring_quiet( true);
    counters_.drop_overflow_ms += samples_to_ms((int64_t)dropped, sample_rate_);
  }
}

void JitterBuffer::drop_one_catchup_frame(int64_t now_ns) {
  bool quiet_wait_expired =
      eligible_since_ns_ >= 0 &&
      (now_ns - eligible_since_ns_) >= (int64_t)cfg_.quiet_wait_ms * kMsToNs;

  int64_t depth_before = depth_ms();
  size_t dropped = drop_one_frame_preferring_quiet(quiet_wait_expired);
  if (dropped == 0) return;  

  counters_.drop_catchup_ms += samples_to_ms((int64_t)dropped, sample_rate_);
  if (!catchup_period_has_drop_) {
    counters_.queue_before_ms = depth_before;
    catchup_period_has_drop_  = true;
  }
  counters_.queue_after_ms = depth_ms();
  last_drop_ns_      = now_ns;
  eligible_since_ns_ = -1;
}

int JitterBuffer::catchup_hold_ms_for(int64_t excess_ms) const {
  double k = (double)cfg_.catchup_hold_ms / (double)(cfg_.hard_cap_ms - cfg_.slack_ms);
  double hold = (double)cfg_.catchup_hold_ms - k * (double)(excess_ms - cfg_.slack_ms);
  return (int)std::clamp(hold, 0.0, (double)cfg_.catchup_hold_ms);
}

int JitterBuffer::catchup_gap_ms_for(int64_t excess_ms) const {
  double gap = (double)cfg_.catchup_gap_ms / (1.0 + (double)excess_ms / (double)cfg_.hard_cap_ms);
  return (int)std::clamp(gap, (double)cfg_.catchup_gap_min_ms, (double)cfg_.catchup_gap_ms);
}

void JitterBuffer::run_catchup(int64_t now_ns) {
  if (!cfg_.catchup_enabled) {
    over_target_seen_ = false;
    catching_up_       = false;
    return;
  }
  int64_t depth     = depth_ms();
  int64_t excess_ms = std::max((int64_t)0, depth - (int64_t)target_ms_);
  if (!catching_up_) {
    if (depth > target_ms_ + cfg_.slack_ms) {
      if (!over_target_seen_) {
        over_target_seen_     = true;
        over_target_since_ns_ = now_ns;
      } else if (now_ns - over_target_since_ns_ >= (int64_t)catchup_hold_ms_for(excess_ms) * kMsToNs) {
        catching_up_       = true;
        eligible_since_ns_ = -1;
      }
    } else {
      over_target_seen_ = false;
    }
  }
  if (!catching_up_) return;

  if (depth <= target_ms_) {
    catching_up_ = false;
    return;
  }
  if (now_ns - last_drop_ns_ < (int64_t)catchup_gap_ms_for(excess_ms) * kMsToNs) return;
  if (eligible_since_ns_ < 0) eligible_since_ns_ = now_ns;
  drop_one_catchup_frame(now_ns);
}

void JitterBuffer::push(int64_t now_ns, std::vector<float>&& pcm) {
  size_t n = pcm.size();
  frames_.push_back({std::move(pcm)});
  total_samples_ += n;

  enforce_hard_cap();
  maybe_update_target(now_ns);
  run_catchup(now_ns);

  if (cfg_.scale_enabled) {
    int64_t depth     = depth_ms();
    int64_t excess_ms = std::max((int64_t)0, depth - (int64_t)target_ms_);
    int64_t elapsed_ms = last_scale_update_ns_ < 0
        ? 0 : (now_ns - last_scale_update_ns_) / kMsToNs;
    scaler_.update(excess_ms, elapsed_ms);
    last_scale_update_ns_ = now_ns;
  }
}

void JitterBuffer::advance_front(size_t count) {
  while (count > 0 && !frames_.empty()) {
    size_t avail = frames_.front().pcm.size() - head_off_;
    size_t take  = std::min(count, avail);
    head_off_      += take;
    total_samples_ -= take;
    count          -= take;
    if (head_off_ >= frames_.front().pcm.size()) {
      frames_.pop_front();
      head_off_ = 0;
    }
  }
}

float JitterBuffer::sample_at(size_t offset) const {
  size_t remaining = offset;
  for (size_t fi = 0; fi < frames_.size(); fi++) {
    size_t start = (fi == 0) ? head_off_ : 0;
    size_t avail = frames_[fi].pcm.size() - start;
    if (remaining < avail) return frames_[fi].pcm[start + remaining];
    remaining -= avail;
  }
  return 0.0f;  
}

size_t JitterBuffer::pop(int64_t now_ns, float* out, size_t n, int64_t* consumed_samples) {
  (void)now_ns;
  if (prefilling_) {
    if (depth_ms() < target_ms_) return 0;
    prefilling_ = false;
  }

  float rate = cfg_.scale_enabled ? scaler_.current_rate() : 1.0f;
  size_t got, consumed;
  if (rate <= 1.0f) {
    got = 0;
    while (got < n && !frames_.empty()) {
      auto&  front = frames_.front().pcm;
      size_t avail = front.size() - head_off_;
      size_t take  = std::min(n - got, avail);
      std::copy(front.begin() + (ptrdiff_t)head_off_,
                front.begin() + (ptrdiff_t)(head_off_ + take),
                out + got);
      got += take;
      head_off_      += take;
      total_samples_ -= take;
      if (head_off_ >= front.size()) {
        frames_.pop_front();
        head_off_ = 0;
      }
    }
    consumed = got;
  } else {
    double cursor = cursor_;
    got = 0;
    while (got < n) {
      size_t i0 = (size_t)cursor;
      if (i0 >= total_samples_) break;  
      float  s0   = sample_at(i0);
      size_t i1   = i0 + 1;
      float  s1   = (i1 < total_samples_) ? sample_at(i1) : s0;
      double frac = cursor - (double)i0;
      out[got++]  = (float)(s0 + frac * (double)(s1 - s0));
      cursor += (double)rate;
    }
    consumed = (size_t)cursor;
    cursor -= (double)consumed;
    advance_front(consumed);
    cursor_ = cursor;
  }

  if (consumed_samples) *consumed_samples = (int64_t)consumed;
  if (got < n) {
    prefilling_ = true;
  }
  return got;
}

void JitterBuffer::set_jitter_ms(double j) { jitter_ms_ = j; }

void JitterBuffer::clear() {
  frames_.clear();
  head_off_      = 0;
  total_samples_ = 0;
  prefilling_    = true;
  catching_up_   = false;
  over_target_seen_ = false;
  eligible_since_ns_ = -1;
  cursor_        = 0.0;  
}

JbCounters JitterBuffer::take_counters() {
  JbCounters c = counters_;
  counters_ = JbCounters{};
  catchup_period_has_drop_ = false;
  return c;
}

}  
