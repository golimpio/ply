//go:build unix

package library

import (
	"os"
	"syscall"
)

// deviceOf returns the device of the directory at path, following symlinks (a
// root may be one; the directories a walk meets never are).
func deviceOf(path string) (uint64, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Dev), true
}
