package main

import (
	"fmt"
	"net"
)

type TCPConn struct {
	conn net.Conn
}

func NewTCPConnection(conn net.Conn) *TCPConn {
	return &TCPConn{conn: conn}
}

func (t *TCPConn) Do() {
	defer t.conn.Close()
	// Handle the connection (e.g., read/write data)
	fmt.Printf("Handling connection from %s\n", t.conn.RemoteAddr())
	// setup http 1.1 headers
	t.conn.Write([]byte("HTTP/1.1 200 OK\r\n"))
	t.conn.Write([]byte("Content-Type: text/plain\r\n"))
	t.conn.Write([]byte("Content-Length: 13\r\n"))
	t.conn.Write([]byte("\r\n"))
	t.conn.Write([]byte("Hello World!"))
}
