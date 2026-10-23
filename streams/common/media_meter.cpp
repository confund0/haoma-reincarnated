#include "media_meter.h"

#include <cstdio>

namespace haoma::streams {

namespace {

constexpr int64_t kBaseRiseLimitNs = 200'000;  

constexpr int64_t kSummaryPeriodNs = 1'000'000'000;  

size_t clamped_len(int n, size_t bufsize) {
  if (n <= 0) return 0;
  size_t len = (size_t)n;
  return len < bufsize ? len : bufsize - 1;
}

}  

int64_t DelayExcess::on_frame(int64_t now_ns, int64_t pts_ns) {
  int64_t d = now_ns - pts_ns;
  if (!have_base_) {
    base_ns_    = d;
    base_at_ns_ = now_ns;
    have_base_  = true;
    return 0;
  }
  int64_t elapsed = now_ns - base_at_ns_;
  if (elapsed < 0) elapsed = 0;
  int64_t max_rise = (int64_t)((double)elapsed * ((double)kBaseRiseLimitNs / (double)kSummaryPeriodNs));
  int64_t allowed  = base_ns_ + max_rise;
  base_ns_ = (d < allowed) ? d : allowed;
  base_at_ns_ = now_ns;
  int64_t excess = d - base_ns_;
  if (excess < 0) excess = 0;
  return excess;
}

void MinAvgMax::reset() {
  have_  = false;
  min_   = 0;
  max_   = 0;
  sum_   = 0;
  count_ = 0;
}

void MinAvgMax::add(int64_t v) {
  if (!have_) {
    min_  = v;
    max_  = v;
    have_ = true;
  } else {
    if (v < min_) min_ = v;
    if (v > max_) max_ = v;
  }
  sum_ += v;
  count_++;
}

bool PeriodClock::tick(int64_t now_ns, int64_t* period_ms) {
  int64_t elapsed = now_ns - period_start_ns_;
  if (elapsed < kSummaryPeriodNs) return false;
  if (period_ms) *period_ms = elapsed / 1'000'000;
  int64_t periods = elapsed / kSummaryPeriodNs;
  period_start_ns_ += periods * kSummaryPeriodNs;
  return true;
}

std::string format_spk_summary(const SpkPeriod& p) {
  char buf[512];
  int n = std::snprintf(buf, sizeof(buf),
    "\"period_ms\":%lld,\"frames_in\":%llu,\"played_ms\":%lld,"
    "\"underrun_ms\":%lld,\"gap_ms\":%lld,"
    "\"drop_overflow_ms\":%lld,\"drop_catchup_ms\":%lld,\"drop_replay_ms\":%lld,"
    "\"drop_aead_ms\":%lld,\"drop_mute_ms\":%lld,\"drop_decode_ms\":%lld,\"plc_ms\":%lld,"
    "\"queue_min_ms\":%lld,\"queue_avg_ms\":%lld,\"queue_max_ms\":%lld,"
    "\"target_ms\":%d,\"jitter_ms\":%.2f,\"scale_rate\":%.3f,"
    "\"delay_excess_min_ms\":%lld,\"delay_excess_avg_ms\":%lld,\"delay_excess_max_ms\":%lld,"
    "\"bytes_in\":%llu",
    (long long)p.period_ms, (unsigned long long)p.frames_in, (long long)p.played_ms,
    (long long)p.underrun_ms, (long long)p.gap_ms,
    (long long)p.drop_overflow_ms, (long long)p.drop_catchup_ms, (long long)p.drop_replay_ms,
    (long long)p.drop_aead_ms, (long long)p.drop_mute_ms, (long long)p.drop_decode_ms, (long long)p.plc_ms,
    (long long)p.queue_min_ms, (long long)p.queue_avg_ms, (long long)p.queue_max_ms,
    p.target_ms, p.jitter_ms, (double)p.scale_rate,
    (long long)p.delay_excess_min_ms, (long long)p.delay_excess_avg_ms, (long long)p.delay_excess_max_ms,
    (unsigned long long)p.bytes_in);
  return std::string(buf, clamped_len(n, sizeof(buf)));
}

std::string format_mic_summary(const MicPeriod& p) {
  char buf[256];
  int n = std::snprintf(buf, sizeof(buf),
    "\"period_ms\":%lld,\"frames_captured\":%llu,\"frames_out\":%llu,"
    "\"write_block_max_ms\":%lld,",
    (long long)p.period_ms, (unsigned long long)p.frames_captured,
    (unsigned long long)p.frames_out, (long long)p.write_block_max_ms);
  std::string s(buf, clamped_len(n, sizeof(buf)));

  if (p.xrun_count != -1) {
    char xbuf[48];
    int xn = std::snprintf(xbuf, sizeof(xbuf), "\"xrun_count\":%d,", p.xrun_count);
    s.append(xbuf, clamped_len(xn, sizeof(xbuf)));
  }

  char buf2[128];
  int n2 = std::snprintf(buf2, sizeof(buf2),
    "\"bitrate_kbps\":%d,\"bytes_out\":%llu,\"muted\":%s",
    p.bitrate_kbps, (unsigned long long)p.bytes_out, p.muted ? "true" : "false");
  s.append(buf2, clamped_len(n2, sizeof(buf2)));
  return s;
}

}  
