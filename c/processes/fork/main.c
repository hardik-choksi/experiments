#include <stdio.h>
#include <stdlib.h>
#include <sys/wait.h>
#include <unistd.h>

int main(int argc, char *argv[]) {
  int children = 5;
  int isParent = 1;
  for (int i = 0; i < children; i++) {
    pid_t pid = fork();
    if (pid == 0) {
      isParent = 0;
      // Child process
      int my_pid = getpid();
      printf("Child(%d) Process executing\n", my_pid);
      sleep(10);
      printf("Child(%d) done\n", my_pid);
      exit(0);
    }
    // else if (pid > 0) {
    //   // Parent Process
    //   int ppid = getpid();
    //   printf("Parent(%d) process waiting for child(%d) \n", ppid, pid);
    //   int status;
    //   int c_pid = wait(&status);
    //   printf("Child process terminated,PID: %d, status: %d\n", c_pid, status);
    // } else {
    //   // fork faiiled
    //   perror("Fork Failed");
    //   return 1;
    // }
    //
  }
  if (isParent) {
    printf("Baap(%d) is waiting\n", getpid());
    for (int i = 0; i < children; i++) {
      int status;
      int cpid = wait(&status);
      printf("Child(%d) completed.\n", cpid);
    }
  }

  return 0;
}
