#pragma once
#include <cstdint>
#include <string>

namespace haoma::streams {

int listen_local(uint16_t port);

int listen_local_unix(const std::string& name);

int accept_one(int listen_fd);

}
