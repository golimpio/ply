package proc

import (
	"sync"
	"time"
)

// ExitGrace is how long a Close that has killed its ffmpeg runs waits for them to be
// seen exiting. A process that is reading from a share that is slow or gone does not
// die until the read returns (measured: 18 s), and shutting down must not wait for
// that. What guards the group meanwhile (Pdeathsig, the reaper) is released only when
// the process has exited, and ends it when ply is gone.
const ExitGrace = 2 * time.Second

// Await waits for wg, but not longer than grace. It reports whether wg was done.
func Await(wg *sync.WaitGroup, grace time.Duration) bool {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	t := time.NewTimer(grace)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}
