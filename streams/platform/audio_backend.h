#pragma once
#include <cstddef>
#include <functional>
#include <memory>

// Per-platform audio I/O abstraction. Voice profile locked in ADR-040 + Calls-1c.
// One concrete implementation file per platform; CMake selects exactly one.
namespace haoma::streams {

constexpr int SAMPLE_RATE   = 48000;
constexpr int CHANNELS      = 1;
constexpr int FRAME_MS      = 20;
constexpr int FRAME_SAMPLES = SAMPLE_RATE * FRAME_MS / 1000;  // 960

// Capture: backend invokes on_frame with FRAME_SAMPLES floats per call.
// Callback runs on the backend's audio thread — keep the work bounded.
class AudioCapture {
public:
  virtual ~AudioCapture() = default;
  virtual bool open(std::function<void(const float* samples)> on_frame) = 0;
  virtual void close() = 0;
};

// Playback: backend pulls samples via on_need; caller writes up to n_samples
// floats and returns count actually written. Backend silences any remainder.
class AudioPlayback {
public:
  virtual ~AudioPlayback() = default;
  virtual bool open(std::function<size_t(float* out, size_t n_samples)> on_need) = 0;
  virtual void close() = 0;
};

std::unique_ptr<AudioCapture>  make_capture();
std::unique_ptr<AudioPlayback> make_playback();

}
