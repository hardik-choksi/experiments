#include <sys/select.h>
#include <unistd.h>

// fds => client fds
void event_loop(int *fds, int nfds) {
  fd_set read_set; // bit map of fds
  while (1) {
    // init
    FD_ZERO(&read_set);
    int max_fd = 0;
    for (int i = 0; i < nfds; i++) {
      FD_SET(fds[i], &read_set);
      if (fds[i] > max_fd)
        max_fd = fds[i];
    }
    // Block until at least one fd is readable. Kernel scans ALL fds (O(n))
    // select(int nfds, fd_set *restrict readfds, fd_set *restrict writefds,
    // fd_set *restrict exceptfds, struct timeval *restrict timeout)
    select(max_fd + 1, &read_set, NULL, NULL, NULL);

    // Check which fds are ready (Another O(n) scan)
    for (int i = 0; i < nfds; i++) {
      if (FD_ISSET(fds[i], &read_set)) {
        char buf[4096];
        ssize_t n = read(fds[i], buf, sizeof buf);
        // process data...
      }
    }
  }
}
