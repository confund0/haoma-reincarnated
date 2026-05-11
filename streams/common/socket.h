#pragma once
#include <cstdint>

namespace haoma::streams {

// Bind 127.0.0.1:port (loopback only — never 0.0.0.0) and listen with backlog 1.
int listen_local(uint16_t port);

// Accept one client and close the listening fd. Returns client fd or -1.
int accept_one(int listen_fd);

}
