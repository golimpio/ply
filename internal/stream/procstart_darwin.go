package stream

import (
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

// processStart returns a token that says when process pid started: equal tokens
// for one pid mean one process, so a pid that has been reused is told from the
// process it was. It fails if there is no such process. (sysctl kern.proc.pid
// holds the start time; it does not change when a process execs.)
func processStart(pid int) (string, error) {
	ki, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", err
	}
	if int(ki.Proc.P_pid) != pid {
		return "", syscall.ESRCH
	}
	return fmt.Sprintf("%d.%06d", ki.Proc.P_starttime.Sec, ki.Proc.P_starttime.Usec), nil
}
