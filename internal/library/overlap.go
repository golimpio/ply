package library

import (
	"os"
	"path/filepath"
	"strings"
)

// Folders are compared as directories, not as spelled: a symlink, a symlinked
// parent, or another case on a disk that ignores case (APFS by default) does not
// make a folder another one.

// nameInside reports whether path is dir or inside it, by name.
func nameInside(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// SameDir reports whether a and b are the same directory however they are
// spelled: the same name, or the same directory on disk (os.SameFile).
func SameDir(a, b string) bool {
	if a == b {
		return true
	}
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	return err == nil && os.SameFile(fa, fb)
}

// Inside reports whether dir is root or lies below it. By name that is the
// first test; then dir's real path is walked up, looking for root on disk, so
// that a symlink into root, or a path through a symlinked parent, is not missed.
// A root that is not on disk is only compared by name.
func Inside(root, dir string) bool {
	if nameInside(root, dir) {
		return true
	}
	rs, err := os.Stat(root)
	if err != nil {
		return false
	}
	p, err := filepath.EvalSymlinks(dir)
	if err != nil {
		p = dir
	}
	for {
		if fi, err := os.Stat(p); err == nil && os.SameFile(fi, rs) {
			return true
		}
		up := filepath.Dir(p)
		if up == p {
			return false
		}
		p = up
	}
}

// Overlap reports whether one of the two folders is the other or holds it.
func Overlap(a, b string) bool { return Inside(a, b) || Inside(b, a) }
