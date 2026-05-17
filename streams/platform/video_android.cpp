// NdkCamera video backend (Android, API 26+). Implements VideoCapture
// from platform/video_backend.h.
//
// Mirrors audio_android.cpp's posture: own the platform sensor inside
// the streamer process. Permission inherited from the app UID
// (CAMERA permission is granted by the host APK in V-2b's manifest).
//
// Chosen lens: LENS_FACING_FRONT. Geometry: caller-supplied width x
// height @ fps via VideoCapture::open. Output format request: YUV_420_888
// (which the device may deliver as planar I420 or semi-planar NV12/21).
// On callback, we repack into packed I420 (Y / U / V contiguous) using
// the per-plane pixel & row strides AImage reports.

#include "video_backend.h"
#include "../common/log.h"

#include <camera/NdkCameraDevice.h>
#include <camera/NdkCameraManager.h>
#include <camera/NdkCameraMetadata.h>
#include <camera/NdkCaptureRequest.h>
#include <camera/NdkCameraCaptureSession.h>
#include <media/NdkImage.h>
#include <media/NdkImageReader.h>

#include <atomic>
#include <cstring>
#include <functional>
#include <memory>
#include <mutex>
#include <string>
#include <vector>

namespace haoma::streams {
namespace {

struct NdkVideoCapture final : VideoCapture {
  ~NdkVideoCapture() override { close(); }

  bool open(int width, int height, int fps,
            std::function<void(const uint8_t* i420)> on_frame) override;
  void close() override;

  static void on_image_avail(void* ctx, AImageReader* reader);
  static void on_camera_disconnect(void* ctx, ACameraDevice* device);
  static void on_camera_error(void* ctx, ACameraDevice* device, int error);
  static void on_session_active(void* ctx, ACameraCaptureSession* session);
  static void on_session_ready(void* ctx, ACameraCaptureSession* session);
  static void on_session_closed(void* ctx, ACameraCaptureSession* session);

  int width_  = 0;
  int height_ = 0;
  int fps_    = 15;
  std::function<void(const uint8_t* i420)> on_frame_;
  std::vector<uint8_t> packed_;  // reusable I420 staging buffer

  ACameraManager*        mgr_     = nullptr;
  ACameraDevice*         device_  = nullptr;
  AImageReader*          reader_  = nullptr;
  ANativeWindow*         window_  = nullptr;
  ACaptureSessionOutputContainer* out_container_ = nullptr;
  ACaptureSessionOutput*  out_     = nullptr;
  ACameraOutputTarget*    target_  = nullptr;
  ACaptureRequest*        request_ = nullptr;
  ACameraCaptureSession*  session_ = nullptr;

  std::mutex             mu_;
  std::atomic<bool>      closed_{false};
};

void NdkVideoCapture::on_image_avail(void* ctx, AImageReader* reader) {
  auto* self = static_cast<NdkVideoCapture*>(ctx);
  if (!self || self->closed_.load()) return;

  AImage* image = nullptr;
  if (AImageReader_acquireLatestImage(reader, &image) != AMEDIA_OK || !image) return;

  int32_t fmt = 0;
  AImage_getFormat(image, &fmt);
  if (fmt != AIMAGE_FORMAT_YUV_420_888) {
    LOG_ERR("ndkcam: unexpected format %d (want YUV_420_888)", fmt);
    AImage_delete(image);
    return;
  }

  int32_t w = self->width_;
  int32_t h = self->height_;
  const size_t y_bytes  = (size_t)w * (size_t)h;
  const size_t uv_bytes = (size_t)(w / 2) * (size_t)(h / 2);
  self->packed_.resize(y_bytes + 2 * uv_bytes);

  uint8_t* y_dst = self->packed_.data();
  uint8_t* u_dst = y_dst + y_bytes;
  uint8_t* v_dst = u_dst + uv_bytes;

  for (int plane_idx = 0; plane_idx < 3; ++plane_idx) {
    uint8_t* plane_ptr = nullptr;
    int plane_len = 0;
    int row_stride = 0;
    int pixel_stride = 0;
    if (AImage_getPlaneData(image, plane_idx, &plane_ptr, &plane_len) != AMEDIA_OK) {
      AImage_delete(image);
      return;
    }
    AImage_getPlaneRowStride(image, plane_idx, &row_stride);
    AImage_getPlanePixelStride(image, plane_idx, &pixel_stride);
    if (!plane_ptr || row_stride <= 0 || pixel_stride <= 0) {
      AImage_delete(image);
      return;
    }

    const int plane_w = (plane_idx == 0) ? w : (w / 2);
    const int plane_h = (plane_idx == 0) ? h : (h / 2);
    uint8_t* dst = (plane_idx == 0) ? y_dst : (plane_idx == 1 ? u_dst : v_dst);

    if (pixel_stride == 1 && row_stride == plane_w) {
      // Packed row, no stride padding — single memcpy.
      std::memcpy(dst, plane_ptr, (size_t)plane_w * (size_t)plane_h);
    } else if (pixel_stride == 1) {
      // Planar, but row-stride-padded — copy row by row.
      for (int r = 0; r < plane_h; ++r) {
        std::memcpy(dst + (size_t)r * (size_t)plane_w,
                    plane_ptr + (size_t)r * (size_t)row_stride,
                    (size_t)plane_w);
      }
    } else {
      // Semi-planar (NV12/NV21) — pixel stride 2 means UV is
      // interleaved in this plane's buffer. De-interleave into our
      // contiguous U or V destination.
      for (int r = 0; r < plane_h; ++r) {
        const uint8_t* src_row = plane_ptr + (size_t)r * (size_t)row_stride;
        uint8_t* dst_row = dst + (size_t)r * (size_t)plane_w;
        for (int c = 0; c < plane_w; ++c) {
          dst_row[c] = src_row[(size_t)c * (size_t)pixel_stride];
        }
      }
    }
  }

  AImage_delete(image);

  if (self->on_frame_) self->on_frame_(self->packed_.data());
}

void NdkVideoCapture::on_camera_disconnect(void* /*ctx*/, ACameraDevice* /*device*/) {
  LOG_ERR("ndkcam: camera disconnected");
}

void NdkVideoCapture::on_camera_error(void* /*ctx*/, ACameraDevice* /*device*/, int error) {
  LOG_ERR("ndkcam: camera error %d", error);
}

void NdkVideoCapture::on_session_active(void* /*ctx*/, ACameraCaptureSession* /*session*/) {}
void NdkVideoCapture::on_session_ready (void* /*ctx*/, ACameraCaptureSession* /*session*/) {}
void NdkVideoCapture::on_session_closed(void* /*ctx*/, ACameraCaptureSession* /*session*/) {}

// Pick the first camera reporting LENS_FACING_FRONT. Returns empty
// string if none found.
std::string find_front_camera(ACameraManager* mgr) {
  ACameraIdList* ids = nullptr;
  if (ACameraManager_getCameraIdList(mgr, &ids) != ACAMERA_OK || !ids) return "";
  std::string picked;
  for (int i = 0; i < ids->numCameras; ++i) {
    const char* id = ids->cameraIds[i];
    ACameraMetadata* meta = nullptr;
    if (ACameraManager_getCameraCharacteristics(mgr, id, &meta) != ACAMERA_OK) continue;
    ACameraMetadata_const_entry e{};
    if (ACameraMetadata_getConstEntry(meta, ACAMERA_LENS_FACING, &e) == ACAMERA_OK
        && e.count > 0 && e.data.u8[0] == ACAMERA_LENS_FACING_FRONT) {
      picked = id;
      ACameraMetadata_free(meta);
      break;
    }
    ACameraMetadata_free(meta);
  }
  ACameraManager_deleteCameraIdList(ids);
  return picked;
}

bool NdkVideoCapture::open(int width, int height, int fps,
                           std::function<void(const uint8_t* i420)> on_frame) {
  std::lock_guard<std::mutex> lk(mu_);
  width_  = width;
  height_ = height;
  fps_    = fps > 0 ? fps : 15;
  on_frame_ = std::move(on_frame);

  mgr_ = ACameraManager_create();
  if (!mgr_) { LOG_ERR("ndkcam: ACameraManager_create failed"); return false; }

  std::string cam_id = find_front_camera(mgr_);
  if (cam_id.empty()) {
    LOG_ERR("ndkcam: no front camera found");
    return false;
  }

  if (AImageReader_new(width_, height_, AIMAGE_FORMAT_YUV_420_888, /*maxImages=*/2, &reader_) != AMEDIA_OK
      || !reader_) {
    LOG_ERR("ndkcam: AImageReader_new failed");
    return false;
  }
  AImageReader_ImageListener listener{ this, &NdkVideoCapture::on_image_avail };
  AImageReader_setImageListener(reader_, &listener);
  if (AImageReader_getWindow(reader_, &window_) != AMEDIA_OK || !window_) {
    LOG_ERR("ndkcam: AImageReader_getWindow failed");
    return false;
  }

  ACameraDevice_StateCallbacks cam_cbs{
      this, &NdkVideoCapture::on_camera_disconnect, &NdkVideoCapture::on_camera_error };
  if (ACameraManager_openCamera(mgr_, cam_id.c_str(), &cam_cbs, &device_) != ACAMERA_OK
      || !device_) {
    LOG_ERR("ndkcam: openCamera('%s') failed", cam_id.c_str());
    return false;
  }

  if (ACameraDevice_createCaptureRequest(device_, TEMPLATE_RECORD, &request_) != ACAMERA_OK
      || !request_) {
    LOG_ERR("ndkcam: createCaptureRequest failed");
    return false;
  }
  if (ACameraOutputTarget_create(window_, &target_) != ACAMERA_OK || !target_) {
    LOG_ERR("ndkcam: outputTarget_create failed");
    return false;
  }
  ACaptureRequest_addTarget(request_, target_);

  if (ACaptureSessionOutputContainer_create(&out_container_) != ACAMERA_OK || !out_container_) {
    LOG_ERR("ndkcam: outputContainer_create failed");
    return false;
  }
  if (ACaptureSessionOutput_create(window_, &out_) != ACAMERA_OK || !out_) {
    LOG_ERR("ndkcam: output_create failed");
    return false;
  }
  ACaptureSessionOutputContainer_add(out_container_, out_);

  ACameraCaptureSession_stateCallbacks sess_cbs{
      this,
      &NdkVideoCapture::on_session_closed,
      &NdkVideoCapture::on_session_ready,
      &NdkVideoCapture::on_session_active };
  if (ACameraDevice_createCaptureSession(device_, out_container_, &sess_cbs, &session_) != ACAMERA_OK
      || !session_) {
    LOG_ERR("ndkcam: createCaptureSession failed");
    return false;
  }

  if (ACameraCaptureSession_setRepeatingRequest(session_, nullptr, 1, &request_, nullptr) != ACAMERA_OK) {
    LOG_ERR("ndkcam: setRepeatingRequest failed");
    return false;
  }

  LOG_INFO("ndkcam: front camera opened %dx%d @ %dfps (id=%s)",
           width_, height_, fps_, cam_id.c_str());
  return true;
}

void NdkVideoCapture::close() {
  bool was = closed_.exchange(true);
  if (was) return;
  std::lock_guard<std::mutex> lk(mu_);

  if (session_) {
    ACameraCaptureSession_stopRepeating(session_);
    ACameraCaptureSession_close(session_);
    session_ = nullptr;
  }
  if (request_) { ACaptureRequest_free(request_); request_ = nullptr; }
  if (target_)  { ACameraOutputTarget_free(target_); target_ = nullptr; }
  if (out_)     { ACaptureSessionOutput_free(out_); out_ = nullptr; }
  if (out_container_) {
    ACaptureSessionOutputContainer_free(out_container_);
    out_container_ = nullptr;
  }
  if (device_)  { ACameraDevice_close(device_); device_ = nullptr; }
  if (reader_)  { AImageReader_delete(reader_); reader_ = nullptr; window_ = nullptr; }
  if (mgr_)     { ACameraManager_delete(mgr_); mgr_ = nullptr; }
}

}  // namespace

std::unique_ptr<VideoCapture> make_video_capture(const VideoCaptureConfig& /*cfg*/) {
  return std::make_unique<NdkVideoCapture>();
}

}  // namespace haoma::streams
