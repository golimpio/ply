package stream

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The session registry: one file, <data>/sessions/<sid>.json, for every session
// that has an ffmpeg running. It is what lets a ply find, after it crashed or was
// killed, the ffmpeg process groups that no longer have a parent, whatever
// TMPDIR the earlier ply had (a launchd job and a terminal differ) and whatever
// state the process is in (a stopped one is not found by looking at what is
// running, and holds its source file open for ever).
//
// The file is written when ffmpeg has started and before Create returns, and
// removed when the process has been waited for. A process is only ever killed
// for what a file says if it is still the process the file describes: the pid
// is alive and started when the file says it did (pids get reused).

// regEntry is the content of a registry file.
type regEntry struct {
	SID     string `json:"sid"`
	Item    int64  `json:"item"`
	Title   string `json:"title,omitempty"`
	PID     int    `json:"pid"`   // ffmpeg
	PGID    int    `json:"pgid"`  // its process group, which it leads
	Start   string `json:"start"` // when PID started (processStart)
	Program string `json:"program"`
	Dir     string `json:"dir"` // the session directory

	Owner      int       `json:"owner"`      // the ply that made it
	OwnerStart string    `json:"ownerStart"` // when that ply started
	Written    time.Time `json:"written"`
}

// registry is the directory of registry files. A nil *registry does nothing.
type registry struct {
	dir        string
	log        *slog.Logger
	ownerStart string
}

func newRegistry(dir string, log *slog.Logger) (*registry, error) {
	if dir == "" {
		return nil, nil
	}
	// What is in it is acted on with kill(2): it must be a directory of ours, not a
	// link to somebody else's, and nobody else's to write in.
	if err := prepareRoot(dir); err != nil {
		return nil, err
	}
	start, _ := processStart(os.Getpid())
	return &registry{dir: dir, log: log, ownerStart: start}, nil
}

func (r *registry) path(sid string) string { return filepath.Join(r.dir, sid+".json") }

// write records e, replacing the file of its session (atomically: a crash in
// the middle leaves the old file or the new one).
func (r *registry) write(e regEntry) error {
	if r == nil {
		return nil
	}
	e.Owner, e.OwnerStart, e.Written = os.Getpid(), r.ownerStart, time.Now()
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(r.dir, ".tmp-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), r.path(e.SID)); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// remove deletes the file of session sid.
func (r *registry) remove(sid string) {
	if r != nil {
		_ = os.Remove(r.path(sid))
	}
}

// removeRun deletes the file of session sid if it still describes ffmpeg pid: a
// session whose ffmpeg was replaced by another has the new one's file by then.
func (r *registry) removeRun(sid string, pid int) {
	if r == nil {
		return
	}
	if e, err := r.read(r.path(sid)); err == nil && e.PID != pid {
		return
	}
	r.remove(sid)
}

func (r *registry) read(path string) (regEntry, error) {
	var e regEntry
	b, err := os.ReadFile(path)
	if err != nil {
		return e, err
	}
	if err := json.Unmarshal(b, &e); err != nil {
		return e, err
	}
	if e.SID == "" || e.PID <= 1 || e.PGID <= 1 || e.Start == "" {
		return e, errors.New("incomplete registry file")
	}
	// ffmpeg leads the group it is started in: a group that is not named by its
	// leader is not one ply made, and the start time says nothing about it.
	if e.PID != e.PGID {
		return e, errors.New("registry file whose pid does not lead its group")
	}
	return e, nil
}

// regFile is a registry file as read by entries.
type regFile struct {
	path  string
	entry regEntry
	err   error // the file is not a usable entry
}

// entries lists the files in the registry. Files that are not usable come back
// with err set, so that the caller can remove them.
func (r *registry) entries() []regFile {
	if r == nil {
		return nil
	}
	ds, err := os.ReadDir(r.dir)
	if err != nil {
		return nil
	}
	var out []regFile
	for _, d := range ds {
		name := d.Name()
		if d.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		p := filepath.Join(r.dir, name)
		e, err := r.read(p)
		out = append(out, regFile{path: p, entry: e, err: err})
	}
	return out
}

// foreignOwner reports whether e was made by a ply that is still running and is
// not this one: its sessions are not ours to kill (two plys can share a data
// directory, briefly, while one is replaced by the next).
func foreignOwner(e regEntry) bool {
	if e.Owner <= 1 || e.Owner == os.Getpid() {
		return false
	}
	start, err := processStart(e.Owner)
	return err == nil && start == e.OwnerStart
}

// stillRunning reports whether e describes a live ffmpeg: its pid is alive, it
// started when the file says, and it still leads the group the file names.
func stillRunning(e regEntry) bool {
	start, err := processStart(e.PID)
	return err == nil && start == e.Start && groupOf(e.PID) == e.PGID
}

// sweep kills what the registry files of ply processes that are gone say is
// still running, and removes the files and the session directories (those
// inside tempRoot). It returns how many process groups it killed.
func (r *registry) sweep(tempRoot string) (killed int) {
	r.clearTemporaries()
	for _, f := range r.entries() {
		if f.err != nil {
			r.log.Warn("removing an unusable session registry file", "file", filepath.Base(f.path), "err", f.err)
			_ = os.Remove(f.path)
			continue
		}
		if foreignOwner(f.entry) {
			continue
		}
		if r.reap(f.entry, tempRoot) {
			killed++
		}
		_ = os.Remove(f.path)
	}
	return killed
}

// reap kills the ffmpeg group e describes, if it is running, and removes its
// session directory. It reports whether it killed anything.
func (r *registry) reap(e regEntry, tempRoot string) (killed bool) {
	if stillRunning(e) && killGroup(e.PGID) {
		killed = true
		r.log.Warn("killed an ffmpeg left behind by a ply that is gone", "pgid", e.PGID, "session", e.SID, "item", e.Item)
	}
	if rel, ok := below(tempRoot, e.Dir); ok {
		// (Removed by the name made from the root and the cleaned path below it, not
		// the one the file gave.)
		if err := os.RemoveAll(filepath.Join(tempRoot, rel)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			r.log.Warn("remove the directory of a dead session", "session", e.SID, "err", err)
		}
	}
	return killed
}

// below returns where path is under root, if it is a proper descendant of it
// (not root itself, not outside it).
func below(root, path string) (rel string, ok bool) {
	if path == "" {
		return "", false
	}
	rel, err := filepath.Rel(root, filepath.Clean(path))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return rel, true
}

// staleTemporary is how old a half-written registry file has to be to be taken
// for the leftover of a crash: writing one takes microseconds.
const staleTemporary = time.Hour

// clearTemporaries removes the temporary files (see write) that a crash left.
func (r *registry) clearTemporaries() {
	if r == nil {
		return
	}
	ds, err := os.ReadDir(r.dir)
	if err != nil {
		return
	}
	for _, d := range ds {
		if d.IsDir() || !strings.HasPrefix(d.Name(), ".tmp-") {
			continue
		}
		if fi, err := d.Info(); err == nil && time.Since(fi.ModTime()) > staleTemporary {
			_ = os.Remove(filepath.Join(r.dir, d.Name()))
		}
	}
}
