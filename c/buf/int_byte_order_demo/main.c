#include <stdio.h>
#include <arpa/inet.h>

void foo() {
  // 0x101
  short num = 5;
  short n = 0b101;
  short x = 0xF5;
  printf("%d\n",n << 1);
  printf("%d\n", x);
  //
}

int main() {
    short n = 1; // Host short integer
    unsigned short network_order = htons(n); // Convert to network byte order

    printf("Host value: %d\n", n);            // Display the original value
    printf("Network byte order: %d\n", (int)network_order); // Display the converted value

    foo();

    return 0;
}


/*
 * 10 = 2 : 1 * 2^1 + 0 * 2^0 = 2
 * 110 = 6 : 1 * 2^2 + 1 * 2^1 + 0 * 2^0 = 6
 * 4 2 1
 */
