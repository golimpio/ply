package subs

import (
	"os"

	"golang.org/x/sys/unix"
)

// readAheadOff turns off the kernel's reading ahead on f (fcntl F_RDAHEAD), which
// belongs to the open file and ends with it.
func readAheadOff(f *os.File) error {
	_, err := unix.FcntlInt(f.Fd(), unix.F_RDAHEAD, 0)
	return err
}
