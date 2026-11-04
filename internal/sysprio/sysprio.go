// Package sysprio lowers the priority of the child processes that do
// background work (scans, probes, frame grabs, subtitle extraction), so that
// they yield the CPU and the disk to playback and browsing. Playback ffmpeg is
// never demoted.
//
// The child gets a niceness everywhere. On macOS it is also put under the
// Darwin background policy (what `taskpolicy -b` does): the lowest CPU
// priority band, throttled disk and network I/O, and the efficiency cores on
// Apple Silicon. On Linux its I/O goes to the idle class (honoured by the bfq
// scheduler; mq-deadline and network filesystems ignore it).
//
// Errors are ignored: a child that cannot be demoted still does its work.
package sysprio

// Background demotes the running process pid for background work, with
// niceness nice (0 leaves the niceness alone).
func Background(pid, nice int) { background(pid, nice) }

// Foreground undoes Background for pid as far as an unprivileged process
// can: the macOS policy and the Linux I/O class are taken back, a niceness
// is not (only root may lower one). It is for work that becomes urgent while
// it runs, such as extraction for the item that is being played.
func Foreground(pid int) { foreground(pid) }
