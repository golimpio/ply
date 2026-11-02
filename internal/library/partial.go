package library

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"ply/internal/store"
)

// Dir is a directory for ScanDirs.
type Dir struct {
	// Path is absolute and clean, spelled as under a configured root (not
	// symlink-resolved).
	Path string
	// Recursive also scans the subdirectories. A configured root is always
	// scanned recursively.
	Recursive bool
	// Sidecars says metadata files (.nfo, artwork) changed in Path: the items
	// under it are reported through OnSidecarsChanged.
	Sidecars bool
}

// ErrNotScanned marks a ScanDirs error that kept the scan from running at all
// (the library could not be read): nothing was looked at, so the directories
// should be scanned again later. Other errors leave a partial scan done as
// far as it could go.
var ErrNotScanned = errors.New("partial scan did not run")

// Result says what ScanDirs did.
type Result struct {
	Added []int64 // items created, or moved to a new path (same id)
	// Deferred lists directories to scan again after the settle time: they
	// hold files still settling, or written within the last minute (a copy
	// that stalled, or on darwin a write that made no directory event).
	Deferred []Dir
}

// ScanDirs scans part of the library: the video files in dirs, with the same
// rules as a full scan. Rows are removed only below a directory that exists
// and was walked without a single error, and never those of a configured root
// in scope that turned up no video files at all (an unmounted volume). A file
// moved between directories keeps its id when both directories are in the same
// call (or the old file is otherwise known to be gone). Directories outside the
// configured roots, or inside a hidden one, are ignored. It waits for a scan
// that is already running.
func (s *Scanner) ScanDirs(ctx context.Context, dirs []Dir) (Result, error) {
	scopes, sidecarDirs := s.scopes(dirs)
	if len(scopes) == 0 && len(sidecarDirs) == 0 {
		return Result{}, nil
	}
	if err := s.acquire(ctx); err != nil {
		return Result{}, err
	}
	defer s.release()

	start := time.Now()
	states, err := s.store.FileStates(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("%w: load library state: %w", ErrNotScanned, err)
	}
	s.loadMounts(ctx)
	p := s.newPass(states)
	defer p.saveMounts(ctx)
	claimant := func(path string) string {
		if root, ok := p.owners[path]; ok {
			return root
		}
		return s.firstRoot(path)
	}

	var (
		errs  []error
		clean []Dir // scopes that exist and were walked without errors
	)
	for _, d := range scopes {
		if ctx.Err() != nil {
			break
		}
		fi, err := os.Stat(d.Path)
		if err != nil || !fi.IsDir() {
			// Gone (or replaced by a file): whatever was in it is handled by a
			// scan of its parent. Nothing here is removed.
			continue
		}
		walkErrors, err := p.walk(ctx, d.Path, d.Recursive, claimant)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("%s: walk: %w", d.Path, err))
		case walkErrors > 0:
			errs = append(errs, fmt.Errorf("%s: %d errors while walking; removals skipped", d.Path, walkErrors))
		default:
			clean = append(clean, d)
		}
	}
	if ctx.Err() == nil {
		errs = append(errs, p.pairMoves(ctx))
	}
	errs = append(errs, p.indexAll(ctx)...)
	if err := ctx.Err(); err != nil {
		return Result{}, errors.Join(append(errs, err)...)
	}
	for _, d := range clean {
		errs = append(errs, p.removeIn(ctx, d))
	}

	res := Result{Added: p.added}
	for dir, recursive := range p.deferred {
		res.Deferred = append(res.Deferred, Dir{Path: dir, Recursive: recursive})
	}
	slices.SortFunc(res.Deferred, func(a, b Dir) int { return strings.Compare(a.Path, b.Path) })
	// Moved items too: their sidecar artwork, if any, is somewhere else now.
	sidecarIDs := p.itemsUnder(sidecarDirs)
	sidecarIDs = append(sidecarIDs, p.moved...)
	slices.Sort(sidecarIDs)

	if p.sum.indexed.Load() > 0 || p.sum.moved > 0 || p.sum.removed.Load() > 0 {
		s.log.Info("partial scan complete", "dirs", len(scopes), "indexed", p.sum.indexed.Load(),
			"moved", p.sum.moved, "removed", p.sum.removed.Load(),
			"duration", time.Since(start).Round(time.Millisecond))
	}
	s.notify(res.Added, sidecarIDs)
	return res, errors.Join(errs...)
}

// removeIn removes the rows of files under d that were not found, leaving
// alone the configured roots in scope that turned up nothing.
func (p *pass) removeIn(ctx context.Context, d Dir) error {
	protected := map[string]bool{}
	for _, root := range p.s.roots {
		if root != d.Path && !(d.Recursive && within(d.Path, root)) {
			continue
		}
		if n := p.rowsOf(root); p.seenBy[root] == 0 && n > 0 {
			protected[root] = true
			p.s.log.Warn("no video files found, but indexed items came from this root; "+
				"refusing to remove them (is the volume mounted?)", "root", root, "items", n)
		}
	}
	err := p.removeMissing(ctx, func(path string, st store.FileState) bool {
		if !p.s.configured(st.Root) || protected[st.Root] {
			return false
		}
		if d.Recursive {
			return within(d.Path, path)
		}
		return filepath.Dir(path) == d.Path
	})
	if err != nil {
		return fmt.Errorf("%s: %w", d.Path, err)
	}
	return nil
}

// itemsUnder returns the ids of the items below any of dirs, minus those
// reported as added, sorted.
func (p *pass) itemsUnder(dirs []string) []int64 {
	if len(dirs) == 0 {
		return nil
	}
	added := make(map[int64]bool, len(p.added))
	for _, id := range p.added {
		added[id] = true
	}
	var ids []int64
	for path, st := range p.states {
		if added[st.ID] {
			continue
		}
		if slices.ContainsFunc(dirs, func(d string) bool { return within(d, path) }) {
			ids = append(ids, st.ID)
		}
	}
	slices.Sort(ids)
	return ids
}

// scopes validates and merges dirs: it drops directories outside the
// configured roots or inside a hidden directory, scans a root recursively, and
// drops a directory already covered by a recursive one. It also returns the
// directories whose sidecars changed.
func (s *Scanner) scopes(dirs []Dir) (scopes []Dir, sidecarDirs []string) {
	byPath := map[string]Dir{}
	for _, d := range dirs {
		d.Path = filepath.Clean(d.Path)
		if !filepath.IsAbs(d.Path) {
			s.log.Warn("partial scan: ignoring a relative directory", "dir", d.Path)
			continue
		}
		reachable := false
		for _, root := range s.roots {
			if d.Path == root {
				d.Recursive, reachable = true, true
			} else if reaches(root, d.Path) {
				reachable = true
			}
		}
		if !reachable {
			s.log.Warn("partial scan: ignoring a directory outside the media roots", "dir", d.Path)
			continue
		}
		if d.Sidecars && !slices.Contains(sidecarDirs, d.Path) {
			sidecarDirs = append(sidecarDirs, d.Path)
		}
		if prev, ok := byPath[d.Path]; ok {
			d.Recursive = d.Recursive || prev.Recursive
		}
		d.Sidecars = false
		byPath[d.Path] = d
	}
	for path, d := range byPath {
		covered := false
		for other, o := range byPath {
			if other != path && o.Recursive && within(other, path) {
				covered = true
				break
			}
		}
		if !covered {
			scopes = append(scopes, d)
		}
	}
	slices.SortFunc(scopes, func(a, b Dir) int { return strings.Compare(a.Path, b.Path) })
	slices.Sort(sidecarDirs)
	return scopes, sidecarDirs
}

// firstRoot is the first configured root that reaches path, the one a full
// scan would let index it, or "".
func (s *Scanner) firstRoot(path string) string {
	for _, root := range s.roots {
		if reaches(root, path) {
			return root
		}
	}
	return ""
}

// reaches reports whether a walk of root gets to path: path is inside it and
// no directory on the way (nor path itself) is hidden.
func reaches(root, path string) bool {
	if !within(root, path) {
		return false
	}
	rel, _ := filepath.Rel(root, path)
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if strings.HasPrefix(part, ".") {
			return false
		}
	}
	return true
}
