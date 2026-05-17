#include "socket.h"
#include "log.h"
#include <sys/socket.h>
#include <sys/un.h>
#include <netinet/in.h>
#include <arpa/inet.h>
#include <unistd.h>
#include <cerrno>
#include <cstddef>
#include <cstring>

namespace haoma::streams {

int listen_local(uint16_t port) {
  int fd = ::socket(AF_INET, SOCK_STREAM, 0);
  if (fd < 0) { LOG_ERR("socket: %s", std::strerror(errno)); return -1; }

  int yes = 1;
  ::setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &yes, sizeof(yes));

  sockaddr_in addr{};
  addr.sin_family = AF_INET;
  addr.sin_port = htons(port);
  addr.sin_addr.s_addr = htonl(INADDR_LOOPBACK);

  if (::bind(fd, (sockaddr*)&addr, sizeof(addr)) < 0) {
    LOG_ERR("bind 127.0.0.1:%u: %s", port, std::strerror(errno));
    ::close(fd);
    return -1;
  }
  if (::listen(fd, 1) < 0) {
    LOG_ERR("listen: %s", std::strerror(errno));
    ::close(fd);
    return -1;
  }
  return fd;
}

int listen_local_unix(const std::string& name) {
  int fd = ::socket(AF_UNIX, SOCK_STREAM, 0);
  if (fd < 0) { LOG_ERR("socket(AF_UNIX): %s", std::strerror(errno)); return -1; }

  sockaddr_un addr{};
  addr.sun_family = AF_UNIX;
  // Abstract namespace: leading NUL, then the name bytes. No fs entry,
  // no cleanup needed, name lives only while the listening fd is open.
  if (name.size() + 1 > sizeof(addr.sun_path)) {
    LOG_ERR("listen_local_unix: name too long (%zu)", name.size());
    ::close(fd);
    return -1;
  }
  addr.sun_path[0] = '\0';
  std::memcpy(addr.sun_path + 1, name.data(), name.size());
  socklen_t addr_len = static_cast<socklen_t>(
      offsetof(sockaddr_un, sun_path) + 1 + name.size());

  if (::bind(fd, (sockaddr*)&addr, addr_len) < 0) {
    LOG_ERR("bind unix:@%s: %s", name.c_str(), std::strerror(errno));
    ::close(fd);
    return -1;
  }
  if (::listen(fd, 128) < 0) {
    LOG_ERR("listen unix:@%s: %s", name.c_str(), std::strerror(errno));
    ::close(fd);
    return -1;
  }
  return fd;
}

int accept_one(int listen_fd) {
  int cfd = ::accept(listen_fd, nullptr, nullptr);
  if (cfd < 0) {
    if (errno == EINTR) {
      LOG_INFO("accept interrupted (shutdown)");
    } else {
      LOG_ERR("accept: %s", std::strerror(errno));
    }
    ::close(listen_fd);
    return -1;
  }
  ::close(listen_fd);
  return cfd;
}

}
