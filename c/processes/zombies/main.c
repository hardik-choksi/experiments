#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/types.h>
#include <unistd.h>

void handle_signal(int sig) { printf("Exiting, code: %d\n", sig); }

int main() {
  // Register handler for SIGINT (Ctrl+C)
  struct sigaction sa;
  sa.sa_handler = handle_signal;
  sigemptyset(&sa.sa_mask);
  sa.sa_flags = 0;
  sigaction(SIGINT, &sa, NULL);

  // making 5 zombies.
  for (int i = 0; i < 5; ++i) {
    pid_t pid = fork();
    if (pid == 0) {
      sleep(5);
      exit(0);
    } else {
      printf("%d will become zombie after 5 seconds...\n", pid);
    }
  }

  printf("Waiting for signal (Ctrl+C)... \n");
  // Suspend execution until ANY signal is caught
  pause();

  return 0;
}
