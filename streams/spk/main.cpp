
#include "../common/framing.h"
#include "../common/socket.h"
#include "../common/log.h"
#include "../common/aead.h"
#include "../common/key_fd.h"
#include "../common/control.h"
#include "../common/media_meter.h"
#include "../common/jitter_buffer.h"
#include "../platform/audio_backend.h"

#include <opus/opus.h>

#include <atomic>
#include <chrono>
#include <csignal>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <deque>
#include <mutex>
#include <string>
#include <thread>
#include <vector>
#include <unistd.h>
#include <sys/socket.h>

using namespace haoma::streams;

namespace {

struct Args {
  uint16_t    port = 0;
  std::string stream_id;
  bool        trace = false;
  bool        stream_debug = false;
  int   jb_start_target_ms    = JbConfig{}.start_target_ms;
  int   jb_target_min_ms      = JbConfig{}.target_min_ms;
  int   jb_target_max_ms      = JbConfig{}.target_max_ms;
  int   jb_slack_ms           = JbConfig{}.slack_ms;
  int   jb_catchup_hold_ms    = JbConfig{}.catchup_hold_ms;
  int   jb_catchup_gap_ms     = JbConfig{}.catchup_gap_ms;
  int   jb_catchup_gap_min_ms = JbConfig{}.catchup_gap_min_ms;
  int   jb_quiet_wait_ms      = JbConfig{}.quiet_wait_ms;
  int   jb_hard_cap_ms        = JbConfig{}.hard_cap_ms;
  float jb_quiet_rms          = JbConfig{}.quiet_rms;
  bool  jb_catchup_enabled    = JbConfig{}.catchup_enabled;
  bool  jb_scale_enabled      = JbConfig{}.scale_enabled;
  float jb_scale_max_rate     = JbConfig{}.scale_max_rate;
  float jb_scale_step_per_ms  = JbConfig{}.scale_step_per_ms;
};

bool parse_args(int argc, char** argv, Args& a) {
  for (int i = 1; i < argc; ++i) {
    std::string s = argv[i];
    if ((s == "--port" || s == "-p") && i + 1 < argc) {
      int p = std::atoi(argv[++i]);
      if (p < 1 || p > 65535) { LOG_ERR("invalid --port: %d", p); return false; }
      a.port = (uint16_t)p;
    } else if (s == "--stream-id" && i + 1 < argc) {
      a.stream_id = argv[++i];
    } else if (s == "--trace") {
      a.trace = true;
    } else if (s == "--stream-debug") {
      a.stream_debug = true;
    } else if (s == "--jb-start-target-ms" && i + 1 < argc) {
      int v = std::atoi(argv[++i]);
      if (v <= 0) { LOG_ERR("invalid --jb-start-target-ms: %d", v); return false; }
      a.jb_start_target_ms = v;
    } else if (s == "--jb-target-min-ms" && i + 1 < argc) {
      int v = std::atoi(argv[++i]);
      if (v <= 0) { LOG_ERR("invalid --jb-target-min-ms: %d", v); return false; }
      a.jb_target_min_ms = v;
    } else if (s == "--jb-target-max-ms" && i + 1 < argc) {
      int v = std::atoi(argv[++i]);
      if (v <= 0) { LOG_ERR("invalid --jb-target-max-ms: %d", v); return false; }
      a.jb_target_max_ms = v;
    } else if (s == "--jb-slack-ms" && i + 1 < argc) {
      int v = std::atoi(argv[++i]);
      if (v <= 0) { LOG_ERR("invalid --jb-slack-ms: %d", v); return false; }
      a.jb_slack_ms = v;
    } else if (s == "--jb-catchup-hold-ms" && i + 1 < argc) {
      int v = std::atoi(argv[++i]);
      if (v <= 0) { LOG_ERR("invalid --jb-catchup-hold-ms: %d", v); return false; }
      a.jb_catchup_hold_ms = v;
    } else if (s == "--jb-catchup-gap-ms" && i + 1 < argc) {
      int v = std::atoi(argv[++i]);
      if (v <= 0) { LOG_ERR("invalid --jb-catchup-gap-ms: %d", v); return false; }
      a.jb_catchup_gap_ms = v;
    } else if (s == "--jb-catchup-gap-min-ms" && i + 1 < argc) {
      int v = std::atoi(argv[++i]);
      if (v <= 0) { LOG_ERR("invalid --jb-catchup-gap-min-ms: %d", v); return false; }
      a.jb_catchup_gap_min_ms = v;
    } else if (s == "--jb-quiet-wait-ms" && i + 1 < argc) {
      int v = std::atoi(argv[++i]);
      if (v <= 0) { LOG_ERR("invalid --jb-quiet-wait-ms: %d", v); return false; }
      a.jb_quiet_wait_ms = v;
    } else if (s == "--jb-hard-cap-ms" && i + 1 < argc) {
      int v = std::atoi(argv[++i]);
      if (v <= 0) { LOG_ERR("invalid --jb-hard-cap-ms: %d", v); return false; }
      a.jb_hard_cap_ms = v;
    } else if (s == "--jb-quiet-rms" && i + 1 < argc) {
      float v = (float)std::atof(argv[++i]);
      if (!(v > 0)) { LOG_ERR("invalid --jb-quiet-rms: %f", (double)v); return false; }
      a.jb_quiet_rms = v;
    } else if (s == "--jb-catchup-disabled") {
      a.jb_catchup_enabled = false;
    } else if (s == "--jb-scale-enabled") {
      a.jb_scale_enabled = true;
    } else if (s == "--jb-scale-max-rate" && i + 1 < argc) {
      float v = (float)std::atof(argv[++i]);
      if (!(v > 1.0f)) { LOG_ERR("invalid --jb-scale-max-rate: %f", (double)v); return false; }
      a.jb_scale_max_rate = v;
    } else if (s == "--jb-scale-step-per-ms" && i + 1 < argc) {
      float v = (float)std::atof(argv[++i]);
      if (!(v > 0)) { LOG_ERR("invalid --jb-scale-step-per-ms: %f", (double)v); return false; }
      a.jb_scale_step_per_ms = v;
    } else if (s == "--help" || s == "-h") {
      std::fprintf(stderr,
        "usage: haoma-spk --port N --stream-id ID [--trace] [--stream-debug]\n"
        "  binds 127.0.0.1:N, accepts ONE client, AEAD-opens incoming\n"
        "  framed Opus, plays back at 48k/mono. Bad tag = silent drop.\n"
        "  Reads 32-byte key from the first 32 bytes of stdin;\n"
        "  remainder of stdin is JSON-line control input.\n"
        "  --stream-id ID is one of: mic | cam | screen.\n"
        "  --trace ups stats cadence + emits per-frame trace lines.\n"
        "  --stream-debug emits a 200 ms \"sample\" line.\n"
        "  --jb-* tune the jitter buffer (see JbConfig in jitter_buffer.h);\n"
        "  --jb-catchup-disabled turns off catch-up. --jb-scale-enabled turns\n"
        "  on the playout time-scaler, off by default. target-min\n"
        "  <= target-max, catchup-gap-min <= catchup-gap, scale-max-rate > 1.0,\n"
        "  scale-step-per-ms > 0, hard-cap > slack and > 0.\n");
      std::exit(0);
    } else {
      LOG_ERR("unknown arg: %s", argv[i]);
      return false;
    }
  }
  if (a.port == 0) { LOG_ERR("--port required"); return false; }
  if (a.stream_id.empty()) { LOG_ERR("--stream-id required"); return false; }
  if (a.jb_hard_cap_ms <= a.jb_slack_ms) {
    LOG_ERR("invalid jitter-buffer config: --jb-hard-cap-ms (%d) must be > --jb-slack-ms (%d)",
            a.jb_hard_cap_ms, a.jb_slack_ms);
    return false;
  }
  if (a.jb_hard_cap_ms <= 0) {
    LOG_ERR("invalid jitter-buffer config: --jb-hard-cap-ms (%d) must be > 0", a.jb_hard_cap_ms);
    return false;
  }
  if (a.jb_target_min_ms > a.jb_target_max_ms) {
    LOG_ERR("invalid jitter-buffer config: --jb-target-min-ms (%d) must be <= --jb-target-max-ms (%d)",
            a.jb_target_min_ms, a.jb_target_max_ms);
    return false;
  }
  if (a.jb_catchup_gap_min_ms > a.jb_catchup_gap_ms) {
    LOG_ERR("invalid jitter-buffer config: --jb-catchup-gap-min-ms (%d) must be <= --jb-catchup-gap-ms (%d)",
            a.jb_catchup_gap_min_ms, a.jb_catchup_gap_ms);
    return false;
  }
  return true;
}

class SpkQueue {
  JitterBuffer jb_;
  std::mutex   m_;

 public:
  explicit SpkQueue(JbConfig cfg, int64_t sample_rate) : jb_(cfg, sample_rate) {}

  void push(int64_t now_ns, std::vector<float>&& chunk) {
    std::lock_guard<std::mutex> lk(m_);
    jb_.push(now_ns, std::move(chunk));
  }
  size_t pop(int64_t now_ns, float* out, size_t n, int64_t* consumed_samples = nullptr) {
    std::lock_guard<std::mutex> lk(m_);
    return jb_.pop(now_ns, out, n, consumed_samples);
  }
  void clear() {
    std::lock_guard<std::mutex> lk(m_);
    jb_.clear();
  }
  void set_jitter_ms(double j) {
    std::lock_guard<std::mutex> lk(m_);
    jb_.set_jitter_ms(j);
  }
  JbCounters take_counters() {
    std::lock_guard<std::mutex> lk(m_);
    return jb_.take_counters();
  }
  int64_t depth_ms() {
    std::lock_guard<std::mutex> lk(m_);
    return jb_.depth_ms();
  }
  int target_ms() {
    std::lock_guard<std::mutex> lk(m_);
    return jb_.target_ms();
  }
  double jitter_ms() {
    std::lock_guard<std::mutex> lk(m_);
    return jb_.jitter_ms();
  }
  float scale_rate() {
    std::lock_guard<std::mutex> lk(m_);
    return jb_.scale_rate();
  }
  bool is_prefilling() {
    std::lock_guard<std::mutex> lk(m_);
    return jb_.is_prefilling();
  }
};

std::atomic<bool> g_done{false};
void on_signal(int) { g_done.store(true); }

struct SpkMeterAccum {
  std::mutex  mu;
  DelayExcess delay;
  MinAvgMax   queue_ms;
  MinAvgMax   delay_excess_ms;
  uint64_t    frames_in             = 0;
  int64_t     gap_ms                = 0;
  int64_t     drop_replay_ms        = 0;
  int64_t     drop_aead_ms          = 0;
  int64_t     drop_mute_ms          = 0;
  int64_t     drop_decode_ms        = 0;
  int64_t     plc_ms                = 0;  
  uint64_t    bytes_in              = 0;

  std::atomic<int64_t> played_samples{0};
  std::atomic<int64_t> underrun_samples{0};
  std::atomic<int64_t> underrun_event_pending{0};
  std::atomic<bool>    ever_pushed{false};

  void reset_period_locked() {
    queue_ms.reset();
    delay_excess_ms.reset();
    frames_in             = 0;
    gap_ms                = 0;
    drop_replay_ms        = 0;
    drop_aead_ms          = 0;
    drop_mute_ms          = 0;
    drop_decode_ms        = 0;
    plc_ms                = 0;
    bytes_in              = 0;
  }
};

}  

int main(int argc, char** argv) {
  set_log_tag("haoma-spk");
  Args a;
  if (!parse_args(argc, argv, a)) {
    std::fprintf(stderr, "usage: haoma-spk --port N --stream-id ID [--trace]\n");
    return 1;
  }

  struct sigaction sa{};
  sa.sa_handler = on_signal;
  ::sigemptyset(&sa.sa_mask);
  sa.sa_flags = 0;
  ::sigaction(SIGINT,  &sa, nullptr);
  ::sigaction(SIGTERM, &sa, nullptr);
  sa.sa_handler = SIG_IGN;
  ::sigaction(SIGPIPE, &sa, nullptr);

  if (!Aead::global_init()) return 2;

  uint8_t key[AEAD_KEY_BYTES];
  if (!read_key(STDIN_FILENO, key)) return 2;
  Aead aead;
  aead.configure(key, a.stream_id);
  for (size_t i = 0; i < sizeof(key); ++i) key[i] = 0;

  int err = 0;
  OpusDecoder* dec = opus_decoder_create(SAMPLE_RATE, CHANNELS, &err);
  if (!dec || err != OPUS_OK) {
    LOG_ERR("opus_decoder_create: %s", opus_strerror(err));
    return 2;
  }

  int lfd = listen_local(a.port);
  if (lfd < 0) { opus_decoder_destroy(dec); return 3; }
  LOG_INFO("listening on 127.0.0.1:%u (stream=%s)", a.port, a.stream_id.c_str());

  emit_ready();

  int cfd = accept_one(lfd);
  if (cfd < 0) { opus_decoder_destroy(dec); return 4; }
  LOG_INFO("client connected");

  auto pb = make_playback();
  if (!pb) {
    LOG_ERR("make_playback returned null");
    emit_error("audio_playback_open");
    ::close(cfd); opus_decoder_destroy(dec);
    return 5;
  }

  JbConfig jb_cfg;
  jb_cfg.start_target_ms    = a.jb_start_target_ms;
  jb_cfg.target_min_ms      = a.jb_target_min_ms;
  jb_cfg.target_max_ms      = a.jb_target_max_ms;
  jb_cfg.slack_ms           = a.jb_slack_ms;
  jb_cfg.catchup_hold_ms    = a.jb_catchup_hold_ms;
  jb_cfg.catchup_gap_ms     = a.jb_catchup_gap_ms;
  jb_cfg.catchup_gap_min_ms = a.jb_catchup_gap_min_ms;
  jb_cfg.quiet_wait_ms      = a.jb_quiet_wait_ms;
  jb_cfg.hard_cap_ms        = a.jb_hard_cap_ms;
  jb_cfg.quiet_rms          = a.jb_quiet_rms;
  jb_cfg.catchup_enabled    = a.jb_catchup_enabled;
  jb_cfg.scale_enabled      = a.jb_scale_enabled;
  jb_cfg.scale_max_rate     = a.jb_scale_max_rate;
  jb_cfg.scale_step_per_ms  = a.jb_scale_step_per_ms;
  SpkQueue           queue(jb_cfg, SAMPLE_RATE);
  Stats              stats;
  JitterTracker      jitter;
  SpkMeterAccum      meter;
  std::atomic<bool>  trace{a.trace};
  std::atomic<bool>  stream_debug{a.stream_debug};
  std::atomic<bool>  stats_req{false};
  std::atomic<bool>  muted{false};

  std::atomic<uint64_t> sample_frames_in{0};
  std::atomic<int64_t>  sample_plc_ms{0};
  std::atomic<int64_t>  sample_drop_ms{0};  
  std::atomic<int64_t>  sample_last_delay_excess_ms{0};

  uint64_t           expected_counter = 0;
  bool               first_frame      = true;
  uint64_t           aead_fail_count  = 0;

  struct ClockEntry { int64_t sample_idx_start; int64_t sender_pts_ns; };
  std::mutex                    clock_mu;
  std::deque<ClockEntry>        clock_ring;
  int64_t                       total_pushed_samples = 0;
  constexpr size_t              kClockRingCap = 200;

  std::atomic<int64_t>          total_output_samples{0};
  std::atomic<int64_t>          total_source_consumed_samples{0};
  struct RateEntry { int64_t output_cum; int64_t source_cum; };
  std::deque<RateEntry>         rate_ring;
  constexpr size_t              kRateRingCap = 200;

  std::thread reader([&]() {
    std::vector<uint8_t> cipher(MAX_PAYLOAD_LEN);
    std::vector<uint8_t> plain(MAX_PAYLOAD_LEN);
    uint8_t tag[FRAME_TAG_LEN];
    while (!g_done.load()) {
      uint64_t counter = 0;
      uint64_t pts_ns  = 0;
      int64_t n = read_frame(cfd, &counter, &pts_ns, cipher.data(), cipher.size(), tag);
      if (n == 0) { LOG_INFO("peer EOF"); g_done.store(true); break; }
      if (n <  0) { LOG_ERR("read_frame error"); g_done.store(true); break; }

      stats.bytes_in.fetch_add(FRAME_OVERHEAD + (uint64_t)n);
      jitter.on_frame_arrival(std::chrono::steady_clock::now());
      queue.set_jitter_ms(jitter.current_ms());

      { std::lock_guard<std::mutex> lk(meter.mu); meter.bytes_in += FRAME_OVERHEAD + (uint64_t)n; }

      if (!aead.open(counter, pts_ns, cipher.data(), (size_t)n, tag, plain.data())) {
        if (aead_fail_count == 0) {
          LOG_INFO("AEAD verify failed at counter %llu — wrong key or tampered frame; silently dropping",
                   (unsigned long long)counter);
        }
        aead_fail_count++;
        stats.frames_dropped.fetch_add(1);
        { std::lock_guard<std::mutex> lk(meter.mu); meter.drop_aead_ms += FRAME_MS; }
        sample_drop_ms.fetch_add(FRAME_MS, std::memory_order_relaxed);
        continue;  
      }

      {
        std::lock_guard<std::mutex> lk(meter.mu);
        int64_t excess_ns = meter.delay.on_frame(steady_now_ns(), (int64_t)pts_ns);
        meter.delay_excess_ms.add(excess_ns / 1000000);
        sample_last_delay_excess_ms.store(excess_ns / 1000000, std::memory_order_relaxed);
      }

      if (first_frame) {
        expected_counter = counter;
        first_frame = false;
      } else if (counter < expected_counter) {
        LOG_DBG("counter replay: got %llu expected >= %llu — dropping",
                (unsigned long long)counter, (unsigned long long)expected_counter);
        stats.frames_dropped.fetch_add(1);
        { std::lock_guard<std::mutex> lk(meter.mu); meter.drop_replay_ms += FRAME_MS; }
        sample_drop_ms.fetch_add(FRAME_MS, std::memory_order_relaxed);
        continue;
      } else if (counter != expected_counter) {
        LOG_DBG("counter skew: got %llu expected %llu",
                (unsigned long long)counter, (unsigned long long)expected_counter);
        uint64_t gap_frames = counter - expected_counter;
        int      plc_n      = plc_frames_for_gap(expected_counter, counter);

        int plc_done = 0;
        for (int i = 0; i < plc_n; i++) {
          std::vector<float> plc_pcm(FRAME_SAMPLES);
          int plc_decoded = opus_decode_float(dec, nullptr, 0,
                                              plc_pcm.data(), FRAME_SAMPLES, 0);
          if (plc_decoded < 0) break;  
          plc_pcm.resize((size_t)plc_decoded);
          size_t plc_n_samples = plc_pcm.size();
          {
            std::lock_guard<std::mutex> lk(clock_mu);
            total_pushed_samples += (int64_t)plc_n_samples;
          }
          queue.push(steady_now_ns(), std::move(plc_pcm));
          {
            int64_t plc_ms_now = samples_to_ms((int64_t)plc_n_samples, SAMPLE_RATE);
            std::lock_guard<std::mutex> lk(meter.mu);
            meter.plc_ms += plc_ms_now;
            sample_plc_ms.fetch_add(plc_ms_now, std::memory_order_relaxed);
          }
          plc_done++;
        }

        uint64_t remaining_gap_frames = gap_frames - (uint64_t)plc_done;
        {
          std::lock_guard<std::mutex> lk(meter.mu);
          meter.gap_ms += (int64_t)remaining_gap_frames * FRAME_MS;
        }
        char fields[80];
        std::snprintf(fields, sizeof(fields), "\"missing_ms\":%lld,\"plc_ms\":%lld",
                      (long long)(gap_frames * FRAME_MS), (long long)(plc_done * FRAME_MS));
        emit_event("gap", fields);
      }
      expected_counter = counter + 1;

      stats.frames_in.fetch_add(1);
      { std::lock_guard<std::mutex> lk(meter.mu); meter.frames_in++; }
      sample_frames_in.fetch_add(1, std::memory_order_relaxed);
      if (trace.load()) emit_trace_frame(counter, (uint32_t)(FRAME_OVERHEAD + n), muted.load());

      if (muted.load()) {
        stats.frames_dropped.fetch_add(1);
        { std::lock_guard<std::mutex> lk(meter.mu); meter.drop_mute_ms += FRAME_MS; }
        sample_drop_ms.fetch_add(FRAME_MS, std::memory_order_relaxed);
        continue;
      }

      std::vector<float> pcm(FRAME_SAMPLES);
      int decoded = opus_decode_float(dec, plain.data(), (opus_int32)n,
                                      pcm.data(), FRAME_SAMPLES, 0);
      if (decoded < 0) {
        LOG_ERR("opus_decode_float: %s", opus_strerror(decoded));
        stats.frames_dropped.fetch_add(1);
        { std::lock_guard<std::mutex> lk(meter.mu); meter.drop_decode_ms += FRAME_MS; }
        sample_drop_ms.fetch_add(FRAME_MS, std::memory_order_relaxed);
        continue;
      }
      pcm.resize((size_t)decoded);
      size_t pcm_n = pcm.size();
      {
        std::lock_guard<std::mutex> lk(clock_mu);
        clock_ring.push_back({total_pushed_samples, (int64_t)pts_ns});
        total_pushed_samples += (int64_t)pcm_n;
        while (clock_ring.size() > kClockRingCap) clock_ring.pop_front();
      }
      queue.push(steady_now_ns(), std::move(pcm));
      int64_t queue_now_ms = queue.depth_ms();
      meter.ever_pushed.store(true, std::memory_order_relaxed);
      {
        std::lock_guard<std::mutex> lk(meter.mu);
        meter.queue_ms.add(queue_now_ms);
      }
    }
  });

  bool ok = pb->open([&](float* out, size_t n) -> size_t {
    int64_t source_consumed = 0;
    size_t got = queue.pop(steady_now_ns(), out, n, &source_consumed);
    total_output_samples.fetch_add((int64_t)got, std::memory_order_relaxed);
    total_source_consumed_samples.fetch_add(source_consumed, std::memory_order_relaxed);
    meter.played_samples.fetch_add((int64_t)got, std::memory_order_relaxed);
    if (got < n && meter.ever_pushed.load(std::memory_order_relaxed) && !muted.load()) {
      size_t shortfall = n - got;
      meter.underrun_samples.fetch_add((int64_t)shortfall, std::memory_order_relaxed);
      meter.underrun_event_pending.fetch_add((int64_t)shortfall, std::memory_order_relaxed);
    }
    return got;
  });
  if (!ok) {
    LOG_ERR("audio playback open failed");
    emit_error("audio_playback_open");
    g_done.store(true);
  } else {
    LOG_INFO("playing...");
  }

  std::thread ctrl_th(control_loop, STDIN_FILENO, std::ref(g_done),
    [&](const ControlMsg& m) {
      switch (m.cmd) {
        case Command::Mute:
          muted.store(true);
          queue.clear();
          LOG_INFO("muted");
          break;
        case Command::Unmute:
          muted.store(false);
          LOG_INFO("unmuted");
          break;
        case Command::Bitrate:
          emit_warn("bitrate_not_supported_on_decoder");
          break;
        case Command::Stats:
          stats_req.store(true);
          break;
        case Command::Exit:
          LOG_INFO("exit command");
          g_done.store(true);
          break;
        case Command::Unknown:
          emit_warn("unknown_command");
          break;
      }
    });

  std::thread stats_th(stats_loop, std::ref(stats), &jitter,
                       std::ref(g_done), std::ref(trace), std::ref(stats_req));

  int64_t last_underrun_log_ns = -1;  
  int64_t pending_underrun_ms  = 0;   
  int     last_target_ms       = jb_cfg.start_target_ms;
  int64_t accum_drop_catchup_ms  = 0;
  int64_t accum_drop_overflow_ms = 0;
  int64_t accum_queue_before_ms  = -1;  
  int64_t accum_queue_after_ms   = 0;   
  std::thread meter_th([&]() {
    PeriodClock period_clock(steady_now_ns());
    while (!g_done.load()) {
      for (int slept = 0; slept < 200 && !g_done.load(); slept += 100) {
        std::this_thread::sleep_for(std::chrono::milliseconds(100));
      }
      if (g_done.load()) break;

      int64_t pending_underrun_samples =
          meter.underrun_event_pending.exchange(0, std::memory_order_relaxed);
      int64_t underrun_ms_this_tick = samples_to_ms(pending_underrun_samples, SAMPLE_RATE);
      if (underrun_ms_this_tick > 0) {
        pending_underrun_ms += underrun_ms_this_tick;
      }
      if (pending_underrun_ms > 0) {
        int64_t now_ns      = steady_now_ns();
        int64_t queue_ms_now = queue.depth_ms();
        char fields[96];
        if (last_underrun_log_ns < 0) {
          std::snprintf(fields, sizeof(fields), "\"underrun_ms\":%lld,\"queue_ms\":%lld",
                        (long long)pending_underrun_ms, (long long)queue_ms_now);
          emit_event("underrun", fields);
          last_underrun_log_ns = now_ns;
          pending_underrun_ms  = 0;
        } else if (now_ns - last_underrun_log_ns >= 1000000000LL) {
          int64_t since_ms = (now_ns - last_underrun_log_ns) / 1000000;
          std::snprintf(fields, sizeof(fields),
                        "\"underrun_ms\":%lld,\"queue_ms\":%lld,\"since_ms\":%lld",
                        (long long)pending_underrun_ms, (long long)queue_ms_now, (long long)since_ms);
          emit_event("underrun", fields);
          last_underrun_log_ns = now_ns;
          pending_underrun_ms  = 0;
        }
      }

      int current_target_ms = queue.target_ms();
      if (current_target_ms != last_target_ms) {
        char fields[96];
        std::snprintf(fields, sizeof(fields), "\"target_ms\":%d,\"was_ms\":%d,\"jitter_ms\":%.2f",
                      current_target_ms, last_target_ms, queue.jitter_ms());
        emit_event("target", fields);
        last_target_ms = current_target_ms;
      }

      JbCounters jbc_tick = queue.take_counters();
      accum_drop_catchup_ms  += jbc_tick.drop_catchup_ms;
      accum_drop_overflow_ms += jbc_tick.drop_overflow_ms;
      if (jbc_tick.drop_catchup_ms > 0) {
        if (accum_queue_before_ms < 0) accum_queue_before_ms = jbc_tick.queue_before_ms;
        accum_queue_after_ms = jbc_tick.queue_after_ms;
      }

      if (stream_debug.load(std::memory_order_relaxed)) {
        int64_t drop_ms_this_tick = jbc_tick.drop_catchup_ms + jbc_tick.drop_overflow_ms +
            sample_drop_ms.exchange(0, std::memory_order_relaxed);
        char fields[192];
        std::snprintf(fields, sizeof(fields),
                      "\"in\":%llu,\"q\":%lld,\"dx\":%lld,\"drop\":%lld,\"ur\":%lld,\"plc\":%lld",
                      (unsigned long long)sample_frames_in.exchange(0, std::memory_order_relaxed),
                      (long long)queue.depth_ms(),
                      (long long)sample_last_delay_excess_ms.load(std::memory_order_relaxed),
                      (long long)drop_ms_this_tick,
                      (long long)underrun_ms_this_tick,
                      (long long)sample_plc_ms.exchange(0, std::memory_order_relaxed));
        emit_sample(fields);
      }

      int64_t period_ms = 0;
      bool due;
      {
        std::lock_guard<std::mutex> lk(meter.mu);
        due = period_clock.tick(steady_now_ns(), &period_ms);
      }
      if (!due) continue;

      SpkPeriod p;
      p.played_ms   = samples_to_ms(meter.played_samples.exchange(0, std::memory_order_relaxed), SAMPLE_RATE);
      p.underrun_ms = samples_to_ms(meter.underrun_samples.exchange(0, std::memory_order_relaxed), SAMPLE_RATE);
      if (accum_drop_catchup_ms > 0) {
        char fields[96];
        std::snprintf(fields, sizeof(fields),
                      "\"dropped_ms\":%lld,\"queue_before_ms\":%lld,\"queue_after_ms\":%lld",
                      (long long)accum_drop_catchup_ms, (long long)accum_queue_before_ms,
                      (long long)accum_queue_after_ms);
        emit_event("catchup", fields);
      }
      {
        std::lock_guard<std::mutex> lk(meter.mu);
        p.frames_in           = meter.frames_in;
        p.gap_ms              = meter.gap_ms;
        p.drop_overflow_ms    = accum_drop_overflow_ms;
        p.drop_catchup_ms     = accum_drop_catchup_ms;
        p.drop_replay_ms      = meter.drop_replay_ms;
        p.drop_aead_ms        = meter.drop_aead_ms;
        p.drop_mute_ms        = meter.drop_mute_ms;
        p.drop_decode_ms      = meter.drop_decode_ms;
        p.plc_ms              = meter.plc_ms;
        p.queue_min_ms        = meter.queue_ms.min();
        p.queue_avg_ms        = meter.queue_ms.avg();
        p.queue_max_ms        = meter.queue_ms.max();
        p.delay_excess_min_ms = meter.delay_excess_ms.min();
        p.delay_excess_avg_ms = meter.delay_excess_ms.avg();
        p.delay_excess_max_ms = meter.delay_excess_ms.max();
        p.bytes_in            = meter.bytes_in;
        meter.reset_period_locked();
      }
      accum_drop_catchup_ms  = 0;
      accum_drop_overflow_ms = 0;
      accum_queue_before_ms  = -1;
      p.period_ms  = period_ms;
      p.target_ms  = current_target_ms;
      p.jitter_ms  = jitter.current_ms();
      p.scale_rate = queue.scale_rate();
      emit_summary(format_spk_summary(p));
    }
  });

  AudioPlayback* pb_raw = pb.get();
  std::thread clock_th([&]() {
    while (!g_done.load()) {
      std::this_thread::sleep_for(std::chrono::milliseconds(100));
      if (g_done.load()) break;
      int64_t fp = 0, mono_ns = 0;
      if (!pb_raw->query_render_timestamp(&fp, &mono_ns)) continue;

      rate_ring.push_back({total_output_samples.load(std::memory_order_relaxed),
                           total_source_consumed_samples.load(std::memory_order_relaxed)});
      while (rate_ring.size() > kRateRingCap) rate_ring.pop_front();

      int64_t source_pos = 0;
      {
        const RateEntry* match = nullptr;
        for (auto it = rate_ring.rbegin(); it != rate_ring.rend(); ++it) {
          if (it->output_cum <= fp) { match = &(*it); break; }
        }
        if (!match) continue;  
        source_pos = match->source_cum + (fp - match->output_cum);
      }

      int64_t pts_at_fp = 0;
      {
        std::lock_guard<std::mutex> lk(clock_mu);
        if (clock_ring.empty()) continue;
        const ClockEntry* match = nullptr;
        for (auto it = clock_ring.rbegin(); it != clock_ring.rend(); ++it) {
          if (it->sample_idx_start <= source_pos) { match = &(*it); break; }
        }
        if (!match) continue;  
        int64_t offset_samples = source_pos - match->sample_idx_start;
        int64_t offset_ns = offset_samples * (int64_t)1000000000 / SAMPLE_RATE;
        pts_at_fp = match->sender_pts_ns + offset_ns;
      }
      emit_clock_sample(mono_ns, pts_at_fp);
    }
  });

  while (!g_done.load()) ::usleep(100000);

  pb->close();
  ::shutdown(cfd, SHUT_RDWR);
  reader.join();
  ::close(cfd);
  opus_decoder_destroy(dec);

  ctrl_th.join();
  stats_th.join();
  meter_th.join();
  clock_th.join();

  if (aead_fail_count > 0) {
    LOG_INFO("AEAD failures over the call: %llu", (unsigned long long)aead_fail_count);
  }
  LOG_INFO("clean exit");
  return 0;
}
