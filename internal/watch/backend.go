package watch

import "errors"

// op says what happened. An event may carry several flags.
type op uint8

const (
	opCreate op = 1 << iota
	opWrite
	opRemove
	opRename
	opChmod
	// opDirGone: the watched directory itself was removed, renamed away or
	// revoked. Its watch is dead; the event is the only word on it.
	opDirGone
)

// event is something a backend saw in a watched directory.
type event struct {
	// dir is the watched directory, spelled as it was passed to add. For
	// opDirGone it is the directory that is gone.
	dir string
	// name is the entry of dir that changed. "" means something in dir
	// changed and the backend cannot tell what (kqueue directory watches).
	name string
	op   op
}

// errOverflow is sent on a backend's errors channel when the kernel dropped
// events: anything may have changed.
var errOverflow = errors.New("file event queue overflowed")

// errBackendClosed is what a backend's add answers once it is closed.
var errBackendClosed = errors.New("watch backend closed")

// backend delivers change notifications for a set of directories. Its channels
// are closed after close returns or when it breaks for good.
type backend interface {
	// add watches dir (not its subdirectories). Adding a watched dir again is
	// fine.
	add(dir string) error
	// remove stops watching dir. A directory that is not watched, or whose
	// watch died with it, is not an error worth reporting.
	remove(dir string) error
	events() <-chan event
	errors() <-chan error
	close() error
}

// newBackend makes the backend of the platform: kqueue on directories only
// on darwin, fsnotify elsewhere. Tests replace the Watcher's mkBackend.
var newBackend = defaultBackend
