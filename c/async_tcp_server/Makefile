CC      ?= gcc
CFLAGS  ?= -std=c11 -Wall -Wextra -Wpedantic -O2 -D_GNU_SOURCE
OBJS     = server.o connection.o

all: run

server: $(OBJS)
	$(CC) $(CFLAGS) -o $@ $(OBJS)

%.o: %.c connection.h
	$(CC) $(CFLAGS) -c -o $@ $<

run: server
	./$<

debug: CFLAGS = -std=c11 -Wall -Wextra -Wpedantic -O0 -g -fsanitize=address,undefined -D_GNU_SOURCE
debug: clean server
	./server

.PHONY: all run debug clean

clean:
	rm -f server $(OBJS)
