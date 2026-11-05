//go:build unix && !darwin

package tier

import (
	"fmt"
	"syscall"
)

// diskOf names the disk path is on by its device (st_dev).
func diskOf(path string) (string, bool) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return "", false
	}
	return fmt.Sprintf("dev:%d", st.Dev), true
}
