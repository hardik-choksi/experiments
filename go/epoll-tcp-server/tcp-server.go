package main

import (
	"bytes"
	"fmt"
	"log"
	"net"
	"os"
	"syscall"
)

func main() {
	defer func() {
		if r := recover(); r != nil {
			log.Println("Recovered in main\n", r)
		}
	}()
	max_clients := 511
	// this is where server will receive events (epoller will fill this array)
	events := make([]syscall.EpollEvent, max_clients)
	// create a tcp stream socket on which server can listen for connections
	serverFD, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_NONBLOCK|syscall.SOCK_STREAM, 0)
	if err != nil {
		ePrintf("error creating serer socket: %s\n", err)
		return
	}
	defer syscall.Close(serverFD)

	syscall.SetsockoptInt(serverFD, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)

	ip4 := net.ParseIP("127.0.0.1").To4()
	// bind interface-ip:port with server socket
	if err := syscall.Bind(serverFD, &syscall.SockaddrInet4{
		Port: 8080,
		Addr: [4]byte{ip4[0], ip4[1], ip4[2], ip4[3]},
	}); err != nil {
		ePrintf("error binding server fd to 127.0.0.1:8080: %s\n", err)
		return
	}
	// listen for connections
	if err := syscall.Listen(serverFD, max_clients); err != nil {
		ePrintf("error listening on 127.0.0.1:8080: %s\n", err)
		return
	}
	fmt.Println("tcp server started on 127.0.0.1:8080")

	// create epoller object
	epollerFd, err := syscall.EpollCreate1(0)
	if err != nil {
		ePrintf("error creating epoller: %s\n", err)
		return
	}
	defer syscall.Close(epollerFd)

	// specify which events we wanna get notified about on server socket (incoming connections)
	serverSocketEvent := syscall.EpollEvent{
		Events: syscall.EPOLLIN,
		Fd:     int32(serverFD),
	}

	// add server listen event to epoller, epoller has an internal map
	err = syscall.EpollCtl(epollerFd, syscall.EPOLL_CTL_ADD, serverFD, &serverSocketEvent)
	if err != nil {
		ePrintf("error adding server connection request to epoller: %s\n", err)
		return
	}

	for {
		// wait for events to occur
		n, err := syscall.EpollWait(epollerFd, events, -1)
		if err != nil {
			if err == syscall.EINTR {
				continue
			}
			ePrintf("error getting epoller events: %s\n", err)
			continue
		}

		for i := range n {
			event := events[i]
			eventFd := event.Fd
			if eventFd == int32(serverFD) {
				// means there is an incoming connection request from a client
				// accept it - this is good otherwise accept is an blocking syscall
				clientFd, clientAddr, err := syscall.Accept(serverFD)
				if err != nil {
					ePrintf("error accepting connection from a client: %s\n", err)
					continue
				}

				if err := syscall.SetNonblock(clientFd, true); err != nil {
					ePrintf("error while making clientfd (%d) non-blocking: %s\n", clientFd, err)
				}

				conn := NewConnection(clientFd, clientAddr, ip4, 8080)
				Connections[clientFd] = conn
				fmt.Printf("new connection from %s (fd=%d)\n", conn.RemoteAddr(), clientFd)
				// now we need to add this clientfd into our epoller object so epoller can listen to io events (especially when client sends data)
				clientEvent := syscall.EpollEvent{
					Events: syscall.EPOLLIN,
					Fd:     int32(clientFd),
				}
				if err := syscall.EpollCtl(epollerFd, syscall.EPOLL_CTL_ADD, clientFd, &clientEvent); err != nil {
					ePrintf("error adding client(%d) io event to epoller(%d): %s\n", clientFd, epollerFd, err)
					continue
				}
			} else {
				conn, ok := Connections[int(eventFd)]
				if !ok {
					ePrintf("unknown fd %d in epoll event, skipping\n", eventFd)
					continue
				}
				buf := make([]byte, 1024)
				n, err := conn.Read(buf)
				// n==0: client sent FIN (graceful close). err!=EAGAIN: real error (RST, timeout). EAGAIN: no data yet, skip.
				if n == 0 || (err != nil && err != syscall.EAGAIN) {
					conn.Close()
					syscall.EpollCtl(epollerFd, syscall.EPOLL_CTL_DEL, int(eventFd), nil)
					delete(Connections, int(eventFd))
					continue
				}
				if err == syscall.EAGAIN {
					continue
				}
				// HTTP/1.0 defaults to close, HTTP/1.1 defaults to keep-alive
				keepAlive := bytes.Contains(buf[:n], []byte("HTTP/1.1"))
				if keepAlive {
					conn.Write([]byte("HTTP/1.1 200 OK\r\nConnection: keep-alive\r\nContent-Length: 13\r\n\r\nHello, world!"))
				} else {
					conn.Write([]byte("HTTP/1.0 200 OK\r\nConnection: close\r\nContent-Length: 13\r\n\r\nHello, world!"))
					conn.Close()
					syscall.EpollCtl(epollerFd, syscall.EPOLL_CTL_DEL, int(eventFd), nil)
					delete(Connections, int(eventFd))
				}
			}
		}
	}
}

func ePrintf(f string, a ...any) {
	fmt.Fprintf(os.Stderr, f, a...)
}
