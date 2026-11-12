//go:build unix

package proc

import (
	"os"
	"os/exec"
	"strconv"
)

// StartReaper starts `exe --reap <pgid> <our pid>`: detached (a session of its
// own, no terminal, no standard files, no inherited directory), so that it
// survives ply. When ply is gone it kills the process group pgid, stopped or not
// (stream.ReapMain is what exe runs). The caller ends it with StopReaper once the
// group has ended first.
func StartReaper(exe string, pgid int) (*exec.Cmd, error) {
	cmd := exec.Command(exe, "--reap", strconv.Itoa(pgid), strconv.Itoa(os.Getpid()))
	cmd.Dir = "/"
	cmd.Env = []string{}
	OwnSession(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}
