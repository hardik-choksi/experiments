package main

import (
	"sync"
	"time"
)

func main() {
	var wg sync.WaitGroup
	wg.Add(10)

	for range 10 {
		go work(&wg)
	}

	wg.Wait()

	time.Sleep(3 * time.Second)
}

func work(wg *sync.WaitGroup) {
	time.Sleep(1 * time.Second)

	var count int
	for range int(1e10) {
		count++
	}

	wg.Done()
}
