#include "aead.h"
#include "log.h"

#include <sodium.h>
#include <cstring>

namespace haoma::streams {
namespace {

void be64(uint8_t* p, uint64_t v) {
  for (int i = 7; i >= 0; --i) { p[i] = v & 0xff; v >>= 8; }
}

void make_nonce(uint64_t counter, uint8_t out[AEAD_NONCE_BYTES]) {
  std::memset(out, 0, 4);     // 4-byte zero pad
  be64(out + 4, counter);     // 8-byte BE counter
}

// AAD = stream_id ASCII || be64(counter) || be64(pts_ns). Returned via
// out_buf which the caller sizes to stream_id.size() + 16.
size_t make_aad(const std::string& stream_id, uint64_t counter, uint64_t pts_ns,
                uint8_t* out_buf, size_t out_cap) {
  size_t need = stream_id.size() + 16;
  if (out_cap < need) return 0;
  std::memcpy(out_buf, stream_id.data(), stream_id.size());
  be64(out_buf + stream_id.size(),     counter);
  be64(out_buf + stream_id.size() + 8, pts_ns);
  return need;
}

}  // namespace

Aead::~Aead() {
  sodium_memzero(key_, sizeof(key_));
}

bool Aead::global_init() {
  if (sodium_init() < 0) {
    LOG_ERR("sodium_init failed");
    return false;
  }
  return true;
}

void Aead::configure(const uint8_t key[AEAD_KEY_BYTES], const std::string& stream_id) {
  std::memcpy(key_, key, AEAD_KEY_BYTES);
  stream_id_ = stream_id;
  configured_ = true;
}

bool Aead::seal(uint64_t counter, uint64_t pts_ns,
                const uint8_t* plain, size_t plain_len,
                uint8_t* cipher_out,
                uint8_t tag_out[AEAD_TAG_BYTES]) const {
  if (!configured_) return false;

  uint8_t nonce[AEAD_NONCE_BYTES];
  make_nonce(counter, nonce);

  uint8_t aad_buf[64];   // mic/cam/screen <= 6 bytes + 8 counter + 8 pts; cap 64 is plenty
  size_t aad_len = make_aad(stream_id_, counter, pts_ns, aad_buf, sizeof(aad_buf));
  if (aad_len == 0) return false;

  unsigned long long maclen = 0;
  int rc = crypto_aead_chacha20poly1305_ietf_encrypt_detached(
      cipher_out, tag_out, &maclen,
      plain, (unsigned long long)plain_len,
      aad_buf, (unsigned long long)aad_len,
      nullptr,
      nonce, key_);
  return rc == 0 && maclen == AEAD_TAG_BYTES;
}

bool Aead::open(uint64_t counter, uint64_t pts_ns,
                const uint8_t* cipher, size_t cipher_len,
                const uint8_t tag[AEAD_TAG_BYTES],
                uint8_t* plain_out) const {
  if (!configured_) return false;

  uint8_t nonce[AEAD_NONCE_BYTES];
  make_nonce(counter, nonce);

  uint8_t aad_buf[64];
  size_t aad_len = make_aad(stream_id_, counter, pts_ns, aad_buf, sizeof(aad_buf));
  if (aad_len == 0) return false;

  int rc = crypto_aead_chacha20poly1305_ietf_decrypt_detached(
      plain_out,
      nullptr,
      cipher, (unsigned long long)cipher_len,
      tag,
      aad_buf, (unsigned long long)aad_len,
      nonce, key_);
  return rc == 0;
}

}
