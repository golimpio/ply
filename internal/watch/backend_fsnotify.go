package watch

import (
	"errors"
	"path/filepath"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// fsBackend adapts fsnotify. It is the backend on Linux and the other
// non-darwin systems; on darwin fsnotify opens a descriptor per file, which a
// big library cannot afford, so tests are the only users there.
type fsBackend struct {
	w    *fsnotify.Watcher
	ev   chan event
	errc chan error
	done chan struct{}
	wg   sync.WaitGroup

	mu      sync.Mutex
	watched map[string]struct{}
}

func newFsnotify() (backend, error) {
	w, err := fsnotify.NewBufferedWatcher(256)
	if err != nil {
		return nil, err
	}
	b := &fsBackend{
		w:       w,
		ev:      make(chan event, 64),
		errc:    make(chan error, 4),
		done:    make(chan struct{}),
		watched: map[string]struct{}{},
	}
	b.wg.Add(1)
	go b.run()
	return b, nil
}

func (b *fsBackend) add(dir string) error {
	if err := b.w.Add(dir); err != nil {
		return err
	}
	b.mu.Lock()
	b.watched[dir] = struct{}{}
	b.mu.Unlock()
	return nil
}

func (b *fsBackend) remove(dir string) error {
	b.mu.Lock()
	delete(b.watched, dir)
	b.mu.Unlock()
	err := b.w.Remove(dir)
	if errors.Is(err, fsnotify.ErrNonExistentWatch) {
		return nil
	}
	return err
}

func (b *fsBackend) events() <-chan event { return b.ev }
func (b *fsBackend) errors() <-chan error { return b.errc }

func (b *fsBackend) close() error {
	select {
	case <-b.done:
		return nil
	default:
		close(b.done)
	}
	err := b.w.Close()
	b.wg.Wait()
	return err
}

func (b *fsBackend) run() {
	defer b.wg.Done()
	defer close(b.ev)
	defer close(b.errc)
	for {
		select {
		case <-b.done:
			return
		case e, ok := <-b.w.Events:
			if !ok {
				return
			}
			for _, ev := range b.translate(e) {
				select {
				case b.ev <- ev:
				case <-b.done:
					return
				}
			}
		case err, ok := <-b.w.Errors:
			if !ok {
				return
			}
			if errors.Is(err, fsnotify.ErrEventOverflow) {
				err = errOverflow
			}
			select {
			case b.errc <- err:
			case <-b.done:
				return
			}
		}
	}
}

// translate maps one fsnotify event to ours. fsnotify names the entry by its
// full path; when that entry is a watched directory that was removed or moved
// away, the watch has died with it, so dirGone is sent as well.
func (b *fsBackend) translate(e fsnotify.Event) []event {
	var o op
	for _, m := range [...]struct {
		from fsnotify.Op
		to   op
	}{
		{fsnotify.Create, opCreate}, {fsnotify.Write, opWrite}, {fsnotify.Remove, opRemove},
		{fsnotify.Rename, opRename}, {fsnotify.Chmod, opChmod},
	} {
		if e.Has(m.from) {
			o |= m.to
		}
	}
	out := []event{{dir: filepath.Dir(e.Name), name: filepath.Base(e.Name), op: o}}
	if e.Has(fsnotify.Remove) || e.Has(fsnotify.Rename) {
		b.mu.Lock()
		_, watched := b.watched[e.Name]
		b.mu.Unlock()
		if watched {
			out = append(out, event{dir: e.Name, op: opDirGone})
		}
	}
	return out
}
