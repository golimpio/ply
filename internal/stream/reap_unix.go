//go:build unix

package stream

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// startReaper starts `exe --reap <pgid> <our pid>`: detached (a session of its
// own, no terminal, no standard files, no inherited directory), so that it
// survives ply. The caller kills it when the group ends first.
func startReaper(exe string, pgid int) (*exec.Cmd, error) {
	cmd := exec.Command(exe, "--reap", strconv.Itoa(pgid), strconv.Itoa(os.Getpid()))
	cmd.Dir = "/"
	cmd.Env = []string{}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}
