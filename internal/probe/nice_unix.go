//go:build unix

package probe

import "ply/internal/sysprio"

// setNice demotes a background ffprobe (niceness, and the macOS background
// policy: throttled CPU and I/O). nice 0 is left alone.
func setNice(pid, nice int) {
	if nice != 0 {
		sysprio.Background(pid, nice)
	}
}
