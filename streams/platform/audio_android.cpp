// Android audio backend stub — selected when CMAKE_SYSTEM_NAME == Android.
// Calls-1c is Linux-only; Calls-1g replaces this with the AAudio impl.
#include "audio_backend.h"
#include "../common/log.h"
#include <cstdlib>

namespace haoma::streams {

std::unique_ptr<AudioCapture> make_capture() {
  LOG_ERR("audio_android: AAudio capture not implemented (see Calls-1g)");
  std::abort();
}

std::unique_ptr<AudioPlayback> make_playback() {
  LOG_ERR("audio_android: AAudio playback not implemented (see Calls-1g)");
  std::abort();
}

}
