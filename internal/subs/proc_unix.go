//go:build unix

package subs

import (
	"syscall"

	"ply/internal/sysprio"
)

// setNice demotes a background ffmpeg (niceness, and the macOS background
// policy: throttled CPU and I/O). nice 0 is work somebody waits for (the
// subtitles of the item being played): it is left alone.
func setNice(pid, nice int) {
	if nice != 0 {
		sysprio.Background(pid, nice)
	}
}

// setForeground takes back the background policy of a conversion that somebody
// has started to wait for (the niceness stays: only root may lower it).
func setForeground(pid int) { sysprio.Foreground(pid) }

// pauseGroup suspends ffmpeg and whatever it started, and resumeGroup lets them go
// on. A suspended group is not safe from the death of ply for free: a running one
// outlives it, and a stopped one is only ended by the kernel in some cases (an orphaned
// group, with a stopped member, gets SIGHUP). What ends them every time is what
// internal/proc starts with them: Pdeathsig on Linux, the reaper on macOS (both end a
// stopped group too).
func pauseGroup(pid int)  { _ = syscall.Kill(-pid, syscall.SIGSTOP) }
func resumeGroup(pid int) { _ = syscall.Kill(-pid, syscall.SIGCONT) }
