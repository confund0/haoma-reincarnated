// haoma-cam — captures from the platform camera, encodes to VP8,
// AEAD-seals each compressed frame with ChaCha20-Poly1305, writes
// framed bytes to a single localhost client. ALSO binds a second
// localhost listener that emits the raw I420 captured frames (no
// encode/AEAD) so the host UI can render a zero-latency local
// self-preview without a decode round-trip. V-1.5.
//
// stdin layout (mirrors mic/spk):
//   first 32 bytes  = ChaCha20-Poly1305 key
//   bytes 33..      = JSON-line control commands (mute/unmute/bitrate/stats/exit)
//
// Capture: owned in-binary via VideoCapture abstraction (mirrors mic's
// audio_backend). Linux dev backend = Y4M file source set via
// --y4m-source. Android backend = NdkCamera.
//
// stdout: JSON-line events. The first one is
//   {"type":"ready","raw_unix":"<name>"}
// where <name> is the AF_UNIX abstract-namespace listener for raw I420.
// Data plane = the localhost TCP socket bound on --port (encrypted
// VP8 frames to haomad). Raw I420 plane = the AF_UNIX listener named
// "haoma-cam-<stream_id>-<pid>" (abstract namespace, no fs entry).

#include "../common/framing.h"
#include "../common/socket.h"
#include "../common/log.h"
#include "../common/aead.h"
#include "../common/key_fd.h"
#include "../common/control.h"
#include "../platform/video_backend.h"

#include <vpx/vpx_encoder.h>
#include <vpx/vp8cx.h>

#include <atomic>
#include <chrono>
#include <csignal>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <fcntl.h>
#include <string>
#include <thread>
#include <vector>
#include <unistd.h>
#include <sys/socket.h>
#include <sys/uio.h>

using namespace haoma::streams;

namespace {

struct Args {
  uint16_t    port = 0;
  std::string stream_id;
  std::string y4m_source;
  int         width    = 640;
  int         height   = 480;
  int         fps      = 15;
  int         bitrate_kbps = 200;
  bool        trace    = false;
  bool        input_from_raw = false;
};

void usage() {
  std::fprintf(stderr,
    "usage: haoma-cam --port N --stream-id ID [--y4m-source PATH]\n"
    "                 [--input-from-raw]\n"
    "                 [--width W] [--height H] [--fps F] [--bitrate KBPS] [--trace]\n"
    "  binds 127.0.0.1:N (sealed VP8 to haomad) AND an AF_UNIX abstract-\n"
    "  namespace listener. By default the AF_UNIX listener is a raw I420\n"
    "  side-channel (host UI reads for local self-preview). With\n"
    "  --input-from-raw the listener flips direction: it becomes the\n"
    "  capture source — the connected client writes\n"
    "  `8 BE pts_ns | width*height*3/2 I420 bytes` per frame and cam\n"
    "  encodes those, no in-binary capture is opened. The listener name\n"
    "  is reported in the ready event as raw_unix in both modes.\n"
    "  Reads 32-byte key from the first 32 bytes of stdin;\n"
    "  remainder of stdin is JSON-line control input.\n"
    "  --stream-id ID is one of: mic | cam | screen.\n"
    "  --y4m-source PATH is honored by the Linux dev backend; ignored\n"
    "  when --input-from-raw is set OR on platforms whose backend takes\n"
    "  the platform camera directly.\n"
    "  Defaults: 640x480 @ 15fps @ 200 kbps.\n");
}

bool parse_args(int argc, char** argv, Args& a) {
  for (int i = 1; i < argc; ++i) {
    std::string s = argv[i];
    if ((s == "--port" || s == "-p") && i + 1 < argc) {
      int p = std::atoi(argv[++i]);
      if (p < 1 || p > 65535) { LOG_ERR("invalid --port: %d", p); return false; }
      a.port = (uint16_t)p;
    } else if (s == "--stream-id" && i + 1 < argc) {
      a.stream_id = argv[++i];
    } else if (s == "--y4m-source" && i + 1 < argc) {
      a.y4m_source = argv[++i];
    } else if (s == "--width" && i + 1 < argc) {
      a.width = std::atoi(argv[++i]);
    } else if (s == "--height" && i + 1 < argc) {
      a.height = std::atoi(argv[++i]);
    } else if (s == "--fps" && i + 1 < argc) {
      a.fps = std::atoi(argv[++i]);
    } else if (s == "--bitrate" && i + 1 < argc) {
      a.bitrate_kbps = std::atoi(argv[++i]);
    } else if (s == "--trace") {
      a.trace = true;
    } else if (s == "--input-from-raw") {
      a.input_from_raw = true;
    } else if (s == "--help" || s == "-h") {
      usage();
      std::exit(0);
    } else {
      LOG_ERR("unknown arg: %s", argv[i]);
      return false;
    }
  }
  if (a.port == 0)          { LOG_ERR("--port required"); return false; }
  if (a.stream_id.empty())  { LOG_ERR("--stream-id required"); return false; }
  if (a.width  <= 0 || a.width  > 4096) { LOG_ERR("bad --width %d",  a.width);  return false; }
  if (a.height <= 0 || a.height > 4096) { LOG_ERR("bad --height %d", a.height); return false; }
  if (a.fps    <= 0 || a.fps    > 120)  { LOG_ERR("bad --fps %d",    a.fps);    return false; }
  if (a.bitrate_kbps < 32 || a.bitrate_kbps > 4000) {
    LOG_ERR("bad --bitrate %d (32..4000 kbps)", a.bitrate_kbps);
    return false;
  }
  return true;
}

std::atomic<bool> g_done{false};
void on_signal(int) { g_done.store(true); }

}  // namespace

int main(int argc, char** argv) {
  set_log_tag("haoma-cam");
  Args a;
  if (!parse_args(argc, argv, a)) { usage(); return 1; }

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

  vpx_codec_ctx_t       enc_ctx{};
  vpx_codec_enc_cfg_t   cfg{};
  vpx_codec_iface_t*    iface = vpx_codec_vp8_cx();
  if (vpx_codec_enc_config_default(iface, &cfg, 0) != VPX_CODEC_OK) {
    LOG_ERR("vpx_codec_enc_config_default failed");
    return 2;
  }
  cfg.g_w               = (unsigned int)a.width;
  cfg.g_h               = (unsigned int)a.height;
  cfg.g_timebase.num    = 1;
  cfg.g_timebase.den    = 1000000;  // pts in microseconds
  cfg.rc_target_bitrate = (unsigned int)a.bitrate_kbps;
  cfg.rc_end_usage      = VPX_CBR;
  cfg.g_lag_in_frames   = 0;        // no lookahead → realtime
  cfg.kf_mode           = VPX_KF_AUTO;
  cfg.kf_min_dist       = 0;
  cfg.kf_max_dist       = 30;       // ~2 s @ 15 fps
  cfg.g_error_resilient = 1;
  cfg.g_threads         = 1;
  if (vpx_codec_enc_init(&enc_ctx, iface, &cfg, 0) != VPX_CODEC_OK) {
    LOG_ERR("vpx_codec_enc_init: %s", vpx_codec_error(&enc_ctx));
    return 2;
  }
  vpx_codec_control(&enc_ctx, VP8E_SET_CPUUSED, 8);  // max speed (quality ↓)
  vpx_codec_control(&enc_ctx, VP8E_SET_NOISE_SENSITIVITY, 0);

  int lfd = listen_local(a.port);
  if (lfd < 0) { vpx_codec_destroy(&enc_ctx); return 3; }

  const std::string raw_unix_name =
      "haoma-cam-" + a.stream_id + "-" + std::to_string(::getpid());
  int raw_lfd = listen_local_unix(raw_unix_name);
  if (raw_lfd < 0) {
    LOG_ERR("listen_local_unix (raw) failed");
    ::close(lfd);
    vpx_codec_destroy(&enc_ctx);
    return 3;
  }

  LOG_INFO("listening on 127.0.0.1:%u sealed + unix:@%s raw (stream=%s, %dx%d @ %dfps, %dkbps)",
           a.port, raw_unix_name.c_str(), a.stream_id.c_str(), a.width, a.height, a.fps, a.bitrate_kbps);

  emit_ready(raw_unix_name);

  // Sealed-side client (haomad). Blocks until haomad dials in.
  int cfd = accept_one(lfd);
  if (cfd < 0) {
    ::close(raw_lfd);
    vpx_codec_destroy(&enc_ctx);
    return 4;
  }
  LOG_INFO("sealed client connected");

  // Encode / control / stats state. Shared between the source path
  // (write-mode capture callback OR read-mode raw reader) and the ctrl
  // + stats threads.
  Stats              stats;
  std::atomic<bool>  trace{a.trace};
  std::atomic<bool>  stats_req{false};
  std::atomic<bool>  muted{false};
  std::atomic<int>   request_kbps{0};

  std::atomic<uint64_t> counter{0};
  std::atomic<bool>     write_failed{false};

  const size_t   frame_bytes       = (size_t)a.width * (size_t)a.height * 3 / 2;
  const uint64_t frame_duration_us = 1000000ULL / (uint64_t)a.fps;

  // Raw-side client state. raw_cfd is the accepted fd in both modes —
  // write-mode uses it as the tap target, read-mode publishes it for
  // the cleanup shutdown wake. raw_write_failed is write-mode-only;
  // the encode_and_ship tap is gated on !input_from_raw upstream.
  std::atomic<int>  raw_cfd{-1};
  std::atomic<bool> raw_write_failed{false};

  std::vector<uint8_t> cipher(MAX_PAYLOAD_LEN);

  // encode_and_ship_i420 runs the VP8 encode + AEAD seal + sealed-side
  // write for one I420 frame. In write-mode it also taps the raw side
  // (host UI self-preview); in read-mode the raw side IS the input, so
  // fanning frames back at the writer would just loop. Returns false
  // when the sealed peer has hung up — caller should stop pumping.
  auto encode_and_ship_i420 = [&](const uint8_t* i420, uint64_t pts_ns) -> bool {
    if (write_failed.load()) return false;

    stats.frames_in.fetch_add(1);

    if (!a.input_from_raw) {
      // Raw-port tap (zero-latency self-preview path). Wire shape per
      // frame is `8 BE pts_ns | I420 bytes`. Blocking write_all-style
      // pair: brief renderer-side backpressure beats a sendmsg
      // short-write desync, which would misalign the consumer's
      // per-frame read loop for the rest of the call (see vid's
      // matching change — that's where the symptom actually surfaced).
      int rfd = raw_cfd.load();
      if (rfd >= 0 && !raw_write_failed.load()) {
        uint8_t pts_be[8];
        haoma::streams::w_be64(pts_be, pts_ns);
        if (write_all(rfd, pts_be, sizeof(pts_be)) != (int64_t)sizeof(pts_be) ||
            write_all(rfd, i420, frame_bytes) != (int64_t)frame_bytes) {
          LOG_INFO("raw client write failed (errno=%d) — disabling raw tap", errno);
          raw_write_failed.store(true);
        }
      }
    }

    int kbps = request_kbps.exchange(0);
    if (kbps > 0) {
      cfg.rc_target_bitrate = (unsigned int)kbps;
      if (vpx_codec_enc_config_set(&enc_ctx, &cfg) == VPX_CODEC_OK) {
        LOG_INFO("bitrate -> %d kbps", kbps);
      } else {
        emit_warn("bitrate_set_failed");
      }
    }

    if (muted.load()) {
      stats.frames_dropped.fetch_add(1);
      return true;
    }

    vpx_image_t img;
    if (!vpx_img_wrap(&img, VPX_IMG_FMT_I420,
                      (unsigned int)a.width, (unsigned int)a.height,
                      1, const_cast<uint8_t*>(i420))) {
      LOG_ERR("vpx_img_wrap failed");
      return true;
    }

    uint64_t vpx_pts_us = pts_ns / 1000;
    vpx_codec_err_t er = vpx_codec_encode(&enc_ctx, &img, (vpx_codec_pts_t)vpx_pts_us,
                                          (unsigned long)frame_duration_us,
                                          0, VPX_DL_REALTIME);
    if (er != VPX_CODEC_OK) {
      LOG_ERR("vpx_codec_encode: %s", vpx_codec_error(&enc_ctx));
      stats.frames_dropped.fetch_add(1);
      return true;
    }

    vpx_codec_iter_t iter = nullptr;
    const vpx_codec_cx_pkt_t* pkt = nullptr;
    while ((pkt = vpx_codec_get_cx_data(&enc_ctx, &iter)) != nullptr) {
      if (pkt->kind != VPX_CODEC_CX_FRAME_PKT) continue;
      size_t pkt_sz = pkt->data.frame.sz;
      if (pkt_sz > MAX_PAYLOAD_LEN) {
        LOG_ERR("encoded packet too big: %zu > %zu", pkt_sz, (size_t)MAX_PAYLOAD_LEN);
        stats.frames_dropped.fetch_add(1);
        continue;
      }

      uint8_t tag[FRAME_TAG_LEN];
      if (!aead.seal(counter.load(), pts_ns,
                     (const uint8_t*)pkt->data.frame.buf, pkt_sz,
                     cipher.data(), tag)) {
        LOG_ERR("aead.seal failed");
        continue;
      }

      std::vector<uint8_t> frame(FRAME_OVERHEAD + pkt_sz);
      size_t flen = encode_frame(counter.load(), pts_ns,
                                 cipher.data(), pkt_sz, tag,
                                 frame.data(), frame.size());
      if (flen == 0) { LOG_ERR("encode_frame: payload too big (%zu)", pkt_sz); continue; }
      uint64_t this_counter = counter.fetch_add(1);

      if (write_all(cfd, frame.data(), flen) != (int64_t)flen) {
        LOG_INFO("peer closed (write failed)");
        stats.frames_dropped.fetch_add(1);
        write_failed.store(true);
        g_done.store(true);
        return false;
      }
      stats.frames_out.fetch_add(1);
      stats.bytes_out.fetch_add(flen);
      if (trace.load()) emit_trace_frame(this_counter, (uint32_t)flen, false);
    }
    return true;
  };

  // Raw-side thread. Two roles:
  //   write-mode (default)      — accept ONE UI consumer, set raw_cfd,
  //                               exit; the capture callback then fans
  //                               I420 frames at the consumer.
  //   read-mode (--input-from-raw) — accept ONE app producer, loop
  //                               reading `8 BE pts | I420` per frame
  //                               and driving encode_and_ship_i420
  //                               directly. No in-binary capture is
  //                               opened in this mode.
  std::thread raw_th([&]() {
    int fd = accept_one(raw_lfd);
    if (fd < 0) {
      LOG_INFO("raw accept aborted (shutdown)");
      return;
    }
    LOG_INFO("raw client connected via unix:@%s mode=%s",
             raw_unix_name.c_str(), a.input_from_raw ? "read" : "write");

    if (!a.input_from_raw) {
      // Blocking writes — see encode_and_ship_i420 for the rationale.
      raw_cfd.store(fd);
      return;
    }

    // Read-mode: this thread IS the capture loop. Tear the streamer
    // down on EOF / short read so the orchestrator's teardown path
    // doesn't have to distinguish "writer hung up" from "we crashed".
    // Publish fd into raw_cfd so the main cleanup can shutdown() it to
    // wake a blocked read on the way out (the encode path's tap is
    // input-from-raw-gated, so this slot has no other consumer here).
    raw_cfd.store(fd);
    std::vector<uint8_t> i420(frame_bytes);
    uint8_t pts_be[8];
    while (!g_done.load()) {
      int64_t hr = haoma::streams::read_exact(fd, pts_be, sizeof(pts_be));
      if (hr <= 0) {
        LOG_INFO("raw input: pts header eof (r=%lld)", (long long)hr);
        break;
      }
      int64_t fr = haoma::streams::read_exact(fd, i420.data(), frame_bytes);
      if (fr != (int64_t)frame_bytes) {
        LOG_INFO("raw input: i420 short read (r=%lld want=%zu)",
                 (long long)fr, frame_bytes);
        break;
      }
      uint64_t pts_ns = haoma::streams::r_be64(pts_be);
      if (!encode_and_ship_i420(i420.data(), pts_ns)) break;
    }
    // Leave the fd open + leave raw_cfd populated; main's cleanup is
    // the sole closer (mirrors write-mode). g_done wakes main from the
    // 100ms usleep poll.
    g_done.store(true);
  });

  std::unique_ptr<VideoCapture> cap;
  if (!a.input_from_raw) {
    VideoCaptureConfig vc_cfg;
    vc_cfg.y4m_source = a.y4m_source;
    cap = make_video_capture(vc_cfg);
    if (!cap) {
      LOG_ERR("make_video_capture returned null");
      ::shutdown(cfd, SHUT_RDWR);
      ::close(cfd);
      ::shutdown(raw_lfd, SHUT_RDWR);
      if (raw_th.joinable()) raw_th.join();
      int rfd = raw_cfd.load();
      if (rfd >= 0) ::close(rfd);
      vpx_codec_destroy(&enc_ctx);
      return 5;
    }

    // Shared origin with mic: steady_clock::time_since_epoch in ns,
    // backed by CLOCK_MONOTONIC on Linux + Android. Lets the receiver
    // align video against audio on a single sender timeline.
    bool ok = cap->open(a.width, a.height, a.fps, [&](const uint8_t* i420) {
      uint64_t pts_ns = (uint64_t)std::chrono::duration_cast<std::chrono::nanoseconds>(
          std::chrono::steady_clock::now().time_since_epoch()).count();
      encode_and_ship_i420(i420, pts_ns);
    });
    if (!ok) {
      LOG_ERR("video capture open failed");
      emit_error("video_capture_open");
      ::shutdown(cfd, SHUT_RDWR);
      ::close(cfd);
      int rfd = raw_cfd.load();
      if (rfd >= 0) ::close(rfd);
      ::shutdown(raw_lfd, SHUT_RDWR);
      if (raw_th.joinable()) raw_th.join();
      vpx_codec_destroy(&enc_ctx);
      return 6;
    }
  }

  std::thread ctrl_th(control_loop, STDIN_FILENO, std::ref(g_done),
    [&](const ControlMsg& m) {
      switch (m.cmd) {
        case Command::Mute:    muted.store(true);  LOG_INFO("muted"); break;
        case Command::Unmute:  muted.store(false); LOG_INFO("unmuted"); break;
        case Command::Bitrate:
          if (m.int_arg < 32 || m.int_arg > 4000) {
            emit_warn("bitrate_out_of_range");
            LOG_INFO("rejected bitrate %d kbps (allowed 32..4000)", m.int_arg);
          } else {
            request_kbps.store(m.int_arg);
          }
          break;
        case Command::Stats:   stats_req.store(true); break;
        case Command::Exit:    LOG_INFO("exit command"); g_done.store(true); break;
        case Command::Unknown: emit_warn("unknown_command"); break;
      }
    });

  std::thread stats_th(stats_loop, std::ref(stats), nullptr,
                       std::ref(g_done), std::ref(trace), std::ref(stats_req));

  while (!g_done.load()) ::usleep(100000);

  if (cap) cap->close();
  ::shutdown(cfd, SHUT_RDWR);
  ::close(cfd);

  // Wake the raw thread if it's still blocked on accept (or, in
  // read-mode, blocked on read of a long-idle client), and tear the
  // raw client down. shutdown() on the listening fd interrupts a
  // blocked accept(); shutdown() on raw_cfd interrupts a blocked read.
  ::shutdown(raw_lfd, SHUT_RDWR);
  int rfd = raw_cfd.load();
  if (rfd >= 0) ::shutdown(rfd, SHUT_RDWR);
  if (raw_th.joinable()) raw_th.join();
  if (rfd >= 0) ::close(rfd);
  ::close(raw_lfd);

  vpx_codec_destroy(&enc_ctx);

  ctrl_th.join();
  stats_th.join();

  LOG_INFO("clean exit");
  return 0;
}
