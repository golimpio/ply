//go:build !linux

package proc

import (
	"os/exec"
	"syscall"
)

// dieWithParent does nothing here: only Linux can have the kernel kill a child
// whose parent died. (On macOS ply starts a reaper for each group instead.)
func dieWithParent(*syscall.SysProcAttr) {}

// Start starts cmd and calls exited, on its own goroutine, with the result of
// Wait once it has ended. It returns Start's error.
func Start(cmd *exec.Cmd, exited func(error)) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { exited(cmd.Wait()) }()
	return nil
}
