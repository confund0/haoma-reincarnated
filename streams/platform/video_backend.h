#pragma once
#include <cstddef>
#include <cstdint>
#include <functional>
#include <memory>
#include <string>

namespace haoma::streams {

class VideoCapture {
public:
  virtual ~VideoCapture() = default;

  virtual bool open(int width, int height, int fps,
                    std::function<void(const uint8_t* i420)> on_frame) = 0;

  virtual void close() = 0;
};

struct VideoCaptureConfig {
  std::string y4m_source;  
};

std::unique_ptr<VideoCapture> make_video_capture(const VideoCaptureConfig& cfg);

}
