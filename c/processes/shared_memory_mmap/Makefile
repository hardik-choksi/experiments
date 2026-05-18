CC=gcc
CFLAGS=-g -Wall

all: run

sharedmemory: shared_memory.c
	$(CC) $(CFLAGS) $< -o $@

run: sharedmemory
	./$<

.PHONY: clean

clean:
	rm sharedmemory
