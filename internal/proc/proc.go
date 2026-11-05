// Package proc starts the programs ply runs for its work (ffmpeg, for playback,
// subtitles and artwork) so that none of them outlives ply: each gets a process
// group of its own, which can be stopped and killed as a whole, and a guard against
// the death of ply itself, whatever kills it (kill -9, the OOM killer).
//
// The guard is the kernel's where there is one (Linux: Pdeathsig, see Start) and a
// reaper otherwise (macOS: StartReaper runs `ply --reap`, a detached process that
// watches ply and kills the group when ply is gone). Both end a group that is
// stopped (SIGSTOP) as much as one that is running. Anywhere else there is only the
// next start's sweep of what stream.Manager recorded.
package proc
