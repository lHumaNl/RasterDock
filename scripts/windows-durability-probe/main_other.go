//go:build !windows

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "Windows API diagnostic requires Windows; no observations collected")
	os.Exit(2)
}
