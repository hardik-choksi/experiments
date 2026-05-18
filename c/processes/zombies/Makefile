
CC=gcc
CFLAGS=-g -Wall

all: main

main: main.c
	$(CC) $(CFLAGS) $< -o $@

run: main
	./main

.PHONY: clean

clean:
	rm main
