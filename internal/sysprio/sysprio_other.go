//go:build unix && !darwin && !linux

package sysprio

import "syscall"

func background(pid, nice int) {
	if nice != 0 {
		_ = syscall.Setpriority(syscall.PRIO_PROCESS, pid, nice)
	}
}

func foreground(int) {} // only root may lower a niceness
