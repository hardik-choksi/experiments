
CC=gcc
CFLAGS=-Wall -g
BINS=libtest libstr.so runtime_libtest static_libtest
EXTRA_BINS=libstaticstr.a libstr.h.gch
all: $(BINS)

%.o: %.c %.h
	$(CC) $(CFLAGS) -c $^

libstr.so: libstr.h libstr.c
	$(CC) $(CFLAGS) -fPIC -shared -o $@ libstr.c -lc

libtest: libtest.c libstr.o
	$(CC) $(CFLAGS) -o $@ $^

runtime_libtest: libtest.c
	$(CC) $(CFLAGS) -o $@ $^ -L. -lstr

libstaticstr.a: libstr.o
	ar rcs libstaticstr.a libstr.o

static_libtest: libtest.c
	$(CC) $(CFLAGS) -o $@ $^ -L. -lstaticstr

clean:
	rm *.o $(BINS) $(EXTRA_BINS)
