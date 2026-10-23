
#include "../common/playout_scaler.h"

#include <cmath>
#include <cstdio>

using namespace haoma::streams;

static int fails = 0;
#define EXPECT(cond, msg) do {                                                   \
  if (!(cond)) { std::fprintf(stderr, "FAIL: %s\n", msg); fails++; }             \
} while (0)

namespace {
constexpr double kEps = 1e-4;
}  

static void test_under_slack_stays_at_one() {
  PlayoutScalerConfig cfg;
  PlayoutScaler ps(cfg);

  ps.update( 0,  1000);
  EXPECT(ps.current_rate() == 1.0f, "under slack: zero excess stays at 1.0");

  ps.update(cfg.slack_ms, 1000);
  EXPECT(ps.current_rate() == 1.0f, "under slack: excess == slack_ms stays at 1.0");
}

static void test_step_is_bounded_by_elapsed() {
  PlayoutScalerConfig cfg;
  PlayoutScaler ps(cfg);

  ps.update( cfg.hard_cap_ms,  20);  
  float expect_step = cfg.scale_step_per_ms * 20.0f;
  EXPECT(std::abs(ps.current_rate() - (1.0f + expect_step)) < kEps,
         "bounded step: one 20 ms call moves by scale_step_per_ms*20");
  EXPECT(ps.current_rate() < cfg.scale_max_rate,
         "bounded step: one small call does not jump straight to the target");
}

static void test_ramps_up_to_max_and_holds() {
  PlayoutScalerConfig cfg;
  PlayoutScaler ps(cfg);

  for (int i = 0; i < 100; i++) ps.update(cfg.hard_cap_ms, 10);
  EXPECT(std::abs(ps.current_rate() - cfg.scale_max_rate) < kEps,
         "ramp up: settles at scale_max_rate");

  ps.update(cfg.hard_cap_ms, 10);
  EXPECT(ps.current_rate() <= cfg.scale_max_rate,
         "ramp up: never overshoots scale_max_rate");
}

static void test_ramps_back_down_to_one() {
  PlayoutScalerConfig cfg;
  PlayoutScaler ps(cfg);

  for (int i = 0; i < 100; i++) ps.update(cfg.hard_cap_ms, 10);
  EXPECT(ps.current_rate() > 1.0f, "ramp down: starts from an elevated rate");

  for (int i = 0; i < 100; i++) ps.update(0, 10);
  EXPECT(std::abs(ps.current_rate() - 1.0f) < kEps,
         "ramp down: settles back at 1.0");
}

static void test_midpoint_rate_is_linear() {
  PlayoutScalerConfig cfg;
  PlayoutScaler ps(cfg);

  int64_t mid = cfg.slack_ms + (cfg.hard_cap_ms - cfg.slack_ms) / 2;
  for (int i = 0; i < 200; i++) ps.update(mid, 10);  

  float expect = 1.0f + 0.5f * (cfg.scale_max_rate - 1.0f);
  EXPECT(std::abs(ps.current_rate() - expect) < 0.01f,
         "midpoint: settles halfway between 1.0 and scale_max_rate");
}

int main() {
  test_under_slack_stays_at_one();
  test_step_is_bounded_by_elapsed();
  test_ramps_up_to_max_and_holds();
  test_ramps_back_down_to_one();
  test_midpoint_rate_is_linear();

  if (fails) {
    std::fprintf(stderr, "%d failures\n", fails);
    return 1;
  }
  std::printf("ok\n");
  return 0;
}
