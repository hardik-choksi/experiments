CC      = gcc
CFLAGS  = -Wall -Wextra -O2

memwave: memwave.c
	$(CC) $(CFLAGS) -o $@ $<

.PHONY: run clean
run: memwave
	./memwave

clean:
	rm -f memwave
