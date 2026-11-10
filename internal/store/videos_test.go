package store

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// Version 13 on a database of version 12.
func TestMigration013OnAV12Database(t *testing.T) {
	old, path := openAtVersion(t, 12)
	if _, err := old.rw.Exec(`SELECT 1 FROM title_videos`); err == nil {
		t.Fatal("control: version 12 has title_videos")
	}
	if cols := columns(t, old.rw, "titles"); slices.Contains(cols, "videos_ref") {
		t.Fatalf("control: version 12 titles = %v", cols)
	}
	if _, err := old.rw.Exec(`INSERT INTO titles (id, kind, tmdb_id, title, source, created_at, updated_at) VALUES (7, 'movie', 603, 'The Matrix', 'tmdb', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	// The title is there, never checked.
	got, err := s.GetTitleVideos(ctx, 7)
	if err != nil || got.Ref != "" || got.CheckedAt != 0 || len(got.Videos) != 0 {
		t.Errorf("an old title: %+v, %v", got, err)
	}
	insert := func(site, key, typ string) error {
		_, err := s.rw.Exec(`INSERT OR REPLACE INTO title_videos (title_id, ord, site, key, type) VALUES (7, 0, ?, ?, ?)`, site, key, typ)
		return err
	}
	if err := insert("youtube", "vKQi3bBA1y8", "trailer"); err != nil {
		t.Fatalf("control: a video was refused: %v", err)
	}
	if insert("dailymotion", "x7tgad0", "") == nil || insert("youtube", "", "") == nil || insert("youtube", "vKQi3bBA1y8", "clip") == nil {
		t.Error("a video of another site, with no key or of another type was accepted")
	}
	if _, err := s.rw.Exec(`DELETE FROM titles WHERE id = 7`); err != nil {
		t.Fatal(err)
	}
	if n := queryInt(t, s, `SELECT COUNT(*) FROM title_videos`); n != 0 {
		t.Errorf("%d videos left after their title went", n)
	}
	if v := queryInt(t, s, `SELECT MAX(version) FROM schema_version`); v != slices.Max(migrationNumbers(t)) || v < 13 {
		t.Errorf("schema version = %d", v)
	}
}

func TestTitleVideosAreReplacedWithoutChangingTheLists(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	_, titleID := addMovie(t, s, "/media/m.mkv", tmdbMovie(603, "The Matrix", 1999))
	a := Video{Site: SiteYouTube, Key: "vKQi3bBA1y8", Name: "Official Trailer", Type: VideoTrailer, Language: "en", Official: true, PublishedAt: "1999-03-01"}
	b := Video{Site: SiteVimeo, Key: "76979871", Name: "Teaser", Type: VideoTeaser}
	c := Video{Site: SiteYouTube, Key: "HhesaQXLuRY"}

	gen := s.Generation()
	if err := s.SetTitleVideos(ctx, titleID, "tmdb:603", []Video{a, b}, 100); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTitleVideos(ctx, titleID)
	if err != nil || got.Ref != "tmdb:603" || got.CheckedAt != 100 || !slices.Equal(got.Videos, []Video{a, b}) {
		t.Fatalf("stored: %+v, %v", got, err)
	}
	if s.Generation() != gen {
		t.Error("storing trailers changed Generation: every list's ETag went stale")
	}
	// Positive control: a write a list shows does change it.
	if _, err := s.UpsertTitle(ctx, tmdbMovie(603, "The Matrix", 1999)); err != nil || s.Generation() == gen {
		t.Fatalf("control: a title write left Generation at %d (%v)", gen, err)
	}
	// A refresh of the title keeps them: they have their own age.
	if got, _ := s.GetTitleVideos(ctx, titleID); !slices.Equal(got.Videos, []Video{a, b}) || got.Ref != "tmdb:603" || got.CheckedAt != 100 {
		t.Errorf("after a refresh of the title: %+v", got)
	}

	// Replaced in the order given; none is a title checked that has none.
	if err := s.SetTitleVideos(ctx, titleID, "tmdb:603", []Video{c, a}, 200); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetTitleVideos(ctx, titleID); !slices.Equal(got.Videos, []Video{c, a}) || got.CheckedAt != 200 {
		t.Errorf("replaced: %+v", got)
	}
	if err := s.SetTitleVideos(ctx, titleID, "tmdb:603", nil, 300); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetTitleVideos(ctx, titleID); len(got.Videos) != 0 || got.CheckedAt != 300 {
		t.Errorf("none: %+v", got)
	}

	// A video that is not one is refused, and nothing changes.
	if err := s.SetTitleVideos(ctx, titleID, "tmdb:603", []Video{b}, 350); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Video{{Site: SiteYouTube, Key: `"><script>`}, {Site: "dailymotion", Key: "x7tgad0"}, {Site: SiteVimeo, Key: "abc"}} {
		if err := s.SetTitleVideos(ctx, titleID, "tmdb:603", []Video{a, bad}, 400); err == nil {
			t.Errorf("%+v was stored", bad)
		}
	}
	if got, _ := s.GetTitleVideos(ctx, titleID); got.CheckedAt != 350 || got.Ref != "tmdb:603" || !slices.Equal(got.Videos, []Video{b}) {
		t.Errorf("after the refusals: %+v", got)
	}

	if err := s.SetTitleVideos(ctx, titleID+100, "tmdb:1", []Video{a}, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("no such title, set: %v", err)
	}
	if _, err := s.GetTitleVideos(ctx, titleID+100); !errors.Is(err, ErrNotFound) {
		t.Errorf("no such title, get: %v", err)
	}
}

func TestValidVideo(t *testing.T) {
	for _, tc := range []struct {
		site, key string
		ok        bool
	}{
		{SiteYouTube, "vKQi3bBA1y8", true},
		{SiteYouTube, "a-b_c-d_e-f", true},
		{SiteYouTube, "vKQi3bBA1y", false},
		{SiteYouTube, "vKQi3bBA1y8x", false},
		{SiteYouTube, "vKQi3bBA1y/", false},
		{SiteYouTube, "../../a.jpg", false},
		{SiteVimeo, "76979871", true},
		{SiteVimeo, "7697987a", false},
		{SiteVimeo, "1234567890123", false},
		{SiteVimeo, "", false},
		{"YouTube", "vKQi3bBA1y8", false}, // the stored name only
		{"", "vKQi3bBA1y8", false},
	} {
		if got := ValidVideo(tc.site, tc.key); got != tc.ok {
			t.Errorf("ValidVideo(%q, %q) = %v", tc.site, tc.key, got)
		}
	}
}
