#pragma once
#include <cstddef>
#include <cstdint>
#include <functional>
#include <memory>
#include <string>

// Per-platform video capture abstraction. V-1.5: cam binary owns its
// own sensor (mirrors mic's audio_backend pattern). One concrete impl
// per platform; CMake selects exactly one via STREAMS_VIDEO_IMPL.
namespace haoma::streams {

// Capture: backend invokes on_frame with one I420 plane buffer per
// captured frame. Callback runs on the backend's capture thread —
// keep the work bounded. The buffer pointer is only valid for the
// duration of the call; callee must copy if it needs persistence.
//
// I420 layout: width*height Y bytes, then (width/2)*(height/2) U,
// then the same V. Total = width*height*3/2 bytes. No stride padding
// on the boundary the backend hands off — backends MUST emit packed
// I420 with the planes contiguous, repacking if needed.
class VideoCapture {
public:
  virtual ~VideoCapture() = default;

  // Open the platform sensor at the requested geometry. Returns
  // false on permission denial, device-open failure, or unsupported
  // format. on_frame fires on the capture thread for each I420 frame.
  virtual bool open(int width, int height, int fps,
                    std::function<void(const uint8_t* i420)> on_frame) = 0;

  virtual void close() = 0;
};

// Linux dev backend may consult VideoCaptureConfig::y4m_source for
// the path to a Y4M file. Android backend ignores it. Set before
// calling open().
struct VideoCaptureConfig {
  std::string y4m_source;  // Linux: optional Y4M file path
};

std::unique_ptr<VideoCapture> make_video_capture(const VideoCaptureConfig& cfg);

}
