// Encoder-only stub for haoma-cam on platforms where capture happens
// out-of-process (today: Android, where the Kotlin CameraSource owns
// the sensor + writes I420 into cam via --input-from-raw).
//
// make_video_capture() returns nullptr so cam's --input-from-raw path
// can link without dragging NdkCamera / V4L2 / Y4M code in. The
// runtime guard in cam/main.cpp gates make_video_capture() on
// !input_from_raw, so the nullptr is never observed in production.

#include "video_backend.h"

#include <memory>

namespace haoma::streams {

std::unique_ptr<VideoCapture> make_video_capture(const VideoCaptureConfig& /*cfg*/) {
  return nullptr;
}

}  // namespace haoma::streams
