package main

import (
	"fmt"
	"log"
	"net"
)

const msg_t = 1024

func main() {
	defer func() {
		if r := recover(); r != nil {
			log.Println("Recovered in main", r)
		}
	}()

	listener, err := net.Listen("tcp", ":8080")
	if err != nil {
		panic(err)
	}

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Println("Error accepting connection", err)
			continue
		}
		log.Println("Accepted connection from", conn.RemoteAddr())

		go handle(conn)
	}
}

func handle(conn net.Conn) {
	defer conn.Close()

	in := make([]byte, msg_t)
	out := make([]byte, msg_t)

	_, err := conn.Read(in)
	if err != nil {
		log.Println("Error reading from connection", err)
		return
	}

	fmt.Printf("* [%s]: %s\n", conn.RemoteAddr(), in)

	fmt.Println(">>")

	fmt.Scanln(out)

	_, err = conn.Write(out)
	if err != nil {
		log.Println("Error writing to connection", err)
		return
	}
}
