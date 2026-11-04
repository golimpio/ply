package stream

import (
	"errors"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// reaperWanted says that a ply on this system starts a reaper for each ffmpeg:
// macOS has no parent-death signal.
const reaperWanted = true

// reapPoll is how often the reaper looks for its group when nothing happens.
const reapPoll = 3 * time.Second

// ReapMain is `ply --reap <pgid> <ppid>`, a hidden mode of the same binary that
// ply starts, detached and in a session of its own, for every ffmpeg process
// group it makes. It returns the exit status.
//
// A process that dies leaves no way to tell its children, and ffmpeg has a group
// of its own (so that the whole group can be stopped and killed), which launchd
// does not reach either. The reaper outlives ply for exactly that moment: it
// watches ply's pid, and when ply is gone it kills the group, stopped or not.
func ReapMain(args []string) int {
	if len(args) != 2 {
		return 2
	}
	pgid, err1 := strconv.Atoi(args[0])
	ppid, err2 := strconv.Atoi(args[1])
	if err1 != nil || err2 != nil || pgid <= 1 || ppid <= 1 {
		return 2
	}
	signal.Ignore(syscall.SIGHUP, syscall.SIGPIPE)
	if err := runReaper(pgid, ppid, os.Getppid()); err != nil {
		return 1
	}
	return 0
}

// runReaper is reap, but for a reaper that finds ply already gone: it is started
// by ply, so if its parent (given as parent) is not ply any more it was adopted
// when ply died before it was running, and there is nobody to wait for.
func runReaper(pgid, ppid, parent int) error {
	if parent == ppid {
		return reap(pgid, ppid)
	}
	start, err := processStart(pgid)
	if err != nil || groupOf(pgid) != pgid {
		return nil // the group is gone too
	}
	return killIfSame(pgid, start)
}

// reap waits until process ppid exits and then kills the process group pgid. It
// returns, without killing anything, as soon as the group is gone: its leader
// has exited, or kill(-pgid, 0) says there is nobody in it.
func reap(pgid, ppid int) error {
	// The group is only ours to kill while its leader is the process ply started
	// a moment ago: a pid that has been reused is somebody else.
	start, err := processStart(pgid)
	if err != nil || groupOf(pgid) != pgid {
		return nil // already gone
	}
	kq, err := unix.Kqueue()
	if err != nil {
		return err
	}
	defer unix.Close(kq)
	watch := func(pid int) error {
		ev := unix.Kevent_t{Ident: uint64(pid), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ONESHOT, Fflags: unix.NOTE_EXIT}
		_, err := unix.Kevent(kq, []unix.Kevent_t{ev}, nil, nil)
		return err
	}
	if err := watch(pgid); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil // the leader exited between the two calls
		}
		return err
	}
	if err := watch(ppid); err != nil {
		if !errors.Is(err, syscall.ESRCH) {
			return err
		}
		return killIfSame(pgid, start) // ply is gone already
	}

	var events [2]unix.Kevent_t
	for {
		ts := unix.NsecToTimespec(int64(reapPoll))
		n, err := unix.Kevent(kq, nil, events[:], &ts)
		switch {
		case errors.Is(err, syscall.EINTR):
			continue
		case err != nil:
			return err
		case n == 0: // nothing happened: is there still a group to guard?
			if errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH) {
				return nil
			}
			continue
		}
		for _, ev := range events[:n] {
			switch int(ev.Ident) {
			case ppid:
				return killIfSame(pgid, start)
			case pgid:
				return nil // ffmpeg ended by itself or was stopped by ply
			}
		}
	}
}

// killIfSame kills the group if its leader is still the process that started at
// start.
func killIfSame(pgid int, start string) error {
	if got, err := processStart(pgid); err != nil || got != start || groupOf(pgid) != pgid {
		return nil
	}
	if !killGroup(pgid) {
		return os.ErrProcessDone
	}
	return nil
}
