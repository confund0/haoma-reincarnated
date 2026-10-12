#pragma once
#include <cstdint>
#include "aead.h"

namespace haoma::streams {

bool read_key(int fd, uint8_t out[AEAD_KEY_BYTES]);

}
