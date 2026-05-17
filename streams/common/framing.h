#pragma once
#include <cstdint>
#include <cstddef>

// Wire frame on the localhost socket between haomad and a streamer.
// Layout (ADR-040; tag is real Poly1305 from Calls-1d onward; pts_ns
// added pre-video for A/V sync):
//
//   len(2 BE) | counter(8 BE) | pts_ns(8 BE) | ciphertext(N) | tag(16)
//
// `len` covers everything after the 2-byte length field, i.e. 8 + 8 + N + 16.
// pts_ns is sender-stamped monotonic nanoseconds since the streamer's
// first-frame epoch; receivers use it to align video against audio.
// AEAD lives in aead.h — framing is pure wire byte-shuffling and does not
// know about keys. Caller seals/opens around it.
namespace haoma::streams {

constexpr size_t FRAME_HEADER_LEN = 2 + 8 + 8;
constexpr size_t FRAME_TAG_LEN    = 16;
constexpr size_t FRAME_OVERHEAD   = FRAME_HEADER_LEN + FRAME_TAG_LEN;
// Lifted from 4000 at V-1: VP8 keyframes at 640×480 routinely exceed
// 4 KB. Audio Opus frames stay ~80 B and are unaffected. Ceiling is
// 65503 because the wire `len` field is uint16 BE covering
// counter(8) + pts_ns(8) + cipher(N) + tag(16) ≤ 65535.
constexpr size_t MAX_PAYLOAD_LEN  = 65503;
constexpr size_t MAX_FRAME_LEN    = FRAME_OVERHEAD + MAX_PAYLOAD_LEN;
static_assert(8 + 8 + MAX_PAYLOAD_LEN + FRAME_TAG_LEN <= 0xFFFF,
              "wire body_len is uint16 BE — MAX_PAYLOAD_LEN too large");

// Encodes one frame into out. Returns total frame size, or 0 on overflow.
size_t encode_frame(uint64_t counter, uint64_t pts_ns,
                    const uint8_t* cipher, size_t cipher_len,
                    const uint8_t tag[FRAME_TAG_LEN],
                    uint8_t* out, size_t out_cap);

// Reads one full frame from fd. Cipher bytes go to cipher_out, tag bytes
// to tag_out.
//   > 0  cipher size (counter + pts_out populated)
//   == 0 clean EOF before any byte of a new frame
//   < 0  error / short read mid-frame / oversized
int64_t read_frame(int fd,
                   uint64_t* counter,
                   uint64_t* pts_out,
                   uint8_t* cipher_out, size_t cipher_cap,
                   uint8_t tag_out[FRAME_TAG_LEN]);

int64_t write_all(int fd, const uint8_t* buf, size_t len);

// BE byte-shuffling — exposed because the raw-port I420 frames carry an
// 8-byte pts header that cam writes and the renderer parses.
void     w_be64(uint8_t* p, uint64_t v);
uint64_t r_be64(const uint8_t* p);

}
