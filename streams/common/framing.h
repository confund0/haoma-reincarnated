#pragma once
#include <cstdint>
#include <cstddef>

namespace haoma::streams {

constexpr size_t FRAME_HEADER_LEN = 2 + 8 + 8;
constexpr size_t FRAME_TAG_LEN    = 16;
constexpr size_t FRAME_OVERHEAD   = FRAME_HEADER_LEN + FRAME_TAG_LEN;
constexpr size_t MAX_PAYLOAD_LEN  = 65503;
constexpr size_t MAX_FRAME_LEN    = FRAME_OVERHEAD + MAX_PAYLOAD_LEN;
static_assert(8 + 8 + MAX_PAYLOAD_LEN + FRAME_TAG_LEN <= 0xFFFF,
              "wire body_len is uint16 BE — MAX_PAYLOAD_LEN too large");

size_t encode_frame(uint64_t counter, uint64_t pts_ns,
                    const uint8_t* cipher, size_t cipher_len,
                    const uint8_t tag[FRAME_TAG_LEN],
                    uint8_t* out, size_t out_cap);

int64_t read_frame(int fd,
                   uint64_t* counter,
                   uint64_t* pts_out,
                   uint8_t* cipher_out, size_t cipher_cap,
                   uint8_t tag_out[FRAME_TAG_LEN]);

int64_t write_all(int fd, const uint8_t* buf, size_t len);

int64_t read_exact(int fd, uint8_t* buf, size_t n);

void     w_be64(uint8_t* p, uint64_t v);
uint64_t r_be64(const uint8_t* p);

}
