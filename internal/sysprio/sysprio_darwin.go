package sysprio

import "syscall"

// From <sys/resource.h>: setpriority(PRIO_DARWIN_PROCESS, pid, PRIO_DARWIN_BG)
// puts a process under the background policy; 0 takes it out again.
const (
	prioDarwinProcess = 4
	prioDarwinBG      = 0x1000
)

func background(pid, nice int) {
	if nice != 0 {
		_ = syscall.Setpriority(syscall.PRIO_PROCESS, pid, nice)
	}
	_ = syscall.Setpriority(prioDarwinProcess, pid, prioDarwinBG)
}

// foreground lifts the policy; the niceness stays (only root may lower it).
func foreground(pid int) {
	_ = syscall.Setpriority(prioDarwinProcess, pid, 0)
}
