//go:build unix

package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The data dir holds the database with the TMDB key. Where the id is the first
// thing written to it, ServerID makes the directory: for its owner only. A
// directory that already exists is the user's own and is left as it is.
func TestServerIDMakesAPrivateDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data dir", "ply")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the data dir should not exist yet: %v", err)
	}
	if _, err := ServerID(dir); err != nil {
		t.Fatal(err)
	}
	if m := dirMode(t, dir); m != 0o700 {
		t.Errorf("new data dir mode = %v, want 0700", m)
	}

	existing := t.TempDir()
	if err := os.Chmod(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ServerID(existing); err != nil {
		t.Fatal(err)
	}
	if m := dirMode(t, existing); m != 0o755 {
		t.Errorf("existing data dir mode = %v, want it left at 0755", m)
	}
}

// The URL tokens' secret is the owner's to read, like the database.
func TestURLKeyIsPrivate(t *testing.T) {
	dir := t.TempDir()
	if _, err := URLKey(dir); err != nil {
		t.Fatal(err)
	}
	if m := dirMode(t, filepath.Join(dir, "url-key")); m != 0o600 {
		t.Errorf("url-key mode = %v, want 0600", m)
	}
	if _, err := ServerID(dir); err != nil {
		t.Fatal(err)
	}
	if m := dirMode(t, filepath.Join(dir, "server-id")); m != 0o644 { // control: the id is not secret
		t.Errorf("server-id mode = %v, want 0644", m)
	}
}

func dirMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}
