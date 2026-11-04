package api

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A folder is the same folder, and a folder holds another, however the paths are
// spelled: through a symlink, a symlinked parent, or another case on a disk that
// ignores case.
func TestAddFolderSpelledPaths(t *testing.T) {
	f := newFolderEnv(t, "", "tv:shows")
	link := func(name, target string) string {
		t.Helper()
		p := f.path(name)
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	wantStatus(t, f.post(f.path("movies"), "movies"), 201)
	alias := link("alias", f.path("movies"))
	into := link("into", f.path("movies/extras"))
	above := link("above", f.base)
	disk := link("disk", "/")
	viaParent := link("via-parent", f.base) // via-parent/movies is movies
	tvAlias := link("tv-alias", f.path("tv"))
	elsewhere := link("elsewhere", f.path("other"))
	before := len(f.callLog())

	tests := []struct {
		name, path string
		want       int
		says       string // part of the error
	}{
		{"a symlink to the folder", alias, 409, f.path("movies")},
		{"the folder through a symlinked parent", filepath.Join(viaParent, "movies"), 409, f.path("movies")},
		{"a symlink to a command-line folder", tvAlias, 409, f.path("tv")},
		{"a symlink into the folder", into, 400, f.path("movies")},
		{"inside the folder through a symlinked parent", filepath.Join(viaParent, "movies", "extras"), 400, f.path("movies")},
		{"a symlink to a folder that holds the folder", above, 400, "overlaps"},
		{"a symlink to the whole disk", disk, 400, "overlaps"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := f.post(tt.path, "movies")
			wantJSONError(t, rec, tt.want)
			if !strings.Contains(rec.Body.String(), tt.says) {
				t.Errorf("the error should mention %q: %s", tt.says, rec.Body)
			}
		})
	}
	if saved := f.folders(); len(saved) != 1 {
		t.Errorf("a refusal stored a folder: %+v", saved)
	}
	if calls := f.callLog(); len(calls) != before {
		t.Errorf("a refusal told the scanner: %v", calls[before:])
	}
	// Control: a symlink to a folder that has nothing to do with the others is fine.
	wantStatus(t, f.post(elsewhere, "shows"), 201)
}

// A saved folder that is itself a symlink holds what is inside its target.
func TestAddFolderInsideASymlinkedFolder(t *testing.T) {
	f := newFolderEnv(t, "")
	root := f.path("root-link")
	if err := os.Symlink(f.path("movies"), root); err != nil {
		t.Fatal(err)
	}
	f.add(root, "movies")
	wantJSONError(t, f.post(f.path("movies/extras"), "movies"), 400)
	wantJSONError(t, f.post(f.path("movies"), "movies"), 409)
	wantStatus(t, f.post(f.path("other"), "movies"), 201) // control
}

// A saved folder that is gone from disk is still compared by name.
func TestAddFolderOverlapWithAMissingFolderIsByName(t *testing.T) {
	f := newFolderEnv(t, "")
	f.add(f.path("gone"), "movies") // never created
	wantJSONError(t, f.post(f.path("gone"), "movies"), 409)
	if err := os.MkdirAll(f.path("gone/sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	wantJSONError(t, f.post(f.path("gone/sub"), "movies"), 400)
	wantStatus(t, f.post(f.path("other"), "movies"), 201)
}

// On a disk that ignores case (APFS by default) another spelling of a folder is
// the folder.
func TestAddFolderCaseVariant(t *testing.T) {
	f := newFolderEnv(t, "")
	upper := filepath.Join(f.base, "MOVIES")
	if _, err := os.Stat(upper); err != nil {
		t.Skip("this file system tells movies and MOVIES apart")
	}
	wantStatus(t, f.post(f.path("movies"), "movies"), 201)
	rec := f.post(upper, "movies")
	wantJSONError(t, rec, 409)
	if !strings.Contains(rec.Body.String(), f.path("movies")) {
		t.Errorf("the error should name the folder that is there: %s", rec.Body)
	}
	wantJSONError(t, f.post(filepath.Join(upper, "extras"), "movies"), 400)
	if saved := f.folders(); len(saved) != 1 {
		t.Errorf("stored %+v", saved)
	}
}

// dotFreeTempDir is a temp dir with no dot component in its path, for the tests
// of what /api/fs refuses: t.TempDir() is under a dot directory when TMPDIR is
// (~/.cache/tmp, say). It falls back to /tmp, and skips if that is no better.
func dotFreeTempDir(t *testing.T) string {
	t.Helper()
	dotted := func(p string) bool {
		for _, part := range strings.Split(p, string(filepath.Separator)) {
			if strings.HasPrefix(part, ".") {
				return true
			}
		}
		return false
	}
	if dir := t.TempDir(); !dotted(dir) {
		return dir
	}
	dir, err := os.MkdirTemp("/tmp", "ply-test-")
	if err != nil || dotted(dir) {
		t.Skip("no temp dir without a dot component")
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// What /api/fs refuses is a dot component of the path or of where it leads. The
// library folders and the home directory may sit under a dot directory: only what
// is below them counts.
func TestFSHiddenPathRules(t *testing.T) {
	f := newFolderEnv(t, "")
	root := dotFreeTempDir(t)
	for _, d := range []string{
		"visible/inner", ".secret/inner",
		".lib/movies/sub", ".lib/movies/.git", ".lib/other",
		".home/me/docs", ".home/me/.config",
	} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := func(name, target string) string {
		t.Helper()
		p := filepath.Join(root, name)
		if err := os.Symlink(filepath.Join(root, target), p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	visibleLink := link("to-visible", "visible")
	secretLink := link("to-secret", ".secret/inner")
	escape := link(".lib/movies/escape", ".secret/inner")
	f.add(filepath.Join(root, ".lib", "movies"), "movies")
	t.Setenv("HOME", filepath.Join(root, ".home", "me"))

	for _, tt := range []struct {
		name, path string
		want       int
	}{
		{"a plain folder", filepath.Join(root, "visible"), 200},
		{"a link to a plain folder", visibleLink, 200},
		{"a link to a dot directory", secretLink, 403},
		{"the dot directory", filepath.Join(root, ".secret"), 403},

		// A library folder under a dot directory: browsable, and what is in it.
		{"the library folder", filepath.Join(root, ".lib", "movies"), 200},
		{"below the library folder", filepath.Join(root, ".lib", "movies", "sub"), 200},
		{"a dot directory below the library folder", filepath.Join(root, ".lib", "movies", ".git"), 403},
		{"a link out of the library folder into a dot directory", escape, 403},
		{"above the library folder", filepath.Join(root, ".lib"), 403},
		{"next to the library folder", filepath.Join(root, ".lib", "other"), 403},

		// The home directory under a dot directory: the same.
		{"home", filepath.Join(root, ".home", "me"), 200},
		{"below home", filepath.Join(root, ".home", "me", "docs"), 200},
		{"a dot directory below home", filepath.Join(root, ".home", "me", ".config"), 403},
		{"above home", filepath.Join(root, ".home"), 403},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := f.get("/api/fs?path=" + tt.path)
			if tt.want == 200 {
				wantStatus(t, rec, 200)
			} else {
				wantJSONError(t, rec, tt.want)
			}
		})
	}
	// The link's own error says where it leads.
	if rec := f.get("/api/fs?path=" + secretLink); !strings.Contains(rec.Body.String(), ".secret") {
		t.Errorf("the refusal of a link should say where it leads: %s", rec.Body)
	}
}

// A path with a dot component is not browsable, whether the directory is there or
// not; the listing of its parent leaves it out.
func TestFSRefusesHiddenPaths(t *testing.T) {
	f := newFolderEnv(t, "")
	root := dotFreeTempDir(t)
	for _, d := range []string{"visible/inner", ".hidden/inner", "visible/.git"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Controls: the visible ones list, and the hidden ones are on disk.
	wantStatus(t, f.get("/api/fs?path="+filepath.Join(root, "visible")), 200)
	wantStatus(t, f.get("/api/fs?path="+filepath.Join(root, "visible", "inner")), 200)
	if _, err := os.Stat(filepath.Join(root, ".hidden", "inner")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		filepath.Join(root, ".hidden"),
		filepath.Join(root, ".hidden", "inner"),
		filepath.Join(root, "visible", ".git"),
		filepath.Join(root, ".nowhere"), // not there: still refused, not a 404
		filepath.Join(root, ".nowhere", "x"),
	} {
		wantJSONError(t, f.get("/api/fs?path="+p), 403)
	}
	// "..", "." and dots inside a name are not dot components once cleaned or are no
	// leading dots.
	wantStatus(t, f.get("/api/fs?path="+filepath.Join(root, "visible", "inner", "..")), 200)
	if err := os.Mkdir(filepath.Join(root, "a.b"), 0o755); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, f.get("/api/fs?path="+filepath.Join(root, "a.b")), 200)
}

// /Volumes/Macintosh HD is a link to the whole disk: not a place to start from.
func TestListPlacesLeavesOutLinksToTheWholeDisk(t *testing.T) {
	tmp := t.TempDir()
	vols := filepath.Join(tmp, "Volumes")
	for _, d := range []string{filepath.Join(vols, "Backup"), filepath.Join(tmp, "elsewhere")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("/", filepath.Join(vols, "Macintosh HD")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(tmp, "elsewhere"), filepath.Join(vols, "Share")); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range listPlaces("", []string{vols}, nil) {
		names = append(names, p.Name)
	}
	// Control: other links and real directories stay.
	if want := []string{"Backup", "Share"}; !slices.Equal(names, want) {
		t.Errorf("places = %v, want %v", names, want)
	}
	// Control that the link is a directory the listing would otherwise show.
	dirs, _ := subdirs(vols)
	var all []string
	for _, d := range dirs {
		all = append(all, d.Name)
	}
	if !slices.Contains(all, "Macintosh HD") {
		t.Errorf("control: subdirs does not list the link: %v", all)
	}
}
