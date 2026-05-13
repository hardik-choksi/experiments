package main

import "fmt"

func main() {
	processes := ScanProcesses()
	for _, process := range processes {
		fmt.Println("--------------------------------")
		fmt.Println(process.PID, process.ExeName)
		for _, port := range process.Ports {
			fmt.Println("  ", port.String())
		}
		fmt.Println("--------------------------------")
	}
}
