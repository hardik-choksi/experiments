#include <sys/epoll.h>
#include <sys/socket.h>
#include <unistd.h>

void event_loop(int server_fd) {
  int epfd = epoll_create1(0); // kernet side interest list
  struct epoll_event ev = {.events = EPOLLIN, .data.fd = server_fd};
  epoll_ctl(epfd, EPOLL_CTL_ADD, server_fd, &ev);
  struct epoll_event events[64];

  while (1) {
    // returns only ready fds. O(ready), not O(total)
    int n = epoll_wait(epfd, events, sizeof events, -1);
    for (int i = 0; i < n; i++) {
      if (events[i].data.fd == server_fd) {
        int client = accept(server_fd, NULL, NULL);
        struct epoll_event cev = {.events = EPOLLIN | EPOLLET,
                                  .data.fd = client};
        epoll_ctl(epfd, EPOLL_CTL_ADD, client, &cev);
      } else {
        char buf[4096];
        read(events[i].data.fd, buf, sizeof(buf));
        // process data...
      }
    }
  }
}
