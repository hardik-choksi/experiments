#include <limits.h>
#include <pthread.h>
#include <stdio.h>
#include <unistd.h>

void *myturn(void *arg) {
  for (int i = 0; i < 10; i++) {
    printf("%d: my turn\n", i + 1);
    sleep(1);
  }

  return NULL;
}

void yourturn() {
  for (int i = 0; i < 8; i++) {
    printf("%d: your turn\n", i + 1);
    sleep(1);
  }
}

int main() {
  pthread_t newthread;

  pthread_create(&newthread, NULL, myturn, NULL);
  yourturn();

  int exit_status = INT_MIN;

  pthread_join(newthread, (void *)&exit_status);

  printf("exit status: %d\n", exit_status);

  return 0;
}
