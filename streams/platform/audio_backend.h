#pragma once
#include <cstddef>
#include <cstdint>
#include <functional>
#include <memory>

namespace haoma::streams {

constexpr int SAMPLE_RATE   = 48000;
constexpr int CHANNELS      = 1;
constexpr int FRAME_MS      = 20;
constexpr int FRAME_SAMPLES = SAMPLE_RATE * FRAME_MS / 1000;  

class AudioCapture {
public:
  virtual ~AudioCapture() = default;
  virtual bool open(std::function<void(const float* samples)> on_frame) = 0;
  virtual void close() = 0;
};

class AudioPlayback {
public:
  virtual ~AudioPlayback() = default;
  virtual bool open(std::function<size_t(float* out, size_t n_samples)> on_need) = 0;
  virtual void close() = 0;

  virtual bool query_render_timestamp(int64_t* frame_position,
                                      int64_t* monotonic_ns) = 0;
};

std::unique_ptr<AudioCapture>  make_capture();
std::unique_ptr<AudioPlayback> make_playback();

}
