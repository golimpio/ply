package watch

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"
)

// kqueueBackend watches directories, and only directories, with kqueue. The
// kernel reports that a directory changed but not what changed, so its events
// have no name. fsnotify's kqueue backend would also open a descriptor for
// every file in a watched directory, which a big library cannot afford (the
// default limit is 256, and Go raises it only as far as the hard limit).
type kqueueBackend struct {
	kq           int
	pipeR, pipeW int // self-pipe: a byte on it wakes the loop for close
	ev           chan event
	errc         chan error
	done         chan struct{}
	loopDone     chan struct{}

	mu     sync.Mutex
	closed bool
	byPath map[string]*kdir
	byFd   map[int]*kdir

	closeFd func(fd int) error // syscall.Close, for the watched directories in close
}

type kdir struct {
	path string
	fd   int
	dev  int32
	ino  uint64
}

const (
	// openFlags: O_EVTONLY opens for event notification only (no read access,
	// does not block unmounting).
	openFlags = syscall.O_RDONLY | syscall.O_EVTONLY | syscall.O_CLOEXEC | syscall.O_DIRECTORY
	noteAny   = syscall.NOTE_WRITE | syscall.NOTE_DELETE | syscall.NOTE_RENAME | syscall.NOTE_REVOKE | syscall.NOTE_EXTEND
	noteGone  = syscall.NOTE_DELETE | syscall.NOTE_RENAME | syscall.NOTE_REVOKE

	// closeConcurrency is how many watched directories close closes at once.
	// Closing one on a network share waits for the server to drop its change
	// notification: on an SMB NAS (measured 2026-11-11) 200 took 13 s one by one
	// and 0.6 s 32 at a time. One by one, a library of 700 folders held up
	// ply's shutdown past its deadline, and then its exit: the kernel closes what
	// is left the same way, with the terminal waiting.
	closeConcurrency = 32
)

func newKqueue() (backend, error) {
	b, err := openKqueue()
	if err != nil {
		return nil, err
	}
	go b.loop()
	return b, nil
}

// openKqueue makes the backend without starting its loop (tests start it).
func openKqueue() (*kqueueBackend, error) {
	kq, err := syscall.Kqueue()
	if err != nil {
		return nil, fmt.Errorf("kqueue: %w", err)
	}
	syscall.CloseOnExec(kq)

	var p [2]int
	syscall.ForkLock.RLock()
	err = syscall.Pipe(p[:])
	if err == nil {
		syscall.CloseOnExec(p[0])
		syscall.CloseOnExec(p[1])
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		syscall.Close(kq)
		return nil, fmt.Errorf("kqueue wake pipe: %w", err)
	}
	b := &kqueueBackend{
		kq: kq, pipeR: p[0], pipeW: p[1],
		ev:       make(chan event, 256),
		errc:     make(chan error, 4),
		done:     make(chan struct{}),
		loopDone: make(chan struct{}),
		byPath:   map[string]*kdir{},
		byFd:     map[int]*kdir{},
		closeFd:  syscall.Close,
	}
	reg := syscall.Kevent_t{Ident: uint64(p[0]), Filter: syscall.EVFILT_READ, Flags: syscall.EV_ADD}
	err = errors.Join(syscall.SetNonblock(p[0], true), syscall.SetNonblock(p[1], true))
	if err == nil {
		_, err = syscall.Kevent(kq, []syscall.Kevent_t{reg}, nil, nil)
	}
	if err != nil {
		b.closeFds()
		return nil, fmt.Errorf("kqueue: %w", err)
	}
	return b, nil
}

func (b *kqueueBackend) closeFds() {
	syscall.Close(b.pipeR)
	syscall.Close(b.pipeW)
	syscall.Close(b.kq)
}

func (b *kqueueBackend) add(dir string) error {
	var fd int
	for {
		var err error
		fd, err = syscall.Open(dir, openFlags, 0)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return &os.PathError{Op: "open", Path: dir, Err: err}
		}
		break
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		syscall.Close(fd)
		return &os.PathError{Op: "fstat", Path: dir, Err: err}
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		syscall.Close(fd)
		return errBackendClosed
	}
	if old := b.byPath[dir]; old != nil {
		if old.dev == st.Dev && old.ino == st.Ino {
			syscall.Close(fd)
			return nil
		}
		b.forget(old) // the path now names another directory
	}
	reg := syscall.Kevent_t{
		Ident: uint64(fd), Filter: syscall.EVFILT_VNODE,
		Flags: syscall.EV_ADD | syscall.EV_CLEAR, Fflags: noteAny,
	}
	if _, err := syscall.Kevent(b.kq, []syscall.Kevent_t{reg}, nil, nil); err != nil {
		syscall.Close(fd)
		return fmt.Errorf("kevent %s: %w", dir, err)
	}
	d := &kdir{path: dir, fd: fd, dev: st.Dev, ino: st.Ino}
	b.byPath[dir], b.byFd[fd] = d, d
	return nil
}

func (b *kqueueBackend) remove(dir string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if d := b.byPath[dir]; d != nil {
		b.forget(d)
	}
	return nil
}

// forget drops d and closes its descriptor, which also removes its kevent.
// b.mu must be held.
func (b *kqueueBackend) forget(d *kdir) {
	delete(b.byFd, d.fd)
	if b.byPath[d.path] == d {
		delete(b.byPath, d.path)
	}
	syscall.Close(d.fd)
}

// nameless: kqueue directory events say that a directory changed, not what.
func (b *kqueueBackend) nameless() bool { return true }

func (b *kqueueBackend) events() <-chan event { return b.ev }
func (b *kqueueBackend) errors() <-chan error { return b.errc }

func (b *kqueueBackend) close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	close(b.done)
	b.mu.Unlock()

	syscall.Write(b.pipeW, []byte{0}) // wake the loop; a full pipe already has
	<-b.loopDone

	b.mu.Lock()
	fds := make([]int, 0, len(b.byFd))
	for fd := range b.byFd {
		fds = append(fds, fd)
	}
	clear(b.byFd)
	clear(b.byPath)
	b.mu.Unlock()

	parallel(len(fds), closeConcurrency, nil, func(k int) { b.closeFd(fds[k]) })
	b.closeFds()
	return nil
}

func (b *kqueueBackend) loop() {
	defer close(b.loopDone)
	defer close(b.ev)
	defer close(b.errc)
	buf := make([]syscall.Kevent_t, 128)
	for {
		n, err := syscall.Kevent(b.kq, nil, buf, nil)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			select {
			case b.errc <- fmt.Errorf("kevent: %w", err):
			case <-b.done:
			}
			return
		}
		for _, k := range buf[:n] {
			if int(k.Ident) == b.pipeR {
				var drain [16]byte
				syscall.Read(b.pipeR, drain[:])
				continue
			}
			b.dispatch(k)
		}
		select {
		case <-b.done:
			return
		default:
		}
	}
}

func (b *kqueueBackend) dispatch(k syscall.Kevent_t) {
	b.mu.Lock()
	d := b.byFd[int(k.Ident)]
	if d == nil {
		b.mu.Unlock()
		return // removed while the event was on its way
	}
	var ev event
	switch {
	case k.Fflags&noteGone != 0:
		// The vnode is deleted, renamed or revoked: the watch is dead.
		// Closing the descriptor now keeps the table tidy even if nobody
		// calls remove.
		b.forget(d)
		ev = event{dir: d.path, op: opDirGone}
	case k.Fflags&(syscall.NOTE_WRITE|syscall.NOTE_EXTEND) != 0:
		ev = event{dir: d.path, op: opWrite}
	default:
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()
	select {
	case b.ev <- ev:
	case <-b.done:
	}
}
