package sysprio

import "syscall"

// ioprio_set(2): IOPRIO_WHO_PROCESS, and the class in the top bits.
const (
	ioprioWhoProcess = 1
	ioprioClassShift = 13
	ioprioClassBE    = 2
	ioprioClassIdle  = 3
)

func setIOPrio(pid, class, level int) {
	_, _, _ = syscall.Syscall(syscall.SYS_IOPRIO_SET, ioprioWhoProcess, uintptr(pid),
		uintptr(class<<ioprioClassShift|level))
}

func background(pid, nice int) {
	if nice != 0 {
		_ = syscall.Setpriority(syscall.PRIO_PROCESS, pid, nice)
	}
	setIOPrio(pid, ioprioClassIdle, 0)
}

func foreground(pid int) {
	setIOPrio(pid, ioprioClassBE, 4) // the default best-effort level; the niceness stays
}
