package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestLinkItemTitleStoresAndLinksInOneStep(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	id, _ := s.UpsertItem(ctx, movie("/m/a.mkv", "A"), nil)

	titleID, ok, err := s.LinkItemTitle(ctx, id, fullMatrix(), Match{State: MatchMatched, Score: 0.9, NeedsReview: true}, MatchPending)
	if err != nil || !ok || titleID == 0 {
		t.Fatalf("LinkItemTitle = %d, %v, %v", titleID, ok, err)
	}
	e, _ := s.GetEntry(ctx, id)
	if e.TitleID != titleID || e.MatchState != MatchMatched || !e.NeedsReview {
		t.Errorf("item = %+v", e.Item)
	}
	if m, _ := s.GetItemMatch(ctx, id); m.Score != 0.9 || m.TitleID != titleID {
		t.Errorf("match = %+v", m)
	}
	if d, err := s.GetTitleDetail(ctx, titleID); err != nil || len(d.Cast) != 2 {
		t.Errorf("detail = %+v, %v", d, err)
	}

	// A failure while storing the title leaves the item as it was, and no title.
	other, _ := s.UpsertItem(ctx, movie("/m/b.mkv", "B"), nil)
	bad := tmdbMovie(5, "Bad", 2000)
	bad.Credits = []Credit{{Person: Person{Name: "x"}, Kind: "bogus"}}
	if _, ok, err := s.LinkItemTitle(ctx, other, bad, Match{State: MatchMatched}, MatchPending); err == nil || ok {
		t.Errorf("a bad title linked: %v, %v", ok, err)
	}
	if e, _ := s.GetEntry(ctx, other); e.MatchState != MatchPending || e.TitleID != 0 {
		t.Errorf("item after a failed link: %+v", e.Item)
	}
	if n := count(t, s, "titles"); n != 1 {
		t.Errorf("titles = %d, want 1 (the failed one rolled back)", n)
	}

	if _, _, err := s.LinkItemTitle(ctx, 9999, tmdbMovie(6, "X", 2000), Match{State: MatchMatched}, MatchPending); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown item = %v", err)
	}
	if _, _, err := s.LinkItemTitle(ctx, other, tmdbMovie(6, "X", 2000), Match{State: "maybe"}, ""); err == nil {
		t.Error("accepted a bad state")
	}
	if n := count(t, s, "titles"); n != 1 {
		t.Errorf("titles = %d after refused links", n)
	}
}

func TestLinkItemTitleGivesWayToAPersonsDecision(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	id, _ := s.UpsertItem(ctx, movie("/m/a.mkv", "A"), nil)
	mine, _ := s.UpsertTitle(ctx, tmdbMovie(1, "Mine", 2000))
	// The worker took the item as pending; the person fixed it meanwhile.
	if err := s.SetItemMatch(ctx, id, Match{TitleID: mine, State: MatchManual, Score: 1}); err != nil {
		t.Fatal(err)
	}
	before := count(t, s, "titles")
	titleID, ok, err := s.LinkItemTitle(ctx, id, tmdbMovie(2, "Late result", 2001), Match{State: MatchMatched, Score: 0.9}, MatchPending)
	if err != nil || ok || titleID != 0 {
		t.Fatalf("late result = %d, %v, %v; want it dropped", titleID, ok, err)
	}
	if e, _ := s.GetEntry(ctx, id); e.MatchState != MatchManual || e.TitleID != mine {
		t.Errorf("the worker's late result overwrote the fix: %+v", e.Item)
	}
	if n := count(t, s, "titles"); n != before {
		t.Errorf("a dropped result left a title behind (%d -> %d)", before, n)
	}

	// A person's own action (expect "") always wins.
	if _, ok, err := s.LinkItemTitle(ctx, id, tmdbMovie(3, "Fixed again", 2002), Match{State: MatchManual, Score: 1}, ""); err != nil || !ok {
		t.Fatalf("manual relink = %v, %v", ok, err)
	}
	if e, _ := s.GetEntry(ctx, id); e.MatchState != MatchManual || e.TitleID == mine {
		t.Errorf("manual relink: %+v", e.Item)
	}
}

func TestLinkSeriesTitle(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	s.UpsertItem(ctx, episode("/m/S/S01E01.mkv", "S", 1, 1), nil)

	// A show nobody looked at has no row and counts as pending.
	id, ok, err := s.LinkSeriesTitle(ctx, "S", tmdbSeries(1, "S", 2000), Match{State: MatchMatched, Score: 0.9}, MatchPending)
	if err != nil || !ok || id == 0 {
		t.Fatalf("LinkSeriesTitle = %d, %v, %v", id, ok, err)
	}
	if sm, _ := s.GetSeriesMatch(ctx, "S"); sm.TitleID != id || sm.State != MatchMatched || sm.Score != 0.9 {
		t.Errorf("series = %+v", sm)
	}
	// The person fixes it; a late result is dropped without a trace.
	if err := s.SetSeriesMatch(ctx, "S", Match{State: MatchManual}); err != nil {
		t.Fatal(err)
	}
	before := count(t, s, "titles")
	if _, ok, err := s.LinkSeriesTitle(ctx, "S", tmdbSeries(2, "Late", 2001), Match{State: MatchMatched}, MatchPending); err != nil || ok {
		t.Errorf("late result: %v, %v", ok, err)
	}
	if sm, _ := s.GetSeriesMatch(ctx, "S"); sm.State != MatchManual || sm.TitleID != 0 {
		t.Errorf("series = %+v", sm)
	}
	if count(t, s, "titles") != before {
		t.Error("a dropped result left a title")
	}
	if _, ok, err := s.LinkSeriesTitle(ctx, "S", tmdbSeries(3, "Mine", 2002), Match{State: MatchManual}, ""); err != nil || !ok {
		t.Errorf("manual: %v, %v", ok, err)
	}
	if _, _, err := s.LinkSeriesTitle(ctx, "Nope", tmdbSeries(4, "x", 2000), Match{State: MatchMatched}, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("a show without episodes = %v", err)
	}
	if _, _, err := s.LinkSeriesTitle(ctx, "S", tmdbSeries(4, "x", 2000), Match{State: "maybe"}, ""); err == nil {
		t.Error("accepted a bad state")
	}
}

// A prune cannot take a title between its storing and its linking, because
// there is no between. (When they were two transactions the link failed with
// "FOREIGN KEY constraint failed".)
func TestPruneCannotTakeATitleThatIsBeingLinked(t *testing.T) {
	s, _ := openTemp(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	const items = 40
	ids := make([]int64, items)
	for i := range ids {
		ids[i], _ = s.UpsertItem(ctx, movie(fmt.Sprintf("/m/%d.mkv", i), "x"), nil)
	}
	stop := make(chan struct{})
	var pruners sync.WaitGroup
	for range 2 {
		pruners.Add(1)
		go func() {
			defer pruners.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := s.PruneMetadata(ctx); err != nil {
					t.Errorf("prune: %v", err)
					return
				}
			}
		}()
	}
	var linkers sync.WaitGroup
	for w := range 4 {
		linkers.Add(1)
		go func() {
			defer linkers.Done()
			for round := 0; round < 25; round++ {
				i := (w*7 + round) % items
				in := tmdbMovie(int64(1000+w*100+round), fmt.Sprintf("T%d-%d", w, round), 2000)
				if _, _, err := s.LinkItemTitle(ctx, ids[i], in, Match{State: MatchMatched}, ""); err != nil {
					t.Errorf("link: %v", err)
					return
				}
			}
		}()
	}
	linkers.Wait()
	close(stop)
	pruners.Wait()
}

func TestReindexKeepsTheMatchAndTheEdits(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	it := movie("/m/a.mkv", "A")
	it.Fingerprint = "old"
	id, _ := s.UpsertItem(ctx, it, nil)
	titleID, _, err := s.LinkItemTitle(ctx, id, fullMatrix(), Match{State: MatchMatched, NeedsReview: true, Score: 0.7, Attempts: 2, NextAttemptAt: 99}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetItemArt(ctx, id, Art{Poster: "/m/poster.jpg", Backdrop: "/m/fanart.jpg", Thumb: "/m/thumb.jpg"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetItemEmbeddedCover(ctx, id, 3); err != nil {
		t.Fatal(err)
	}
	patched := "My Title"
	if err := s.PatchTitle(ctx, titleID, TitlePatch{Title: &patched}); err != nil {
		t.Fatal(err)
	}

	// The file changed (a re-encode, a new rip of the same movie): it is
	// re-indexed under the same path. Its match, fix and edits are about the
	// movie, not the bytes, so they stay.
	it2 := movie("/m/a.mkv", "A renamed by the parser")
	it2.Size, it2.MTime, it2.Fingerprint, it2.DurationSec = 999, 42, "new", 100
	if id2, err := s.UpsertItem(ctx, it2, nil); err != nil || id2 != id {
		t.Fatalf("re-index = %d, %v", id2, err)
	}
	e, _ := s.GetEntry(ctx, id)
	if e.TitleID != titleID || e.MatchState != MatchMatched || !e.NeedsReview ||
		e.LocalPoster != "/m/poster.jpg" || e.LocalBackdrop != "/m/fanart.jpg" || e.LocalThumb != "/m/thumb.jpg" {
		t.Errorf("a re-index dropped the metadata: %+v", e.Item)
	}
	if m, _ := s.GetItemMatch(ctx, id); m.Score != 0.7 || m.Attempts != 2 || m.NextAttemptAt != 99 {
		t.Errorf("a re-index reset the matching bookkeeping: %+v", m)
	}
	if e.Fingerprint != "new" || e.Size != 999 || e.Title != "A" { // the file's columns, not what its path says
		t.Errorf("the file's own columns were not updated: %+v", e.Item)
	}
	if title, _ := s.GetTitle(ctx, titleID); title.Title != "My Title" || !title.Locked {
		t.Errorf("a re-index touched the title: %+v", title)
	}
}

func TestACreditNeverTouchesAPersonsFetchedDetails(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	first := tmdbMovie(1, "A", 2000)
	first.Credits = []Credit{castCredit(6384, "Keanu Reeves", "Neo", 0)}
	addMovie(t, s, "/m/a.mkv", first)
	var pid int64
	s.rw.QueryRow(`SELECT id FROM people WHERE tmdb_id = 6384`).Scan(&pid)
	if err := s.SetPersonDetails(ctx, pid, Person{Biography: "Canadian actor.", Birthday: "1964-09-02", PlaceOfBirth: "Beirut",
		KnownFor: "Acting", IMDbID: "nm0000206"}, 12345); err != nil {
		t.Fatal(err)
	}

	// The same person comes up in another title, and again when the first is refreshed.
	second := tmdbMovie(2, "B", 2001)
	second.Credits = []Credit{{Person: Person{TMDBID: 6384, Name: "Keanu C. Reeves", Profile: "tmdb:/new.jpg"}, Kind: CreditCast, Character: "John"}}
	addMovie(t, s, "/m/b.mkv", second)
	if _, err := s.UpsertTitle(ctx, first); err != nil {
		t.Fatal(err)
	}
	p, err := s.GetPerson(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if p.Biography != "Canadian actor." || p.Birthday != "1964-09-02" || p.PlaceOfBirth != "Beirut" || p.KnownFor != "Acting" ||
		p.IMDbID != "nm0000206" || p.FetchedAt != 12345 {
		t.Errorf("fetched details were lost: %+v", p.Person)
	}
	if n := count(t, s, "people"); n != 1 {
		t.Errorf("people = %d", n)
	}

	// The same for a person only an NFO names.
	nfo := &TitleInput{Title: Title{Kind: KindMovie, Title: "Home"}, Credits: []Credit{{Person: Person{Name: "Uncle Bob"}, Kind: CreditCast}}}
	if _, err := s.UpsertTitle(ctx, nfo); err != nil {
		t.Fatal(err)
	}
	var bob int64
	s.rw.QueryRow(`SELECT id FROM people WHERE name = 'Uncle Bob'`).Scan(&bob)
	s.rw.Exec(`UPDATE people SET biography = 'a bio', fetched_at = 7 WHERE id = ?`, bob)
	nfo.ID = 0
	if _, err := s.UpsertTitle(ctx, &TitleInput{Title: Title{Kind: KindMovie, Title: "Home 2"}, Credits: nfo.Credits}); err != nil {
		t.Fatal(err)
	}
	var bio string
	s.rw.QueryRow(`SELECT biography FROM people WHERE id = ?`, bob).Scan(&bio)
	if bio != "a bio" {
		t.Errorf("an NFO person's biography = %q", bio)
	}
}

func TestOnlyShowsWithEpisodesAreInTheLibrary(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	in := tmdbSeries(1, "Ghost Show", 2010)
	in.Genres = []Genre{{ID: 99, Name: "Documentary"}}
	in.Credits = []Credit{castCredit(5, "Ghost Actor", "x", 0)}
	titleID, err := s.UpsertTitle(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	// A show row that points at the title, but no episode file exists.
	if _, err := s.rw.Exec(`INSERT INTO series_matches (show, title_id, match_state) VALUES ('Ghost Show', ?, 'matched')`, titleID); err != nil {
		t.Fatal(err)
	}
	inLibrary := func() (bool, bool, bool, bool) {
		_, detailErr := s.GetTitleDetail(ctx, titleID)
		cards, _ := s.Titles(ctx, TitleFilter{})
		genres, _ := s.Genres(ctx)
		people, _ := s.SearchPeople(ctx, "Ghost", 5)
		refs, _ := s.LibraryTitleRefs(ctx)
		_ = refs
		return detailErr == nil, len(cards) > 0, len(genres) > 0, len(people) > 0
	}
	if d, c, g, p := inLibrary(); d || c || g || p {
		t.Fatalf("a show without episodes is in the library: detail %v cards %v genres %v people %v", d, c, g, p)
	}
	if refs, _ := s.LibraryTitleRefs(ctx); len(refs) != 0 {
		t.Errorf("LibraryTitleRefs = %+v", refs)
	}
	if due, _ := s.TitlesDueForRefresh(ctx, time.Now().Add(100*24*time.Hour), 10); len(due) != 0 {
		t.Errorf("a show without episodes is due for refresh: %+v", due)
	}
	// Positive control: with an episode file, everything finds it.
	s.UpsertItem(ctx, episode("/m/Ghost Show/S01E01.mkv", "Ghost Show", 1, 1), nil)
	if d, c, g, p := inLibrary(); !d || !c || !g || !p {
		t.Errorf("a show with an episode: detail %v cards %v genres %v people %v", d, c, g, p)
	}
	if refs, _ := s.LibraryTitleRefs(ctx); len(refs) != 1 {
		t.Errorf("LibraryTitleRefs = %+v", refs)
	}
}

func TestSeriesSidecarArtBeatsTMDBOnCards(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	id := addShow(t, s, "Show", []int{1}, tmdbSeries(1, "Show", 2010))
	if cards, _ := s.TitleCards(ctx, []int64{id}); cards[id].Poster != "tmdb:/p1.jpg" || cards[id].Backdrop != "tmdb:/b1.jpg" {
		t.Fatalf("before any sidecar: %+v", cards[id])
	}
	if err := s.SetSeriesArt(ctx, "Show", Art{Poster: "/m/Show/poster.jpg"}); err != nil {
		t.Fatal(err)
	}
	sm, _ := s.GetSeriesMatch(ctx, "Show")
	cards, _ := s.TitleCards(ctx, []int64{id})
	if got, want := cards[id].Poster, LocalRef(ArtSeries, sm.ID, "poster"); got != want {
		t.Errorf("poster = %q, want the show's sidecar %q", got, want)
	}
	if got := cards[id].Backdrop; got != "tmdb:/b1.jpg" {
		t.Errorf("backdrop = %q; only the poster has a sidecar", got)
	}
	d, _ := s.GetTitleDetail(ctx, id)
	if d.Poster != LocalRef(ArtSeries, sm.ID, "poster") {
		t.Errorf("detail poster = %q", d.Poster)
	}
}

func TestSearchPeopleTreatsPercentUnderscoreAndBackslashLiterally(t *testing.T) {
	s, _ := openTemp(t)
	in := tmdbMovie(1, "A", 2000)
	names := []string{"Fifty%Fifty", "Under_Score", "UnderXScore", "Back\\Slash", "Plain Name"}
	for i, n := range names {
		in.Credits = append(in.Credits, castCredit(int64(100+i), n, "x", i))
	}
	addMovie(t, s, "/m/a.mkv", in)
	search := func(q string) []string {
		got, err := s.SearchPeople(context.Background(), q, 20)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, p := range got {
			out = append(out, p.Name)
		}
		slices.Sort(out)
		return out
	}
	for q, want := range map[string][]string{
		"%":       {"Fifty%Fifty"},
		"_":       {"Under_Score"},
		"\\":      {"Back\\Slash"},
		"y%f":     {"Fifty%Fifty"},
		"r_s":     {"Under_Score"},
		"under":   {"UnderXScore", "Under_Score"},
		"under_s": {"Under_Score"},
		"x":       {"UnderXScore"},
		"%%":      nil,
		"__":      nil,
	} {
		if got := search(q); !slices.Equal(got, want) {
			t.Errorf("search %q = %q, want %q", q, got, want)
		}
	}
}
