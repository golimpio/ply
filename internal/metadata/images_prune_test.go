package metadata

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Prune drops the files of refs the library no longer uses, and keeps the
// current poster's sizes and the covers of items that are still there.
func TestImageCachePrune(t *testing.T) {
	dir := t.TempDir()
	c := &ImageCache{dir: dir}
	mk := func(rel string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{
		"tmdb/w342/keep.jpg", "tmdb/w500/keep.jpg", "tmdb/w342/gone.jpg", "tmdb/w342/.dl-inflight",
		"tvdb/w342/banners/posters/keep.jpg", "tvdb/w342/banners/posters/gone.jpg",
		"embedded/7.jpg", "embedded/9.jpg",
	} {
		mk(f)
	}
	refs := map[string]bool{"tmdb:/keep.jpg": true, "tvdb:/banners/posters/keep.jpg": true}
	items := map[int64]int64{7: 1}
	n, err := c.Prune(context.Background(), refs, items)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("pruned %d files, want 3", n)
	}
	for _, keep := range []string{"tmdb/w342/keep.jpg", "tmdb/w500/keep.jpg", "tmdb/w342/.dl-inflight", "tvdb/w342/banners/posters/keep.jpg", "embedded/7.jpg"} {
		if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
			t.Errorf("a live file was removed: %s (%v)", keep, err)
		}
	}
	for _, gone := range []string{"tmdb/w342/gone.jpg", "tvdb/w342/banners/posters/gone.jpg", "embedded/9.jpg"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Errorf("an orphan was kept: %s", gone)
		}
	}
}

func TestImageCachePruneNilAndEmpty(t *testing.T) {
	var c *ImageCache
	if n, err := c.Prune(context.Background(), nil, nil); n != 0 || err != nil {
		t.Errorf("nil cache: %d, %v", n, err)
	}
	empty := &ImageCache{dir: t.TempDir()}
	if n, err := empty.Prune(context.Background(), nil, nil); n != 0 || err != nil {
		t.Errorf("empty cache: %d, %v", n, err)
	}
}