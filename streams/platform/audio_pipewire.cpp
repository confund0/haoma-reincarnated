// PipeWire audio backend (Linux). Implements both capture and playback halves
// of platform/audio_backend.h. Each binary (haoma-mic / haoma-spk) links this
// file and references only the half it needs.
//
// PipeWire client API: thread-loop owns the event pump; pw_stream_new_simple
// auto-attaches the listener; on_process callback is the hot path.

#include "audio_backend.h"
#include "../common/log.h"

#include <pipewire/pipewire.h>
#include <spa/param/audio/format-utils.h>
#include <spa/param/audio/raw.h>

#include <atomic>
#include <cstring>
#include <functional>
#include <memory>
#include <vector>

namespace haoma::streams {
namespace {

std::atomic<bool> g_pw_inited{false};
void ensure_pw_init() {
  bool exp = false;
  if (g_pw_inited.compare_exchange_strong(exp, true)) {
    pw_init(nullptr, nullptr);
  }
}

const spa_pod* build_format_pod(uint8_t* buf, size_t buf_size) {
  spa_pod_builder b = SPA_POD_BUILDER_INIT(buf, (uint32_t)buf_size);
  spa_audio_info_raw info{};
  info.format   = SPA_AUDIO_FORMAT_F32;
  info.rate     = SAMPLE_RATE;
  info.channels = CHANNELS;
  return spa_format_audio_raw_build(&b, SPA_PARAM_EnumFormat, &info);
}

// ── Capture ────────────────────────────────────────────────────────────────

struct PWCapture final : AudioCapture {
  pw_thread_loop* loop   = nullptr;
  pw_stream*      stream = nullptr;
  std::function<void(const float*)> on_frame;
  std::vector<float> acc;
  std::atomic<bool> stopped{false};

  static void on_process(void* ud);

  bool open(std::function<void(const float*)> cb) override;
  void close() override;
  ~PWCapture() override { close(); }
};

const pw_stream_events kCaptureEvents = []() {
  pw_stream_events e{};
  e.version = PW_VERSION_STREAM_EVENTS;
  e.process = &PWCapture::on_process;
  return e;
}();

void PWCapture::on_process(void* ud) {
  auto* self = static_cast<PWCapture*>(ud);
  if (self->stopped.load()) return;

  pw_buffer* b = pw_stream_dequeue_buffer(self->stream);
  if (!b) return;

  spa_buffer* sb = b->buffer;
  if (sb->n_datas < 1 || !sb->datas[0].data) {
    pw_stream_queue_buffer(self->stream, b);
    return;
  }
  spa_data& d = sb->datas[0];
  uint32_t offset = d.chunk->offset;
  uint32_t size   = d.chunk->size;

  if (size > 0) {
    const float* in = reinterpret_cast<const float*>(reinterpret_cast<uint8_t*>(d.data) + offset);
    size_t n_samples = size / sizeof(float);
    for (size_t i = 0; i < n_samples; ++i) {
      self->acc.push_back(in[i]);
      if (self->acc.size() >= (size_t)FRAME_SAMPLES) {
        self->on_frame(self->acc.data());
        self->acc.clear();
      }
    }
  }
  pw_stream_queue_buffer(self->stream, b);
}

bool PWCapture::open(std::function<void(const float*)> cb) {
  on_frame = std::move(cb);
  acc.reserve(FRAME_SAMPLES * 2);
  ensure_pw_init();

  loop = pw_thread_loop_new("haoma-mic", nullptr);
  if (!loop) { LOG_ERR("pw_thread_loop_new failed"); return false; }

  pw_properties* props = pw_properties_new(
      PW_KEY_MEDIA_TYPE,     "Audio",
      PW_KEY_MEDIA_CATEGORY, "Capture",
      PW_KEY_MEDIA_ROLE,     "Communication",
      PW_KEY_NODE_NAME,      "haoma-mic",
      nullptr);

  stream = pw_stream_new_simple(
      pw_thread_loop_get_loop(loop),
      "haoma-mic",
      props,
      &kCaptureEvents,
      this);
  if (!stream) { LOG_ERR("pw_stream_new_simple (capture) failed"); return false; }

  uint8_t pod_buf[1024];
  const spa_pod* params[1] = { build_format_pod(pod_buf, sizeof(pod_buf)) };

  int rc = pw_stream_connect(
      stream,
      PW_DIRECTION_INPUT,
      PW_ID_ANY,
      static_cast<pw_stream_flags>(PW_STREAM_FLAG_AUTOCONNECT
                                 | PW_STREAM_FLAG_MAP_BUFFERS
                                 | PW_STREAM_FLAG_RT_PROCESS),
      params, 1);
  if (rc < 0) { LOG_ERR("pw_stream_connect (capture) rc=%d", rc); return false; }

  if (pw_thread_loop_start(loop) != 0) {
    LOG_ERR("pw_thread_loop_start (capture) failed");
    return false;
  }
  return true;
}

void PWCapture::close() {
  stopped.store(true);
  if (loop) pw_thread_loop_stop(loop);
  if (stream) { pw_stream_destroy(stream); stream = nullptr; }
  if (loop)   { pw_thread_loop_destroy(loop); loop = nullptr; }
}

// ── Playback ───────────────────────────────────────────────────────────────

struct PWPlayback final : AudioPlayback {
  pw_thread_loop* loop   = nullptr;
  pw_stream*      stream = nullptr;
  std::function<size_t(float*, size_t)> on_need;
  std::atomic<bool> stopped{false};

  static void on_process(void* ud);

  bool open(std::function<size_t(float*, size_t)> cb) override;
  void close() override;
  ~PWPlayback() override { close(); }
};

const pw_stream_events kPlaybackEvents = []() {
  pw_stream_events e{};
  e.version = PW_VERSION_STREAM_EVENTS;
  e.process = &PWPlayback::on_process;
  return e;
}();

void PWPlayback::on_process(void* ud) {
  auto* self = static_cast<PWPlayback*>(ud);
  if (self->stopped.load()) return;

  pw_buffer* b = pw_stream_dequeue_buffer(self->stream);
  if (!b) return;

  spa_buffer* sb = b->buffer;
  if (sb->n_datas < 1 || !sb->datas[0].data) {
    pw_stream_queue_buffer(self->stream, b);
    return;
  }
  spa_data& d = sb->datas[0];
  uint32_t stride = sizeof(float) * CHANNELS;
  uint32_t cap_samples = d.maxsize / stride;
  uint32_t want_samples = cap_samples;
  if (b->requested != 0 && b->requested < cap_samples) {
    want_samples = (uint32_t)b->requested;
  }

  float* out = reinterpret_cast<float*>(d.data);
  size_t got = self->on_need(out, want_samples);
  if (got < want_samples) {
    std::memset(out + got, 0, (want_samples - got) * sizeof(float));
  }
  d.chunk->offset = 0;
  d.chunk->stride = stride;
  d.chunk->size   = want_samples * stride;
  pw_stream_queue_buffer(self->stream, b);
}

bool PWPlayback::open(std::function<size_t(float*, size_t)> cb) {
  on_need = std::move(cb);
  ensure_pw_init();

  loop = pw_thread_loop_new("haoma-spk", nullptr);
  if (!loop) { LOG_ERR("pw_thread_loop_new failed"); return false; }

  pw_properties* props = pw_properties_new(
      PW_KEY_MEDIA_TYPE,     "Audio",
      PW_KEY_MEDIA_CATEGORY, "Playback",
      PW_KEY_MEDIA_ROLE,     "Communication",
      PW_KEY_NODE_NAME,      "haoma-spk",
      nullptr);

  stream = pw_stream_new_simple(
      pw_thread_loop_get_loop(loop),
      "haoma-spk",
      props,
      &kPlaybackEvents,
      this);
  if (!stream) { LOG_ERR("pw_stream_new_simple (playback) failed"); return false; }

  uint8_t pod_buf[1024];
  const spa_pod* params[1] = { build_format_pod(pod_buf, sizeof(pod_buf)) };

  int rc = pw_stream_connect(
      stream,
      PW_DIRECTION_OUTPUT,
      PW_ID_ANY,
      static_cast<pw_stream_flags>(PW_STREAM_FLAG_AUTOCONNECT
                                 | PW_STREAM_FLAG_MAP_BUFFERS
                                 | PW_STREAM_FLAG_RT_PROCESS),
      params, 1);
  if (rc < 0) { LOG_ERR("pw_stream_connect (playback) rc=%d", rc); return false; }

  if (pw_thread_loop_start(loop) != 0) {
    LOG_ERR("pw_thread_loop_start (playback) failed");
    return false;
  }
  return true;
}

void PWPlayback::close() {
  stopped.store(true);
  if (loop) pw_thread_loop_stop(loop);
  if (stream) { pw_stream_destroy(stream); stream = nullptr; }
  if (loop)   { pw_thread_loop_destroy(loop); loop = nullptr; }
}

}  // namespace

std::unique_ptr<AudioCapture>  make_capture()  { return std::make_unique<PWCapture>(); }
std::unique_ptr<AudioPlayback> make_playback() { return std::make_unique<PWPlayback>(); }

}  // namespace haoma::streams
