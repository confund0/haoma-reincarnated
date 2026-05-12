// AAudio audio backend (Android, API 26+). Implements both halves of
// platform/audio_backend.h.
//
// AAudio's data-callback model: the framework owns a high-priority audio
// thread that invokes our static on_data hook with a raw float buffer.
// Capture: setFramesPerDataCallback is best-effort, so we accumulate into a
// FRAME_SAMPLES-sized buffer like audio_pipewire.cpp's PWCapture does.
// Playback: one shot per callback — fill what on_need gives us, silence
// the remainder.  Mirrors PWPlayback.
//
// Voice-tuning knobs (AAUDIO_INPUT_PRESET_VOICE_COMMUNICATION, setUsage,
// setContentType) are API 28-gated and guarded with
// android_get_device_api_level() so the build stays minSdk-26 friendly.
// Without these, AudioManager.setCommunicationDevice() routes voice calls
// at the framework layer but our AAudio streams still play through MEDIA
// usage and the route picker has no observable effect — VOICE_COMMUNICATION
// + SPEECH content tags are what tell AudioFlinger to follow the comm
// device.

#include "audio_backend.h"
#include "../common/log.h"

#include <aaudio/AAudio.h>
#include <android/api-level.h>
#include <dlfcn.h>
#include <mutex>
#include <thread>
#include <chrono>

#include <atomic>
#include <cstring>
#include <functional>
#include <memory>
#include <vector>

namespace haoma::streams {
namespace {

// ── Voice-tuning trampolines ───────────────────────────────────────────────
//
// AAudioStreamBuilder_{setInputPreset,setUsage,setContentType} are API
// 28+ symbols marked __INTRODUCED_IN(28); a direct call refuses to
// compile when we target API 26 (NDK r29 escalates the deprecation to
// a hard error and __builtin_available / -Wno-unguarded-availability
// pragma don't silence it). dlsym lets us discover the symbol at
// runtime and skip the call entirely on API 26/27 without a compile
// guard. Looked-up once via std::call_once; the trampoline is a
// no-op when libaaudio doesn't ship the function on this device.

using SetInputPresetFn   = void (*)(AAudioStreamBuilder*, aaudio_input_preset_t);
using SetUsageFn         = void (*)(AAudioStreamBuilder*, aaudio_usage_t);
using SetContentTypeFn   = void (*)(AAudioStreamBuilder*, aaudio_content_type_t);

SetInputPresetFn p_setInputPreset = nullptr;
SetUsageFn       p_setUsage       = nullptr;
SetContentTypeFn p_setContentType = nullptr;
std::once_flag   voice_init_flag;

void init_voice_symbols() {
  if (android_get_device_api_level() < 28) return;
  void* lib = dlopen("libaaudio.so", RTLD_NOW | RTLD_NOLOAD);
  if (!lib) lib = dlopen("libaaudio.so", RTLD_NOW);
  if (!lib) return;
  p_setInputPreset = reinterpret_cast<SetInputPresetFn>(
      dlsym(lib, "AAudioStreamBuilder_setInputPreset"));
  p_setUsage = reinterpret_cast<SetUsageFn>(
      dlsym(lib, "AAudioStreamBuilder_setUsage"));
  p_setContentType = reinterpret_cast<SetContentTypeFn>(
      dlsym(lib, "AAudioStreamBuilder_setContentType"));
}

// ── Capture ────────────────────────────────────────────────────────────────

struct AACapture final : AudioCapture {
  // `stream` swaps under the reopen worker — protected by `swap_mu` so
  // close() can't race a mid-reopen pointer flip and double-close. The
  // data callback never touches `stream` directly (AAudio passes it as
  // the first arg), so no mutex is held on the hot path.
  AAudioStream*                     stream = nullptr;
  std::mutex                        swap_mu;
  std::function<void(const float*)> on_frame;
  std::vector<float>                acc;
  std::atomic<bool>                 stopped{false};
  std::atomic<bool>                 shutting_down{false};
  std::atomic<bool>                 reopen_inflight{false};

  static aaudio_data_callback_result_t on_data(
      AAudioStream* s, void* ud, void* audio_data, int32_t num_frames);
  static void on_error(AAudioStream* s, void* ud, aaudio_result_t error);

  bool open(std::function<void(const float*)> cb) override;
  void close() override;
  ~AACapture() override { close(); }

  bool open_stream();   // build+open+start; assigns `stream`
  void reopen_after_disconnect();
};

aaudio_data_callback_result_t AACapture::on_data(
    AAudioStream* /*s*/, void* ud, void* audio_data, int32_t num_frames) {
  auto* self = static_cast<AACapture*>(ud);
  if (self->stopped.load()) return AAUDIO_CALLBACK_RESULT_STOP;

  // CHANNELS == 1 so one frame == one float sample.
  const float* in = static_cast<const float*>(audio_data);
  for (int32_t i = 0; i < num_frames; ++i) {
    self->acc.push_back(in[i]);
    if (self->acc.size() >= (size_t)FRAME_SAMPLES) {
      self->on_frame(self->acc.data());
      self->acc.clear();
    }
  }
  return AAUDIO_CALLBACK_RESULT_CONTINUE;
}

void AACapture::on_error(AAudioStream* /*s*/, void* ud, aaudio_result_t error) {
  auto* self = static_cast<AACapture*>(ud);
  LOG_ERR("aaudio capture error: %s", AAudio_convertResultToText(error));
  // AAUDIO_ERROR_DISCONNECTED fires whenever the routing destination
  // changes (user picks Speaker, BT (dis)connect, headset plug). The
  // stream is dead; AAudio docs explicitly forbid calling AAudio APIs
  // from this callback — they can deadlock with the audio thread.
  // Spawn a detached worker that closes the dead stream and opens a
  // fresh one with the same parameters; data + error callbacks survive
  // because the lambdas / function pointers live on `this`.
  if (error != AAUDIO_ERROR_DISCONNECTED) return;
  if (self->shutting_down.load()) return;
  bool expected = false;
  if (!self->reopen_inflight.compare_exchange_strong(expected, true)) return;
  std::thread([self]() { self->reopen_after_disconnect(); }).detach();
}

bool AACapture::open(std::function<void(const float*)> cb) {
  on_frame = std::move(cb);
  acc.reserve(FRAME_SAMPLES * 2);
  return open_stream();
}

bool AACapture::open_stream() {
  AAudioStreamBuilder* b = nullptr;
  aaudio_result_t rc = AAudio_createStreamBuilder(&b);
  if (rc != AAUDIO_OK) {
    LOG_ERR("AAudio_createStreamBuilder: %s", AAudio_convertResultToText(rc));
    return false;
  }

  AAudioStreamBuilder_setDirection(b, AAUDIO_DIRECTION_INPUT);
  AAudioStreamBuilder_setSampleRate(b, SAMPLE_RATE);
  AAudioStreamBuilder_setChannelCount(b, CHANNELS);
  AAudioStreamBuilder_setFormat(b, AAUDIO_FORMAT_PCM_FLOAT);
  AAudioStreamBuilder_setSharingMode(b, AAUDIO_SHARING_MODE_SHARED);
  AAudioStreamBuilder_setPerformanceMode(b, AAUDIO_PERFORMANCE_MODE_LOW_LATENCY);
  AAudioStreamBuilder_setFramesPerDataCallback(b, FRAME_SAMPLES);
  AAudioStreamBuilder_setDataCallback(b, &AACapture::on_data, this);
  AAudioStreamBuilder_setErrorCallback(b, &AACapture::on_error, this);
  // Voice-comm input preset is gated on API 28+; trampoline is a
  // no-op on API 26/27 (default capture preset stays in effect).
  std::call_once(voice_init_flag, init_voice_symbols);
  if (p_setInputPreset) {
    p_setInputPreset(b, AAUDIO_INPUT_PRESET_VOICE_COMMUNICATION);
  }

  AAudioStream* s = nullptr;
  rc = AAudioStreamBuilder_openStream(b, &s);
  AAudioStreamBuilder_delete(b);
  if (rc != AAUDIO_OK) {
    LOG_ERR("AAudioStreamBuilder_openStream (capture): %s",
            AAudio_convertResultToText(rc));
    return false;
  }

  // AAudio may negotiate format/rate/channels different from what we asked
  // for; the F32/48k/mono triplet is supported by every recent device but
  // a mismatch here would silently break Opus encoding downstream.
  if (AAudioStream_getFormat(s)        != AAUDIO_FORMAT_PCM_FLOAT ||
      AAudioStream_getSampleRate(s)    != SAMPLE_RATE ||
      AAudioStream_getChannelCount(s)  != CHANNELS) {
    LOG_ERR("aaudio negotiated unexpected format: rate=%d ch=%d fmt=%d",
            AAudioStream_getSampleRate(s),
            AAudioStream_getChannelCount(s),
            AAudioStream_getFormat(s));
    AAudioStream_close(s);
    return false;
  }

  rc = AAudioStream_requestStart(s);
  if (rc != AAUDIO_OK) {
    LOG_ERR("AAudioStream_requestStart (capture): %s",
            AAudio_convertResultToText(rc));
    AAudioStream_close(s);
    return false;
  }
  std::lock_guard<std::mutex> lock(swap_mu);
  stream = s;
  return true;
}

void AACapture::reopen_after_disconnect() {
  // Brief backoff so AudioFlinger settles after the route change —
  // hammering reopen synchronously can race with the comm-device flip
  // and yield a second DISCONNECTED.
  std::this_thread::sleep_for(std::chrono::milliseconds(50));
  if (shutting_down.load()) {
    reopen_inflight.store(false);
    return;
  }
  AAudioStream* old = nullptr;
  {
    std::lock_guard<std::mutex> lock(swap_mu);
    old = stream;
    stream = nullptr;
  }
  if (old) {
    AAudioStream_requestStop(old);
    AAudioStream_close(old);
  }
  acc.clear();
  bool ok = open_stream();
  if (ok) {
    LOG_INFO("aaudio capture reopened after DISCONNECTED");
  } else {
    LOG_ERR("aaudio capture reopen failed — call audio will stay silent");
    stopped.store(true);
  }
  reopen_inflight.store(false);
}

void AACapture::close() {
  shutting_down.store(true);
  stopped.store(true);
  // Drain any in-flight reopen before we tear down the stream so the
  // worker thread doesn't observe a closed stream pointer or assign a
  // fresh one after our close().
  while (reopen_inflight.load()) {
    std::this_thread::sleep_for(std::chrono::milliseconds(5));
  }
  AAudioStream* s = nullptr;
  {
    std::lock_guard<std::mutex> lock(swap_mu);
    s = stream;
    stream = nullptr;
  }
  if (s) {
    AAudioStream_requestStop(s);
    AAudioStream_close(s);
  }
}

// ── Playback ───────────────────────────────────────────────────────────────
//
// Mirror of AACapture's reopen-on-DISCONNECTED ladder. The same routing
// events (Speaker/Earpiece toggle, BT (dis)connect, headset plug) tear
// down output streams just like input ones — and a silent playback path
// is half of the "voice disappears on both sides" failure mode.

struct AAPlayback final : AudioPlayback {
  AAudioStream*                         stream = nullptr;
  std::mutex                            swap_mu;
  std::function<size_t(float*, size_t)> on_need;
  std::atomic<bool>                     stopped{false};
  std::atomic<bool>                     shutting_down{false};
  std::atomic<bool>                     reopen_inflight{false};

  static aaudio_data_callback_result_t on_data(
      AAudioStream* s, void* ud, void* audio_data, int32_t num_frames);
  static void on_error(AAudioStream* s, void* ud, aaudio_result_t error);

  bool open(std::function<size_t(float*, size_t)> cb) override;
  void close() override;
  ~AAPlayback() override { close(); }

  bool open_stream();
  void reopen_after_disconnect();
};

aaudio_data_callback_result_t AAPlayback::on_data(
    AAudioStream* /*s*/, void* ud, void* audio_data, int32_t num_frames) {
  auto* self = static_cast<AAPlayback*>(ud);
  if (self->stopped.load()) return AAUDIO_CALLBACK_RESULT_STOP;

  // CHANNELS == 1 so one frame == one float sample.
  float*       out  = static_cast<float*>(audio_data);
  const size_t want = (size_t)num_frames;
  size_t       got  = self->on_need(out, want);
  if (got < want) {
    std::memset(out + got, 0, (want - got) * sizeof(float));
  }
  return AAUDIO_CALLBACK_RESULT_CONTINUE;
}

void AAPlayback::on_error(AAudioStream* /*s*/, void* ud, aaudio_result_t error) {
  auto* self = static_cast<AAPlayback*>(ud);
  LOG_ERR("aaudio playback error: %s", AAudio_convertResultToText(error));
  if (error != AAUDIO_ERROR_DISCONNECTED) return;
  if (self->shutting_down.load()) return;
  bool expected = false;
  if (!self->reopen_inflight.compare_exchange_strong(expected, true)) return;
  std::thread([self]() { self->reopen_after_disconnect(); }).detach();
}

bool AAPlayback::open(std::function<size_t(float*, size_t)> cb) {
  on_need = std::move(cb);
  return open_stream();
}

bool AAPlayback::open_stream() {
  AAudioStreamBuilder* b = nullptr;
  aaudio_result_t rc = AAudio_createStreamBuilder(&b);
  if (rc != AAUDIO_OK) {
    LOG_ERR("AAudio_createStreamBuilder: %s", AAudio_convertResultToText(rc));
    return false;
  }

  AAudioStreamBuilder_setDirection(b, AAUDIO_DIRECTION_OUTPUT);
  AAudioStreamBuilder_setSampleRate(b, SAMPLE_RATE);
  AAudioStreamBuilder_setChannelCount(b, CHANNELS);
  AAudioStreamBuilder_setFormat(b, AAUDIO_FORMAT_PCM_FLOAT);
  AAudioStreamBuilder_setSharingMode(b, AAUDIO_SHARING_MODE_SHARED);
  AAudioStreamBuilder_setPerformanceMode(b, AAUDIO_PERFORMANCE_MODE_LOW_LATENCY);
  AAudioStreamBuilder_setFramesPerDataCallback(b, FRAME_SAMPLES);
  AAudioStreamBuilder_setDataCallback(b, &AAPlayback::on_data, this);
  AAudioStreamBuilder_setErrorCallback(b, &AAPlayback::on_error, this);
  std::call_once(voice_init_flag, init_voice_symbols);
  if (p_setUsage) p_setUsage(b, AAUDIO_USAGE_VOICE_COMMUNICATION);
  if (p_setContentType) p_setContentType(b, AAUDIO_CONTENT_TYPE_SPEECH);

  AAudioStream* s = nullptr;
  rc = AAudioStreamBuilder_openStream(b, &s);
  AAudioStreamBuilder_delete(b);
  if (rc != AAUDIO_OK) {
    LOG_ERR("AAudioStreamBuilder_openStream (playback): %s",
            AAudio_convertResultToText(rc));
    return false;
  }

  if (AAudioStream_getFormat(s)        != AAUDIO_FORMAT_PCM_FLOAT ||
      AAudioStream_getSampleRate(s)    != SAMPLE_RATE ||
      AAudioStream_getChannelCount(s)  != CHANNELS) {
    LOG_ERR("aaudio negotiated unexpected format: rate=%d ch=%d fmt=%d",
            AAudioStream_getSampleRate(s),
            AAudioStream_getChannelCount(s),
            AAudioStream_getFormat(s));
    AAudioStream_close(s);
    return false;
  }

  rc = AAudioStream_requestStart(s);
  if (rc != AAUDIO_OK) {
    LOG_ERR("AAudioStream_requestStart (playback): %s",
            AAudio_convertResultToText(rc));
    AAudioStream_close(s);
    return false;
  }
  std::lock_guard<std::mutex> lock(swap_mu);
  stream = s;
  return true;
}

void AAPlayback::reopen_after_disconnect() {
  std::this_thread::sleep_for(std::chrono::milliseconds(50));
  if (shutting_down.load()) {
    reopen_inflight.store(false);
    return;
  }
  AAudioStream* old = nullptr;
  {
    std::lock_guard<std::mutex> lock(swap_mu);
    old = stream;
    stream = nullptr;
  }
  if (old) {
    AAudioStream_requestStop(old);
    AAudioStream_close(old);
  }
  bool ok = open_stream();
  if (ok) {
    LOG_INFO("aaudio playback reopened after DISCONNECTED");
  } else {
    LOG_ERR("aaudio playback reopen failed — peer audio will stay silent");
    stopped.store(true);
  }
  reopen_inflight.store(false);
}

void AAPlayback::close() {
  shutting_down.store(true);
  stopped.store(true);
  while (reopen_inflight.load()) {
    std::this_thread::sleep_for(std::chrono::milliseconds(5));
  }
  AAudioStream* s = nullptr;
  {
    std::lock_guard<std::mutex> lock(swap_mu);
    s = stream;
    stream = nullptr;
  }
  if (s) {
    AAudioStream_requestStop(s);
    AAudioStream_close(s);
  }
}

}  // namespace

std::unique_ptr<AudioCapture>  make_capture()  { return std::make_unique<AACapture>(); }
std::unique_ptr<AudioPlayback> make_playback() { return std::make_unique<AAPlayback>(); }

}  // namespace haoma::streams
