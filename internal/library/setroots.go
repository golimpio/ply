package library

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"ply/internal/store"
)

// freshScanRetries is how many times the scan of a folder that was just added
// looks again, freshRetryPad after the settle time, at the directories that held
// files still being written.
var (
	freshScanRetries = 5
	freshRetryPad    = time.Second // variables for the tests

	// dropWait is how long DropRoot waits for the scan that is running before it
	// gives up with ErrScanBusy.
	dropWait = 30 * time.Second
)

// ErrScanBusy is what DropRoot returns when a scan that is running does not
// finish within dropWait (a slow network disk, say). Nothing was removed and the
// folder is back in the scanner's roots; the caller may try again later.
var ErrScanBusy = errors.New("a scan of the library is still running")

// snapshot returns the media roots now, and the roots that were taken out but
// whose items are still to be dropped (see removeOrphans). A pass works on one
// snapshot from start to end.
func (s *Scanner) snapshot() (roots []Root, retired map[string]bool) {
	s.rootMu.Lock()
	defer s.rootMu.Unlock()
	return slices.Clone(s.roots), maps.Clone(s.retired)
}

// rootKind returns the type of a configured root; ok is false for a path that is
// not one.
func (s *Scanner) rootKind(path string) (kind string, ok bool) {
	s.rootMu.Lock()
	defer s.rootMu.Unlock()
	for _, r := range s.roots {
		if r.Path == path {
			return r.Kind, true
		}
	}
	return "", false
}

// rootRetired reports whether root was taken out of the roots and its items are
// yet to be dropped: a pass that is running leaves such a folder alone, so that
// DropRoot, which waits for the pass, does not wait for a walk of files that are
// about to go.
func (s *Scanner) rootRetired(root string) bool {
	s.rootMu.Lock()
	defer s.rootMu.Unlock()
	return s.retired[root]
}

// SetRoots replaces the media roots, which must be absolute and clean. It is
// live: it takes effect for the next scan (one that is running goes on with the
// roots it began with), and it does not block, so the caller may hold its own
// locks. A Root with no Kind keeps the type the scanner already has for its path
// (a -media folder that was guessed), and is read as ParseName does until it is
// guessed: the first scan that sees all of it, without files still settling, and
// finds files gives it a type (GuessKind) and stores it (Store.UpsertFlagFolder).
// A scanner whose roots all come from NewScanner never guesses: it reads every
// file as ParseName does.
//
//   - A root that was not there is scanned in the background, once the scan that
//     may be running is over; its files then come into the library.
//   - A root that has a type (not "") and is given another one has its items read
//     again in the background (Store.SetParsed): kind, title, show, season and
//     episode, as ParseNameIn reads them now. Only the items whose reading changes
//     are touched. The manual matches that cannot stay are kept in the archive,
//     and come back if the type does. This is for a person's choice: the PATCH of
//     a folder, or a type changed on the command line (the server gives the
//     scanner the types the folders had before, then the ones they have now).
//   - A root that had no type yet (every root of a new scanner) takes the type
//     without reading anything again: what its items were read with is not known,
//     and ply does not restructure a library on its own.
//   - A root that is gone is no longer scanned. Its items stay until DropRoot
//     removes them, and no scan removes them meanwhile, whatever happens to its
//     files.
//
// Wait waits for the background work.
func (s *Scanner) SetRoots(roots []Root) {
	var fresh, retyped []string
	s.rootMu.Lock()
	s.managed = true
	known := make(map[string]string, len(s.roots))
	for _, r := range s.roots {
		known[r.Path] = r.Kind
	}
	next := make([]Root, 0, len(roots))
	kept := make(map[string]bool, len(roots))
	for _, r := range roots {
		if kept[r.Path] {
			continue
		}
		kept[r.Path] = true
		old, had := known[r.Path]
		switch {
		case !had:
			fresh = append(fresh, r.Path)
		case r.Kind == "":
			r.Kind = old
		case old != "" && r.Kind != old:
			retyped = append(retyped, r.Path)
		}
		delete(s.retired, r.Path)
		next = append(next, r)
	}
	for _, r := range s.roots {
		if !kept[r.Path] {
			s.retired[r.Path] = true
		}
	}
	s.roots = next
	s.rootMu.Unlock()

	if len(fresh) == 0 && len(retyped) == 0 {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if len(retyped) > 0 {
			s.reparseRoots(s.ctx, retyped)
		}
		if len(fresh) > 0 {
			s.scanFresh(s.ctx, fresh)
		}
	}()
}

// scanFresh scans the folders that were just added, whole, as a partial scan
// (never at the same time as a full one). Directories left for later because
// their files were still being written are looked at again after the settle time.
func (s *Scanner) scanFresh(ctx context.Context, roots []string) {
	dirs := make([]Dir, len(roots))
	for i, r := range roots {
		dirs[i] = Dir{Path: r, Recursive: true}
	}
	for attempt := 0; len(dirs) > 0 && attempt <= freshScanRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(s.settle + freshRetryPad):
			case <-ctx.Done():
				return
			}
		}
		res, err := s.ScanDirs(ctx, dirs)
		if err != nil && ctx.Err() == nil {
			s.log.Warn("scan of a new library folder finished with errors", "folders", roots, "err", err)
		}
		dirs = res.Deferred
	}
}

// reparseRoots reads the items of roots again, under the scan semaphore, and
// tells the OnAdded hook which items it changed (they are pending again). The
// manual matches the archive keeps for them (a type that is given back) are
// reported.
func (s *Scanner) reparseRoots(ctx context.Context, roots []string) {
	if err := s.acquire(ctx); err != nil {
		return
	}
	defer s.release()
	var ids []int64
	for _, root := range roots {
		got, err := s.reparse(ctx, root, nil)
		if err != nil && ctx.Err() == nil {
			s.log.Warn("reading the items of a library folder again", "folder", root, "err", err)
		}
		ids = append(ids, got...)
	}
	s.notify(ids, nil)
	s.reportMatches(ctx)
}

// readings returns, for the items whose reading as kind says differs from the one
// they have, the new reading (Store.SetParsed). Whether an episode's name has a
// number (needs_review) is not a difference: the flag alone is no reason to forget
// a match, and it is the matcher's for a movie.
func readings(root, kind string, items []store.RootItem) []store.ParsedRow {
	r := Root{Path: root, Kind: kind}
	var rows []store.ParsedRow
	for _, it := range items {
		p := ParseNameIn(r, it.Path)
		row := store.ParsedRow{ID: it.ID, Kind: p.Kind, Title: p.Title, Year: p.Year, Show: p.Show,
			Season: p.Season, Episode: p.Episode, NeedsReview: p.NeedsReview}
		have := it.ParsedRow
		have.NeedsReview = row.NeedsReview
		if have != row {
			rows = append(rows, row)
		}
	}
	return rows
}

// reparse reads the file names of root's items again, as its type says, and
// stores the readings that differ from what the items have (Store.SetParsed, which
// also drops the matches that no longer fit, keeping the manual ones in the
// archive). only, when not nil, limits it to those items. It returns the ids of the
// items it changed. The caller holds the scan semaphore. A root that is not
// configured is left alone.
func (s *Scanner) reparse(ctx context.Context, root string, only map[int64]bool) ([]int64, error) {
	kind, ok := s.rootKind(root)
	if !ok {
		return nil, nil
	}
	items, err := s.store.RootItems(ctx, root)
	if err != nil {
		return nil, err
	}
	rows := readings(root, kind, items)
	if only != nil {
		rows = slices.DeleteFunc(rows, func(r store.ParsedRow) bool { return !only[r.ID] })
	}
	return s.writeReadings(ctx, root, kind, rows)
}

func (s *Scanner) writeReadings(ctx context.Context, root, kind string, rows []store.ParsedRow) ([]int64, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	if err := s.store.SetParsed(ctx, rows); err != nil {
		return nil, err
	}
	ids := make([]int64, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	s.log.Info("library folder read again", "folder", root, "type", kind, "items", len(ids))
	return ids, nil
}

// hasDeferred reports whether the pass left a directory of root to be looked at
// again: files in it were still being written, or had been a moment ago.
func (p *pass) hasDeferred(root string) bool {
	for dir := range p.deferred {
		if dir == root || within(root, dir) {
			return true
		}
	}
	return false
}

// finishRoots is what a pass does with the roots it saw whole and with nothing
// wrong: it notes the time (LastScan), and gives the -media folders that have no
// type yet their one-time guess. A root with directories left for later (files
// still settling) is neither: the folder is not all there yet. The caller holds
// the scan semaphore.
func (p *pass) finishRoots(ctx context.Context, scanned []string) []error {
	s := p.s
	var done []string
	for _, root := range scanned {
		if !p.hasDeferred(root) && !p.left(root) {
			done = append(done, root)
		}
	}
	if len(done) == 0 {
		return nil
	}
	s.rootMu.Lock()
	at := s.now()
	for _, root := range done {
		s.lastScan[root] = at
	}
	s.rootMu.Unlock()

	var errs []error
	for _, root := range done {
		ids, err := p.guessKind(ctx, root)
		p.reparsed = append(p.reparsed, ids...)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// Reread reads the items of folders again as their types say, in the background.
// It is for a type a person gave a folder on the command line that had items
// already, read before types existed (a first -shows for a folder that was -media):
// the files that are new would otherwise be read one way and the old ones another (a
// show in a folder called "Lost (2004)" is "Lost (2004)" now, and was "Lost"). A
// warning says how many items change. A folder with no type, or that is not one of
// the roots, is left alone. done, if not nil, is called for each folder once it has
// been looked at: err is nil when its items read as its type says (including when
// there was nothing to do), so that the caller can forget it was asked. It is not
// called for a folder the scanner did not get to (it ended first). Wait waits for it.
func (s *Scanner) Reread(roots []string, done func(root string, err error)) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.rereadTyped(s.ctx, roots, done)
	}()
}

func (s *Scanner) rereadTyped(ctx context.Context, roots []string, done func(root string, err error)) {
	if err := s.acquire(ctx); err != nil {
		return
	}
	defer s.release()
	var ids []int64
	for _, root := range roots {
		err := s.rereadOne(ctx, root, &ids)
		if err != nil && ctx.Err() != nil {
			break // the scanner is ending: the folder was not done
		}
		if err != nil {
			s.log.Warn("reading the items of a library folder again", "folder", root, "err", err)
		}
		if done != nil {
			done(root, err)
		}
	}
	s.notify(ids, nil)
	s.reportMatches(ctx)
}

func (s *Scanner) rereadOne(ctx context.Context, root string, ids *[]int64) error {
	kind, ok := s.rootKind(root)
	if !ok || kind == "" {
		return nil
	}
	items, err := s.store.RootItems(ctx, root)
	if err != nil {
		return err
	}
	rows := readings(root, kind, items)
	if len(rows) > 0 {
		s.log.Warn(fmt.Sprintf("folder %s: given as %s on the command line, and it had items from before folders had types; %d of its %d items are read again as that type says",
			root, kind, len(rows), len(items)), "folder", root, "type", kind, "changed", len(rows), "items", len(items))
	}
	got, err := s.writeReadings(ctx, root, kind, rows)
	*ids = append(*ids, got...)
	return err
}

// guessKind gives a root that has no type yet its guess, once it has files:
// shows when more than half are named like episodes, else movies (GuessKind). The
// guess is stored (Store.UpsertFlagFolder, which keeps a type stored meanwhile)
// and the root takes it. Which files are read as that type depends on who made them,
// which the database knows (Store.TypedSince: the item was added before this version
// first ran on it or after), not this process, its ids or how many times it was
// restarted before the guess:
//
//   - added by this version: read without a type, and read again now;
//   - added before: they were read, perhaps long ago, by a scanner that knew no types,
//     and a library is not restructured on a guess (a mixed folder would lose its
//     episodes' show, and the manual matches that go with them). A warning says how
//     many of them the guess leaves as they are; a person who wants them read again
//     changes the type in Settings.
//
// It returns the ids of the items it changed. The caller holds the scan semaphore.
func (p *pass) guessKind(ctx context.Context, root string) ([]int64, error) {
	s := p.s
	s.rootMu.Lock()
	managed := s.managed
	s.rootMu.Unlock()
	if kind, ok := s.rootKind(root); !managed || !ok || kind != "" {
		return nil, nil
	}
	items, err := s.store.RootItems(ctx, root)
	if err != nil || len(items) == 0 {
		return nil, err // nothing to go by yet
	}
	since, err := s.store.TypedSince(ctx)
	if err != nil {
		return nil, err
	}
	legacy := map[int64]bool{} // the items an earlier version left
	for _, it := range items {
		legacy[it.ID] = it.AddedAt < since
	}
	paths := make([]string, len(items))
	for i, it := range items {
		paths[i] = it.Path
	}
	kind := GuessKind(paths)
	f, _, err := s.store.UpsertFlagFolder(ctx, root, "", kind)
	if err != nil {
		return nil, err
	}
	if f.ID != 0 {
		kind = f.Kind
	}
	s.rootMu.Lock()
	adopted := false
	for i, r := range s.roots {
		if r.Path == root && r.Kind == "" {
			s.roots[i].Kind, adopted = kind, true
		}
	}
	s.rootMu.Unlock()
	if !adopted {
		return nil, nil
	}
	rows := readings(root, kind, items)
	s.log.Info("guessed the type of a library folder", "folder", root, "type", kind, "files", len(paths))
	current := make(map[int64]string, len(items)) // the kind each item has now
	for _, it := range items {
		current[it.ID] = it.Kind
	}
	var mine []store.ParsedRow
	left := map[string]int{}
	for _, r := range rows {
		if legacy[r.ID] {
			left[current[r.ID]]++
		} else {
			mine = append(mine, r)
		}
	}
	if len(left) > 0 {
		var parts []string
		for _, k := range []string{store.KindEpisode, store.KindMovie} {
			if n := left[k]; n > 0 {
				parts = append(parts, fmt.Sprintf("%d existing %s", n, plural(n, k)))
			}
		}
		s.log.Warn(fmt.Sprintf("folder %s: guessed %s; %s kept as they are; change the type in Settings to read them again",
			root, kind, strings.Join(parts, " and ")), "folder", root, "type", kind, "episodes", left[store.KindEpisode],
			"movies", left[store.KindMovie])
	}
	return s.writeReadings(ctx, root, kind, mine)
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// LastScan is when a scan last covered all of root without trouble, or the zero
// time if none has since the scanner started.
func (s *Scanner) LastScan(root string) time.Time {
	s.rootMu.Lock()
	defer s.rootMu.Unlock()
	return s.lastScan[root]
}

// DropRoot removes a library folder's items (Store.ArchiveAndDeleteRoot: what a
// person made of them is archived first, unless forget is set) and returns how
// many it removed. It is safe against scans:
//
//   - the root is taken out of the scanner's roots first, so that a scan that
//     starts from now on, or one that is waiting for its turn, does not bring the
//     files back, and no scan removes the items meanwhile (removeOrphans);
//   - it then waits for the pass that is running, if any, and holds the scan
//     semaphore while it removes the items, so that nothing indexes the folder
//     under its hands. A pass that is running stops looking at the folder as soon
//     as it is taken out. ctx ends the wait, and so does dropWait: then it returns
//     ErrScanBusy.
//
// If it fails, the root is put back (when DropRoot took it out; a caller that
// removed it with SetRoots first puts it back the same way), a scan of it is
// started, and the error comes back with the count of the items that were removed
// before it: the removal goes in batches, and calling DropRoot again finishes it.
// The files whose items were removed come back with that scan, restored from their
// archive.
func (s *Scanner) DropRoot(ctx context.Context, path string, forget bool) (int, error) {
	var taken *takenRoot
	s.rootMu.Lock()
	if i := slices.IndexFunc(s.roots, func(r Root) bool { return r.Path == path }); i >= 0 {
		taken = &takenRoot{s.roots[i], i}
		s.roots = slices.Delete(slices.Clone(s.roots), i, i+1)
	}
	s.retired[path] = true
	s.rootMu.Unlock()

	wait, cancel := context.WithTimeout(ctx, dropWait)
	err := s.acquire(wait)
	cancel()
	if err != nil {
		s.putBack(path, taken)
		if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			return 0, ErrScanBusy
		}
		return 0, err
	}
	defer s.release()
	n, err := s.store.ArchiveAndDeleteRoot(ctx, path, forget)
	if err != nil {
		s.putBack(path, taken)
		return n, err
	}
	s.rootMu.Lock()
	delete(s.retired, path)
	delete(s.lastScan, path)
	s.rootMu.Unlock()
	s.log.Info("library folder removed", "folder", path, "items", n, "archived", !forget)
	return n, nil
}

// takenRoot is a root DropRoot took out, and where it stood (the order of the
// roots decides which of two nested ones indexes a file).
type takenRoot struct {
	Root
	at int
}

// putBack undoes DropRoot's removal of the root it took out, unless the path is a
// root again by now (SetRoots), and scans it: a pass that was running skipped its
// files. With nothing taken out there is nothing to undo: the caller that removed
// the root first puts it back itself (which scans it), and the root stays retired
// until it does, so that no scan removes its items meanwhile.
func (s *Scanner) putBack(path string, taken *takenRoot) {
	if taken == nil {
		return
	}
	s.rootMu.Lock()
	delete(s.retired, path)
	back := !slices.ContainsFunc(s.roots, func(r Root) bool { return r.Path == path })
	if back {
		s.roots = slices.Insert(slices.Clone(s.roots), min(taken.at, len(s.roots)), taken.Root)
	}
	s.rootMu.Unlock()
	if back {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.scanFresh(s.ctx, []string{path})
		}()
	}
}
