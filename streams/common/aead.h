#pragma once
#include <cstdint>
#include <cstddef>
#include <string>

// ChaCha20-Poly1305 (IETF) wrapper for Calls-1d (ADR-040 Decision 3).
//
//   nonce = 4 zero bytes || be64(counter)              (12 bytes total)
//   AAD   = stream_id ASCII || be64(counter) || be64(pts_ns)
//   tag   = 16 bytes (Poly1305), detached on the wire
//
// pts_ns binds the frame's presentation timestamp into the AAD so a same-
// counter replay at a different sender-clock time still fails integrity.
//
// Each streamer holds one Aead. Key arrives as the first 32 bytes of
// stdin at startup (see key_fd.h) and is wiped on destruction.
namespace haoma::streams {

constexpr size_t AEAD_KEY_BYTES   = 32;
constexpr size_t AEAD_NONCE_BYTES = 12;
constexpr size_t AEAD_TAG_BYTES   = 16;

class Aead {
public:
  Aead() = default;
  ~Aead();
  Aead(const Aead&)            = delete;
  Aead& operator=(const Aead&) = delete;

  // sodium_init(); call once before constructing any Aead. Returns false on
  // unrecoverable libsodium init failure.
  static bool global_init();

  // Adopts key (32B) and stream_id ("mic" / "cam" / "screen"). Caller may
  // wipe its own copy of key after this returns.
  void configure(const uint8_t key[AEAD_KEY_BYTES], const std::string& stream_id);

  // Encrypts plain[0..plain_len) into cipher_out (same length). Tag goes
  // into tag_out. Returns false if not configured.
  bool seal(uint64_t counter, uint64_t pts_ns,
            const uint8_t* plain, size_t plain_len,
            uint8_t* cipher_out,
            uint8_t tag_out[AEAD_TAG_BYTES]) const;

  // Decrypts cipher[0..cipher_len) into plain_out (same length) iff tag
  // verifies. Returns false on tag mismatch (caller should drop the frame).
  bool open(uint64_t counter, uint64_t pts_ns,
            const uint8_t* cipher, size_t cipher_len,
            const uint8_t tag[AEAD_TAG_BYTES],
            uint8_t* plain_out) const;

private:
  uint8_t     key_[AEAD_KEY_BYTES]{};
  std::string stream_id_;
  bool        configured_ = false;
};

}
