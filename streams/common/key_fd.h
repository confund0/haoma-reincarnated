#pragma once
#include <cstdint>
#include "aead.h"

// Reads exactly AEAD_KEY_BYTES off `fd` using raw read(2) (no stdio
// buffering — control plane in Calls-1e shares the same fd for JSON-lines,
// so we must not consume anything beyond the key). Does NOT close `fd`.
//
// In production (Calls-1d+) `fd` is stdin (0): the spawning haoma writes
// the 32-byte key as the very first bytes, then later writes JSON-line
// control commands on the same pipe. Per ADR-040 Decision 3: not argv
// (leaks via /proc/.../cmdline), not env (/proc/.../environ).
namespace haoma::streams {

bool read_key(int fd, uint8_t out[AEAD_KEY_BYTES]);

}
