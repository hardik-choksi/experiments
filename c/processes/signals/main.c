// btw you can make your own user defined signals
#include<stdio.h>
#include <signal.h>
#include <unistd.h>

void handler(int n) {
  printf("I will not die(%d)\n", n);
}

int main() {
  signal(SIGINT, handler);

  while(1) {
    printf("[%d]: hehe, wasting your cycles...\n", getpid());
    sleep(1);
  }

  return 0;
}
