//go:build unix

package probe

import "syscall"

func setNice(pid, nice int) {
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, pid, nice)
}
