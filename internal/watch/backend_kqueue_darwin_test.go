package watch

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestKqueue(t *testing.T) backend {
	t.Helper()
	b, err := newKqueue()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.close() })
	return b
}

// next returns the next event, failing the test after a while.
func next(t *testing.T, b backend) event {
	t.Helper()
	select {
	case ev, ok := <-b.events():
		if !ok {
			t.Fatal("events channel closed")
		}
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
	}
	return event{}
}

// expect reads events until all of want have come. Repeated writes are
// ignored: the kernel may report one change more than once.
func expect(t *testing.T, b backend, want ...event) {
	t.Helper()
	missing := map[event]bool{}
	for _, ev := range want {
		missing[ev] = true
	}
	for len(missing) > 0 {
		ev := next(t, b)
		if !missing[ev] && ev.op != opWrite {
			t.Fatalf("unexpected event %+v, waiting for %v", ev, missing)
		}
		delete(missing, ev)
	}
}

// noEvent fails if an event arrives within a short while.
func noEvent(t *testing.T, b backend) {
	t.Helper()
	select {
	case ev := <-b.events():
		t.Fatalf("unexpected event %+v", ev)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestKqueueWatchesDirectoriesNotFiles(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"a", "b", "c"} {
		for i := range 60 {
			touch(t, filepath.Join(root, d, "f"+string(rune('A'+i%26))+string(rune('a'+i/26))+".mkv"), 1)
		}
	}
	before := openFDs(t)
	b := newTestKqueue(t)
	during := openFDs(t)
	for _, d := range []string{"a", "b", "c"} {
		if err := b.add(filepath.Join(root, d)); err != nil {
			t.Fatal(err)
		}
	}
	// Three directories with 180 files between them cost three descriptors
	// (the kqueue and its wake pipe were opened by newKqueue, counted before).
	if got := openFDs(t) - during; got != 3 {
		t.Fatalf("watching 3 directories took %d descriptors, want 3", got)
	}
	// Positive control for the count: adding one takes exactly one more.
	if err := b.add(root); err != nil {
		t.Fatal(err)
	}
	if got := openFDs(t) - during; got != 4 {
		t.Fatalf("4 directories took %d descriptors", got)
	}
	// The same directory again takes none.
	if err := b.add(root); err != nil {
		t.Fatal(err)
	}
	if got := openFDs(t) - during; got != 4 {
		t.Fatalf("adding a watched directory again left %d descriptors, want 4", got)
	}

	b.remove(filepath.Join(root, "a"))
	if got := openFDs(t) - during; got != 3 {
		t.Fatalf("after remove: %d descriptors, want 3", got)
	}
	b.remove(filepath.Join(root, "a")) // not watched: fine
	b.remove(filepath.Join(root, "nowhere"))

	b.close()
	if got := openFDs(t); got != before {
		t.Fatalf("after close: %d descriptors, %d before the backend", got, before)
	}
	b.close() // twice is fine
}

func TestKqueueEvents(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "d", "gone", "moved")
	d, gone, moved := filepath.Join(root, "d"), filepath.Join(root, "gone"), filepath.Join(root, "moved")
	b := newTestKqueue(t)
	for _, dir := range []string{root, d, gone, moved} {
		if err := b.add(dir); err != nil {
			t.Fatal(err)
		}
	}

	// A file made in d: something changed in d, and the event has no name.
	touch(t, filepath.Join(d, "a.mkv"), 1)
	expect(t, b, event{dir: d, op: opWrite})
	// A directory removed: it is gone, and its parent changed.
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	expect(t, b, event{dir: gone, op: opDirGone}, event{dir: root, op: opWrite})
	// Renamed away.
	if err := os.Rename(moved, filepath.Join(root, "elsewhere")); err != nil {
		t.Fatal(err)
	}
	expect(t, b, event{dir: moved, op: opDirGone}, event{dir: root, op: opWrite})
	// A gone directory's descriptor was closed by the backend itself: a new
	// directory at the same path is a new watch, and it works.
	mkdirs(t, root, "gone")
	expect(t, b, event{dir: root, op: opWrite})
	if err := b.add(gone); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(gone, "x.mkv"), 1)
	expect(t, b, event{dir: gone, op: opWrite})
	// Removed watches are quiet (after letting repeats of earlier writes pass).
	b.remove(d)
	time.Sleep(100 * time.Millisecond)
	for len(b.events()) > 0 {
		<-b.events()
	}
	touch(t, filepath.Join(d, "b.mkv"), 1)
	noEvent(t, b)
}

func TestKqueueReplacedDirectoryGetsANewWatch(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "d")
	d := filepath.Join(root, "d")
	b := newTestKqueue(t)
	if err := b.add(d); err != nil {
		t.Fatal(err)
	}
	before := openFDs(t)
	// Another directory takes the name while the old one is kept alive.
	if err := os.Rename(d, d+".old"); err != nil {
		t.Fatal(err)
	}
	mkdirs(t, root, "d")
	expect(t, b, event{dir: d, op: opDirGone})
	if err := b.add(d); err != nil {
		t.Fatal(err)
	}
	if got := openFDs(t); got != before {
		t.Fatalf("%d descriptors, want %d: the dead watch was not replaced", got, before)
	}
	touch(t, filepath.Join(d, "x.mkv"), 1)
	expect(t, b, event{dir: d, op: opWrite})
}

func TestKqueueAddErrors(t *testing.T) {
	root := t.TempDir()
	b := newTestKqueue(t)
	touch(t, filepath.Join(root, "file"), 1)
	if err := b.add(filepath.Join(root, "missing")); !os.IsNotExist(err) {
		t.Fatalf("missing directory: %v", err)
	}
	if err := b.add(filepath.Join(root, "file")); err == nil {
		t.Fatal("a file was watched")
	}
	b.close()
	if err := b.add(root); err == nil {
		t.Fatal("added after close")
	}
	if _, ok := <-b.events(); ok {
		t.Fatal("events channel not closed")
	}
}
