
#include "video_backend.h"

#include <memory>

namespace haoma::streams {

std::unique_ptr<VideoCapture> make_video_capture(const VideoCaptureConfig&  ) {
  return nullptr;
}

}  
