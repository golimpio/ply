package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"ply/internal/library"
	"ply/internal/store"
)

// Library folders (docs/M7-FOLDERS.md): which directories ply serves, and what
// each one holds. Everything here is private (see sameSiteOnly and cors).

// folderInfo is a library folder as the API shows it.
type folderInfo struct {
	ID      int64  `json:"id"` // 0 for a -media folder whose type is not known yet
	Path    string `json:"path"`
	Kind    string `json:"kind"` // "movies" | "shows"; "" until a -media folder is guessed
	Guessed bool   `json:"guessed"`
	// Source is "flag" for a folder given on the command line, "ui" for one added
	// here. KindLocked: the type came from -movies or -shows and cannot be changed.
	Source     string `json:"source"`
	KindLocked bool   `json:"kindLocked"`
	Items      int    `json:"items"`
	// LastScanAt is when a scan last covered all of the folder without trouble,
	// unix seconds; 0 = not since the server started.
	LastScanAt int64  `json:"lastScanAt"`
	State      string `json:"state"` // "ok" | "missing" | "error"
	Error      string `json:"error"`
}

// flagKind returns the type a command-line folder was given with ("" for -media)
// and whether path is a command-line folder at all.
func (s *Server) flagKind(path string) (kind string, ok bool) {
	for _, r := range s.FlagFolders {
		if r.Path == path {
			return r.Kind, true
		}
	}
	return "", false
}

// dirState reports whether a folder can be read: "ok", "missing" or "error",
// with what the operating system said.
func dirState(path string) (state, msg string) {
	fi, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "missing", err.Error()
	case err != nil:
		return "error", err.Error()
	case !fi.IsDir():
		return "error", path + " is not a directory"
	}
	d, err := os.Open(path)
	if err != nil {
		return "error", err.Error()
	}
	defer d.Close()
	if _, err := d.Readdirnames(1); err != nil && !errors.Is(err, io.EOF) {
		return "error", err.Error()
	}
	return "ok", ""
}

func (s *Server) folderInfo(f store.Folder, items map[string]int) folderInfo {
	info := folderInfo{ID: f.ID, Path: f.Path, Kind: f.Kind, Guessed: f.Guessed, Source: "ui", Items: items[f.Path]}
	if kind, ok := s.flagKind(f.Path); ok {
		info.Source, info.KindLocked = "flag", kind != ""
	}
	info.State, info.Error = dirState(f.Path)
	if s.LastScan != nil {
		if t := s.LastScan(f.Path); !t.IsZero() {
			info.LastScanAt = t.Unix()
		}
	}
	return info
}

// folderInfos lists the saved folders, then the command-line ones that have no
// row yet (a -media folder waiting for its first scan to guess its type).
func (s *Server) folderInfos(ctx context.Context) ([]folderInfo, error) {
	rows, err := s.Store.Folders(ctx)
	if err != nil {
		return nil, err
	}
	items, err := s.Store.CountItemsByRoot(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]folderInfo, 0, len(rows)+len(s.FlagFolders))
	saved := make(map[string]bool, len(rows))
	for _, f := range rows {
		saved[f.Path] = true
		out = append(out, s.folderInfo(f, items))
	}
	for _, r := range s.FlagFolders {
		if !saved[r.Path] {
			out = append(out, s.folderInfo(store.Folder{Path: r.Path, Kind: r.Kind}, items))
		}
	}
	return out, nil
}

// activeRoots is what the scanner and the watcher should cover: the
// command-line folders and the saved ones.
func (s *Server) activeRoots(ctx context.Context) ([]library.Root, error) {
	rows, err := s.Store.Folders(ctx)
	if err != nil {
		return nil, err
	}
	saved := make([]library.Root, len(rows))
	for i, f := range rows {
		saved[i] = library.Root{Path: f.Path, Kind: f.Kind}
	}
	return library.MergeRoots(s.FlagFolders, saved), nil
}

// rootsChanged tells whoever follows the folders that they changed. A failure
// is logged, not returned: the change itself is stored, and a restart picks it up.
func (s *Server) rootsChanged(ctx context.Context) {
	if s.SetRoots == nil {
		return
	}
	roots, err := s.activeRoots(ctx)
	if err != nil {
		s.Logger.Error("folders: reading the active roots", "err", err)
		return
	}
	s.SetRoots(roots)
}

func (s *Server) handleFolders(w http.ResponseWriter, r *http.Request) {
	infos, err := s.folderInfos(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, infos)
}

// readBody decodes a small JSON request body into v; it answers 400 itself.
func readBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func (s *Server) handleAddFolder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
		Kind string `json:"kind"`
	}
	if !readBody(w, r, &req) {
		return
	}
	if !library.ValidKind(req.Kind) {
		writeError(w, http.StatusBadRequest, `kind must be "movies" or "shows"`)
		return
	}
	if !filepath.IsAbs(req.Path) {
		writeError(w, http.StatusBadRequest, "path must be absolute")
		return
	}
	path := filepath.Clean(req.Path)

	s.foldersMu.Lock()
	defer s.foldersMu.Unlock()
	ctx := r.Context()
	active, err := s.activeRoots(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	for _, a := range active {
		if library.SameDir(a.Path, path) {
			writeError(w, http.StatusConflict, "this folder is already in the library (as "+a.Path+")")
			return
		}
	}
	if state, msg := dirState(path); state != "ok" {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("cannot use %s as a library folder: %s", path, msg))
		return
	}
	for _, a := range active {
		if library.Overlap(a.Path, path) {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("%s overlaps the library folder %s: a folder cannot hold or sit inside another", path, a.Path))
			return
		}
	}
	f, err := s.Store.AddFolder(ctx, path, req.Kind, false)
	if errors.Is(err, store.ErrFolderExists) {
		writeError(w, http.StatusConflict, "this folder is already in the library")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.rootsChanged(ctx)
	counts, _ := s.Store.CountItemsByRoot(ctx)
	writeJSON(w, http.StatusCreated, s.folderInfo(f, counts))
}

// loadFolder resolves {id}; it writes the error response and reports false if
// there is no such saved folder.
func (s *Server) loadFolder(w http.ResponseWriter, r *http.Request) (store.Folder, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "folder not found")
		return store.Folder{}, false
	}
	f, err := s.Store.GetFolder(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "folder not found")
		return store.Folder{}, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return store.Folder{}, false
	}
	return f, true
}

func (s *Server) handlePatchFolder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind string `json:"kind"`
	}
	if !readBody(w, r, &req) {
		return
	}
	if !library.ValidKind(req.Kind) {
		writeError(w, http.StatusBadRequest, `kind must be "movies" or "shows"`)
		return
	}
	s.foldersMu.Lock()
	defer s.foldersMu.Unlock()
	f, ok := s.loadFolder(w, r)
	if !ok {
		return
	}
	if kind, isFlag := s.flagKind(f.Path); isFlag && kind != "" {
		writeError(w, http.StatusConflict, "the type of this folder was set on the command line (-"+kind+")")
		return
	}
	ctx := r.Context()
	// The scanner sees the new type through SetRoots and re-parses the items.
	if err := s.Store.SetFolderKind(ctx, f.ID, req.Kind); err != nil {
		s.internalError(w, r, err)
		return
	}
	f.Kind, f.Guessed = req.Kind, false
	s.rootsChanged(ctx)
	counts, _ := s.Store.CountItemsByRoot(ctx)
	writeJSON(w, http.StatusOK, s.folderInfo(f, counts))
}

func (s *Server) handleDeleteFolder(w http.ResponseWriter, r *http.Request) {
	forget := false
	switch r.URL.Query().Get("forget") {
	case "", "0", "false":
	case "1", "true":
		forget = true
	default:
		writeError(w, http.StatusBadRequest, "forget must be 1 or 0")
		return
	}
	s.foldersMu.Lock()
	defer s.foldersMu.Unlock()
	f, ok := s.loadFolder(w, r)
	if !ok {
		return
	}
	if _, isFlag := s.flagKind(f.Path); isFlag {
		writeError(w, http.StatusConflict, "this folder was set on the command line; it would come back at the next start")
		return
	}
	// From here on it runs to its end whether or not the client stays: a request
	// that is cancelled while DropRoot waits for a scan must not leave the folder
	// saved and no longer scanned.
	ctx := context.WithoutCancel(r.Context())

	// The order matters. The scanner and the watcher stop covering the folder
	// first, so that no scan or event brings its files back while they are dropped
	// (a scan that sneaked in later would delete them again with nothing archived,
	// as the files of a root that is no longer configured). Then the items go,
	// waiting for a scan that is running. Then the row. The lock is held through
	// all of it, which keeps the folder from being added again meanwhile.
	roots, err := s.activeRoots(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	all := slices.Clone(roots) // DeleteFunc below changes roots; this is what a restore gives back
	restore := func() {
		if s.SetRoots != nil {
			s.SetRoots(all) // from memory: the store may be the very thing that failed
		}
	}
	if s.SetRoots != nil {
		s.SetRoots(slices.DeleteFunc(roots, func(r library.Root) bool { return r.Path == f.Path }))
	}
	drop := s.DropRoot
	if drop == nil {
		drop = s.Store.ArchiveAndDeleteRoot
	}
	if _, err := drop(ctx, f.Path, forget); err != nil {
		restore() // the folder is still there: cover it again
		if errors.Is(err, library.ErrScanBusy) {
			// A scan of the library is taking its time (a slow disk). Nothing was removed.
			w.Header().Set("Retry-After", "30")
			writeError(w, http.StatusServiceUnavailable, "a scan of the library is still running; the folder was not removed, try again in a moment")
			return
		}
		s.internalError(w, r, err)
		return
	}
	if err := s.Store.DeleteFolder(ctx, f.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		restore() // its items are gone and the folder is not: a scan puts them back, restored
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// fsEntry is a directory in a listing.
type fsEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// fsListing is the answer of GET /api/fs: the directories of Path, or, with no
// path, the places to start from.
type fsListing struct {
	Path   string    `json:"path"`
	Parent string    `json:"parent"`
	Dirs   []fsEntry `json:"dirs"`
	Places []fsEntry `json:"places,omitempty"`
}

// subdirs lists the directories in dir, leaving out files and dot-folders. A
// symlink to a directory counts as one. Entries that cannot be looked at are
// skipped.
func subdirs(dir string) ([]fsEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]fsEntry, 0, len(entries))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		isDir := e.IsDir()
		if e.Type()&fs.ModeSymlink != 0 {
			fi, err := os.Stat(p)
			isDir = err == nil && fi.IsDir()
		}
		if isDir {
			out = append(out, fsEntry{Name: e.Name(), Path: p})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if a != b {
			return a < b
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// mountParents are the directories whose entries are the disks and shares
// mounted on this kind of machine.
func mountParents(goos string) []string {
	switch goos {
	case "darwin":
		return []string{"/Volumes"}
	case "linux":
		return []string{"/media", "/mnt"}
	}
	return nil
}

// listPlaces is where the folder browser starts: the home directory, whatever is
// mounted under the parents, and the folders already in the library.
func listPlaces(home string, parents, folders []string) []fsEntry {
	out := []fsEntry{}
	seen := map[string]bool{}
	add := func(name, path string) {
		if path != "" && !seen[path] {
			seen[path] = true
			out = append(out, fsEntry{Name: name, Path: path})
		}
	}
	if home != "" {
		add("Home", home)
	}
	for _, parent := range parents {
		dirs, _ := subdirs(parent) // a machine without /mnt has no mounts there
		for _, d := range dirs {
			// /Volumes/Macintosh HD is a link to the whole disk: not a place to start from.
			if real, err := filepath.EvalSymlinks(d.Path); err == nil && real == string(filepath.Separator) {
				continue
			}
			add(d.Name, d.Path)
		}
	}
	for _, f := range folders {
		add(filepath.Base(f), f)
	}
	return out
}

// hiddenPath reports whether p has a component that starts with a dot. The
// folders in exempt (the library folders and the home directory, spelled as given
// or as they resolve) may sit under a dot directory: what is above them does not
// count, only what is below.
func hiddenPath(p string, exempt []string) bool {
	sep := string(filepath.Separator)
	rel := p
	for _, e := range exempt {
		for _, c := range []string{e, realPath(e)} {
			if c == "" {
				continue
			}
			if r, err := filepath.Rel(c, p); err == nil && r != ".." && !strings.HasPrefix(r, ".."+sep) && len(r) < len(rel) {
				rel = r
			}
		}
	}
	if rel == "." {
		return false
	}
	for _, part := range strings.Split(rel, sep) {
		if strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}

// realPath is p with its symlinks resolved, or "" if it cannot be.
func realPath(p string) string {
	if p == "" {
		return ""
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return ""
	}
	return real
}

func (s *Server) handleFS(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("path")
	if raw == "" {
		home, _ := os.UserHomeDir()
		active, err := s.activeRoots(r.Context())
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		folders := make([]string, len(active))
		for i, a := range active {
			folders[i] = a.Path
		}
		writeJSON(w, http.StatusOK, fsListing{Dirs: []fsEntry{}, Places: listPlaces(home, mountParents(runtime.GOOS), folders)})
		return
	}
	if !filepath.IsAbs(raw) {
		writeError(w, http.StatusBadRequest, "path must be absolute")
		return
	}
	dir := filepath.Clean(raw)
	home, _ := os.UserHomeDir()
	active, err := s.activeRoots(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	exempt := []string{home}
	for _, a := range active {
		exempt = append(exempt, a.Path)
	}
	if hiddenPath(dir, exempt) {
		writeError(w, http.StatusForbidden, "hidden directories are not browsable: "+dir)
		return
	}
	fi, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR):
		writeError(w, http.StatusNotFound, "no such directory: "+dir)
		return
	case err != nil:
		writeError(w, http.StatusForbidden, err.Error())
		return
	case !fi.IsDir():
		writeError(w, http.StatusBadRequest, dir+" is not a directory")
		return
	}
	// A link to a dot directory is one too.
	if real, err := filepath.EvalSymlinks(dir); err == nil && hiddenPath(real, exempt) {
		writeError(w, http.StatusForbidden, "hidden directories are not browsable: "+dir+" is "+real)
		return
	}
	dirs, err := subdirs(dir)
	if err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	parent := filepath.Dir(dir)
	if parent == dir {
		parent = "" // the top
	}
	writeJSON(w, http.StatusOK, fsListing{Path: dir, Parent: parent, Dirs: dirs})
}
