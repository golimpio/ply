package stream

import (
	"os/exec"
	"runtime"
	"syscall"
)

// dieWithParent asks the kernel to SIGKILL the child when its parent dies, so a
// ply that is killed (OOM, kill -9) leaves no ffmpeg behind, whoever restarts
// it and wherever.
func dieWithParent(attr *syscall.SysProcAttr) { attr.Pdeathsig = syscall.SIGKILL }

// startProcess starts cmd and calls exited, on its own goroutine, with the
// result of Wait once it has ended. It returns Start's error.
//
// Pdeathsig is tied to the thread that forked the child, not to the process:
// the signal is sent when that thread exits, and Go moves goroutines between
// threads and may retire the threads it has no use for. So the fork and the Wait
// are done by one goroutine that stays on its thread. (Today's runtime would
// mostly keep that thread anyway, blocked in the wait for the child; no test can
// tell the lock from that, as the mutant that removes it shows. The lock makes it
// a guarantee, not a property of the scheduler.) It is never unlocked: the
// thread ends with the goroutine, after the child.
func startProcess(cmd *exec.Cmd, exited func(error)) error {
	started := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		err := cmd.Start()
		started <- err
		if err == nil {
			exited(cmd.Wait())
		}
	}()
	return <-started
}
