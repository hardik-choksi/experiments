package main

import (
	"fmt"
	"net"
)

func main() {
	workerPool := NewWorkerPool(3)

	listener, err := net.Listen("tcp", ":9999")
	if err != nil {
		fmt.Println("Error starting server:", err)
		return
	}
	defer listener.Close()
	fmt.Println("Server is listening on port 9999...")

	for {
		conn, err := listener.Accept()
		if err != nil {
			fmt.Println("Error accepting connection:", err)
			continue
		}
		fmt.Println("New client connected:", conn.RemoteAddr())
		tcpConn := NewTCPConnection(conn)
		workerPool.AddJob(tcpConn)
	}
}
