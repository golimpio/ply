//go:build unix

package stream

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"
)

// ownGroup puts the child in its own process group and makes cancelling the
// command kill the whole group, so no ffmpeg helper can outlive its session.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
}

// signalGroup sends SIGSTOP (stop true) or SIGCONT to the child's group.
func signalGroup(cmd *exec.Cmd, stop bool) error {
	sig := syscall.SIGCONT
	if stop {
		sig = syscall.SIGSTOP
	}
	return syscall.Kill(-cmd.Process.Pid, sig)
}

func setNice(pid, nice int) { _ = syscall.Setpriority(syscall.PRIO_PROCESS, pid, nice) }

// processAlive reports whether a process with this pid exists.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// usage returns the CPU time and peak resident set size (bytes) of a finished
// child.
func usage(ps *os.ProcessState) (cpu time.Duration, maxRSS int64) {
	if ps == nil {
		return 0, 0
	}
	ru, ok := ps.SysUsage().(*syscall.Rusage)
	if !ok {
		return 0, 0
	}
	maxRSS = int64(ru.Maxrss)
	if runtime.GOOS != "darwin" {
		maxRSS *= 1024 // kilobytes on Linux
	}
	return ps.UserTime() + ps.SystemTime(), maxRSS
}
