/*
 * named pipes are important because they persist in the system, and can be used outside of program.
 */
#include <ctype.h>
#include <fcntl.h>
#include <stdio.h>
#include <unistd.h>

int main(void) {
  // first, create a pip using `mkfifo maro_pipe`
  int fd = open("maro_pipe", O_RDONLY);
  char c;

  while (read(fd, &c, 1) > 0) {
    printf("%c", toupper(c));
  }
  close(fd);

  return 0;
}
