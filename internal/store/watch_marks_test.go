package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"
)

func TestEpisodeCountsDone(t *testing.T) {
	for _, tc := range []struct {
		c    EpisodeCounts
		want bool
	}{
		{EpisodeCounts{}, false}, // nothing to watch is not watched
		{EpisodeCounts{EpisodeCount: 2, Watched: 2}, true},
		{EpisodeCounts{EpisodeCount: 2, Watched: 1}, false},
		{EpisodeCounts{EpisodeCount: 2, InProgress: 2}, false},
		{EpisodeCounts{EpisodeCount: 1}, false},
	} {
		if got := tc.c.Done(); got != tc.want {
			t.Errorf("%+v.Done() = %v, want %v", tc.c, got, tc.want)
		}
	}
}

func showCounts(t *testing.T, s *Store, name string) Show {
	t.Helper()
	shows, err := s.Shows(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, sh := range shows {
		if sh.Name == name {
			return sh
		}
	}
	t.Fatalf("no show %q", name)
	return Show{}
}

// Two files of one episode are one episode, watched when either is, and started when neither is watched
// and one is started.
func TestTwoCopiesOfAnEpisodeAreOneEpisode(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	e1 := addEpisode(t, s, "Alpha", 1, 1)
	e1b, err := s.UpsertItem(ctx, episode("/media/Alpha/S01E01 (2160p).mkv", "Alpha", 1, 1), nil)
	if err != nil {
		t.Fatal(err)
	}
	e2 := addEpisode(t, s, "Alpha", 1, 2)
	want := func(what string, c EpisodeCounts) {
		t.Helper()
		got := showCounts(t, s, "Alpha")
		if got.EpisodeCounts != c || got.BySeason[1] != c {
			t.Errorf("%s: show %+v, season 1 %+v; want %+v", what, got.EpisodeCounts, got.BySeason[1], c)
		}
	}
	want("three files, two episodes", EpisodeCounts{EpisodeCount: 2})

	if _, err := s.SetProgress(ctx, e1b, 100, 2400); err != nil { // the copy is started
		t.Fatal(err)
	}
	want("one copy started", EpisodeCounts{EpisodeCount: 2, InProgress: 1})
	if _, err := s.SetProgress(ctx, e1, 2300, 2400); err != nil { // the other is watched to the end
		t.Fatal(err)
	}
	want("one copy watched, the other started", EpisodeCounts{EpisodeCount: 2, Watched: 1})
	if _, err := s.SetProgress(ctx, e2, 2300, 2400); err != nil {
		t.Fatal(err)
	}
	want("both episodes watched", EpisodeCounts{EpisodeCount: 2, Watched: 2})
	if !showCounts(t, s, "Alpha").Done() {
		t.Error("a show whose episodes are all watched in one copy or the other is not done")
	}

	// The series card counts the same way, whatever the copies and whatever the shows.
	title, err := s.UpsertTitle(ctx, tmdbSeries(1396, "Alpha", 2008))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSeriesMatch(ctx, "Alpha", Match{TitleID: title, State: MatchMatched, Score: 0.9}); err != nil {
		t.Fatal(err)
	}
	cards, err := s.Titles(ctx, TitleFilter{})
	if err != nil || len(cards) != 1 || *cards[0].Episodes != (EpisodeCounts{EpisodeCount: 2, Watched: 2}) || !cards[0].Watched {
		t.Errorf("the card = %+v, %v", cards, err)
	}

	// Files with no number in their name are an episode each.
	addEpisode(t, s, "Beta", 1, 0)
	if _, err := s.UpsertItem(ctx, episode("/media/Beta/Other.mkv", "Beta", 1, 0), nil); err != nil {
		t.Fatal(err)
	}
	if got := showCounts(t, s, "Beta").EpisodeCount; got != 2 {
		t.Errorf("two files with no number are %d episodes, want 2", got)
	}
}

// The specials do not keep a show from being watched; they have counts of their own.
func TestSpecialsDoNotKeepAShowFromBeingDone(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	special := addEpisode(t, s, "Alpha", 0, 1)
	for ep := 1; ep <= 2; ep++ {
		if _, err := s.SetProgress(ctx, addEpisode(t, s, "Alpha", 1, ep), 2300, 2400); err != nil {
			t.Fatal(err)
		}
	}
	sh := showCounts(t, s, "Alpha")
	if want := (EpisodeCounts{EpisodeCount: 2, Watched: 2}); sh.EpisodeCounts != want || !sh.Done() {
		t.Errorf("the show = %+v (done %v), want %+v and done", sh.EpisodeCounts, sh.Done(), want)
	}
	if got, want := sh.BySeason[0], (EpisodeCounts{EpisodeCount: 1}); got != want || got.Done() {
		t.Errorf("the specials = %+v, want %+v, not done", got, want)
	}
	if got := sh.BySeason[1]; !got.Done() {
		t.Errorf("season 1 = %+v, want done", got)
	}
	// The series is done too, in its card.
	title, err := s.UpsertTitle(ctx, tmdbSeries(1396, "Alpha", 2008))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSeriesMatch(ctx, "Alpha", Match{TitleID: title, State: MatchMatched, Score: 0.9}); err != nil {
		t.Fatal(err)
	}
	if cards, _ := s.Titles(ctx, TitleFilter{}); len(cards) != 1 || !cards[0].Watched || *cards[0].Episodes != (EpisodeCounts{EpisodeCount: 2, Watched: 2}) {
		t.Errorf("the card = %+v", cards)
	}
	// Positive control: a regular episode that is not watched keeps it from being done, specials watched or not.
	addEpisode(t, s, "Alpha", 1, 3)
	if _, err := s.SetProgress(ctx, special, 2300, 2400); err != nil {
		t.Fatal(err)
	}
	if sh := showCounts(t, s, "Alpha"); sh.Done() || sh.Watched != 2 || sh.EpisodeCount != 3 {
		t.Errorf("a show with a regular episode to watch = %+v", sh.EpisodeCounts)
	}

	// A show of nothing but specials is counted by them.
	only := addEpisode(t, s, "Extras", 0, 1)
	if sh := showCounts(t, s, "Extras"); sh.EpisodeCounts != (EpisodeCounts{EpisodeCount: 1}) || sh.Done() {
		t.Errorf("a show of specials = %+v", sh.EpisodeCounts)
	}
	if _, err := s.SetProgress(ctx, only, 2300, 2400); err != nil {
		t.Fatal(err)
	}
	if sh := showCounts(t, s, "Extras"); !sh.Done() {
		t.Errorf("a show of specials, watched = %+v", sh.EpisodeCounts)
	}
}

// marks is the number of marks waiting.
func marks(t *testing.T, s *Store) int { t.Helper(); return count(t, s, "title_watch") }

// A film that is upgraded (the new file at another path, the old one deleted) is still watched, with
// its time and its count.
func TestFilmUpgradeKeepsTheFilmWatched(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	old, title := addMovie(t, s, "/media/Film (2020) 1080p.mkv", tmdbMovie(603, "The Matrix", 1999))
	if _, err := s.SetItemWatched(ctx, old, true); err != nil {
		t.Fatal(err)
	}
	setWatchedAt(t, s, old, 1000, 2)

	// The new file is indexed (not matched yet), and the old one goes.
	fresh, err := s.UpsertItem(ctx, movie("/media/Film (2020) 2160p.mkv", "The Matrix"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteItems(ctx, []int64{old}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, "progress"); n != 0 { // positive control: the old file's row went with it
		t.Fatalf("control: %d progress rows after the old file was deleted", n)
	}
	if n := marks(t, s); n != 1 {
		t.Fatalf("%d marks after a watched film was deleted, want 1", n)
	}
	if p := entryProgress(t, s, fresh); p != nil {
		t.Fatalf("control: the new file has progress %+v before it is linked", p)
	}

	// The title waits for the file: nothing takes it away. (Control: one nobody waits for is pruned.)
	bareItem, bare := addMovie(t, s, "/media/Bare (2001).mkv", tmdbMovie(700, "Bare", 2001))
	if err := s.DeleteItems(ctx, []int64{bareItem}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PruneMetadata(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetTitle(ctx, bare); !errors.Is(err, ErrNotFound) {
		t.Errorf("control: the title nobody waits for is still there: %v", err)
	}
	if _, err := s.GetTitle(ctx, title); err != nil {
		t.Fatalf("the title a mark waits at was pruned: %v", err)
	}

	if err := s.SetItemMatch(ctx, fresh, Match{TitleID: title, State: MatchMatched, Score: 0.95}); err != nil {
		t.Fatal(err)
	}
	p := entryProgress(t, s, fresh)
	if p == nil || p.WatchedAt != 1000 || p.PlayCount != 2 || p.PositionSec != 0 || !p.Watched() {
		t.Errorf("the new file after it was linked = %+v, want watched at 1000, twice, at position 0", p)
	}
	if n := marks(t, s); n != 0 {
		t.Errorf("%d marks left after one was applied", n)
	}
	if w := mustFilmWatch(t, s, title); w.PlayCount != 2 {
		t.Errorf("the film = %+v", w)
	}
}

func mustFilmWatch(t *testing.T, s *Store, title int64) FilmWatch {
	t.Helper()
	m, err := s.FilmWatches(context.Background(), []int64{title})
	if err != nil {
		t.Fatal(err)
	}
	w, ok := m[title]
	if !ok {
		t.Fatalf("the film %d is not watched", title)
	}
	return w
}

// The other order: the new file is linked to the title first, and the old one is deleted after. The
// new one has the state at once, in the same write that deletes the old one.
func TestFilmUpgradeWithTheNewFileLinkedFirst(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	old, title := addMovie(t, s, "/media/Film 1080p.mkv", tmdbMovie(603, "The Matrix", 1999))
	if _, err := s.SetItemWatched(ctx, old, true); err != nil {
		t.Fatal(err)
	}
	setWatchedAt(t, s, old, 1000, 3)
	fresh := addVersion(t, s, "/media/Film 2160p.mkv", title)
	if p := entryProgress(t, s, fresh); p != nil {
		t.Fatalf("control: a second version has progress %+v", p)
	}
	if err := s.DeleteItems(ctx, []int64{old}); err != nil {
		t.Fatal(err)
	}
	if p := entryProgress(t, s, fresh); p == nil || p.WatchedAt != 1000 || p.PlayCount != 3 {
		t.Errorf("the version that stays = %+v, want watched at 1000, three times", p)
	}
	if n := marks(t, s); n != 0 {
		t.Errorf("%d marks left although the film has a file to carry it", n)
	}
}

func matchedShow(t *testing.T, s *Store, show string, tmdb int64) int64 {
	t.Helper()
	ctx := context.Background()
	title, err := s.UpsertTitle(ctx, tmdbSeries(tmdb, show, 2008))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSeriesMatch(ctx, show, Match{TitleID: title, State: MatchMatched, Score: 0.9}); err != nil {
		t.Fatal(err)
	}
	return title
}

// An episode that is upgraded is still watched; other episodes, other seasons, and other shows
// are not touched by its mark.
func TestEpisodeUpgradeKeepsTheEpisodeWatched(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	e1, e2 := addEpisode(t, s, "Alpha", 1, 1), addEpisode(t, s, "Alpha", 1, 2)
	matchedShow(t, s, "Alpha", 1396)
	other := addEpisode(t, s, "Beta", 1, 2) // another series, with the same numbers
	matchedShow(t, s, "Beta", 1399)
	if _, err := s.SetItemWatched(ctx, e2, true); err != nil {
		t.Fatal(err)
	}
	setWatchedAt(t, s, e2, 1000, 2)

	if err := s.DeleteItems(ctx, []int64{e2}); err != nil {
		t.Fatal(err)
	}
	if n := marks(t, s); n != 1 {
		t.Fatalf("%d marks after a watched episode was deleted, want 1", n)
	}
	// Files that are not that episode: other numbers, another season, another series' S01E02 (already
	// there, and not watched).
	notIt := []int64{addEpisode(t, s, "Alpha", 1, 3), addEpisode(t, s, "Alpha", 2, 2)}
	for _, id := range append(notIt, e1, other) {
		if p := entryProgress(t, s, id); p != nil {
			t.Errorf("file %d, which is not the episode, has %+v", id, p)
		}
	}
	if n := marks(t, s); n != 1 {
		t.Fatalf("%d marks after files that are not the episode arrived, want the 1 that waits", n)
	}

	fresh, err := s.UpsertItem(ctx, episode("/media/Alpha/Alpha.S01E02.2160p.mkv", "Alpha", 1, 2), nil)
	if err != nil {
		t.Fatal(err)
	}
	if p := entryProgress(t, s, fresh); p == nil || p.WatchedAt != 1000 || p.PlayCount != 2 || p.PositionSec != 0 {
		t.Errorf("the new file of the episode = %+v, want watched at 1000, twice", p)
	}
	if n := marks(t, s); n != 0 {
		t.Errorf("%d marks left after one was applied", n)
	}
	if sh := showCounts(t, s, "Alpha"); sh.BySeason[1].Watched != 1 {
		t.Errorf("season 1 after the upgrade = %+v", sh.BySeason[1])
	}
}

// The new file of an episode may arrive before the old one goes.
func TestEpisodeUpgradeWithTheNewFileFirst(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	e1 := addEpisode(t, s, "Alpha", 1, 1)
	matchedShow(t, s, "Alpha", 1396)
	if _, err := s.SetItemWatched(ctx, e1, true); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.UpsertItem(ctx, episode("/media/Alpha/S01E01 upgraded.mkv", "Alpha", 1, 1), nil)
	if err != nil {
		t.Fatal(err)
	}
	if p := entryProgress(t, s, fresh); p != nil {
		t.Fatalf("control: the second copy has %+v", p)
	}
	if err := s.DeleteItems(ctx, []int64{e1}); err != nil {
		t.Fatal(err)
	}
	if p := entryProgress(t, s, fresh); p == nil || !p.Watched() {
		t.Errorf("the copy that stays = %+v, want watched", p)
	}
	if n := marks(t, s); n != 0 {
		t.Errorf("%d marks left", n)
	}
}

// A mark belongs to its title: another title does not take it, and a file that no title is known for leaves none.
func TestMarksDoNotCarryOverToAnotherTitle(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	film, filmTitle := addMovie(t, s, "/media/A (2000).mkv", tmdbMovie(603, "A", 2000))
	if _, err := s.SetItemWatched(ctx, film, true); err != nil {
		t.Fatal(err)
	}
	ep := addEpisode(t, s, "Alpha", 1, 1)
	matchedShow(t, s, "Alpha", 1396)
	if _, err := s.SetItemWatched(ctx, ep, true); err != nil {
		t.Fatal(err)
	}
	loose, _ := s.UpsertItem(ctx, movie("/media/Loose (1999).mkv", "Loose"), nil) // no title
	unmatched := addEpisode(t, s, "Gamma", 1, 1)                                  // no series title
	for _, id := range []int64{loose, unmatched} {
		if _, err := s.SetItemWatched(ctx, id, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteItems(ctx, []int64{film, ep, loose, unmatched}); err != nil {
		t.Fatal(err)
	}
	if n := marks(t, s); n != 2 { // the film's and the episode's; the files with no title left none
		t.Fatalf("%d marks, want 2", n)
	}

	// Another film is linked, another series gets the same episode: neither is watched, and both marks wait.
	other, otherTitle := addMovie(t, s, "/media/B (2001).mkv", tmdbMovie(604, "B", 2001))
	betaEp := addEpisode(t, s, "Beta", 1, 1)
	matchedShow(t, s, "Beta", 1399)
	for _, id := range []int64{other, betaEp} {
		if p := entryProgress(t, s, id); p != nil {
			t.Errorf("file %d of another title has %+v", id, p)
		}
	}
	if otherTitle == filmTitle || marks(t, s) != 2 {
		t.Errorf("%d marks left after other titles arrived, want both", marks(t, s))
	}
	// Positive control: the right title's file takes its mark.
	again := addVersion(t, s, "/media/A (2000) 2160p.mkv", filmTitle)
	if p := entryProgress(t, s, again); p == nil || !p.Watched() || marks(t, s) != 1 {
		t.Errorf("the file of the title = %+v (%d marks)", p, marks(t, s))
	}
}

// Marked unwatched, a title, a season or a show leaves no mark to bring it back.
func TestUnmarkingDeletesTheMarksToo(t *testing.T) {
	for name, unmark := range map[string]func(*Store, int64) error{
		"the season": func(s *Store, _ int64) error {
			_, err := s.SetSeasonWatched(context.Background(), "Alpha", 1, false)
			return err
		},
		"the show": func(s *Store, _ int64) error {
			_, err := s.SetShowWatched(context.Background(), "Alpha", false)
			return err
		},
		"the title": func(s *Store, title int64) error {
			_, err := s.SetTitleWatched(context.Background(), title, false)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := openTemp(t)
			ctx := context.Background()
			e1, e2 := addEpisode(t, s, "Alpha", 1, 1), addEpisode(t, s, "Alpha", 1, 2)
			title := matchedShow(t, s, "Alpha", 1396)
			if _, err := s.SetShowWatched(ctx, "Alpha", true); err != nil {
				t.Fatal(err)
			}
			if err := s.DeleteItems(ctx, []int64{e2}); err != nil { // its file goes, and waits for another
				t.Fatal(err)
			}
			if marks(t, s) != 1 {
				t.Fatalf("control: %d marks", marks(t, s))
			}
			if err := unmark(s, title); err != nil {
				t.Fatal(err)
			}
			if p := entryProgress(t, s, e1); p != nil {
				t.Fatalf("control: the episode that stayed is still %+v", p)
			}
			if n := marks(t, s); n != 0 {
				t.Errorf("%d marks left after %s was marked unwatched", n, name)
			}
			again, err := s.UpsertItem(ctx, episode("/media/Alpha/S01E02 again.mkv", "Alpha", 1, 2), nil)
			if err != nil {
				t.Fatal(err)
			}
			if p := entryProgress(t, s, again); p != nil {
				t.Errorf("the new file of an episode that was marked unwatched has %+v", p)
			}
		})
	}

	// Another season's marks stay (control), and so does a mark when an item of another episode is unmarked.
	s, _ := openTemp(t)
	ctx := context.Background()
	addEpisode(t, s, "Alpha", 1, 1)
	s2e1, s2e2 := addEpisode(t, s, "Alpha", 2, 1), addEpisode(t, s, "Alpha", 2, 2)
	matchedShow(t, s, "Alpha", 1396)
	if _, err := s.SetShowWatched(ctx, "Alpha", true); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteItems(ctx, []int64{s2e2}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetSeasonWatched(ctx, "Alpha", 1, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetItemWatched(ctx, s2e1, false); err != nil {
		t.Fatal(err)
	}
	if n := marks(t, s); n != 1 {
		t.Errorf("%d marks after unmarking another season and another episode, want the 1 that stays", n)
	}
	// The item itself: a mark of its episode that is somehow there goes with it.
	if _, err := s.rw.Exec(`INSERT INTO title_watch (title_id, season, episode, watched_at, play_count, removed_at)
		SELECT title_id, 2, 1, 5, 1, 5 FROM series_matches WHERE show = 'Alpha'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetItemWatched(ctx, s2e1, false); err != nil {
		t.Fatal(err)
	}
	if n := marks(t, s); n != 1 {
		t.Errorf("%d marks after unmarking an episode that had one, want only the other season's episode's", n)
	}
}

// A mark does not wait for ever, and while it waits it keeps its title.
func TestMarksExpire(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	oldFilm, oldTitle := addMovie(t, s, "/media/Old (1990).mkv", tmdbMovie(800, "Old", 1990))
	youngFilm, youngTitle := addMovie(t, s, "/media/Young (2020).mkv", tmdbMovie(801, "Young", 2020))
	for _, id := range []int64{oldFilm, youngFilm} {
		if _, err := s.SetItemWatched(ctx, id, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteItems(ctx, []int64{oldFilm, youngFilm}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.rw.Exec(`UPDATE title_watch SET removed_at = ? WHERE title_id = ?`, time.Now().Add(-markTTL-time.Hour).Unix(), oldTitle); err != nil {
		t.Fatal(err)
	}
	if marks(t, s) != 2 {
		t.Fatalf("control: %d marks", marks(t, s))
	}
	if _, err := s.PruneMetadata(ctx); err != nil {
		t.Fatal(err)
	}
	if n := marks(t, s); n != 1 {
		t.Errorf("%d marks after the prune, want only the young one", n)
	}
	if _, err := s.GetTitle(ctx, oldTitle); !errors.Is(err, ErrNotFound) {
		t.Errorf("the title of a mark that expired is still there: %v", err)
	}
	if _, err := s.GetTitle(ctx, youngTitle); err != nil {
		t.Errorf("the title of a young mark was pruned: %v", err)
	}
}

// Taking a folder out of the library leaves no marks: its files' state is in the archive (by path), or
// was to be forgotten.
func TestRemovingAFolderLeavesNoMarks(t *testing.T) {
	for _, forget := range []bool{false, true} {
		t.Run(fmt.Sprintf("forget=%v", forget), func(t *testing.T) {
			s, _ := openTemp(t)
			ctx := context.Background()
			it := movie("/lib/seen.mkv", "Seen")
			it.Root, it.Size = "/lib", 100
			id, _ := s.UpsertItem(ctx, it, nil)
			title, _ := s.UpsertTitle(ctx, tmdbMovie(603, "Seen", 2000))
			if err := s.SetItemMatch(ctx, id, Match{TitleID: title, State: MatchMatched, Score: 1}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.SetItemWatched(ctx, id, true); err != nil {
				t.Fatal(err)
			}
			if n, err := s.ArchiveAndDeleteRoot(ctx, "/lib", forget); err != nil || n != 1 {
				t.Fatalf("ArchiveAndDeleteRoot = %d, %v", n, err)
			}
			if n := marks(t, s); n != 0 {
				t.Errorf("%d marks after a folder was removed", n)
			}
			archived := count(t, s, "item_archive")
			if forget == (archived != 0) { // positive control: the archive has it unless it was to be forgotten
				t.Errorf("%d archive rows with forget=%v", archived, forget)
			}
		})
	}
}

// Version 12 on a database of version 11.
func TestMigration012OnAV11Database(t *testing.T) {
	old, path := openAtVersion(t, 11)
	if cols := columns(t, old.rw, "progress"); !slices.Contains(cols, "watched_at") {
		t.Fatalf("control: version 11 progress = %v", cols)
	}
	if _, err := old.rw.Exec(`SELECT 1 FROM title_watch`); err == nil {
		t.Fatal("control: version 11 has title_watch")
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
	if got, want := columns(t, s.rw, "title_watch"), []string{"title_id", "season", "episode", "watched_at", "play_count", "removed_at"}; !slices.Equal(got, want) {
		t.Errorf("title_watch columns = %v, want %v", got, want)
	}
	insert := func(at int64, plays int) error {
		_, err := s.rw.Exec(`INSERT OR REPLACE INTO title_watch (title_id, watched_at, play_count, removed_at) VALUES (7, ?, ?, 1)`, at, plays)
		return err
	}
	if err := insert(5, 1); err != nil {
		t.Fatalf("control: a mark was refused: %v", err)
	}
	if insert(0, 1) == nil || insert(5, 0) == nil {
		t.Error("a mark with no time or no count was accepted")
	}
	if _, err := s.rw.Exec(`DELETE FROM titles WHERE id = 7`); err != nil {
		t.Fatal(err)
	}
	if n := marks(t, s); n != 0 {
		t.Errorf("%d marks left after their title went", n)
	}
	if v := queryInt(t, s, `SELECT MAX(version) FROM schema_version`); v != slices.Max(migrationNumbers(t)) || v < 12 {
		t.Errorf("schema version = %d", v)
	}
}
