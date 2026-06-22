
all: run

server:
	go build -o $@ .

run: server
	./$<

debug: server
	GOMAXPROCS=4 GODEBUG=schedtrace=1000 ./$<

.PHONY: clean

clean:
	rm server
