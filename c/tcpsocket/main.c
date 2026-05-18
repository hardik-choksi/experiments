#include <arpa/inet.h>
#include <netinet/in.h>
#include <stdio.h>
#include <string.h>
#include <strings.h>
#include <sys/socket.h>
#include <unistd.h>

#define MAXLINE 4096
// todo: add proper error handling
int main(int argc, char const *argv[]) {
  int socketfd;
  int sendbytes;
  struct sockaddr_in serveraddr;
  char sendline[MAXLINE];
  char recvline[MAXLINE];

  if (argc <= 1) {
    printf("usage: %s server_ip_address", argv[0]);
    return 0;
  }

  socketfd = socket(AF_INET, SOCK_STREAM, 0);
  if (socketfd < 0)
    return -1;

  bzero(&serveraddr, sizeof serveraddr);
  serveraddr.sin_family = AF_INET;
  serveraddr.sin_port = htons(80);

  if (inet_pton(AF_INET, argv[1], &serveraddr.sin_addr) < 0)
    return -1;

  if (connect(socketfd, (struct sockaddr *)&serveraddr, sizeof(serveraddr)) < 0)
    return -1;

  sprintf(sendline, "GET / HTTP/1.1\r\n\r\n");
  sendbytes = strlen(sendline);

  if (write(socketfd, sendline, sendbytes) != sendbytes)
    return -1;

  memset(recvline, 0, MAXLINE);

  while (read(socketfd, recvline, MAXLINE - 1) > 0) {
    printf("%s", recvline);
    memset(recvline, 0, MAXLINE);
  }

  return 0;
}
