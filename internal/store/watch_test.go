package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"
)

// entryProgress is the progress the store gives for an item (nil: none).
func entryProgress(t *testing.T, s *Store, id int64) *Progress {
	t.Helper()
	e, err := s.GetEntry(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return e.Progress
}

// addVersion indexes another file of a film that already has a title.
func addVersion(t *testing.T, s *Store, path string, titleID int64) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := s.UpsertItem(ctx, movie(path, "A film"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetItemMatch(ctx, id, Match{TitleID: titleID, State: MatchMatched, Score: 0.95}); err != nil {
		t.Fatal(err)
	}
	return id
}

func addEpisode(t *testing.T, s *Store, show string, season, ep int) int64 {
	t.Helper()
	id, err := s.UpsertItem(context.Background(), episode(fmt.Sprintf("/media/%s/S%02dE%02d.mkv", show, season, ep), show, season, ep), nil)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func coveredIDs(items []WatchedItem) []int64 {
	out := make([]int64, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}

func setWatchedAt(t *testing.T, s *Store, id, at int64, plays int) {
	t.Helper()
	if _, err := s.rw.Exec(`UPDATE progress SET watched_at = ?, play_count = ? WHERE item_id = ?`, at, plays, id); err != nil {
		t.Fatal(err)
	}
}

// A watch is counted once, when the position gets to the end from somewhere before it. Reports from the
// credits on are not another watch, a file that is played again is still watched, and a second viewing that
// gets to the end is a second watch.
func TestSetProgressCountsAWatchWhenItGetsToTheEnd(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	id, err := s.UpsertItem(ctx, movie("/media/a.mp4", "A"), nil)
	if err != nil {
		t.Fatal(err)
	}
	report := func(pos float64) *Progress {
		t.Helper()
		p, err := s.SetProgress(ctx, id, pos, 7200)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}

	if p := report(600); p == nil || p.Watched() || p.PlayCount != 0 || p.WatchedAt != 0 {
		t.Fatalf("part way: %+v, want started and not watched", p)
	}
	before := time.Now().Unix()
	p := report(6600)
	if p == nil || !p.Watched() || p.PlayCount != 1 || p.WatchedAt < before || p.WatchedAt > time.Now().Unix() {
		t.Fatalf("to the end: %+v, want watched once, now", p)
	}
	if cont, _ := s.Continue(ctx, 10); len(cont) != 0 {
		t.Errorf("Continue lists a file that was watched to the end: %+v", cont)
	}

	// The time of the watch is the first one's while the position stays at the end.
	setWatchedAt(t, s, id, 1000, 1)
	for _, pos := range []float64{6700, 7200, 7200} {
		if p := report(pos); p.PlayCount != 1 || p.WatchedAt != 1000 {
			t.Errorf("a report at %v from the end on: %+v, want the watch counted once, at 1000", pos, p)
		}
	}

	// Played again and left at once: still watched, position kept as it is, no new watch.
	if p := report(12); p == nil || !p.Watched() || p.PositionSec != 12 || p.PlayCount != 1 {
		t.Errorf("left at 12 s: %+v, want a row that is still watched", p)
	}
	if got := entryProgress(t, s, id); got == nil || !got.Watched() {
		t.Errorf("stored after the restart: %+v", got)
	}
	if p := report(3000); p.PlayCount != 1 || p.WatchedAt != 1000 {
		t.Errorf("half way through a second viewing: %+v, want the first watch still", p)
	}
	if cont, _ := s.Continue(ctx, 10); len(cont) != 0 {
		t.Errorf("Continue lists a file that is watched again: %+v", cont)
	}

	// The second viewing gets to the end.
	if p := report(6900); p.PlayCount != 2 || p.WatchedAt < before {
		t.Errorf("second viewing to the end: %+v, want 2 watches, the last one now", p)
	}
}

// The table refuses a count and a time that disagree, which is what keeps "watched" a single fact.
func TestProgressTableChecksTheTimeAgainstTheCount(t *testing.T) {
	s, _ := openTemp(t)
	id, _ := s.UpsertItem(context.Background(), movie("/media/a.mp4", "A"), nil)
	insert := func(at int64, plays int) error {
		_, err := s.rw.Exec(`INSERT OR REPLACE INTO progress (item_id, position_sec, duration_sec, watched_at, play_count, updated_at)
			VALUES (?, 0, 0, ?, ?, 1)`, id, at, plays)
		return err
	}
	if err := insert(5, 1); err != nil {
		t.Fatalf("control: a watched file with a count was refused: %v", err)
	}
	if err := insert(0, 0); err != nil {
		t.Fatalf("control: an unwatched file with no count was refused: %v", err)
	}
	if err := insert(5, 0); err == nil {
		t.Error("a watch time with no count was accepted")
	}
	if err := insert(0, 2); err == nil {
		t.Error("a count with no watch time was accepted")
	}
}

func TestMarkAnItemWatchedAndUnwatched(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	id, _ := s.UpsertItem(ctx, movie("/media/a.mp4", "A"), nil)
	if _, err := s.SetProgress(ctx, id, 600, 7200); err != nil {
		t.Fatal(err)
	}
	if cont, _ := s.Continue(ctx, 10); len(cont) != 1 {
		t.Fatalf("control: Continue = %d items, want the one that was started", len(cont))
	}

	before := time.Now().Unix()
	got, err := s.SetItemWatched(ctx, id, true)
	if err != nil || len(got) != 1 || got[0].ID != id {
		t.Fatalf("SetItemWatched = %+v, %v", got, err)
	}
	p := got[0].Progress
	if p == nil || !p.Watched() || p.PositionSec != 0 || p.PlayCount != 1 || p.WatchedAt < before || p.DurationSec != 7200 {
		t.Errorf("marked watched: %+v, want watched now once, at position 0, with the duration it had", p)
	}
	// A file that had no row takes the duration of the file.
	fresh, _ := s.UpsertItem(ctx, movie("/media/fresh.mp4", "Fresh"), nil)
	if got, err := s.SetItemWatched(ctx, fresh, true); err != nil || len(got) != 1 || got[0].Progress == nil || got[0].Progress.DurationSec != 7200.5 {
		t.Errorf("marking a file that had no row: %+v, %v; want the file's duration, 7200.5", got, err)
	}
	if _, err := s.SetItemWatched(ctx, fresh, false); err != nil {
		t.Fatal(err)
	}
	if stored := entryProgress(t, s, id); stored == nil || *stored != *p {
		t.Errorf("stored %+v, answered %+v", stored, p)
	}
	if cont, _ := s.Continue(ctx, 10); len(cont) != 0 {
		t.Errorf("Continue lists a file that was marked watched: %+v", cont)
	}

	// Marking again keeps when it was watched and how often; a position it has by then goes.
	setWatchedAt(t, s, id, 1000, 3)
	if _, err := s.SetProgress(ctx, id, 500, 7200); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetItemWatched(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	if p := entryProgress(t, s, id); p.WatchedAt != 1000 || p.PlayCount != 3 || p.PositionSec != 0 {
		t.Errorf("marked again: %+v, want the time and count it had, at position 0", p)
	}

	// Unwatched: the row goes, with the position.
	if _, err := s.SetProgress(ctx, id, 500, 7200); err != nil {
		t.Fatal(err)
	}
	if p := entryProgress(t, s, id); p == nil || p.PositionSec != 500 {
		t.Fatalf("control: the file has no position to clear: %+v", p)
	}
	got, err = s.SetItemWatched(ctx, id, false)
	if err != nil || len(got) != 1 || got[0].Progress != nil {
		t.Fatalf("SetItemWatched(false) = %+v, %v; want the file with no progress", got, err)
	}
	if n := count(t, s, "progress"); n != 0 {
		t.Errorf("%d progress rows left after unwatching", n)
	}
	// And on a file that has none, nothing happens.
	if got, err := s.SetItemWatched(ctx, id, false); err != nil || len(got) != 1 || got[0].Progress != nil {
		t.Errorf("unwatching a file that is not: %+v, %v", got, err)
	}
	if _, err := s.SetItemWatched(ctx, 999, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetItemWatched(unknown) = %v, want ErrNotFound", err)
	}
}

// A film is one thing to watch, whatever number of files it is on.
func TestTitleWatchedCoversEveryVersionOfAFilm(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	hd, title := addMovie(t, s, "/media/m-1080.mp4", tmdbMovie(603, "The Matrix", 1999))
	uhd := addVersion(t, s, "/media/m-2160.mp4", title)
	cut := addVersion(t, s, "/media/m-cut.mp4", title)
	other, otherTitle := addMovie(t, s, "/media/o.mp4", tmdbMovie(604, "Reloaded", 2003))
	loose, _ := s.UpsertItem(ctx, movie("/media/loose.mp4", "Loose"), nil)
	if _, err := s.SetProgress(ctx, uhd, 500, 7200); err != nil {
		t.Fatal(err)
	}

	got, err := s.SetTitleWatched(ctx, title, true)
	if err != nil {
		t.Fatal(err)
	}
	if want := []int64{hd, uhd, cut}; !slices.Equal(coveredIDs(got), want) {
		t.Fatalf("covered %v, want the three versions %v", coveredIDs(got), want)
	}
	for _, it := range got {
		if it.Progress == nil || !it.Progress.Watched() || it.Progress.PositionSec != 0 || it.Progress.PlayCount != 1 {
			t.Errorf("version %d: %+v, want watched at position 0", it.ID, it.Progress)
		}
	}
	// Positive control for the absences: three rows were written, and only theirs.
	if n := count(t, s, "progress"); n != 3 {
		t.Errorf("%d progress rows, want the 3 versions'", n)
	}
	for _, id := range []int64{other, loose} {
		if p := entryProgress(t, s, id); p != nil {
			t.Errorf("item %d outside the title was marked: %+v", id, p)
		}
	}
	films, err := s.FilmWatches(ctx, []int64{title, otherTitle})
	if err != nil || len(films) != 1 || films[title].PlayCount != 1 || films[title].WatchedAt == 0 {
		t.Errorf("FilmWatches = %+v, %v; want only the title that was marked", films, err)
	}

	// A version that comes later: the film was seen, so it is watched, although the new file has no row.
	late := addVersion(t, s, "/media/m-late.mp4", title)
	if p := entryProgress(t, s, late); p != nil {
		t.Fatalf("control: a new file has progress %+v", p)
	}
	if films, _ := s.FilmWatches(ctx, []int64{title}); films[title].PlayCount != 1 {
		t.Errorf("a film with a new version is %+v, want it still watched", films[title])
	}

	// Marking one version unwatched is marking the film: every version is cleared, positions with them.
	got, err = s.SetItemWatched(ctx, late, false)
	if err != nil || !slices.Equal(coveredIDs(got), []int64{hd, uhd, cut, late}) {
		t.Fatalf("SetItemWatched on a version covered %v, %v; want the four versions", coveredIDs(got), err)
	}
	if n := count(t, s, "progress"); n != 0 {
		t.Errorf("%d progress rows left after the film was marked unwatched", n)
	}
	if films, _ := s.FilmWatches(ctx, []int64{title}); len(films) != 0 {
		t.Errorf("FilmWatches = %+v after unwatching, want none", films)
	}

	// And watched, through one version.
	if _, err := s.SetItemWatched(ctx, cut, true); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, "progress"); n != 4 {
		t.Errorf("%d progress rows after marking a version watched, want the film's 4", n)
	}
	// A file no title is known for is marked alone.
	if got, err := s.SetItemWatched(ctx, loose, true); err != nil || !slices.Equal(coveredIDs(got), []int64{loose}) {
		t.Errorf("SetItemWatched on a loose file covered %v, %v", coveredIDs(got), err)
	}
	if p := entryProgress(t, s, other); p != nil {
		t.Errorf("another film was marked: %+v", p)
	}

	// A title nothing in the library is on has nothing to mark.
	bare, err := s.UpsertTitle(ctx, tmdbMovie(605, "Revolutions", 2003))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{bare, 12345} {
		if _, err := s.SetTitleWatched(ctx, id, true); !errors.Is(err, ErrNotFound) {
			t.Errorf("SetTitleWatched(%d) = %v, want ErrNotFound", id, err)
		}
	}
}

func TestSeasonAndShowWatched(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	special := addEpisode(t, s, "Alpha", 0, 1)
	a1, a2, a3 := addEpisode(t, s, "Alpha", 1, 1), addEpisode(t, s, "Alpha", 1, 2), addEpisode(t, s, "Alpha", 1, 3)
	b1, b2 := addEpisode(t, s, "Alpha", 2, 1), addEpisode(t, s, "Alpha", 2, 2)
	other := addEpisode(t, s, "Beta", 1, 1)
	counts := func(show string) Show {
		t.Helper()
		shows, err := s.Shows(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, sh := range shows {
			if sh.Name == show {
				return sh
			}
		}
		t.Fatalf("no show %q", show)
		return Show{}
	}
	if c := counts("Alpha"); c.EpisodeCounts != (EpisodeCounts{EpisodeCount: 5}) { // the special is not in the show's count
		t.Fatalf("control: Alpha before = %+v", c.EpisodeCounts)
	}

	got, err := s.SetSeasonWatched(ctx, "Alpha", 1, true)
	if err != nil || !slices.Equal(coveredIDs(got), []int64{a1, a2, a3}) {
		t.Fatalf("SetSeasonWatched covered %v, %v; want season 1's episodes in order", coveredIDs(got), err)
	}
	alpha := counts("Alpha")
	if want := (EpisodeCounts{EpisodeCount: 5, Watched: 3}); alpha.EpisodeCounts != want {
		t.Errorf("Alpha after season 1 = %+v, want %+v", alpha.EpisodeCounts, want)
	}
	if want := map[int]EpisodeCounts{0: {EpisodeCount: 1}, 1: {EpisodeCount: 3, Watched: 3}, 2: {EpisodeCount: 2}}; !mapsEqual(alpha.BySeason, want) {
		t.Errorf("Alpha by season = %+v, want %+v", alpha.BySeason, want)
	}
	if c := counts("Beta"); c.Watched != 0 || c.EpisodeCount != 1 {
		t.Errorf("Beta, another show: %+v", c.EpisodeCounts)
	}
	if p := entryProgress(t, s, other); p != nil {
		t.Errorf("an episode of another show was marked: %+v", p)
	}

	// Started episodes are counted, and a mark takes their position and leaves the others' history alone.
	if _, err := s.SetProgress(ctx, b1, 100, 2400); err != nil {
		t.Fatal(err)
	}
	if c := counts("Alpha"); c.InProgress != 1 || c.BySeason[2].InProgress != 1 || c.BySeason[1].InProgress != 0 {
		t.Errorf("Alpha with one started: %+v by season %+v", c.EpisodeCounts, c.BySeason)
	}
	setWatchedAt(t, s, a2, 1000, 2)
	got, err = s.SetShowWatched(ctx, "Alpha", true)
	if err != nil || !slices.Equal(coveredIDs(got), []int64{special, a1, a2, a3, b1, b2}) {
		t.Fatalf("SetShowWatched covered %v, %v", coveredIDs(got), err)
	}
	if want := (EpisodeCounts{EpisodeCount: 5, Watched: 5}); counts("Alpha").EpisodeCounts != want {
		t.Errorf("Alpha after the show = %+v, want %+v", counts("Alpha").EpisodeCounts, want)
	}
	if p := entryProgress(t, s, b1); p.PositionSec != 0 || !p.Watched() {
		t.Errorf("the started episode after the show was marked: %+v", p)
	}
	if p := entryProgress(t, s, a2); p.WatchedAt != 1000 || p.PlayCount != 2 {
		t.Errorf("an episode seen before: %+v, want its time and count kept", p)
	}
	if p := entryProgress(t, s, other); p != nil {
		t.Errorf("an episode of another show was marked: %+v", p)
	}

	// One season back to unwatched.
	got, err = s.SetSeasonWatched(ctx, "Alpha", 2, false)
	if err != nil || !slices.Equal(coveredIDs(got), []int64{b1, b2}) {
		t.Fatalf("SetSeasonWatched(false) covered %v, %v", coveredIDs(got), err)
	}
	alpha = counts("Alpha")
	if want := (EpisodeCounts{EpisodeCount: 5, Watched: 3}); alpha.EpisodeCounts != want || alpha.BySeason[2] != (EpisodeCounts{EpisodeCount: 2}) {
		t.Errorf("Alpha after season 2 went back: %+v by season %+v", alpha.EpisodeCounts, alpha.BySeason)
	}
	if n := count(t, s, "progress"); n != 4 {
		t.Errorf("%d progress rows, want the 4 episodes that are still watched", n)
	}

	// The specials are a season.
	if _, err := s.SetSeasonWatched(ctx, "Alpha", 0, false); err != nil {
		t.Fatal(err)
	}
	if p := entryProgress(t, s, special); p != nil {
		t.Errorf("the special after unwatching season 0: %+v", p)
	}

	// Nothing to mark.
	for name, call := range map[string]func() error{
		"a season that has no episodes": func() error { _, err := s.SetSeasonWatched(ctx, "Alpha", 9, true); return err },
		"a show that is not there":      func() error { _, err := s.SetShowWatched(ctx, "Nope", true); return err },
		"a show by another case":        func() error { _, err := s.SetShowWatched(ctx, "alpha", true); return err },
	} {
		if err := call(); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v, want ErrNotFound", name, err)
		}
	}
}

func mapsEqual[K comparable, V comparable](a, b map[K]V) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// A series title covers the episodes of every show it is linked to, and of no other.
func TestSeriesTitleWatchedCoversTheEpisodesOfItsShows(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	e1, e2 := addEpisode(t, s, "Alpha", 1, 1), addEpisode(t, s, "Alpha", 1, 2)
	twin := addEpisode(t, s, "Alpha (US)", 1, 1) // the same series, under another folder name
	other := addEpisode(t, s, "Beta", 1, 1)
	title, err := s.UpsertTitle(ctx, tmdbSeries(1396, "Alpha", 2008))
	if err != nil {
		t.Fatal(err)
	}
	for _, show := range []string{"Alpha", "Alpha (US)"} {
		if err := s.SetSeriesMatch(ctx, show, Match{TitleID: title, State: MatchMatched, Score: 0.9}); err != nil {
			t.Fatal(err)
		}
	}
	// The card of a title that is not marked: an unwatched series with its counts.
	cards, err := s.Titles(ctx, TitleFilter{})
	if err != nil || len(cards) != 1 || cards[0].Watched || cards[0].Episodes == nil || *cards[0].Episodes != (EpisodeCounts{EpisodeCount: 2}) { // S01E01 is one episode in both shows
		t.Fatalf("control: cards before = %+v, %v", cards, err)
	}

	got, err := s.SetTitleWatched(ctx, title, true)
	if err != nil || !slices.Equal(coveredIDs(got), []int64{e1, e2, twin}) {
		t.Fatalf("SetTitleWatched covered %v, %v; want the episodes of both shows", coveredIDs(got), err)
	}
	if p := entryProgress(t, s, other); p != nil {
		t.Errorf("an episode of a show outside the title was marked: %+v", p)
	}
	cards, _ = s.Titles(ctx, TitleFilter{})
	if len(cards) != 1 || !cards[0].Watched || *cards[0].Episodes != (EpisodeCounts{EpisodeCount: 2, Watched: 2}) {
		t.Errorf("card after marking = %+v", cards)
	}
	d, err := s.GetTitleDetail(ctx, title)
	if err != nil || !d.Watched || d.Episodes == nil || d.Episodes.Watched != 2 {
		t.Errorf("detail after marking: watched %v, %+v, %v", d.Watched, d.Episodes, err)
	}

	if _, err := s.SetTitleWatched(ctx, title, false); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, "progress"); n != 0 {
		t.Errorf("%d progress rows after the series was marked unwatched", n)
	}
}

// The cards of titles carry the watch state: a film is watched when any version is, a series when all its
// episodes are, and a series says how far it is.
func TestCardsCarryTheWatchState(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	film := fullMatrix()
	hd, filmTitle := addMovie(t, s, "/media/m-1080.mp4", film)
	uhd := addVersion(t, s, "/media/m-2160.mp4", filmTitle)
	e1, e2, e3 := addEpisode(t, s, "Alpha", 1, 1), addEpisode(t, s, "Alpha", 1, 2), addEpisode(t, s, "Alpha", 1, 3)
	seriesTitle, err := s.UpsertTitle(ctx, tmdbSeries(1396, "Alpha", 2008))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSeriesMatch(ctx, "Alpha", Match{TitleID: seriesTitle, State: MatchMatched, Score: 0.9}); err != nil {
		t.Fatal(err)
	}
	card := func(kind string) TitleCard {
		t.Helper()
		cards, err := s.Titles(ctx, TitleFilter{})
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cards {
			if c.Kind == kind {
				return c
			}
		}
		t.Fatalf("no %s card in %+v", kind, cards)
		return TitleCard{}
	}
	if c := card(KindMovie); c.Watched || c.Episodes != nil {
		t.Fatalf("control: a film nobody watched is %+v", c)
	}
	if c := card(KindSeries); c.Watched || c.Episodes == nil || *c.Episodes != (EpisodeCounts{EpisodeCount: 3}) {
		t.Fatalf("control: a series nobody watched is %+v", c)
	}

	// One version of the film, to the end: the film is watched.
	if _, err := s.SetProgress(ctx, uhd, 7000, 7200); err != nil {
		t.Fatal(err)
	}
	if c := card(KindMovie); !c.Watched {
		t.Errorf("a film with a version watched: %+v", c)
	}
	if p := entryProgress(t, s, hd); p != nil {
		t.Errorf("control: the other version has progress %+v", p)
	}
	// Part way through one version of a film nobody finished is not watching it.
	if _, err := s.SetItemWatched(ctx, hd, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetProgress(ctx, hd, 300, 7200); err != nil {
		t.Fatal(err)
	}
	if c := card(KindMovie); c.Watched {
		t.Errorf("a film started and not finished: %+v", c)
	}

	for _, id := range []int64{e1, e2} {
		if _, err := s.SetProgress(ctx, id, 2300, 2400); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SetProgress(ctx, e3, 300, 2400); err != nil {
		t.Fatal(err)
	}
	c := card(KindSeries)
	if c.Watched || *c.Episodes != (EpisodeCounts{EpisodeCount: 3, Watched: 2, InProgress: 1}) {
		t.Errorf("a series two thirds through: %+v / %+v", c, c.Episodes)
	}
	if _, err := s.SetProgress(ctx, e3, 2300, 2400); err != nil {
		t.Fatal(err)
	}
	if c := card(KindSeries); !c.Watched || *c.Episodes != (EpisodeCounts{EpisodeCount: 3, Watched: 3}) {
		t.Errorf("a series all watched: %+v / %+v", c, c.Episodes)
	}

	// The same cards in a person's credits.
	people, err := s.SearchPeople(ctx, "Keanu", 5)
	if err != nil || len(people) != 1 {
		t.Fatalf("people = %+v, %v", people, err)
	}
	if _, err := s.SetItemWatched(ctx, hd, true); err != nil {
		t.Fatal(err)
	}
	pd, err := s.GetPerson(ctx, people[0].ID)
	if err != nil || len(pd.Credits) == 0 || !pd.Credits[0].Title.Watched {
		t.Errorf("a person's credit for a watched film: %+v, %v", pd.Credits, err)
	}
}

// The watch state goes with a removed folder and comes back with it.
func TestArchiveKeepsTheWatchState(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	it := movie("/lib/seen.mp4", "Seen")
	it.Root, it.Size = "/lib", 100
	id, err := s.UpsertItem(ctx, it, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetItemWatched(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	setWatchedAt(t, s, id, 1234, 3)

	if n, err := s.ArchiveAndDeleteRoot(ctx, "/lib", false); err != nil || n != 1 {
		t.Fatalf("ArchiveAndDeleteRoot = %d, %v", n, err)
	}
	var at int64
	var plays int
	if err := s.rw.QueryRow(`SELECT watched_at, play_count FROM item_archive WHERE path = '/lib/seen.mp4'`).Scan(&at, &plays); err != nil || at != 1234 || plays != 3 {
		t.Fatalf("archived as %d, %d (%v), want 1234 and 3", at, plays, err)
	}

	// It comes back as the file it was (same size): watched, as often as before.
	again := movie("/lib/seen.mp4", "Seen")
	again.Root, again.Size = "/lib", 100
	id2, _ := s.UpsertItem(ctx, again, nil)
	if _, _, ok, err := s.RestoreArchived(ctx, id2, "/lib/seen.mp4", 100); err != nil || !ok {
		t.Fatalf("RestoreArchived = %v, %v", ok, err)
	}
	if p := entryProgress(t, s, id2); p == nil || p.WatchedAt != 1234 || p.PlayCount != 3 {
		t.Errorf("restored as %+v, want watched at 1234, 3 times", p)
	}
}

// A database of the version before this one (a flag on each progress row) is upgraded with what it holds:
// a file that was watched is watched once, at the time its row last changed.
func TestMigration011OnAV10Database(t *testing.T) {
	ctx := context.Background()
	old, path := openAtVersion(t, 10)
	started := insertOldItem(t, old, "/media/started.mp4", "Started")
	watched := insertOldItem(t, old, "/media/watched.mp4", "Watched")
	early := insertOldItem(t, old, "/media/early.mp4", "Early")
	for _, q := range []string{
		fmt.Sprintf(`INSERT INTO progress (item_id, position_sec, duration_sec, watched, updated_at) VALUES (%d, 600, 7200, 0, 1700000000123456789)`, started),
		fmt.Sprintf(`INSERT INTO progress (item_id, position_sec, duration_sec, watched, updated_at) VALUES (%d, 7100, 7200, 1, 1700000100000000000)`, watched),
		fmt.Sprintf(`INSERT INTO progress (item_id, position_sec, duration_sec, watched, updated_at) VALUES (%d, 7100, 7200, 1, 5)`, early),
		`INSERT INTO item_archive (path, size, position_sec, duration_sec, watched, progress_at, archived_at) VALUES ('/gone/seen.mp4', 5, 7100, 7200, 1, 1700000200000000000, 1)`,
		`INSERT INTO item_archive (path, size, position_sec, duration_sec, watched, progress_at, archived_at) VALUES ('/gone/half.mp4', 5, 600, 7200, 0, 1700000200000000000, 1)`,
	} {
		if _, err := old.rw.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// Positive control: version 10 has the flag and none of what 11 adds.
	if cols := columns(t, old.rw, "progress"); !slices.Contains(cols, "watched") || slices.Contains(cols, "watched_at") {
		t.Fatalf("version 10 progress columns = %v", cols)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if version := queryInt(t, s, `SELECT MAX(version) FROM schema_version`); version != slices.Max(migrationNumbers(t)) || version < 11 {
		t.Fatalf("schema version = %d, want the last migration (at least 11)", version)
	}
	if got, want := columns(t, s.rw, "progress"), []string{"item_id", "position_sec", "duration_sec", "watched_at", "play_count", "updated_at"}; !slices.Equal(got, want) {
		t.Errorf("progress columns = %v, want %v", got, want)
	}
	if cols := columns(t, s.rw, "item_archive"); slices.Contains(cols, "watched") || !slices.Contains(cols, "watched_at") || !slices.Contains(cols, "play_count") {
		t.Errorf("item_archive columns = %v", cols)
	}

	if p := entryProgress(t, s, started); p == nil || p.PositionSec != 600 || p.DurationSec != 7200 || p.Watched() || p.PlayCount != 0 || p.UpdatedAt != 1700000000123456789 {
		t.Errorf("the started file after the upgrade: %+v", p)
	}
	if p := entryProgress(t, s, watched); p == nil || p.WatchedAt != 1700000100 || p.PlayCount != 1 || p.PositionSec != 7100 || p.UpdatedAt != 1700000100000000000 {
		t.Errorf("the watched file after the upgrade: %+v, want watched at 1700000100, once", p)
	}
	if p := entryProgress(t, s, early); p == nil || !p.Watched() || p.PlayCount != 1 {
		t.Errorf("a watched file whose row has a tiny time: %+v, want watched all the same", p)
	}
	if cont, err := s.Continue(ctx, 10); err != nil || len(cont) != 1 || cont[0].ID != started {
		t.Errorf("Continue after the upgrade = %+v, %v; want only the started file", cont, err)
	}
	for path, want := range map[string][2]int64{"/gone/seen.mp4": {1700000200, 1}, "/gone/half.mp4": {0, 0}} {
		var at, plays int64
		if err := s.rw.QueryRow(`SELECT watched_at, play_count FROM item_archive WHERE path = ?`, path).Scan(&at, &plays); err != nil || [2]int64{at, plays} != want {
			t.Errorf("archive row %s = %d, %d (%v), want %v", path, at, plays, err, want)
		}
	}

	// The new table still goes with its item, and the old rows are still checked against items.
	if n := queryInt(t, s, `SELECT COUNT(*) FROM pragma_foreign_key_check`); n != 0 {
		t.Errorf("%d foreign key violations after the upgrade", n)
	}
	if _, err := s.rw.Exec(`DELETE FROM items WHERE id = ?`, watched); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, "progress"); n != 2 {
		t.Errorf("%d progress rows after deleting an item, want 2 (cascade)", n)
	}
	// And the first call that writes works on what was upgraded.
	if _, err := s.SetProgress(ctx, started, 7000, 7200); err != nil {
		t.Fatal(err)
	}
	if p := entryProgress(t, s, started); !p.Watched() || p.PlayCount != 1 {
		t.Errorf("finishing the started file: %+v", p)
	}
}
