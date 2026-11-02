//go:build !darwin

package watch

func defaultBackend() (backend, error) { return newFsnotify() }

// dirLimit is how many directories can be watched; -1 is no limit (inotify
// refuses further watches with ENOSPC instead).
func dirLimit() int { return -1 }
