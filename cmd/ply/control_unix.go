//go:build unix

package main

import (
	"errors"
	"syscall"
)

// processAlive reports whether a process with this pid exists (one of another user's counts).
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
