#include <stdio.h>
#include <sys/wait.h>
#include <unistd.h>

int main(int argc, char **argv) {
  int pipefds[2];
  pipe(pipefds);

  printf("read pipe fd: %d\n", pipefds[0]);
  printf("write pipe fd: %d\n", pipefds[1]);

  write(pipefds[1], "Are ya winning son?", 20);

  if (fork() == 0) {
    char buf[20];
    // sleep(5);
    read(pipefds[0], buf, 20);
    // for (int i = 0; i < 5; ++i) {
    //   read(pipefds[0], buf, 20);
    //   printf("[%d] msg fom dad: %s\n", getpid(), buf);
    // }
    printf("[%d] dad opens the door, said: %s\n", getpid(), buf);

    char *ans = "yes, dad\n";
    write(pipefds[1], ans, 8);

    return 0;
  }

  // for (int i = 0; i < 5; ++i) {
  //   char buf[50];
  //   sprintf(buf, "%d msg", i+1);
  //   write(pipefds[1], buf, sizeof(buf));
  // }

  int status;
  wait(&status);
  char bf1[100];
  read(pipefds[0], bf1, 100);
  printf("[%d] child : %s\n", getpid(), bf1);
  printf("[%d] child completed with status: %d\n", getpid(), status);
  return 0;
}
