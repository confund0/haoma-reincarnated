// haoma-vid — accepts one localhost client on --port (sealed VP8 frames
// from haomad), AEAD-opens each frame with ChaCha20-Poly1305, decodes
// VP8, writes raw I420 frames to a SECOND localhost listener (the host
// UI dials it for the remote-tile renderer). A bad key (or tampered
// frame) silently drops at the AEAD layer — no decode, connection stays
// open. V-1.5: dropped --output-fd; raw-port listener replaces it,
// matching cam's V-1.5 shape.
//
// stdin layout (mirrors mic/spk/cam):
//   first 32 bytes  = ChaCha20-Poly1305 key
//   bytes 33..      = JSON-line control commands (mute/unmute/stats/exit;
//                     bitrate rejected with a warn — decoder has none)
//
// stdout: JSON-line events. The first one is
//   {"type":"ready","raw_unix":"<name>"}
// where <name> is the AF_UNIX abstract-namespace listener for raw I420.
// Data plane = the localhost TCP socket bound on --port (sealed VP8
// frames in). Raw I420 plane = the AF_UNIX listener named
// "haoma-vid-<stream_id>-<pid>" (abstract namespace, no fs entry).

#include "../common/framing.h"
#include "../common/socket.h"
#include "../common/log.h"
#include "../common/aead.h"
#include "../common/key_fd.h"
#include "../common/control.h"

#include <vpx/vpx_decoder.h>
#include <vpx/vp8dx.h>

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
  bool        trace = false;
};

void usage() {
  std::fprintf(stderr,
    "usage: haoma-vid --port N --stream-id ID [--trace]\n"
    "  binds 127.0.0.1:N (sealed VP8 in from haomad) AND an AF_UNIX\n"
    "  abstract-namespace listener (raw I420 out for host UI, reported\n"
    "  in ready event as raw_unix). Accepts ONE client on each. AEAD-opens\n"
    "  incoming framed VP8, decodes, pushes raw I420 to the raw client.\n"
    "  Bad tag = silent drop. Slow raw client = frames dropped, decode\n"
    "  pipeline unaffected.\n"
    "  Reads 32-byte key from the first 32 bytes of stdin;\n"
    "  remainder of stdin is JSON-line control input.\n"
    "  --stream-id ID is one of: mic | cam | screen.\n");
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
    } else if (s == "--trace") {
      a.trace = true;
    } else if (s == "--help" || s == "-h") {
      usage();
      std::exit(0);
    } else {
      LOG_ERR("unknown arg: %s", argv[i]);
      return false;
    }
  }
  if (a.port == 0)         { LOG_ERR("--port required"); return false; }
  if (a.stream_id.empty()) { LOG_ERR("--stream-id required"); return false; }
  return true;
}

std::atomic<bool> g_done{false};
void on_signal(int) { g_done.store(true); }

// Pack one I420 plane row-by-row into a contiguous output buffer,
// stripping stride padding if the codec allocated wider rows than the
// actual pixel width.
void pack_plane(uint8_t* dst, const uint8_t* plane, int stride, int w, int h) {
  for (int r = 0; r < h; ++r) {
    std::memcpy(dst + (size_t)r * (size_t)w,
                plane + (size_t)r * (size_t)stride,
                (size_t)w);
  }
}

}  // namespace

int main(int argc, char** argv) {
  set_log_tag("haoma-vid");
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

  vpx_codec_ctx_t dec_ctx{};
  if (vpx_codec_dec_init(&dec_ctx, vpx_codec_vp8_dx(), nullptr, 0) != VPX_CODEC_OK) {
    LOG_ERR("vpx_codec_dec_init: %s", vpx_codec_error(&dec_ctx));
    return 2;
  }

  int lfd = listen_local(a.port);
  if (lfd < 0) { vpx_codec_destroy(&dec_ctx); return 3; }

  const std::string raw_unix_name =
      "haoma-vid-" + a.stream_id + "-" + std::to_string(::getpid());
  int raw_lfd = listen_local_unix(raw_unix_name);
  if (raw_lfd < 0) {
    LOG_ERR("listen_local_unix (raw) failed");
    ::close(lfd);
    vpx_codec_destroy(&dec_ctx);
    return 3;
  }

  LOG_INFO("listening on 127.0.0.1:%u sealed + unix:@%s raw (stream=%s)",
           a.port, raw_unix_name.c_str(), a.stream_id.c_str());

  emit_ready(raw_unix_name);

  // Sealed-side client (haomad). Blocks until haomad dials in.
  int cfd = accept_one(lfd);
  if (cfd < 0) {
    ::close(raw_lfd);
    vpx_codec_destroy(&dec_ctx);
    return 4;
  }
  LOG_INFO("sealed client connected");

  // Raw-side client (host UI). Accept in a side thread so the decoder
  // can begin even if the UI hasn't dialed yet — UI might be hidden
  // (InCallBar mode); we don't want to block decode on it. Non-blocking
  // writes once connected so slow UI consumer never stalls decode.
  std::atomic<int> raw_cfd{-1};
  std::atomic<bool> raw_write_failed{false};
  std::thread raw_accept_th([&]() {
    int fd = accept_one(raw_lfd);
    if (fd < 0) {
      LOG_INFO("raw accept aborted (shutdown)");
      return;
    }
    int flags = ::fcntl(fd, F_GETFL, 0);
    if (flags >= 0) ::fcntl(fd, F_SETFL, flags | O_NONBLOCK);
    raw_cfd.store(fd);
    LOG_INFO("raw client connected via unix:@%s", raw_unix_name.c_str());
  });

  Stats              stats;
  JitterTracker      jitter;
  std::atomic<bool>  trace{a.trace};
  std::atomic<bool>  stats_req{false};
  std::atomic<bool>  muted{false};
  uint64_t           expected_counter = 0;
  bool               first_frame      = true;
  uint64_t           aead_fail_count  = 0;

  std::thread reader([&]() {
    std::vector<uint8_t> cipher(MAX_PAYLOAD_LEN);
    std::vector<uint8_t> plain(MAX_PAYLOAD_LEN);
    std::vector<uint8_t> packed;  // contiguous I420 for the raw socket
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

      vpx_codec_err_t er = vpx_codec_decode(&dec_ctx, plain.data(), (unsigned int)n, nullptr, 0);
      if (er != VPX_CODEC_OK) {
        LOG_ERR("vpx_codec_decode: %s", vpx_codec_error(&dec_ctx));
        stats.frames_dropped.fetch_add(1);
        continue;
      }

      vpx_codec_iter_t iter = nullptr;
      const vpx_image_t* img = nullptr;
      while ((img = vpx_codec_get_frame(&dec_ctx, &iter)) != nullptr) {
        if (muted.load()) {
          stats.frames_dropped.fetch_add(1);
          continue;
        }
        if (img->fmt != VPX_IMG_FMT_I420) {
          emit_warn("decoded_non_i420");
          stats.frames_dropped.fetch_add(1);
          continue;
        }
        int w = (int)img->d_w;
        int h = (int)img->d_h;
        const size_t frame_bytes = (size_t)w * (size_t)h * 3 / 2;

        int rfd = raw_cfd.load();
        if (rfd < 0 || raw_write_failed.load()) {
          stats.frames_dropped.fetch_add(1);
          continue;
        }

        packed.resize(frame_bytes);
        pack_plane(packed.data(),
                   img->planes[VPX_PLANE_Y], img->stride[VPX_PLANE_Y], w,     h);
        pack_plane(packed.data() + (size_t)w * (size_t)h,
                   img->planes[VPX_PLANE_U], img->stride[VPX_PLANE_U], w / 2, h / 2);
        pack_plane(packed.data() + (size_t)w * (size_t)h + (size_t)(w / 2) * (size_t)(h / 2),
                   img->planes[VPX_PLANE_V], img->stride[VPX_PLANE_V], w / 2, h / 2);

        // Raw-port frame: `8 BE pts_ns | I420 bytes`. pts is the
        // sender-stamped value just verified by AEAD — receiver uses it
        // to slave video display to spk's audio playback clock.
        uint8_t pts_be[8];
        haoma::streams::w_be64(pts_be, pts_ns);
        struct iovec iov[2];
        iov[0].iov_base = pts_be;
        iov[0].iov_len  = sizeof(pts_be);
        iov[1].iov_base = packed.data();
        iov[1].iov_len  = frame_bytes;
        struct msghdr msg = {};
        msg.msg_iov    = iov;
        msg.msg_iovlen = 2;
        ssize_t r = ::sendmsg(rfd, &msg, MSG_NOSIGNAL | MSG_DONTWAIT);
        if (r < 0) {
          if (errno == EAGAIN || errno == EWOULDBLOCK || errno == EINTR) {
            stats.frames_dropped.fetch_add(1);
            continue;
          }
          LOG_INFO("raw client write failed (errno=%d) — disabling raw tap", errno);
          raw_write_failed.store(true);
          stats.frames_dropped.fetch_add(1);
          continue;
        }
        if ((size_t)r != sizeof(pts_be) + frame_bytes) {
          // Short write on the raw socket = drop this frame; the
          // raw consumer must read full frames.
          stats.frames_dropped.fetch_add(1);
          continue;
        }
        stats.frames_out.fetch_add(1);
        stats.bytes_out.fetch_add(frame_bytes);
      }
    }
  });

  std::thread ctrl_th(control_loop, STDIN_FILENO, std::ref(g_done),
    [&](const ControlMsg& m) {
      switch (m.cmd) {
        case Command::Mute:    muted.store(true);  LOG_INFO("muted"); break;
        case Command::Unmute:  muted.store(false); LOG_INFO("unmuted"); break;
        case Command::Bitrate: emit_warn("bitrate_not_supported_on_decoder"); break;
        case Command::Stats:   stats_req.store(true); break;
        case Command::Exit:    LOG_INFO("exit command"); g_done.store(true); break;
        case Command::Unknown: emit_warn("unknown_command"); break;
      }
    });

  std::thread stats_th(stats_loop, std::ref(stats), &jitter,
                       std::ref(g_done), std::ref(trace), std::ref(stats_req));

  while (!g_done.load()) ::usleep(100000);

  ::shutdown(cfd, SHUT_RDWR);
  reader.join();
  ::close(cfd);

  ::shutdown(raw_lfd, SHUT_RDWR);
  if (raw_accept_th.joinable()) raw_accept_th.join();
  int rfd = raw_cfd.load();
  if (rfd >= 0) { ::shutdown(rfd, SHUT_RDWR); ::close(rfd); }

  vpx_codec_destroy(&dec_ctx);

  ctrl_th.join();
  stats_th.join();

  if (aead_fail_count > 0) {
    LOG_INFO("AEAD failures over the call: %llu", (unsigned long long)aead_fail_count);
  }
  LOG_INFO("clean exit");
  return 0;
}
