CC=gcc
CFLAGS=-Wall

all: liblevomaro.so driver

shared.o: shared.c levomaro.h
	$(CC) $(CFLAGS) -fPIC -c $< -o $@

liblevomaro.so: shared.o
	$(CC) $(CFLAGS) -shared $< -o $@

driver: driver.c liblevomaro.so
	$(CC) $(CFLAGS) -L. -Wl,-rpath,. -o $@ $< -llevomaro

.PHONY: all clean
clean:
	rm -f *.o *.so driver
