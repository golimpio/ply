package watch

import "syscall"

// defaultBackend is kqueue in a watch helper process (helper.go): ply's exit
// then never waits for the kernel to let go of the watches.
func defaultBackend() (backend, error) { return startHelper() }

// localBackend is the backend the watch helper runs.
func localBackend() (backend, error) { return newKqueue() }

// dirLimit is how many directories can be watched: one descriptor each, in the
// helper, which has ply's limit, with some to spare. -1 is no limit.
func dirLimit() int {
	var rl syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rl); err != nil || rl.Cur >= 1<<30 {
		return -1
	}
	return max(int(rl.Cur)-512, 0)
}
