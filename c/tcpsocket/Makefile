
CC=gcc
CFLAGS=-g -Wall

all: main

main: main.c
	$(CC) $(CFLAGS) -o $@ $^

run: main
	./$<

.PHONY: clean
clean:
	rm -f main *.o *.so
