#include <endian.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <unistd.h>

int main(int argc, char **argv) {
  int children = 10;
  pid_t pids[children];

  for (int i = 0; i < children; i++) { // fire up child processes
    pid_t pid = fork();

    if (pid == 0) { // child process
      sleep(60);
      exit(0);
    }

    pids[i] = pid;
  }

  // check their statuse periodically (every 5 seconds)
  while (1) {
    sleep(5);
    printf("checking child process status...\n");

    for (int i = 0; i < children; i++) {
      pid_t pid = pids[i];
      int status;
      pid_t res = waitpid(pid, &status, WNOHANG);
      if (res == 0) {
        //waiting
        printf("%d: waiting\n", pids[i]);
      } else if (res > 0) {
        printf("%d: finished with status %d\n", pids[i], status);
      } else {
        printf("could not check stauts of process %d\n", pids[i]);
      }
    }
  }
}
