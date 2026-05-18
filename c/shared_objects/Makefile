CC=gcc
CARGS=-Wall

all: sharedlibraryuse

sharedlibraryuse: sharedlibraryuse.c
	$(CC) $(CARGS) $< -lm -save-temps -o $@

static: sharedlibraryuse.c
	$(CC) $(CARGS) $< -lm -save-temps -static -o $@

.PHONY: clean

clean:
	ls | grep -Ev "sharedlibraryuse.c|Makefile" | xargs rm -f
