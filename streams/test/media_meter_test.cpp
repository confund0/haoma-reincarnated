
#include "../common/media_meter.h"

#include <cstdio>

using namespace haoma::streams;

static int fails = 0;
#define EXPECT(cond, msg) do {                                                   \
  if (!(cond)) { std::fprintf(stderr, "FAIL: %s\n", msg); fails++; }             \
} while (0)

namespace {
constexpr int64_t kMs = 1000000;  
constexpr int64_t kS  = 1000000000;  
}  

static void test_delay_excess_steady() {
  DelayExcess de;
  int64_t pts = 0;
  int64_t fixed_delay = 100 * kMs;
  EXPECT(de.on_frame(pts + fixed_delay, pts) == 0, "steady: first frame sets baseline, excess 0");
  for (int i = 1; i < 50; i++) {
    pts += 20 * kMs;
    int64_t excess = de.on_frame(pts + fixed_delay, pts);
    EXPECT(excess == 0, "steady: constant delay keeps excess 0");
  }
}

static void test_delay_excess_pause_then_burst() {
  DelayExcess de;
  EXPECT(de.on_frame(100 * kMs, 0) == 0, "pause: baseline frame");

  int64_t excess = de.on_frame(3120 * kMs, 20 * kMs);  
  EXPECT(excess >= 2990 * kMs && excess <= 3000 * kMs,
         "pause: burst frame shows ~3000 ms excess");

  excess = de.on_frame(3121 * kMs, 3021 * kMs);  
  EXPECT(excess == 0, "pause: back to 0 once the backlog has drained");
}

static void test_delay_excess_base_rise_limit() {
  DelayExcess de2;
  EXPECT(de2.on_frame(0, 0) == 0, "rise limit: baseline frame (d=0)");

  int64_t now1 = kS;
  int64_t e1 = de2.on_frame(now1, now1 - 1 * kMs);  
  EXPECT(e1 == 800000, "rise limit: fast jump only partly absorbed");

  int64_t now2 = 2 * kS;
  int64_t e2 = de2.on_frame(now2, now2 - 400000);  
  EXPECT(e2 == 0, "rise limit: on-budget drift fully absorbed");
}

static void test_samples_to_ms() {
  EXPECT(samples_to_ms(960, 48000) == 20, "samples_to_ms: one 20 ms frame");
  EXPECT(samples_to_ms(48000, 48000) == 1000, "samples_to_ms: one full second");
  EXPECT(samples_to_ms(0, 48000) == 0, "samples_to_ms: zero samples");
  EXPECT(samples_to_ms(1920, 48000) == 40, "samples_to_ms: two dropped frames, not one");
}

static void test_xrun_delta() {
  EXPECT(xrun_delta(5, 2) == 3, "xrun_delta: rise");
  EXPECT(xrun_delta(2, 2) == 0, "xrun_delta: no rise");
  EXPECT(xrun_delta(0, 7) == 0, "xrun_delta: reopen with no new xrun yet");
  EXPECT(xrun_delta(2, 7) == 2, "xrun_delta: reopen, new reading is the whole rise");
}

static void test_min_avg_max() {
  MinAvgMax m;
  EXPECT(m.min() == 0 && m.avg() == 0 && m.max() == 0, "empty period reports 0/0/0");
  m.add(10);
  m.add(30);
  m.add(20);
  EXPECT(m.min() == 10, "min of 10,30,20");
  EXPECT(m.max() == 30, "max of 10,30,20");
  EXPECT(m.avg() == 20, "avg of 10,30,20");
  m.reset();
  EXPECT(m.min() == 0 && m.avg() == 0 && m.max() == 0, "reset back to empty");
}

static void test_period_clock_late_tick_and_pause() {
  PeriodClock pc(0);
  int64_t period_ms = 0;

  EXPECT(!pc.tick(500 * kMs, &period_ms), "no summary before 1 s");

  EXPECT(pc.tick(1400 * kMs, &period_ms), "late tick: summary due");
  EXPECT(period_ms == 1400, "late tick: real elapsed span, not 1000");

  EXPECT(!pc.tick(1900 * kMs, &period_ms), "not yet 1 s since the catch-up");
  EXPECT(pc.tick(2000 * kMs, &period_ms), "back on the 1 s grid: summary due");
  EXPECT(period_ms == 1000, "back on the 1 s grid: exactly 1000 ms");
}

static void test_period_clock_long_pause() {
  PeriodClock pc(0);
  int64_t period_ms = 0;
  EXPECT(pc.tick(10400 * kMs, &period_ms), "10.4 s pause: summary due");
  EXPECT(period_ms == 10400, "10.4 s pause: real elapsed span");

  EXPECT(!pc.tick(10400 * kMs + 1, &period_ms), "no second summary right away");
  EXPECT(pc.tick(11000 * kMs, &period_ms), "next summary exactly 1 s later");
  EXPECT(period_ms == 1000, "next summary back on the 1 s grid");
}

static void test_format_spk_summary() {
  SpkPeriod p;
  p.period_ms = 1000;
  p.frames_in = 50;
  p.played_ms = 1000;
  p.underrun_ms = 20;
  p.gap_ms = 40;
  p.drop_overflow_ms = 0;
  p.drop_catchup_ms = 0;
  p.drop_replay_ms = 0;
  p.drop_aead_ms = 0;
  p.drop_mute_ms = 0;
  p.drop_decode_ms = 0;
  p.plc_ms = 0;
  p.queue_min_ms = 100;
  p.queue_avg_ms = 118;
  p.queue_max_ms = 140;
  p.target_ms = 120;
  p.jitter_ms = 12.4;
  p.scale_rate = 1.0f;
  p.delay_excess_min_ms = 20;
  p.delay_excess_avg_ms = 40;
  p.delay_excess_max_ms = 60;
  p.bytes_in = 5693;

  std::string s = format_spk_summary(p);

  std::string expected =
    "\"period_ms\":1000,\"frames_in\":50,\"played_ms\":1000,"
    "\"underrun_ms\":20,\"gap_ms\":40,"
    "\"drop_overflow_ms\":0,\"drop_catchup_ms\":0,\"drop_replay_ms\":0,"
    "\"drop_aead_ms\":0,\"drop_mute_ms\":0,\"drop_decode_ms\":0,\"plc_ms\":0,"
    "\"queue_min_ms\":100,\"queue_avg_ms\":118,\"queue_max_ms\":140,"
    "\"target_ms\":120,\"jitter_ms\":12.40,\"scale_rate\":1.000,"
    "\"delay_excess_min_ms\":20,\"delay_excess_avg_ms\":40,\"delay_excess_max_ms\":60,"
    "\"bytes_in\":5693";
  EXPECT(s == expected, "spk summary matches the exact field contract");
}

static void test_format_mic_summary_xrun() {
  MicPeriod p;
  p.period_ms = 1000;
  p.frames_captured = 50;
  p.frames_out = 50;
  p.write_block_max_ms = 0;
  p.xrun_count = -1;
  p.bitrate_kbps = 32;
  p.bytes_out = 5693;
  p.muted = false;

  std::string s_no_xrun = format_mic_summary(p);
  std::string expected_no_xrun =
    "\"period_ms\":1000,\"frames_captured\":50,\"frames_out\":50,"
    "\"write_block_max_ms\":0,"
    "\"bitrate_kbps\":32,\"bytes_out\":5693,\"muted\":false";
  EXPECT(s_no_xrun == expected_no_xrun,
         "mic summary (xrun_count=-1) matches the exact field contract, xrun_count left out");

  p.xrun_count = 3;
  p.muted = true;
  std::string s_xrun = format_mic_summary(p);
  std::string expected_xrun =
    "\"period_ms\":1000,\"frames_captured\":50,\"frames_out\":50,"
    "\"write_block_max_ms\":0,\"xrun_count\":3,"
    "\"bitrate_kbps\":32,\"bytes_out\":5693,\"muted\":true";
  EXPECT(s_xrun == expected_xrun,
         "mic summary (xrun_count=3) matches the exact field contract, xrun_count included");
}

int main() {
  test_delay_excess_steady();
  test_delay_excess_pause_then_burst();
  test_delay_excess_base_rise_limit();
  test_samples_to_ms();
  test_xrun_delta();
  test_min_avg_max();
  test_period_clock_late_tick_and_pause();
  test_period_clock_long_pause();
  test_format_spk_summary();
  test_format_mic_summary_xrun();

  if (fails) {
    std::fprintf(stderr, "%d failures\n", fails);
    return 1;
  }
  std::fprintf(stderr, "all passed\n");
  return 0;
}
