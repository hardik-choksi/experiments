#include <stdio.h>
#include <unistd.h>
int main(int argc, char **argv) {
  if (!fork()) {
    printf("child process\n");
    execlp("ls", "ls", "-lAh", NULL);
  } else {
    printf("parent prcoess\n");
  }

  return 0;
}
