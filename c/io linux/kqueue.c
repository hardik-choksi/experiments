#include <sys/eventfd.h>
#include <sys/socket.h>
#include <unistd.h>

void event_loop(int server_fd) {
  int kq = kqueue();
  struct kevent change;
  EV_SET(&change, server_fd, EVFILT_READ, EV_ADD, 0, 0, NULL);
  kevent(kq, &change, 1, NULL, 0, NULL); // register, dont wait
  struct kevent events[64];

  while (1) {
    int n = kevent(kq, NULL, 0, events, 64, NULL); // wait
    for (int i = 0; i < n; i++) {
      if ((int)events[i].ident == server_fd) {
        int client = accept(server_fd, NULL, NULL);
        struct kevent cev;
        EV_SET(&cev, client, EVLIFT_READ, EV_ADD | EV_ONESHOT, 0, 0, NULL);
        kevent(kq, &cev, 1, NULL, 0, NULL);
      } else {
        char buf[4096];
        read((int)events[i].ident, buf, sizeof(buf));
        // process data...
      }
    }
  }
}
