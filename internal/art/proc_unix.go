//go:build unix

package art

import (
	"os/exec"
	"syscall"

	"ply/internal/sysprio"
)

// ownProcessGroup starts ffmpeg in its own process group and makes context
// cancellation kill the whole group, so no child outlives the request.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}

// setNice demotes a background ffmpeg (niceness, and the macOS background
// policy: throttled CPU and I/O). nice 0 is work somebody waits for (the art of
// the item being opened): it is left alone.
func setNice(pid, nice int) {
	if nice != 0 {
		sysprio.Background(pid, nice)
	}
}
