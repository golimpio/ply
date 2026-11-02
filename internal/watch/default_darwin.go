package watch

import "syscall"

func defaultBackend() (backend, error) { return newKqueue() }

// dirLimit is how many directories the process can watch: one descriptor
// each, with some left for the database, sockets and ffmpeg. -1 is no limit.
func dirLimit() int {
	var rl syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rl); err != nil || rl.Cur >= 1<<30 {
		return -1
	}
	return max(int(rl.Cur)-512, 0)
}
