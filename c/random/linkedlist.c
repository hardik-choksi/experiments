#include<stdio.h>
#include <stdlib.h>

struct node {
  int x; // 8 bytes
  struct node* next; // 8 bytes
};

struct node* newNode(int size) {
  void *ptr = malloc(size);
}

int main() {
  int arr[] = {1,2,3,4,5,6};
  int size = sizeof(struct node);
  printf("node size: %d\n", size);
  void *ptr = malloc(size);
  printf("new created node address: %p\n", ptr);


  free(ptr);
  return 0;
} // the cherno c++
