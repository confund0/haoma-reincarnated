#include "socket.h"
#include "log.h"
#include <sys/socket.h>
#include <netinet/in.h>
#include <arpa/inet.h>
#include <unistd.h>
#include <cerrno>
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
