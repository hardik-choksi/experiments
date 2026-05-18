CC=gcc
CFLAGS=-g -Wall

all: main name_pipe

main: main.c
	$(CC) $(CFLAGS) $< -o $@

name_pipe: named_pipe.c
	$(CC) $(CFLAGS) $< -o $@

run: main
	./$<

namedpipe: name_pipe
	./$<

.PHONY: clean

clean:
	rm main name_pipe
