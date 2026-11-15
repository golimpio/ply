//go:build !unix

package main

import "os"

// processAlive reports whether a process with this pid exists: on Windows, FindProcess opens it.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	p.Release()
	return true
}
