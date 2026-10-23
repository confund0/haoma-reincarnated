
#include "../common/jitter_buffer.h"

#include <cmath>
#include <cstdio>
#include <vector>

using namespace haoma::streams;

static int fails = 0;
#define EXPECT(cond, msg) do {                                                   \
  if (!(cond)) { std::fprintf(stderr, "FAIL: %s\n", msg); fails++; }             \
} while (0)

namespace {
constexpr int64_t kMs           = 1000000;     
constexpr int     kSampleRate   = 48000;
constexpr size_t  kFrameSamples = 960;          

std::vector<float> loud_frame() {
  return std::vector<float>(kFrameSamples, 0.5f);  
}
std::vector<float> quiet_frame() {
  return std::vector<float>(kFrameSamples, 0.0f);  
}

double rms(const float* buf, size_t n) {
  double sum_sq = 0.0;
  for (size_t i = 0; i < n; i++) sum_sq += (double)buf[i] * (double)buf[i];
  return std::sqrt(sum_sq / (double)n);
}

struct Lcg {
  uint32_t s;
  explicit Lcg(uint32_t seed) : s(seed) {}
  uint32_t next() { s = s * 1664525u + 1013904223u; return s; }
  double   uniform() { return (double)(next() >> 8) / (double)(1u << 24); }  
};
}  

static void test_steady_no_drops() {
  JbConfig cfg;  
  JitterBuffer jb(cfg, kSampleRate);

  int64_t t = 0;
  for (int i = 0; i < 6; i++) {
    jb.push(t, loud_frame());
    t += 20 * kMs;
  }
  EXPECT(jb.depth_ms() >= cfg.start_target_ms, "steady: primed to target");

  std::vector<float> out(kFrameSamples);
  for (int i = 0; i < 100; i++) {
    t += 20 * kMs;
    jb.push(t, loud_frame());
    size_t got = jb.pop(t, out.data(), kFrameSamples);
    EXPECT(got == kFrameSamples, "steady: full pop, no underrun once primed");
  }

  JbCounters c = jb.take_counters();
  EXPECT(c.drop_catchup_ms == 0, "steady: no catch-up drops");
  EXPECT(c.drop_overflow_ms == 0, "steady: no hard-cap drops");
}

static void test_stall_then_burst_catchup() {
  JbConfig cfg;  
  JitterBuffer jb(cfg, kSampleRate);

  int64_t t = 0;
  for (int i = 0; i < 150; i++) {
    bool quiet = (i % 5 != 0);
    jb.push(t, quiet ? quiet_frame() : loud_frame());
    t += 333 * 1000;  
  }
  int64_t depth_after_burst = jb.depth_ms();
  EXPECT(depth_after_burst <= cfg.hard_cap_ms, "burst: hard cap holds immediately");

  std::vector<int64_t> drop_times;
  std::vector<float>   out(kFrameSamples);
  for (int i = 0; i < 300; i++) {
    t += 20 * kMs;
    int64_t depth_before_push = jb.depth_ms();
    jb.push(t, quiet_frame());
    int64_t depth_after_push = jb.depth_ms();
    if (depth_after_push == depth_before_push) drop_times.push_back(t);
    jb.pop(t, out.data(), kFrameSamples);
  }

  EXPECT(jb.depth_ms() <= cfg.start_target_ms + cfg.slack_ms,
         "stall+burst: settles to at most target + slack within 6 s");

  for (size_t i = 1; i < drop_times.size(); i++) {
    EXPECT(drop_times[i] - drop_times[i - 1] >= (int64_t)cfg.catchup_gap_min_ms * kMs,
           "stall+burst: no two catch-up drops closer than the gap floor (catchup_gap_min_ms)");
  }

  JbCounters c = jb.take_counters();
  int64_t expected = depth_after_burst - cfg.start_target_ms;
  EXPECT(c.drop_catchup_ms >= expected - 40 && c.drop_catchup_ms <= expected + 40,
         "stall+burst: drop_catchup_ms roughly matches the post-burst backlog minus target");
}

static void test_catchup_prefers_quiet() {
  JbConfig cfg;
  cfg.start_target_ms = 140;  
  cfg.target_min_ms   = 140;
  cfg.target_max_ms   = 140;  
  cfg.slack_ms        = 0;    
  cfg.hard_cap_ms     = 10000;  
  JitterBuffer jb(cfg, kSampleRate);

  int64_t t = 0;
  for (int i = 0; i < 10; i++) {
    bool quiet = (i % 3 == 2);  
    jb.push(t, quiet ? quiet_frame() : loud_frame());
    t += 1 * kMs;  
  }

  for (int i = 0; i < 100; i++) {
    t += 20 * kMs;
    jb.push(t, loud_frame());
  }

  JbCounters c = jb.take_counters();
  EXPECT(c.drop_catchup_ms == 60, "quiet-pref: exactly 3 frames (60 ms) dropped");

  std::vector<float> out(kFrameSamples);
  int remaining = (int)(jb.depth_ms() / 20);
  EXPECT(remaining == 107, "quiet-pref: 10 original + 100 filler - 3 dropped = 107 frames left");
  for (int i = 0; i < remaining; i++) {
    size_t got = jb.pop(t, out.data(), kFrameSamples);
    EXPECT(got == kFrameSamples, "quiet-pref: every surviving frame pops cleanly");
    EXPECT(rms(out.data(), got) > 0.1, "quiet-pref: every survivor is loud — quiet ones were dropped");
  }
}

static void test_pace_shortens_with_excess() {
  JbConfig cfg;  
  JitterBuffer jb(cfg, kSampleRate);

  int64_t t = 0;
  for (int i = 0; i < 150; i++) {
    bool quiet = (i % 5 != 0);
    jb.push(t, quiet ? quiet_frame() : loud_frame());
    t += 333 * 1000;
  }

  std::vector<int64_t> drop_times;
  std::vector<float>   out(kFrameSamples);
  for (int i = 0; i < 300; i++) {
    t += 20 * kMs;
    int64_t depth_before_push = jb.depth_ms();
    jb.push(t, quiet_frame());
    int64_t depth_after_push = jb.depth_ms();
    if (depth_after_push == depth_before_push) drop_times.push_back(t);
    jb.pop(t, out.data(), kFrameSamples);
  }

  EXPECT(drop_times.size() >= 4, "pace: enough catch-up drops to compare early vs late spacing");
  if (drop_times.size() >= 4) {
    int64_t early_gap = drop_times[1] - drop_times[0];
    int64_t late_gap  = drop_times[drop_times.size() - 1] - drop_times[drop_times.size() - 2];
    EXPECT(early_gap < late_gap,
           "pace: gap between catch-up drops is shorter right after the burst (high excess) "
           "than near settling (low excess)");
  }
}

static void test_hold_shrinks_with_excess() {
  JbConfig cfg;
  cfg.start_target_ms = 0;
  cfg.target_min_ms   = 0;
  cfg.target_max_ms   = 0;
  cfg.slack_ms        = 0;
  cfg.hard_cap_ms     = 2000;
  JitterBuffer jb(cfg, kSampleRate);

  int64_t t = 0;
  for (int i = 0; i < 100; i++) {  
    jb.push(t, quiet_frame());
    t += 1 * kMs;
  }

  EXPECT(jb.depth_ms() < cfg.hard_cap_ms, "hold-shrink: stayed under the hard cap");
  JbCounters c = jb.take_counters();
  EXPECT(c.drop_catchup_ms > 0,
         "hold-shrink: a large excess enters catch-up well under the full "
         "1000 ms hold, within this 100 ms burst");
  EXPECT(c.drop_overflow_ms == 0, "hold-shrink: no hard-cap drops — isolated to catch-up");
}

static void test_hard_cap_prefers_quiet() {
  JbConfig cfg;
  cfg.catchup_enabled = false;
  cfg.hard_cap_ms     = 200;
  JitterBuffer jb(cfg, kSampleRate);

  int64_t t = 0;
  for (int i = 0; i < 15; i++) {
    bool quiet = (i == 7);  
    jb.push(t, quiet ? quiet_frame() : loud_frame());
    t += 1 * kMs;
  }

  JbCounters c = jb.take_counters();
  EXPECT(c.drop_overflow_ms > 0, "cap-pref: hard cap fired (overflow over 200 ms)");
  EXPECT(c.drop_catchup_ms == 0, "cap-pref: catch-up disabled, no catch-up drops");

  std::vector<float> out(kFrameSamples);
  int remaining = (int)(jb.depth_ms() / 20);
  bool any_quiet_survivor = false;
  for (int i = 0; i < remaining; i++) {
    size_t got = jb.pop(t, out.data(), kFrameSamples);
    EXPECT(got == kFrameSamples, "cap-pref: every surviving frame pops cleanly");
    if (rms(out.data(), got) < 0.1) any_quiet_survivor = true;
  }
  EXPECT(!any_quiet_survivor, "cap-pref: the quiet frame among the overflow was the one dropped");
}

static void test_jitter_raises_target_fixes_underruns() {
  JbConfig cfg;  
  JitterBuffer jb(cfg, kSampleRate);
  jb.set_jitter_ms(45.0);  

  constexpr int64_t kStep    = 20 * kMs;
  constexpr double  kAmpMs   = 18.0;  
  Lcg     rng(3);                     
  int64_t next_arrival    = 0;
  bool    underrun_before = false;
  bool    underrun_after  = false;
  std::vector<float> out(kFrameSamples);

  for (int64_t t = 0; t <= 30000 * kMs; t += kStep) {
    while (next_arrival <= t) {
      jb.push(next_arrival, loud_frame());
      double interval_ms = 20.0 - kAmpMs + 2 * kAmpMs * rng.uniform();
      next_arrival += (int64_t)(interval_ms * (double)kMs);
    }
    bool was_prefilling = jb.is_prefilling();
    size_t got = jb.pop(t, out.data(), kFrameSamples);
    if (!was_prefilling && got < kFrameSamples) {
      if (t < 9000 * kMs) underrun_before = true;
      else if (t > 15000 * kMs) underrun_after = true;
    }
  }

  EXPECT(jb.target_ms() > cfg.start_target_ms, "jitter: target rose above the start value");
  EXPECT(underrun_before, "jitter: the jittery pattern underruns the smaller starting target");
  EXPECT(!underrun_after, "jitter: once the target has risen, the same pattern no longer underruns");
}

static void test_underrun_requires_full_prefill() {
  JbConfig cfg;
  JitterBuffer jb(cfg, kSampleRate);

  int64_t t = 0;
  for (int i = 0; i < 6; i++) {  
    jb.push(t, loud_frame());
    t += 20 * kMs;
  }
  std::vector<float> out(kFrameSamples);
  EXPECT(jb.pop(t, out.data(), kFrameSamples) == kFrameSamples, "underrun: primed, first pop full");

  size_t got = kFrameSamples;
  for (int i = 0; i < 10 && got == kFrameSamples; i++) {
    got = jb.pop(t, out.data(), kFrameSamples);
  }
  EXPECT(got < kFrameSamples, "underrun: queue ran dry");
  EXPECT(jb.is_prefilling(), "underrun: now waiting to refill to target");

  jb.push(t, loud_frame());  
  EXPECT(jb.pop(t, out.data(), kFrameSamples) == 0, "underrun: partial refill still withheld");

  for (int i = 0; i < 5; i++) jb.push(t, loud_frame());  
  EXPECT(jb.pop(t, out.data(), kFrameSamples) == kFrameSamples, "underrun: resumes once refilled to target");
}

static void test_plc_frames_for_gap() {
  EXPECT(plc_frames_for_gap(10, 10) == 0, "plc: no gap");
  EXPECT(plc_frames_for_gap(10, 11) == 1, "plc: gap of 1");
  EXPECT(plc_frames_for_gap(10, 13) == 3, "plc: gap of 3");
  EXPECT(plc_frames_for_gap(10, 60) == 3, "plc: gap of 50, capped at 3");
}

static void test_scale_disabled_is_exact_and_rate_pinned() {
  JbConfig cfg;  
  JitterBuffer jb(cfg, kSampleRate);

  int64_t t = 0;
  for (int i = 0; i < 60; i++) {  
    jb.push(t, loud_frame());
    t += 1 * kMs;
  }
  EXPECT(jb.scale_rate() == 1.0f, "scale off: rate pinned at 1.0 regardless of backlog");

  std::vector<float> out(kFrameSamples);
  int64_t consumed = -1;
  size_t got = jb.pop(t, out.data(), kFrameSamples, &consumed);
  EXPECT(got == kFrameSamples, "scale off: full pop");
  EXPECT(consumed == (int64_t)got, "scale off: consumed_samples equals the return value");
}

static void test_scale_enabled_ramps_and_reports_consumed() {
  JbConfig cfg;
  cfg.scale_enabled     = true;
  cfg.scale_step_per_ms = 1.0f;
  cfg.start_target_ms = 0;
  cfg.target_min_ms   = 0;
  cfg.target_max_ms   = 0;
  cfg.slack_ms        = 0;
  JitterBuffer jb(cfg, kSampleRate);

  int64_t t = 0;
  for (int i = 0; i < 60; i++) {
    jb.push(t, loud_frame());
    t += 1 * kMs;
  }
  EXPECT(std::abs(jb.scale_rate() - cfg.scale_max_rate) < 1e-3f,
         "scale on: one push with a huge step ramps straight to scale_max_rate");

  std::vector<float> out(kFrameSamples);
  int64_t consumed = -1;
  size_t got = jb.pop(t, out.data(), kFrameSamples, &consumed);
  EXPECT(got == kFrameSamples, "scale on: still a full pop (plenty buffered)");
  EXPECT(consumed > (int64_t)got, "scale on: consumed more source samples than it emitted");
  EXPECT((double)consumed <= (double)got * cfg.scale_max_rate + 1.0,
         "scale on: consumed stays within one sample of got*rate");
}

static void test_scale_enabled_drains_without_dropping() {
  JbConfig cfg;
  cfg.catchup_enabled   = false;  
  cfg.scale_enabled     = true;
  cfg.scale_step_per_ms = 1.0f;   
  JitterBuffer jb(cfg, kSampleRate);

  int64_t t = 0;
  for (int i = 0; i < 45; i++) {  
    jb.push(t, loud_frame());
    t += 1 * kMs;
  }
  EXPECT(jb.depth_ms() <= cfg.hard_cap_ms, "scale drain: stayed under the hard cap");

  int64_t total_got = 0, total_consumed = 0;
  std::vector<float> out(kFrameSamples);
  for (int i = 0; i < 300; i++) {
    t += 20 * kMs;
    jb.push(t, quiet_frame());
    int64_t consumed = -1;
    size_t got = jb.pop(t, out.data(), kFrameSamples, &consumed);
    total_got      += (int64_t)got;
    total_consumed += consumed;
  }

  EXPECT(total_consumed > total_got,
         "scale drain: more source samples consumed than emitted over the backlog period");
  JbCounters c = jb.take_counters();
  EXPECT(c.drop_catchup_ms == 0 && c.drop_overflow_ms == 0,
         "scale drain: catch-up disabled and the hard cap never fired — nothing was dropped");
}

static void test_interpolation_stays_in_bounds() {
  JbConfig cfg;
  cfg.scale_enabled     = true;
  cfg.scale_step_per_ms = 1.0f;
  cfg.start_target_ms = 0;
  cfg.target_min_ms   = 0;
  cfg.target_max_ms   = 0;
  cfg.slack_ms        = 0;
  JitterBuffer jb(cfg, kSampleRate);

  int64_t t = 0;
  for (int i = 0; i < 40; i++) {  
    std::vector<float> frame(kFrameSamples);
    for (size_t j = 0; j < kFrameSamples; j++) {
      frame[j] = (float)(i * (int)kFrameSamples + (int)j);  
    }
    jb.push(t, std::move(frame));
    t += 1 * kMs;
  }
  EXPECT(jb.scale_rate() > 1.0f, "interp bounds: rate ramped above 1.0 before popping");

  std::vector<float> out(kFrameSamples);
  size_t got = jb.pop(t, out.data(), kFrameSamples);
  EXPECT(got == kFrameSamples, "interp bounds: full pop");
  bool monotonic = true;
  for (size_t i = 1; i < got; i++) {
    if (out[i] < out[i - 1] - 1e-3f) { monotonic = false; break; }
  }
  EXPECT(monotonic,
         "interp bounds: interpolating a globally increasing ramp at rate>1 stays monotonic — no overshoot");
}

int main() {
  test_steady_no_drops();
  test_stall_then_burst_catchup();
  test_catchup_prefers_quiet();
  test_pace_shortens_with_excess();
  test_hold_shrinks_with_excess();
  test_hard_cap_prefers_quiet();
  test_jitter_raises_target_fixes_underruns();
  test_underrun_requires_full_prefill();
  test_plc_frames_for_gap();
  test_scale_disabled_is_exact_and_rate_pinned();
  test_scale_enabled_ramps_and_reports_consumed();
  test_scale_enabled_drains_without_dropping();
  test_interpolation_stays_in_bounds();

  if (fails) {
    std::fprintf(stderr, "%d failures\n", fails);
    return 1;
  }
  std::fprintf(stderr, "all passed\n");
  return 0;
}
