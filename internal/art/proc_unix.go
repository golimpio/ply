//go:build unix

package art

import "ply/internal/sysprio"

// setNice demotes a background ffmpeg (niceness, and the macOS background
// policy: throttled CPU and I/O). nice 0 is work somebody waits for (the art of
// the item being opened): it is left alone.
func setNice(pid, nice int) {
	if nice != 0 {
		sysprio.Background(pid, nice)
	}
}
