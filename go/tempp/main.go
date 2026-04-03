package main

import (
	"log"
	"math/rand"
	"time"
)

func main() {
	rand.Seed(time.Now().UnixNano())

	defer func() {
		if r := recover(); r != nil {
			log.Fatal(r)
		}
	}()

	// MakeRequest()
	// TaskDemo()
	// FileWriteDemo()
	BinaryDemo()
}
