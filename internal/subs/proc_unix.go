//go:build unix

package subs

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup starts ffmpeg in its own process group and makes context
// cancellation kill the whole group, so no child outlives the request.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}

func setNice(pid, nice int) {
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, pid, nice)
}
