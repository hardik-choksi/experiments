package main

import "fmt"

func receiver(ch <-chan int) {
	for {
		msg, ok := <-ch
		if !ok {
			break
		}
		fmt.Printf("receiver: %d\n", msg)
	}
}

func sender(ch chan<- int) {
	for i := range 10 {
		ch <- (i + 1)
	}
	close(ch)
}

func main() {
	ch := make(chan int)
	go sender(ch)
	receiver(ch)
}
