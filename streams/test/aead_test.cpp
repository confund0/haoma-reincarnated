// AEAD round-trip + tamper / wrong-key / wrong-stream-id / wrong-counter.
// Standalone binary — exits 0 on pass, 1 on fail.

#include "../common/aead.h"
#include "../common/framing.h"

#include <cstdio>
#include <cstring>
#include <vector>
#include <unistd.h>

using namespace haoma::streams;

static int fails = 0;
#define EXPECT(cond, msg) do {                          \
  if (!(cond)) { std::fprintf(stderr, "FAIL: %s\n", msg); fails++; } \
} while (0)

int main() {
  if (!Aead::global_init()) { std::fprintf(stderr, "sodium init\n"); return 1; }

  uint8_t key_a[AEAD_KEY_BYTES];
  uint8_t key_b[AEAD_KEY_BYTES];
  for (size_t i = 0; i < AEAD_KEY_BYTES; ++i) {
    key_a[i] = (uint8_t)(0x10 + i);
    key_b[i] = (uint8_t)(0xa0 + i);
  }

  Aead a, b_same, b_wrong_key, b_wrong_id;
  a.configure(key_a, "mic");
  b_same.configure(key_a, "mic");
  b_wrong_key.configure(key_b, "mic");
  b_wrong_id.configure(key_a, "cam");

  uint8_t plain[40];
  for (size_t i = 0; i < sizeof(plain); ++i) plain[i] = (uint8_t)(i * 3 + 7);

  uint8_t cipher[sizeof(plain)];
  uint8_t tag[AEAD_TAG_BYTES];
  uint8_t out[sizeof(plain)];

  constexpr uint64_t kPts = 1'234'567'000ULL;

  // 1. Round-trip with identical config decrypts cleanly.
  EXPECT(a.seal(0, kPts, plain, sizeof(plain), cipher, tag), "seal");
  EXPECT(b_same.open(0, kPts, cipher, sizeof(cipher), tag, out), "open same-config");
  EXPECT(std::memcmp(plain, out, sizeof(plain)) == 0, "plaintext recovered");

  // 2. Wrong key rejects.
  EXPECT(!b_wrong_key.open(0, kPts, cipher, sizeof(cipher), tag, out), "wrong key rejects");

  // 3. Wrong stream-id (AAD differs) rejects.
  EXPECT(!b_wrong_id.open(0, kPts, cipher, sizeof(cipher), tag, out), "wrong stream-id rejects");

  // 4. Wrong counter (nonce differs) rejects.
  EXPECT(!b_same.open(1, kPts, cipher, sizeof(cipher), tag, out), "wrong counter rejects");

  // 4b. Wrong pts_ns (AAD differs) rejects.
  EXPECT(!b_same.open(0, kPts + 1, cipher, sizeof(cipher), tag, out), "wrong pts_ns rejects");

  // 5. Tampered ciphertext rejects.
  uint8_t bent[sizeof(plain)];
  std::memcpy(bent, cipher, sizeof(cipher));
  bent[0] ^= 1;
  EXPECT(!b_same.open(0, kPts, bent, sizeof(bent), tag, out), "tampered cipher rejects");

  // 6. Tampered tag rejects.
  uint8_t bent_tag[AEAD_TAG_BYTES];
  std::memcpy(bent_tag, tag, AEAD_TAG_BYTES);
  bent_tag[0] ^= 1;
  EXPECT(!b_same.open(0, kPts, cipher, sizeof(cipher), bent_tag, out), "tampered tag rejects");

  // 7. encode_frame + read_frame round-trip preserves counter, pts_ns, cipher, tag.
  uint8_t wire[MAX_FRAME_LEN];
  size_t flen = encode_frame(42, kPts, cipher, sizeof(cipher), tag, wire, sizeof(wire));
  EXPECT(flen == FRAME_OVERHEAD + sizeof(cipher), "encode_frame size");

  // Pipe-loopback to exercise read_frame (it takes an fd).
  int pipefd[2];
  if (::pipe(pipefd) != 0) { std::fprintf(stderr, "pipe\n"); return 1; }
  ssize_t w = ::write(pipefd[1], wire, flen);
  EXPECT((size_t)w == flen, "pipe write");
  ::close(pipefd[1]);

  uint64_t got_counter = 0;
  uint64_t got_pts     = 0;
  uint8_t got_cipher[sizeof(cipher)];
  uint8_t got_tag[AEAD_TAG_BYTES];
  int64_t got = read_frame(pipefd[0], &got_counter, &got_pts, got_cipher, sizeof(got_cipher), got_tag);
  EXPECT(got == (int64_t)sizeof(cipher), "read_frame size");
  EXPECT(got_counter == 42, "counter round-trip");
  EXPECT(got_pts == kPts, "pts_ns round-trip");
  EXPECT(std::memcmp(got_cipher, cipher, sizeof(cipher)) == 0, "cipher round-trip");
  EXPECT(std::memcmp(got_tag, tag, AEAD_TAG_BYTES) == 0, "tag round-trip");
  ::close(pipefd[0]);

  if (fails) {
    std::fprintf(stderr, "%d failures\n", fails);
    return 1;
  }
  std::fprintf(stderr, "all passed\n");
  return 0;
}
