
#include "../common/framing.h"
#include "../common/socket.h"
#include "../common/log.h"
#include "../common/aead.h"
#include "../common/key_fd.h"
#include "../common/control.h"
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
    } else if (s == "--help" || s == "-h") {
      std::fprintf(stderr,
        "usage: haoma-spk --port N --stream-id ID [--trace]\n"
        "  binds 127.0.0.1:N, accepts ONE client, AEAD-opens incoming\n"
        "  framed Opus, plays back at 48k/mono. Bad tag = silent drop.\n"
        "  Reads 32-byte key from the first 32 bytes of stdin;\n"
        "  remainder of stdin is JSON-line control input.\n"
        "  --stream-id ID is one of: mic | cam | screen.\n"
        "  --trace ups stats cadence + emits per-frame trace lines.\n");
      std::exit(0);
    } else {
      LOG_ERR("unknown arg: %s", argv[i]);
      return false;
    }
  }
  if (a.port == 0) { LOG_ERR("--port required"); return false; }
  if (a.stream_id.empty()) { LOG_ERR("--stream-id required"); return false; }
  return true;
}

class SampleQueue {
  std::deque<std::vector<float>> q_;
  size_t head_off_ = 0;
  size_t total_    = 0;
  std::mutex m_;
  static constexpr size_t kCapSamples = 24000;

 public:
  bool push(std::vector<float>&& chunk) {
    std::lock_guard<std::mutex> lk(m_);
    bool dropped = false;
    while (!q_.empty() && total_ + chunk.size() > kCapSamples) {
      total_ -= q_.front().size() - head_off_;
      q_.pop_front();
      head_off_ = 0;
      dropped = true;
    }
    total_ += chunk.size();
    q_.push_back(std::move(chunk));
    return dropped;
  }
  size_t pop(float* out, size_t n) {
    std::lock_guard<std::mutex> lk(m_);
    size_t got = 0;
    while (got < n && !q_.empty()) {
      auto& front = q_.front();
      size_t avail = front.size() - head_off_;
      size_t take  = std::min(n - got, avail);
      std::memcpy(out + got, front.data() + head_off_, take * sizeof(float));
      got       += take;
      head_off_ += take;
      total_    -= take;
      if (head_off_ >= front.size()) {
        q_.pop_front();
        head_off_ = 0;
      }
    }
    return got;
  }
  void clear() {
    std::lock_guard<std::mutex> lk(m_);
    q_.clear();
    head_off_ = 0;
    total_    = 0;
  }
};

std::atomic<bool> g_done{false};
void on_signal(int) { g_done.store(true); }

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

  SampleQueue        queue;
  Stats              stats;
  JitterTracker      jitter;
  std::atomic<bool>  trace{a.trace};
  std::atomic<bool>  stats_req{false};
  std::atomic<bool>  muted{false};
  uint64_t           expected_counter = 0;
  bool               first_frame      = true;
  uint64_t           aead_fail_count  = 0;

  struct ClockEntry { int64_t sample_idx_start; int64_t sender_pts_ns; };
  std::mutex                    clock_mu;
  std::deque<ClockEntry>        clock_ring;
  int64_t                       total_pushed_samples = 0;
  constexpr size_t              kClockRingCap = 200;

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

      if (!aead.open(counter, pts_ns, cipher.data(), (size_t)n, tag, plain.data())) {
        if (aead_fail_count == 0) {
          LOG_INFO("AEAD verify failed at counter %llu — wrong key or tampered frame; silently dropping",
                   (unsigned long long)counter);
        }
        aead_fail_count++;
        stats.frames_dropped.fetch_add(1);
        continue;  
      }

      if (first_frame) {
        expected_counter = counter;
        first_frame = false;
      } else if (counter < expected_counter) {
        LOG_DBG("counter replay: got %llu expected >= %llu — dropping",
                (unsigned long long)counter, (unsigned long long)expected_counter);
        stats.frames_dropped.fetch_add(1);
        continue;
      } else if (counter != expected_counter) {
        LOG_DBG("counter skew: got %llu expected %llu",
                (unsigned long long)counter, (unsigned long long)expected_counter);
      }
      expected_counter = counter + 1;

      stats.frames_in.fetch_add(1);
      if (trace.load()) emit_trace_frame(counter, (uint32_t)(FRAME_OVERHEAD + n), muted.load());

      if (muted.load()) {
        stats.frames_dropped.fetch_add(1);
        continue;
      }

      std::vector<float> pcm(FRAME_SAMPLES);
      int decoded = opus_decode_float(dec, plain.data(), (opus_int32)n,
                                      pcm.data(), FRAME_SAMPLES, 0);
      if (decoded < 0) {
        LOG_ERR("opus_decode_float: %s", opus_strerror(decoded));
        stats.frames_dropped.fetch_add(1);
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
      if (queue.push(std::move(pcm))) {
        stats.frames_dropped.fetch_add(1);  
      }
    }
  });

  bool ok = pb->open([&](float* out, size_t n) -> size_t {
    return queue.pop(out, n);
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

  AudioPlayback* pb_raw = pb.get();
  std::thread clock_th([&]() {
    while (!g_done.load()) {
      std::this_thread::sleep_for(std::chrono::milliseconds(100));
      if (g_done.load()) break;
      int64_t fp = 0, mono_ns = 0;
      if (!pb_raw->query_render_timestamp(&fp, &mono_ns)) continue;
      int64_t pts_at_fp = 0;
      {
        std::lock_guard<std::mutex> lk(clock_mu);
        if (clock_ring.empty()) continue;
        const ClockEntry* match = nullptr;
        for (auto it = clock_ring.rbegin(); it != clock_ring.rend(); ++it) {
          if (it->sample_idx_start <= fp) { match = &(*it); break; }
        }
        if (!match) continue;  
        int64_t offset_samples = fp - match->sample_idx_start;
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
  clock_th.join();

  if (aead_fail_count > 0) {
    LOG_INFO("AEAD failures over the call: %llu", (unsigned long long)aead_fail_count);
  }
  LOG_INFO("clean exit");
  return 0;
}
