package main

import "time"

func main() {
	for i := 0; ; i++ {
		time.Sleep(10 * time.Millisecond) // breakpoint
	}
}
