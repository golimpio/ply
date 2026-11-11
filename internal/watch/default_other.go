//go:build !darwin

package watch

// defaultBackend is in-process: inotify lets go of its watches at once.
func defaultBackend() (backend, error) { return newFsnotify() }

// localBackend is the backend a watch helper runs (helper.go), if one is started.
func localBackend() (backend, error) { return newFsnotify() }

// dirLimit is how many directories can be watched; -1 is no limit (inotify
// refuses further watches with ENOSPC instead).
func dirLimit() int { return -1 }
