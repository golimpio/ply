//go:build unix

package stream

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ownGroup puts the child in its own process group and makes cancelling the
// command kill the whole group, so no ffmpeg helper can outlive its session.
// Where the kernel can do it (Linux), the child also dies with ply.
func ownGroup(cmd *exec.Cmd) {
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

// signalGroup sends SIGSTOP (stop true) or SIGCONT to the child's group.
func signalGroup(cmd *exec.Cmd, stop bool) error {
	sig := syscall.SIGCONT
	if stop {
		sig = syscall.SIGSTOP
	}
	return syscall.Kill(-cmd.Process.Pid, sig)
}

// killGroup continues, then kills, the process group pgid. It reports whether
// the kill was delivered. It never touches the group of this process (and so
// whatever started it), or one named by this process's own pid: the pgid comes
// from a file, and a wrong one must not take ply down.
func killGroup(pgid int) bool {
	if pgid <= 1 || pgid == syscall.Getpgrp() || pgid == os.Getpid() {
		return false
	}
	_ = syscall.Kill(-pgid, syscall.SIGCONT)
	return syscall.Kill(-pgid, syscall.SIGKILL) == nil
}

// groupOf is the process group of pid, or 0 if there is no such process.
func groupOf(pid int) int {
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		return 0
	}
	return pgid
}

// killStaleGroup kills the process group pgid, left behind by a ply that
// crashed, if its leader still looks like the program that was recorded
// (pids get reused). A stopped group is continued first. It reports whether
// anything was killed.
func killStaleGroup(pgid int, program string) bool {
	if pgid <= 1 {
		return false
	}
	out, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pgid)).Output()
	if err != nil || !strings.Contains(string(out), filepath.Base(program)) {
		return false
	}
	return killGroup(pgid)
}

// ownedByUs reports whether the file belongs to the user running ply.
func ownedByUs(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return !ok || int(st.Uid) == os.Geteuid()
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
