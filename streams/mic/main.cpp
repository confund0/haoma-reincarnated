// haoma-mic — captures platform mic input, encodes to Opus, AEAD-seals
// each frame with ChaCha20-Poly1305, writes framed bytes to a single
// localhost client. ADR-040 voice profile (48k mono 20ms 32kbps CBR VOIP,
// DTX off). Calls-1e: stdio JSON-line control plane on top of 1d's
// AEAD wrap + key-on-stdin.
//
// stdin layout (Calls-1d → 1e):
//   first 32 bytes  = ChaCha20-Poly1305 key
//   bytes 33..      = JSON-line control commands (mute/unmute/bitrate/stats/exit)
//
// stdout: JSON-line events (ready/stats/warn/error/trace).
// Data plane = the localhost TCP socket bound on --port.

#include "../common/framing.h"
#include "../common/socket.h"
#include "../common/log.h"
#include "../common/aead.h"
#include "../common/key_fd.h"
#include "../common/control.h"
#include "../platform/audio_backend.h"

#include <opus/opus.h>

#include <atomic>
#include <csignal>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <string>
#include <thread>
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
        "usage: haoma-mic --port N --stream-id ID [--trace]\n"
        "  binds 127.0.0.1:N, accepts ONE client, captures mic,\n"
        "  encodes Opus 48k/mono/20ms/32kbps CBR/VOIP, DTX off,\n"
        "  AEAD-seals each frame (ChaCha20-Poly1305).\n"
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

std::atomic<bool> g_done{false};
void on_signal(int) { g_done.store(true); }

}  // namespace

int main(int argc, char** argv) {
  set_log_tag("haoma-mic");
  Args a;
  if (!parse_args(argc, argv, a)) {
    std::fprintf(stderr, "usage: haoma-mic --port N --stream-id ID [--trace]\n");
    return 1;
  }

  // sigaction with SA_RESTART cleared: SIGTERM must interrupt the
  // pre-control-thread accept() — std::signal's SA_RESTART default
  // would auto-resume it, so cancel-before-connect would hit SIGKILL.
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
  OpusEncoder* enc = opus_encoder_create(SAMPLE_RATE, CHANNELS, OPUS_APPLICATION_VOIP, &err);
  if (!enc || err != OPUS_OK) {
    LOG_ERR("opus_encoder_create: %s", opus_strerror(err));
    return 2;
  }
  opus_encoder_ctl(enc, OPUS_SET_BITRATE(32000));
  opus_encoder_ctl(enc, OPUS_SET_VBR(0));   // CBR
  opus_encoder_ctl(enc, OPUS_SET_DTX(0));   // DTX off (locked Calls-1b)

  int lfd = listen_local(a.port);
  if (lfd < 0) { opus_encoder_destroy(enc); return 3; }
  LOG_INFO("listening on 127.0.0.1:%u (stream=%s)", a.port, a.stream_id.c_str());

  // Calls-1f orchestrator gates ProxyServe on this — "ready" means
  // "listener bound, dial me." Audio-open happens after accept_one
  // (which blocks on haomad dialing us); a post-ready open failure
  // surfaces as {"type":"error"} on the events stream.
  emit_ready();

  int cfd = accept_one(lfd);
  if (cfd < 0) { opus_encoder_destroy(enc); return 4; }
  LOG_INFO("client connected");

  auto cap = make_capture();
  if (!cap) {
    LOG_ERR("make_capture returned null");
    ::close(cfd); opus_encoder_destroy(enc);
    return 5;
  }

  Stats              stats;
  std::atomic<bool>  trace{a.trace};
  std::atomic<bool>  stats_req{false};
  std::atomic<bool>  muted{false};
  // Pending bitrate change in kbps; 0 = no pending change.  Capture
  // callback drains it under the encoder's single-thread invariant.
  std::atomic<int>   request_kbps{0};

  uint64_t counter = 0;
  std::atomic<bool> write_failed{false};

  bool ok = cap->open([&](const float* samples) {
    if (write_failed.load()) return;

    int kbps = request_kbps.exchange(0);
    if (kbps > 0) {
      opus_encoder_ctl(enc, OPUS_SET_BITRATE(kbps * 1000));
      LOG_INFO("bitrate -> %d kbps", kbps);
    }

    // Mute substitutes a zero buffer — encoder still emits comfort-noise
    // frames (DTX off) so liveness is preserved on the wire.
    static thread_local float silence[FRAME_SAMPLES] = {0};
    const float* in = muted.load() ? silence : samples;

    uint8_t opus_buf[1500];
    int n = opus_encode_float(enc, in, FRAME_SAMPLES, opus_buf, sizeof(opus_buf));
    if (n < 0) { LOG_ERR("opus_encode_float: %s", opus_strerror(n)); return; }
    if (n == 0) return;  // shouldn't happen with DTX off, defensive

    uint8_t cipher[1500];
    uint8_t tag[FRAME_TAG_LEN];
    if (!aead.seal(counter, opus_buf, (size_t)n, cipher, tag)) {
      LOG_ERR("aead.seal failed");
      return;
    }

    uint8_t frame[MAX_FRAME_LEN];
    size_t flen = encode_frame(counter, cipher, (size_t)n, tag, frame, sizeof(frame));
    if (flen == 0) { LOG_ERR("encode_frame: payload too big (%d)", n); return; }
    uint64_t this_counter = counter;
    counter++;

    if (write_all(cfd, frame, flen) != (int64_t)flen) {
      LOG_INFO("peer closed (write failed)");
      stats.frames_dropped.fetch_add(1);
      write_failed.store(true);
      g_done.store(true);
      return;
    }
    stats.frames_out.fetch_add(1);
    stats.bytes_out.fetch_add(flen);
    if (trace.load()) emit_trace_frame(this_counter, (uint32_t)flen, muted.load());
  });
  if (!ok) {
    LOG_ERR("audio capture open failed");
    emit_error("audio_capture_open");
    ::close(cfd); opus_encoder_destroy(enc);
    return 6;
  }

  LOG_INFO("streaming...");

  std::thread ctrl_th(control_loop, STDIN_FILENO, std::ref(g_done),
    [&](const ControlMsg& m) {
      switch (m.cmd) {
        case Command::Mute:
          muted.store(true);
          LOG_INFO("muted");
          break;
        case Command::Unmute:
          muted.store(false);
          LOG_INFO("unmuted");
          break;
        case Command::Bitrate:
          if (m.int_arg < 6 || m.int_arg > 128) {
            emit_warn("bitrate_out_of_range");
            LOG_INFO("rejected bitrate %d kbps (allowed 6..128)", m.int_arg);
          } else {
            request_kbps.store(m.int_arg);
          }
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

  std::thread stats_th(stats_loop, std::ref(stats), nullptr,
                       std::ref(g_done), std::ref(trace), std::ref(stats_req));

  while (!g_done.load()) ::usleep(100000);

  cap->close();
  ::shutdown(cfd, SHUT_RDWR);
  ::close(cfd);
  opus_encoder_destroy(enc);

  ctrl_th.join();
  stats_th.join();

  LOG_INFO("clean exit");
  return 0;
}
