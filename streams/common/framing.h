#pragma once
#include <cstdint>
#include <cstddef>

// Wire frame on the localhost socket between haomad and a streamer.
// Layout (ADR-040; tag is real Poly1305 from Calls-1d onward):
//
//   len(2 BE) | counter(8 BE) | ciphertext(N) | tag(16)
//
// `len` covers everything after the 2-byte length field, i.e. 8 + N + 16.
// AEAD lives in aead.h — framing is pure wire byte-shuffling and does not
// know about keys. Caller seals/opens around it.
namespace haoma::streams {

constexpr size_t FRAME_HEADER_LEN = 2 + 8;
constexpr size_t FRAME_TAG_LEN    = 16;
constexpr size_t FRAME_OVERHEAD   = FRAME_HEADER_LEN + FRAME_TAG_LEN;
constexpr size_t MAX_PAYLOAD_LEN  = 4000;
constexpr size_t MAX_FRAME_LEN    = FRAME_OVERHEAD + MAX_PAYLOAD_LEN;

// Encodes one frame into out. Returns total frame size, or 0 on overflow.
size_t encode_frame(uint64_t counter,
                    const uint8_t* cipher, size_t cipher_len,
                    const uint8_t tag[FRAME_TAG_LEN],
                    uint8_t* out, size_t out_cap);

// Reads one full frame from fd. Cipher bytes go to cipher_out, tag bytes
// to tag_out.
//   > 0  cipher size (counter populated)
//   == 0 clean EOF before any byte of a new frame
//   < 0  error / short read mid-frame / oversized
int64_t read_frame(int fd,
                   uint64_t* counter,
                   uint8_t* cipher_out, size_t cipher_cap,
                   uint8_t tag_out[FRAME_TAG_LEN]);

int64_t write_all(int fd, const uint8_t* buf, size_t len);

}
