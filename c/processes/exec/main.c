#include <stdio.h>
#include <unistd.h>
int main(int argc, char **argv) {
  if (!fork()) {
    printf("child process(%d) spawned\n", getpid());
    execlp("ls", "ls", "-lAh", NULL);
  } else {
    printf("parent(%d) prcoess...\n", getpid());
  }

  return 0;
}
