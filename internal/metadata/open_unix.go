//go:build unix

package metadata

import (
	"os"
	"syscall"
)

// OpenRegular opens a file for reading, refusing anything that is not a
// regular file. It opens without blocking: a FIFO (or a symlink to one) named
// like a poster or an NFO would otherwise hold the caller forever, waiting for a
// writer that never comes.
func OpenRegular(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		f.Close()
		return nil, ErrNotRegular
	}
	return f, nil
}
