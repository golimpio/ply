package library

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSameDirInsideAndOverlap(t *testing.T) {
	base := t.TempDir()
	mk := func(rel string) string {
		t.Helper()
		p := filepath.Join(base, rel)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	link := func(name, target string) string {
		t.Helper()
		p := filepath.Join(base, name)
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	movies, extras, other := mk("movies"), mk("movies/extras"), mk("other")
	mk("movies2")
	alias := link("alias", movies)
	into := link("into", extras)
	above := link("above", base)
	disk := link("disk", "/")
	viaParent := link("via-parent", base)
	gone := filepath.Join(base, "gone")

	type rel struct{ same, aInB, bInA bool } // for (a, b): a is b; a is b or in it; b is a or in it
	tests := []struct {
		name string
		a, b string
		want rel
	}{
		{"the same name", movies, movies, rel{true, true, true}},
		{"a folder and one below it", extras, movies, rel{false, true, false}},
		{"unrelated folders", movies, other, rel{false, false, false}},
		{"names that start alike", movies, filepath.Join(base, "movies2"), rel{false, false, false}},
		{"a symlink to the folder", alias, movies, rel{true, true, true}},
		{"a symlink into the folder", into, movies, rel{false, true, false}},
		{"the folder through a symlinked parent", filepath.Join(viaParent, "movies"), movies, rel{true, true, true}},
		{"below it through a symlinked parent", filepath.Join(viaParent, "movies", "extras"), movies, rel{false, true, false}},
		{"a symlink to the parent", above, movies, rel{false, false, true}},
		{"a symlink to the whole disk", disk, movies, rel{false, false, true}},
		{"a folder that is not there, by name", gone, gone, rel{true, true, true}},
		{"a folder that is not there, below one that is", filepath.Join(movies, "nope"), movies, rel{false, true, false}},
		{"a folder that is not there, above one that is", gone, filepath.Join(gone, "x"), rel{false, false, true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rel{SameDir(tt.a, tt.b), Inside(tt.b, tt.a), Inside(tt.a, tt.b)}
			if got != tt.want {
				t.Errorf("(same, a in b, b in a) = %v, want %v", got, tt.want)
			}
			if want := tt.want.aInB || tt.want.bInA; Overlap(tt.a, tt.b) != want || Overlap(tt.b, tt.a) != want {
				t.Errorf("Overlap = %v, want %v (in both orders)", Overlap(tt.a, tt.b), want)
			}
		})
	}
}

// On a disk that ignores case (APFS by default) another spelling is the folder.
func TestSameDirIgnoresCaseWhereTheDiskDoes(t *testing.T) {
	base := t.TempDir()
	lower := filepath.Join(base, "movies")
	if err := os.MkdirAll(filepath.Join(lower, "extras"), 0o755); err != nil {
		t.Fatal(err)
	}
	upper := filepath.Join(base, "MOVIES")
	if _, err := os.Stat(upper); err != nil {
		t.Skip("this file system tells movies and MOVIES apart")
	}
	if !SameDir(lower, upper) || !Inside(lower, filepath.Join(upper, "extras")) || !Overlap(upper, filepath.Join(lower, "extras")) {
		t.Error("another case of a folder is not that folder")
	}
}
