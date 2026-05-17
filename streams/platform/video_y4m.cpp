// Linux dev backend for VideoCapture — Y4M file source. Reads a Y4M
// file at the path passed via VideoCaptureConfig::y4m_source, loops
// frames at FPS cadence, restarts on EOF. Used by V-1.5 unit + Linux
// end-to-end tests; real V4L2 / desktop camera is V-4 (post-beta).

#include "video_backend.h"
#include "../common/log.h"

#include <atomic>
#include <cctype>
#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <fstream>
#include <thread>
#include <vector>

namespace haoma::streams {

namespace {

// Read one line ending in '\n'. Returns line bytes WITHOUT the '\n'.
// Empty optional on EOF.
bool read_line(std::ifstream& is, std::string& out) {
  out.clear();
  for (;;) {
    int c = is.get();
    if (c == EOF) return !out.empty();
    if (c == '\n') return true;
    out.push_back((char)c);
  }
}

// Parse "YUV4MPEG2 W640 H480 F15:1 ...". Returns (w, h, fps_num, fps_den).
// Tags can appear in any order; we only inspect W/H/F. Returns false on
// missing W or H — fps falls back to caller-supplied default.
bool parse_y4m_header(const std::string& line, int& w, int& h, int& fps_num, int& fps_den) {
  if (line.compare(0, 9, "YUV4MPEG2") != 0) return false;
  w = h = 0;
  fps_num = 0;
  fps_den = 1;
  size_t i = 9;
  while (i < line.size()) {
    while (i < line.size() && std::isspace((unsigned char)line[i])) i++;
    if (i >= line.size()) break;
    char tag = line[i++];
    std::string val;
    while (i < line.size() && !std::isspace((unsigned char)line[i])) {
      val.push_back(line[i++]);
    }
    if (tag == 'W') w = std::atoi(val.c_str());
    else if (tag == 'H') h = std::atoi(val.c_str());
    else if (tag == 'F') {
      size_t colon = val.find(':');
      if (colon == std::string::npos) {
        fps_num = std::atoi(val.c_str());
      } else {
        fps_num = std::atoi(val.substr(0, colon).c_str());
        fps_den = std::atoi(val.substr(colon + 1).c_str());
        if (fps_den == 0) fps_den = 1;
      }
    }
  }
  return w > 0 && h > 0;
}

class Y4mCapture : public VideoCapture {
 public:
  explicit Y4mCapture(std::string path) : path_(std::move(path)) {}

  ~Y4mCapture() override { close(); }

  bool open(int width, int height, int fps,
            std::function<void(const uint8_t* i420)> on_frame) override {
    if (path_.empty()) {
      LOG_ERR("y4m capture: --y4m-source required on Linux dev backend");
      return false;
    }
    width_ = width;
    height_ = height;
    fps_ = fps > 0 ? fps : 15;
    on_frame_ = std::move(on_frame);
    done_.store(false);
    th_ = std::thread([this]() { run(); });
    return true;
  }

  void close() override {
    done_.store(true);
    if (th_.joinable()) th_.join();
  }

 private:
  void run() {
    while (!done_.load()) {
      std::ifstream is(path_, std::ios::binary);
      if (!is) {
        LOG_ERR("y4m: open '%s' failed", path_.c_str());
        return;
      }
      std::string header;
      if (!read_line(is, header)) {
        LOG_ERR("y4m: empty file");
        return;
      }
      int w = 0, h = 0, fn = 0, fd = 1;
      if (!parse_y4m_header(header, w, h, fn, fd)) {
        LOG_ERR("y4m: bad header: %s", header.c_str());
        return;
      }
      if (w != width_ || h != height_) {
        LOG_ERR("y4m: file is %dx%d but cam configured for %dx%d", w, h, width_, height_);
        return;
      }
      const size_t frame_bytes = (size_t)width_ * (size_t)height_ * 3 / 2;
      std::vector<uint8_t> buf(frame_bytes);

      auto frame_period = std::chrono::nanoseconds(
          (int64_t)1e9 / (int64_t)fps_);
      auto next_due = std::chrono::steady_clock::now();

      while (!done_.load()) {
        std::string fhdr;
        if (!read_line(is, fhdr)) break;  // EOF — outer loop restarts file
        if (fhdr.compare(0, 5, "FRAME") != 0) {
          LOG_ERR("y4m: expected FRAME marker, got '%s'", fhdr.c_str());
          return;
        }
        is.read(reinterpret_cast<char*>(buf.data()), (std::streamsize)frame_bytes);
        if ((size_t)is.gcount() != frame_bytes) break;  // truncated trailing frame

        next_due += frame_period;
        auto now = std::chrono::steady_clock::now();
        if (now < next_due) std::this_thread::sleep_for(next_due - now);

        if (on_frame_) on_frame_(buf.data());
      }
    }
  }

  std::string path_;
  int width_ = 0;
  int height_ = 0;
  int fps_ = 15;
  std::function<void(const uint8_t* i420)> on_frame_;
  std::atomic<bool> done_{false};
  std::thread th_;
};

}  // namespace

std::unique_ptr<VideoCapture> make_video_capture(const VideoCaptureConfig& cfg) {
  return std::make_unique<Y4mCapture>(cfg.y4m_source);
}

}  // namespace haoma::streams
