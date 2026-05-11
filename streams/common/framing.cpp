#include "framing.h"
#include <unistd.h>
#include <cerrno>
#include <cstring>

namespace haoma::streams {
namespace {

void w_be16(uint8_t* p, uint16_t v) {
  p[0] = (v >> 8) & 0xff;
  p[1] = v & 0xff;
}
uint16_t r_be16(const uint8_t* p) {
  return ((uint16_t)p[0] << 8) | (uint16_t)p[1];
}
void w_be64(uint8_t* p, uint64_t v) {
  for (int i = 7; i >= 0; --i) { p[i] = v & 0xff; v >>= 8; }
}
uint64_t r_be64(const uint8_t* p) {
  uint64_t v = 0;
  for (int i = 0; i < 8; ++i) v = (v << 8) | p[i];
  return v;
}

int64_t read_exact(int fd, uint8_t* buf, size_t n) {
  size_t got = 0;
  while (got < n) {
    ssize_t r = ::read(fd, buf + got, n - got);
    if (r > 0) { got += (size_t)r; continue; }
    if (r == 0) return got == 0 ? 0 : -1;  // clean EOF only on frame boundary
    if (errno == EINTR) continue;
    return -1;
  }
  return (int64_t)got;
}

}  // namespace

size_t encode_frame(uint64_t counter,
                    const uint8_t* cipher, size_t cipher_len,
                    const uint8_t tag[FRAME_TAG_LEN],
                    uint8_t* out, size_t out_cap) {
  if (cipher_len > MAX_PAYLOAD_LEN) return 0;
  size_t total = FRAME_OVERHEAD + cipher_len;
  if (out_cap < total) return 0;
  uint16_t body_len = (uint16_t)(8 + cipher_len + FRAME_TAG_LEN);
  w_be16(out, body_len);
  w_be64(out + 2, counter);
  if (cipher_len > 0) std::memcpy(out + 10, cipher, cipher_len);
  std::memcpy(out + 10 + cipher_len, tag, FRAME_TAG_LEN);
  return total;
}

int64_t read_frame(int fd,
                   uint64_t* counter,
                   uint8_t* cipher_out, size_t cipher_cap,
                   uint8_t tag_out[FRAME_TAG_LEN]) {
  uint8_t hdr[2];
  int64_t r = read_exact(fd, hdr, 2);
  if (r <= 0) return r;
  uint16_t body_len = r_be16(hdr);
  if (body_len < 8 + FRAME_TAG_LEN) return -1;
  size_t cipher_len = (size_t)body_len - 8 - FRAME_TAG_LEN;
  if (cipher_len > cipher_cap || cipher_len > MAX_PAYLOAD_LEN) return -1;

  uint8_t cb[8];
  if (read_exact(fd, cb, 8) != 8) return -1;
  *counter = r_be64(cb);

  if (cipher_len > 0 && read_exact(fd, cipher_out, cipher_len) != (int64_t)cipher_len) return -1;
  if (read_exact(fd, tag_out, FRAME_TAG_LEN) != (int64_t)FRAME_TAG_LEN) return -1;

  return (int64_t)cipher_len;
}

int64_t write_all(int fd, const uint8_t* buf, size_t len) {
  size_t sent = 0;
  while (sent < len) {
    ssize_t w = ::write(fd, buf + sent, len - sent);
    if (w > 0) { sent += (size_t)w; continue; }
    if (w < 0 && errno == EINTR) continue;
    return -1;
  }
  return (int64_t)sent;
}

}
