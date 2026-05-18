#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <sys/wait.h>
#include <unistd.h>

#define PAGESIZE 4096

int main(int argc, char **argv) {
  char *shared_memory = (char *)mmap(NULL, PAGESIZE, PROT_READ | PROT_WRITE,
                                     MAP_SHARED | MAP_ANONYMOUS, -1, 0);

  strcpy(shared_memory, "Um hey, parent process speaking!");

  if (fork() == 0) {
    strcpy(shared_memory, "Um hey, child process speaking!");
    exit(0);
  }

  int s;
  wait(&s);
  printf("shared buffer content: %s\n", shared_memory);
  return 0;
}
