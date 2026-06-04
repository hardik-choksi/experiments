#include <stdio.h>
#include <unistd.h>
#include <fcntl.h>

int main(int argc, char** argv) {
  char *command[] = { "grep", "-inE", "struct" };
  char *bin = command[0];

  int redirect_fd = open("file.txt", O_CREAT | O_TRUNC | O_WRONLY);
  dup2(redirect_fd, STDOUT_FILENO);
  close(redirect_fd);

  if (execvp(bin, command) == -1) {
    fprintf(stderr, "Error executing %s\n", bin);
  }

  return 0;
}
