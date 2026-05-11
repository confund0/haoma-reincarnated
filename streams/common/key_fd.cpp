#include "key_fd.h"
#include "log.h"

#include <unistd.h>
#include <cerrno>
#include <cstring>

namespace haoma::streams {

bool read_key(int fd, uint8_t out[AEAD_KEY_BYTES]) {
  size_t got = 0;
  while (got < AEAD_KEY_BYTES) {
    ssize_t r = ::read(fd, out + got, AEAD_KEY_BYTES - got);
    if (r > 0) { got += (size_t)r; continue; }
    if (r == 0) {
      LOG_ERR("key fd %d: short read (%zu/%zu before EOF)", fd, got, (size_t)AEAD_KEY_BYTES);
      return false;
    }
    if (errno == EINTR) continue;
    LOG_ERR("key fd %d: read: %s", fd, std::strerror(errno));
    return false;
  }
  return true;
}

}
