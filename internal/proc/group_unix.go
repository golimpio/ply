//go:build unix

package proc

import (
	"errors"
	"os/exec"
	"syscall"
)

// OwnSession starts the child in a session of its own: no controlling terminal,
// so neither in the terminal's job nor sent its Ctrl-C.
func OwnSession(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

// OwnGroup puts the child in its own process group and makes cancelling the
// command kill the whole group, so no helper it starts can outlive it. Where the
// kernel can do it (Linux), the child also dies with ply (see Start). cmd must have
// been made with exec.CommandContext.
func OwnGroup(cmd *exec.Cmd) {
	attr := &syscall.SysProcAttr{Setpgid: true}
	dieWithParent(attr)
	cmd.SysProcAttr = attr
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
}
