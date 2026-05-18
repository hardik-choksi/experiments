#include <poll.h>
#include <sys/poll.h>
#include <unistd.h>

void event_loop(int *fds, int nfds) {
  struct pollfd pfds[nfds];
  for (int i = 0; i < nfds; i++) {
    pfds[i].fd = fds[i];
    pfds[i].events = POLLIN; // interested in readability
  }

  while (1) {
    // poll(struct pollfd *fds, nfds_t nfds, int timeout)
    int ready = poll(pfds, nfds, -1); // block until any one is ready
    for (int i = 0; i < nfds; i++) {
      if (pfds[i].revents & POLLIN) {
        char buf[4096];
        ssize_t n = read(pfds[i].fd, buf, sizeof buf);
        // process data...
      }
    }
  }
}
