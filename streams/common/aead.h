#pragma once
#include <cstdint>
#include <cstddef>
#include <string>

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

  static bool global_init();

  void configure(const uint8_t key[AEAD_KEY_BYTES], const std::string& stream_id);

  bool seal(uint64_t counter, uint64_t pts_ns,
            const uint8_t* plain, size_t plain_len,
            uint8_t* cipher_out,
            uint8_t tag_out[AEAD_TAG_BYTES]) const;

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
