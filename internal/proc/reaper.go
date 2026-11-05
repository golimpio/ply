package proc

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
)

// StopReaper ends a reaper that StartReaper made, and waits for it: the group it
// guarded is gone, so ply has nothing more to be protected from. nil is fine.
func StopReaper(rp *exec.Cmd) {
	if rp == nil || rp.Process == nil {
		return
	}
	_ = rp.Process.Kill()
	_ = rp.Wait()
}

// ReaperLink makes <dir>/ply-reaper, a symbolic link to the program that is the
// reaper (ply itself), and returns it: the process is then named ply-reaper, and
// `killall ply` (or a script that kills every ply it finds) does not take the
// reapers down with the server they are there to outlive. Where it cannot be made,
// or there is no directory to make it in, the program is run as it is.
func ReaperLink(dir, program string, log *slog.Logger) string {
	if dir == "" {
		return program
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	link, tmp := filepath.Join(dir, "ply-reaper"), filepath.Join(dir, fmt.Sprintf(".ply-reaper-%d", os.Getpid()))
	_ = os.Remove(tmp)
	if err := os.Symlink(program, tmp); err != nil {
		log.Warn("cannot link the reaper; it will run under the name of the server", "err", err)
		return program
	}
	if err := os.Rename(tmp, link); err != nil { // replaces a link an earlier start made, atomically
		_ = os.Remove(tmp)
		log.Warn("cannot link the reaper; it will run under the name of the server", "err", err)
		return program
	}
	return link
}
