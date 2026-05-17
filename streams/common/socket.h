#pragma once
#include <cstdint>
#include <string>

namespace haoma::streams {

// Bind 127.0.0.1:port (loopback only — never 0.0.0.0) and listen with backlog 1.
int listen_local(uint16_t port);

// Bind an AF_UNIX abstract-namespace socket named `name` (leading NUL on the
// wire) and listen with backlog 128. Used by cam/vid for their raw-I420
// second listener: bypasses the loopback TCP stack entirely so cross-process
// connect from Android Kotlin works regardless of Bionic / netd quirks that
// fail TCP loopback. Same code path works on desktop Linux.
int listen_local_unix(const std::string& name);

// Accept one client and close the listening fd. Returns client fd or -1.
int accept_one(int listen_fd);

}
