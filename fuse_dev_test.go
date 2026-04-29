package main

import (
	"fmt"
	"os"
	"syscall"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <mount-dir>\n", os.Args[0])
		os.Exit(1)
	}
	mountDir := os.Args[1]
	var st syscall.Stat_t
	if err := syscall.Stat(mountDir, &st); err != nil {
		fmt.Fprintf(os.Stderr, "stat failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Dev=%d (0x%x)\n", st.Dev, st.Dev)
	fmt.Printf("Rdev=%d (0x%x)\n", st.Rdev, st.Rdev)
}
