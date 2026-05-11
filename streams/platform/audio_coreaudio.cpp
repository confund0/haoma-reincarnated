// macOS / Darwin audio backend stub — selected when CMAKE_SYSTEM_NAME == Darwin.
// Not part of Calls-1c; kept for ADR-040 multiplatform shape.
#include "audio_backend.h"
#include "../common/log.h"
#include <cstdlib>

namespace haoma::streams {

std::unique_ptr<AudioCapture> make_capture() {
  LOG_ERR("audio_coreaudio: capture not implemented");
  std::abort();
}

std::unique_ptr<AudioPlayback> make_playback() {
  LOG_ERR("audio_coreaudio: playback not implemented");
  std::abort();
}

}
