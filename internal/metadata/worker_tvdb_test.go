package metadata_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"ply/internal/metadata"
	"ply/internal/metadata/metadatatest"
	"ply/internal/store"
)

// tenv is a worker environment with TVDB on as well as TMDB: e.fake is TMDB, e.tvdb is TVDB.
type tenv struct {
	*wenv
	tvdb *metadatatest.Fake
}

func newTEnv(t *testing.T, mod ...func(*metadata.WorkerOptions)) *tenv {
	t.Helper()
	e := newWEnv(t, mod...)
	tv := &metadatatest.Fake{Source: metadata.SourceTVDB, ImageBase: e.imgSrv.URL}
	e.src.SetTVDB(tv)
	return &tenv{wenv: e, tvdb: tv}
}

// bbTVDB is Breaking Bad as TVDB gives it; bbTMDB as TMDB does, with the TVDB id in its external ids.
func bbTVDB() *store.TitleInput {
	ep := func(s, n int) store.Episode {
		return store.Episode{Season: s, Number: n, Name: fmt.Sprintf("TVDB S%dE%d", s, n)}
	}
	return &store.TitleInput{Title: store.Title{Kind: store.KindSeries, TVDBID: 81189, TMDBID: 1396, Title: "Breaking Bad", Year: 2008,
		Status: "Ended", Poster: "tvdb:/banners/posters/81189-10.jpg", Backdrop: "tvdb:/banners/fanart/original/81189-1.jpg",
		Source: store.SourceTVDB, EpisodeOrder: store.OrderAired, Overview: "From TVDB"},
		Seasons: []store.Season{{Number: 1, Episodes: []store.Episode{ep(1, 1), ep(1, 2)}}, {Number: 2, Episodes: []store.Episode{ep(2, 1)}}}}
}

func bbTMDB() *store.TitleInput {
	in := breakingBad()
	in.TVDBID, in.Overview = 81189, "From TMDB"
	return in
}

func (e *tenv) withBBBoth() {
	e.tvdb.SeriesSearch = map[string][]metadata.Candidate{"breaking bad": {{ID: 81189, Title: "Breaking Bad", OriginalTitle: "Breaking Bad", Year: 2008, Popularity: 9}}}
	e.tvdb.Shows = map[int64]*store.TitleInput{81189: bbTVDB()}
	e.fake.SeriesSearch = map[string][]metadata.Candidate{"breaking bad": {{ID: 1396, Title: "Breaking Bad", OriginalTitle: "Breaking Bad", Year: 2008, Popularity: 9}}}
	e.fake.Shows = map[int64]*store.TitleInput{1396: bbTMDB()}
}

func TestWorkerShowsComeFromTVDBAndMoviesFromTMDB(t *testing.T) {
	e := newTEnv(t)
	e.withBBBoth()
	e.withMatrix()
	e.addEpisode("Breaking Bad", 1, 2)
	mv := e.addMovie("Movies/The Matrix (1999).mkv", "The Matrix", 1999)
	e.once()

	sm := e.series("Breaking Bad")
	if sm.State != store.MatchMatched || sm.TitleID == 0 || sm.NeedsReview {
		t.Fatalf("show = %+v", sm)
	}
	title := e.title(sm.TitleID)
	if title.Source != store.SourceTVDB || title.TVDBID != 81189 || title.TMDBID != 1396 || title.Overview != "From TVDB" || title.EpisodeOrder != store.OrderAired {
		t.Errorf("show title = %+v", title)
	}
	eps, err := e.st.Episodes(e.ctx, sm.TitleID)
	if err != nil || len(eps) != 3 || eps[1].Name != "TVDB S1E2" {
		t.Errorf("episodes = %+v, %v; the show's own come from TVDB, every season at once", eps, err)
	}
	if n := e.fake.Count("SearchSeries") + e.fake.Count("Series("); n != 0 {
		t.Errorf("TMDB was asked %d times about a show TVDB matched: %q", n, e.fake.Calls())
	}
	if want := []string{`SearchSeries("Breaking Bad", 0)`, "Series(81189, aired)"}; !slices.Equal(e.tvdb.Calls(), want) {
		t.Errorf("TVDB calls = %q, want %q", e.tvdb.Calls(), want)
	}

	// The movie is TMDB's, and TVDB is not asked about it.
	if got := e.entry(mv); got.MatchState != store.MatchMatched || e.title(got.TitleID).Source != store.SourceTMDB || e.title(got.TitleID).TMDBID != 603 {
		t.Errorf("movie = %+v", got.Item)
	}
	if e.tvdb.Count("Movie") != 0 || e.tvdb.Count("SearchMovies") != 0 {
		t.Errorf("TVDB was asked about a movie: %q", e.tvdb.Calls())
	}

	// Its artwork is cached under the TVDB tree, from the TVDB image address.
	for _, f := range []string{"tvdb/w342/banners/posters/81189-10.jpg", "tvdb/w1280/banners/fanart/original/81189-1.jpg", "tmdb/w342/p603.jpg"} {
		if _, err := os.Stat(filepath.Join(e.imgDir, filepath.FromSlash(f))); err != nil {
			t.Errorf("not prefetched: %s", f)
		}
	}
	if st := e.w.Status(e.ctx); st.Shows != "tvdb" || st.ShowsLine != "Shows: TVDB" || !st.TVDBKeySet {
		t.Errorf("status = %+v", st)
	}
}

func TestWorkerFallsBackToTMDBForAShowTVDBDoesNotKnow(t *testing.T) {
	e := newTEnv(t)
	e.fake.SeriesSearch = map[string][]metadata.Candidate{"breaking bad": {{ID: 1396, Title: "Breaking Bad", OriginalTitle: "Breaking Bad", Year: 2008, Popularity: 9}}}
	e.fake.Shows = map[int64]*store.TitleInput{1396: bbTMDB()}
	e.addEpisode("Breaking Bad", 1, 1)
	e.once()

	sm := e.series("Breaking Bad")
	if sm.State != store.MatchMatched {
		t.Fatalf("show = %+v", sm)
	}
	if title := e.title(sm.TitleID); title.Source != store.SourceTMDB || title.TMDBID != 1396 || title.Overview != "From TMDB" {
		t.Errorf("title = %+v", title)
	}
	if e.tvdb.Count("SearchSeries") == 0 {
		t.Error("TVDB was not asked first")
	}
	// Not moved by the pass that matched it either, though TMDB gave it a TVDB id: it is not
	// one of the shows that were matched before TVDB came on.
	if e.tvdb.Count("Series(") != 0 {
		t.Errorf("a show that fell back to TMDB was moved to TVDB at once: %q", e.tvdb.Calls())
	}
	// Nor later by a refresh: TMDB is where it is.
	e.once()
	e.fake.Hook = nil
	e.w.RefreshShow("Breaking Bad")
	e.once()
	if e.tvdb.Count("Series(") != 0 {
		t.Errorf("a show on TMDB was fetched from TVDB: %q", e.tvdb.Calls())
	}
}

func TestWorkerTVDBOutOfReachIsABackoffNotAFallback(t *testing.T) {
	e := newTEnv(t)
	e.withBBBoth()
	e.addEpisode("Breaking Bad", 1, 1)
	e.tvdb.Offline()
	start := e.clock.Now()
	e.once()

	sm := e.series("Breaking Bad")
	if sm.State != store.MatchPending || sm.TitleID != 0 || sm.Attempts != 1 || sm.NextAttemptAt != start.Add(time.Minute).Unix() {
		t.Errorf("show = %+v; want it pending and put off for a minute, and never unmatched", sm.Match)
	}
	if n := len(e.fake.Calls()); n != 0 {
		t.Errorf("TMDB was asked while TVDB was down: %q", e.fake.Calls())
	}
	if st := e.w.Status(e.ctx); st.ShowsLine != "TVDB unreachable" || st.Shows != "tvdb" {
		t.Errorf("status = %q / %q", st.Shows, st.ShowsLine)
	}

	// Back up, a minute later: matched at TVDB, not at TMDB, and the status line is well again.
	e.tvdb.Online()
	e.clock.Advance(time.Minute + time.Second)
	e.once()
	sm = e.series("Breaking Bad")
	if sm.State != store.MatchMatched || e.title(sm.TitleID).Source != store.SourceTVDB {
		t.Errorf("after TVDB came back: %+v", sm)
	}
	if n := len(e.fake.Calls()); n != 0 {
		t.Errorf("TMDB was asked: %q", e.fake.Calls())
	}
	if st := e.w.Status(e.ctx); st.ShowsLine != "Shows: TVDB" {
		t.Errorf("status line = %q", st.ShowsLine)
	}
}

func TestWorkerStatusLineSaysWhereShowsCome(t *testing.T) {
	e := newWEnv(t) // TMDB only
	if st := e.w.Status(e.ctx); st.Shows != "tmdb" || st.ShowsLine != "Shows: TMDB (no TVDB key)" || st.TVDBKeySet {
		t.Errorf("TMDB only: %+v", st)
	}
	e.src.SetTVDB(&metadatatest.Fake{Source: metadata.SourceTVDB})
	if st := e.w.Status(e.ctx); st.Shows != "tvdb" || st.ShowsLine != "Shows: TVDB" || !st.TVDBKeySet {
		t.Errorf("both: %+v", st)
	}
	e.src.Set(nil, true) // no TMDB key, TVDB alone
	if st := e.w.Status(e.ctx); st.Shows != "tvdb" || st.KeySet {
		t.Errorf("TVDB only: %+v", st)
	}
	e.src.Set(e.fake, false) // the privacy switch
	if st := e.w.Status(e.ctx); st.Shows != "" || st.ShowsLine != "" {
		t.Errorf("switched off: %+v", st)
	}
	e.src.SetTVDB(nil)
	e.src.Set(nil, true)
	if st := e.w.Status(e.ctx); st.Shows != "" || st.ShowsLine != "" {
		t.Errorf("no key at all: %+v", st)
	}
}

func TestWorkerWithOnlyATVDBKeyMatchesShowsAndLeavesMoviesPending(t *testing.T) {
	e := newTEnv(t)
	e.withBBBoth()
	e.withMatrix()
	e.src.Set(nil, true) // no TMDB key
	ep := e.addEpisode("Breaking Bad", 1, 1)
	mv := e.addMovie("Movies/The Matrix (1999).mkv", "The Matrix", 1999)
	e.once()
	_ = ep
	if sm := e.series("Breaking Bad"); sm.State != store.MatchMatched || e.title(sm.TitleID).Source != store.SourceTVDB {
		t.Errorf("show = %+v", sm)
	}
	if got := e.entry(mv); got.MatchState != store.MatchPending || got.TitleID != 0 {
		t.Errorf("movie = %+v; with no TMDB key it waits for one", got.Item)
	}
	if len(e.fake.Calls()) != 0 {
		t.Errorf("TMDB was asked: %q", e.fake.Calls())
	}
}

// TVDB's ids and TMDB's are different numbers for different things: a show that TVDB's
// updates feed names is not one that TMDB's changes feed names, and the other way round.
func TestWorkerRefreshUsesTheFeedOfEachTitlesOwnProvider(t *testing.T) {
	e := newTEnv(t)
	// "Same Number" is on TVDB as 500; "Other Number" is on TMDB as 500.
	tvdbShow := &store.TitleInput{Title: store.Title{Kind: store.KindSeries, TVDBID: 500, Title: "Same Number", Source: store.SourceTVDB, Overview: "v1"}}
	tmdbShow := &store.TitleInput{Title: store.Title{Kind: store.KindSeries, TMDBID: 500, Title: "Other Number", Source: store.SourceTMDB, Overview: "v1"},
		Seasons: []store.Season{{Number: 1, Episodes: []store.Episode{{Season: 1, Number: 1}}}}} // its season has episodes: nothing to fetch
	e.tvdb.Shows = map[int64]*store.TitleInput{500: {Title: store.Title{Kind: store.KindSeries, TVDBID: 500, Title: "Same Number", Source: store.SourceTVDB, Overview: "v2"}}}
	e.fake.Shows = map[int64]*store.TitleInput{500: {Title: store.Title{Kind: store.KindSeries, TMDBID: 500, Title: "Other Number", Source: store.SourceTMDB, Overview: "v2"}}}
	titles := map[string]int64{}
	for _, s := range []struct {
		show string
		in   *store.TitleInput
	}{{"Same Number", tvdbShow}, {"Other Number", tmdbShow}} {
		e.addEpisode(s.show, 1, 1)
		s.in.FetchedAt = e.clock.Now().Unix()
		id, err := e.st.UpsertTitle(e.ctx, s.in)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.st.SetSeriesMatch(e.ctx, s.show, store.Match{TitleID: id, State: store.MatchMatched, Score: 1}); err != nil {
			t.Fatal(err)
		}
		titles[s.show] = id
	}
	// The first pass sets the poll time (nothing is old).
	e.once()
	if overview(t, e.wenv, titles["Same Number"]) != "v1" || overview(t, e.wenv, titles["Other Number"]) != "v1" {
		t.Fatal("the first pass refreshed what is fresh")
	}

	e.clock.Advance(25 * time.Hour)
	e.tvdb.ChangedShows = []int64{500}
	e.once()
	if overview(t, e.wenv, titles["Same Number"]) != "v2" || overview(t, e.wenv, titles["Other Number"]) != "v1" {
		t.Errorf("TVDB's updates named 500: same %q other %q; only the TVDB show is due",
			overview(t, e.wenv, titles["Same Number"]), overview(t, e.wenv, titles["Other Number"]))
	}
	if e.tvdb.Count("Changes(series)") != 1 || e.fake.Count("Series(") != 0 {
		t.Errorf("TVDB calls %q, TMDB calls %q", e.tvdb.Calls(), e.fake.Calls())
	}

	// And TMDB's feed names only the TMDB show.
	if _, err := e.st.UpsertTitle(e.ctx, tvdbShow); err != nil { // back to v1
		t.Fatal(err)
	}
	e.clock.Advance(25 * time.Hour)
	e.tvdb.ChangedShows, e.fake.ChangedShows = nil, []int64{500}
	e.once()
	if overview(t, e.wenv, titles["Same Number"]) != "v1" || overview(t, e.wenv, titles["Other Number"]) != "v2" {
		t.Errorf("TMDB's changes named 500: same %q other %q; only the TMDB show is due",
			overview(t, e.wenv, titles["Same Number"]), overview(t, e.wenv, titles["Other Number"]))
	}
}

func TestWorkerRefreshesATVDBShowWhenItsNextEpisodeHasAired(t *testing.T) {
	e := newTEnv(t)
	e.withBBBoth()
	e.addEpisode("Breaking Bad", 1, 1)
	e.once()
	titleID := e.series("Breaking Bad").TitleID
	if e.title(titleID).Source != store.SourceTVDB {
		t.Fatal("not matched at TVDB")
	}

	// The next episode aired 30 hours ago (more than the 6 it waits) and the title was fetched before that.
	aired := e.clock.Now().Add(-30 * time.Hour).Format("2006-01-02")
	old := bbTVDB()
	old.Status, old.NextAirDate, old.FetchedAt = "Continuing", aired, e.clock.Now().Add(-48*time.Hour).Unix()
	if _, err := e.st.UpsertTitle(e.ctx, old); err != nil {
		t.Fatal(err)
	}
	fresh := bbTVDB()
	fresh.Overview = "new episode"
	next := e.clock.Now().Add(6 * 24 * time.Hour).Format("2006-01-02")
	fresh.NextAirDate, fresh.NextEpisodeSeason, fresh.NextEpisodeNumber, fresh.NextEpisodeName = next, 5, 7, "Ozymandias"
	e.tvdb.Shows[81189] = fresh
	before := e.tvdb.Count("Series(")
	e.once()
	if got := e.title(titleID); got.Overview != "new episode" || got.NextAirDate != next || got.NextEpisodeNumber != 7 {
		t.Errorf("a TVDB show whose episode aired was not refreshed: %+v", got)
	}
	if e.tvdb.Count("Series(")-before != 1 || e.fake.Count("Series(") != 0 {
		t.Errorf("TVDB calls %q, TMDB calls %q", e.tvdb.Calls(), e.fake.Calls())
	}
	// Not again until the next one has aired (positive control for the rule above).
	e.once()
	if e.tvdb.Count("Series(")-before != 1 {
		t.Error("refreshed again at once")
	}
}

// When TVDB comes on, the shows TMDB matched move over by the TVDB id that TMDB gave them: no
// search. A person's match moves too and stays theirs; a show TVDB does not know, and one TMDB
// has no TVDB id for, stay on TMDB. It happens once per key.
func TestWorkerMovesShowsFromTMDBToTVDBByIdWhenTVDBComesOn(t *testing.T) {
	e := newWEnv(t) // TMDB only, to begin with
	tvdb := &metadatatest.Fake{Source: metadata.SourceTVDB, ImageBase: e.imgSrv.URL}
	show := func(tmdb, tvdbID int64, name string) *store.TitleInput {
		in := breakingBad()
		in.TMDBID, in.TVDBID, in.Title.Title, in.Overview = tmdb, tvdbID, name, "From TMDB"
		return in
	}
	e.fake.SeriesSearch = map[string][]metadata.Candidate{}
	e.fake.Shows = map[int64]*store.TitleInput{}
	for _, s := range []struct {
		tmdb, tvdb int64
		name       string
	}{{1396, 81189, "Breaking Bad"}, {1438, 79126, "The Wire"}, {2000, 0, "Obscure"}, {1399, 121361, "Manual Show"}} {
		e.fake.SeriesSearch[strings.ToLower(s.name)] = []metadata.Candidate{{ID: s.tmdb, Title: s.name, OriginalTitle: s.name, Popularity: 9}}
		e.fake.Shows[s.tmdb] = show(s.tmdb, s.tvdb, s.name)
		e.addEpisode(s.name, 1, 1)
	}
	e.once()
	if err := e.w.MatchShow(e.ctx, "Manual Show", metadata.Ref{TMDBID: 1399}); err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, n := range []string{"Breaking Bad", "The Wire", "Obscure", "Manual Show"} {
		sm := e.series(n)
		if sm.State == store.MatchPending || e.title(sm.TitleID).Source != store.SourceTMDB {
			t.Fatalf("%s before: %+v", n, sm)
		}
		ids[n] = sm.TitleID
	}
	if e.series("Manual Show").State != store.MatchManual {
		t.Fatal("not manual")
	}

	// TVDB knows two of the shows with a TVDB id, under another overview.
	tvdb.Shows = map[int64]*store.TitleInput{81189: bbTVDB(), 121361: bbTVDB()}
	tvdb.Shows[121361].TVDBID, tvdb.Shows[121361].TMDBID, tvdb.Shows[121361].Title.Title = 121361, 1399, "Manual Show"
	tmdbCalls := len(e.fake.Calls())
	e.src.SetTVDB(tvdb)
	e.once()

	bb, manual := e.title(ids["Breaking Bad"]), e.title(ids["Manual Show"])
	if bb.Source != store.SourceTVDB || bb.TVDBID != 81189 || bb.TMDBID != 1396 || bb.Overview != "From TVDB" {
		t.Errorf("Breaking Bad after the move: %+v", bb)
	}
	if sm := e.series("Breaking Bad"); sm.TitleID != ids["Breaking Bad"] || sm.State != store.MatchMatched {
		t.Errorf("the show's link changed: %+v", sm)
	}
	if manual.Source != store.SourceTVDB {
		t.Errorf("the manual match was not moved: %+v", manual)
	}
	if sm := e.series("Manual Show"); sm.State != store.MatchManual || sm.TitleID != ids["Manual Show"] {
		t.Errorf("a manual match must stay manual: %+v", sm)
	}
	if wire := e.title(ids["The Wire"]); wire.Source != store.SourceTMDB {
		t.Errorf("a show TVDB does not know moved: %+v", wire)
	}
	if obs := e.title(ids["Obscure"]); obs.Source != store.SourceTMDB {
		t.Errorf("a show with no TVDB id moved: %+v", obs)
	}
	if got := e.title(ids["Breaking Bad"]); got.EpisodeOrder != store.OrderAired {
		t.Errorf("order = %q", got.EpisodeOrder)
	}
	// By id: no search at either end, and TMDB was not asked again.
	if tvdb.Count("SearchSeries") != 0 || len(e.fake.Calls()) != tmdbCalls {
		t.Errorf("TVDB calls %q; TMDB calls since: %q", tvdb.Calls(), e.fake.Calls()[tmdbCalls:])
	}
	if want := []string{"Series(79126, aired)", "Series(81189, aired)", "Series(121361, aired)"}; !sameSet(tvdb.Calls(), want) {
		t.Errorf("TVDB was asked %q, want %q (the ones with a TVDB id, once each)", tvdb.Calls(), want)
	}

	// Once: another pass asks TVDB nothing about it, though The Wire is still on TMDB.
	n := len(tvdb.Calls())
	e.once()
	if len(tvdb.Calls()) != n {
		t.Errorf("a second pass asked TVDB again: %q", tvdb.Calls()[n:])
	}
	if v, ok, _ := e.st.GetSetting(e.ctx, metadata.SettingTVDBSwitched); !ok || v != "1" {
		t.Errorf("the note that the move is done = %q, %v", v, ok)
	}

	// A person who now picks TMDB's title for a show, while TVDB is on, keeps it across passes.
	if err := e.w.MatchShow(e.ctx, "Breaking Bad", metadata.Ref{TMDBID: 1396}); err != nil {
		t.Fatal(err)
	}
	if got := e.title(ids["Breaking Bad"]); got.Source != store.SourceTMDB || got.Overview != "From TMDB" {
		t.Fatalf("after picking TMDB's: %+v", got)
	}
	e.clock.Advance(40 * 24 * time.Hour)
	e.once()
	e.w.RefreshShow("Breaking Bad")
	e.once()
	if got := e.title(ids["Breaking Bad"]); got.Source != store.SourceTMDB {
		t.Errorf("a TMDB pick made while TVDB is on was moved: %+v", got)
	}
	if sm := e.series("Breaking Bad"); sm.State != store.MatchManual {
		t.Errorf("state = %q", sm.State)
	}

	// With no TVDB key the note is cleared, so that the next key moves the shows again.
	e.src.SetTVDB(nil)
	e.once()
	if _, ok, _ := e.st.GetSetting(e.ctx, metadata.SettingTVDBSwitched); ok {
		t.Error("the note stayed after the TVDB key went")
	}
	// Positive control that the note is cleared only for a missing key, not for a switched-off one.
	e.src.SetTVDB(tvdb)
	e.once()
	if _, ok, _ := e.st.GetSetting(e.ctx, metadata.SettingTVDBSwitched); !ok {
		t.Fatal("the move did not run again with a key")
	}
	e.src.Set(e.fake, false)
	e.once()
	if _, ok, _ := e.st.GetSetting(e.ctx, metadata.SettingTVDBSwitched); !ok {
		t.Error("switching lookups off cleared the note")
	}
}

func sameSet(got, want []string) bool {
	g, w := slices.Clone(got), slices.Clone(want)
	slices.Sort(g)
	slices.Sort(w)
	return slices.Equal(g, w)
}

// A show TVDB cannot be asked about while moving (out of reach) is tried again later, and the
// move is not marked done; one TVDB refuses is left on TMDB.
func TestWorkerMoveToTVDBWaitsOutAnOutageAndEndsOnARefusedKey(t *testing.T) {
	e := newWEnv(t)
	tvdb := &metadatatest.Fake{Source: metadata.SourceTVDB, ImageBase: e.imgSrv.URL}
	e.fake.SeriesSearch = map[string][]metadata.Candidate{"breaking bad": {{ID: 1396, Title: "Breaking Bad", OriginalTitle: "Breaking Bad", Popularity: 9}}}
	e.fake.Shows = map[int64]*store.TitleInput{1396: bbTMDB()}
	e.addEpisode("Breaking Bad", 1, 1)
	e.once()
	id := e.series("Breaking Bad").TitleID

	tvdb.Shows = map[int64]*store.TitleInput{81189: bbTVDB()}
	tvdb.Offline()
	e.src.SetTVDB(tvdb)
	e.once()
	if e.title(id).Source != store.SourceTMDB {
		t.Fatal("moved while TVDB was down")
	}
	if _, ok, _ := e.st.GetSetting(e.ctx, metadata.SettingTVDBSwitched); ok {
		t.Error("the move was marked done after an outage")
	}
	// It does not hammer TVDB: the next pass within the delay asks nothing.
	n := len(tvdb.Calls())
	e.once()
	if len(tvdb.Calls()) != n {
		t.Errorf("asked again within the delay: %q", tvdb.Calls()[n:])
	}
	tvdb.Online()
	e.clock.Advance(20 * time.Minute)
	e.once()
	if e.title(id).Source != store.SourceTVDB {
		t.Errorf("after TVDB came back: %+v", e.title(id))
	}
}

// Switching the order maps the files to TVDB's episodes in another numbering. The files keep
// their season and episode; what changes is which episode each finds.
func TestWorkerEpisodeOrderRemapsEpisodes(t *testing.T) {
	e := newTEnv(t)
	anime := func(order string) *store.TitleInput {
		ep := func(s, n int, name string) store.Episode { return store.Episode{Season: s, Number: n, Name: name} }
		in := &store.TitleInput{Title: store.Title{Kind: store.KindSeries, TVDBID: 900001, Title: "Test Anime", Source: store.SourceTVDB, EpisodeOrder: order}}
		if order == store.OrderAbsolute {
			in.Seasons = []store.Season{{Number: 1, Episodes: []store.Episode{ep(1, 1, "Awakening"), ep(1, 2, "The Road"), ep(1, 3, "Return"), ep(1, 4, "Reckoning")}}}
		} else {
			in.Seasons = []store.Season{{Number: 1, Episodes: []store.Episode{ep(1, 1, "Awakening"), ep(1, 2, "The Road")}},
				{Number: 2, Episodes: []store.Episode{ep(2, 1, "Return"), ep(2, 2, "Reckoning")}}}
		}
		return in
	}
	e.tvdb.SeriesSearch = map[string][]metadata.Candidate{"test anime": {{ID: 900001, Title: "Test Anime", OriginalTitle: "Test Anime", Popularity: 9}}}
	e.tvdb.Shows = map[int64]*store.TitleInput{900001: anime("aired")}
	e.tvdb.ShowOrders = map[metadatatest.ShowOrder]*store.TitleInput{{ID: 900001, Order: "absolute"}: anime("absolute")}
	for _, se := range [][2]int{{1, 1}, {1, 2}, {2, 1}, {1, 3}} { // the last is a file named by its absolute number
		e.addEpisode("Test Anime", se[0], se[1])
	}
	e.once()
	titleID := e.series("Test Anime").TitleID
	if got := e.title(titleID); got.EpisodeOrder != "aired" || got.Locked {
		t.Fatalf("title = %+v", got)
	}
	name := func(season, ep int) string {
		t.Helper()
		got, err := e.st.GetEpisode(e.ctx, titleID, season, ep)
		if errors.Is(err, store.ErrNotFound) {
			return "-"
		}
		if err != nil {
			t.Fatal(err)
		}
		return got.Name
	}
	if name(2, 1) != "Return" || name(1, 3) != "-" {
		t.Fatalf("aired: S02E01 %q, S01E03 %q", name(2, 1), name(1, 3))
	}

	if err := e.w.SetEpisodeOrder(e.ctx, titleID, store.OrderAbsolute); err != nil {
		t.Fatal(err)
	}
	got := e.title(titleID)
	if got.EpisodeOrder != "absolute" || got.Locked || got.Source != store.SourceTVDB {
		t.Errorf("title after = %+v; the order changes, nothing else, and does not lock", got)
	}
	// S01E03 is now an episode; S02E01 is not any more, and the second season is gone.
	if name(1, 3) != "Return" || name(2, 1) != "-" || name(1, 4) != "Reckoning" {
		t.Errorf("absolute: S01E03 %q S02E01 %q S01E04 %q", name(1, 3), name(2, 1), name(1, 4))
	}
	seasons, _ := e.st.Seasons(e.ctx, titleID)
	if len(seasons) != 1 || seasons[0].Number != 1 {
		t.Errorf("seasons = %+v; the aired seasons are replaced", seasons)
	}
	if want := "Series(900001, absolute)"; e.tvdb.Calls()[len(e.tvdb.Calls())-1] != want {
		t.Errorf("last TVDB call = %q, want %q", e.tvdb.Calls()[len(e.tvdb.Calls())-1], want)
	}

	// A refresh keeps the order; so does matching the show again.
	e.w.RefreshShow("Test Anime")
	e.once()
	if last := e.tvdb.Calls()[len(e.tvdb.Calls())-1]; last != "Series(900001, absolute)" {
		t.Errorf("a refresh asked for %q; it must keep the order", last)
	}
	if err := e.w.MatchShow(e.ctx, "Test Anime", metadata.Ref{TVDBID: 900001}); err != nil {
		t.Fatal(err)
	}
	if e.title(titleID).EpisodeOrder != "absolute" || name(1, 3) != "Return" {
		t.Errorf("a re-match lost the order: %+v", e.title(titleID))
	}

	// And back.
	if err := e.w.SetEpisodeOrder(e.ctx, titleID, store.OrderAired); err != nil {
		t.Fatal(err)
	}
	if name(2, 1) != "Return" || name(1, 3) != "-" || e.title(titleID).EpisodeOrder != "aired" {
		t.Errorf("back to aired: S02E01 %q S01E03 %q", name(2, 1), name(1, 3))
	}

	// What it cannot do: a show not from TVDB, an order TVDB has none of (nothing changes), a
	// title that is not there, TVDB unreachable.
	e.tvdb.Hook = func(_ context.Context, call string) error {
		if strings.Contains(call, "dvd") {
			return metadata.ErrNoSuchOrder
		}
		return nil
	}
	before := e.title(titleID)
	if err := e.w.SetEpisodeOrder(e.ctx, titleID, store.OrderDVD); !errors.Is(err, metadata.ErrNoSuchOrder) {
		t.Errorf("DVD = %v", err)
	}
	if after := e.title(titleID); after.EpisodeOrder != before.EpisodeOrder || name(2, 1) != "Return" {
		t.Errorf("a refused order changed the show: %+v", after)
	}
	if err := e.w.SetEpisodeOrder(e.ctx, titleID, "random"); err == nil {
		t.Error("an unknown order was accepted")
	}
	if err := e.w.SetEpisodeOrder(e.ctx, 99999, store.OrderDVD); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown title = %v", err)
	}
	e.tvdb.Hook = nil
	e.tvdb.Offline()
	if err := e.w.SetEpisodeOrder(e.ctx, titleID, store.OrderAbsolute); !metadata.IsTransient(err) {
		t.Errorf("TVDB down = %v", err)
	}
	if e.title(titleID).EpisodeOrder != "aired" {
		t.Error("the order changed although TVDB could not be reached")
	}
	e.tvdb.Online()

	e.withMatrix()
	mv := e.addMovie("Movies/The Matrix (1999).mkv", "The Matrix", 1999)
	e.once()
	if err := e.w.SetEpisodeOrder(e.ctx, e.entry(mv).TitleID, store.OrderAbsolute); !errors.Is(err, metadata.ErrNotTVDB) {
		t.Errorf("a movie = %v", err)
	}
	e.fake.SeriesSearch = map[string][]metadata.Candidate{"tmdb show": {{ID: 77, Title: "TMDB Show", OriginalTitle: "TMDB Show", Popularity: 9}}}
	e.fake.Shows = map[int64]*store.TitleInput{77: {Title: store.Title{Kind: store.KindSeries, TMDBID: 77, Title: "TMDB Show", Source: store.SourceTMDB}}}
	e.addEpisode("TMDB Show", 1, 1)
	e.once()
	if err := e.w.SetEpisodeOrder(e.ctx, e.series("TMDB Show").TitleID, store.OrderAbsolute); !errors.Is(err, metadata.ErrNotTVDB) {
		t.Errorf("a show from TMDB = %v", err)
	}
	e.src.SetTVDB(nil)
	if err := e.w.SetEpisodeOrder(e.ctx, titleID, store.OrderAbsolute); !errors.Is(err, metadata.ErrNoTVDB) {
		t.Errorf("no TVDB key = %v", err)
	}
}

func TestWorkerManualMatchesThroughTVDB(t *testing.T) {
	e := newTEnv(t)
	e.withBBBoth()
	e.tvdb.Slugs = map[string]int64{"breaking-bad": 81189}
	e.tvdb.FindSeries = map[string][]metadata.Candidate{"imdb_id:tt0903747": {{ID: 81189, Title: "Breaking Bad"}}}
	e.addEpisode("Some Name", 1, 1)

	for _, ref := range []metadata.Ref{{TVDBID: 81189}, {TVDBSlug: "breaking-bad"}, {IMDbID: "tt0903747"}} {
		if err := e.w.UnmatchShow(e.ctx, "Some Name"); err != nil {
			t.Fatal(err)
		}
		if err := e.w.MatchShow(e.ctx, "Some Name", ref); err != nil {
			t.Fatalf("%+v: %v", ref, err)
		}
		sm := e.series("Some Name")
		if title := e.title(sm.TitleID); sm.State != store.MatchManual || title.Source != store.SourceTVDB || title.TVDBID != 81189 {
			t.Errorf("%+v: %+v, %+v", ref, sm.Match, title)
		}
	}
	if e.fake.Count("Series(") != 0 || e.fake.Count("Find") != 0 {
		t.Errorf("TMDB was asked: %q", e.fake.Calls())
	}

	// TMDB's title, picked by hand while TVDB is on, is TMDB's; it replaces TVDB's on the one row.
	if err := e.w.MatchShow(e.ctx, "Some Name", metadata.Ref{TMDBID: 1396}); err != nil {
		t.Fatal(err)
	}
	sm := e.series("Some Name")
	if title := e.title(sm.TitleID); sm.State != store.MatchManual || title.Source != store.SourceTMDB || title.TMDBID != 1396 {
		t.Errorf("TMDB pick: %+v, %+v", sm.Match, title)
	}
	n, err := e.st.Titles(e.ctx, store.TitleFilter{})
	if err != nil || len(n) != 1 {
		t.Errorf("%d titles after matching one show three ways, %v; one show is one title", len(n), err)
	}

	// An unknown TVDB id, and a movie asked a TVDB title.
	if err := e.w.MatchShow(e.ctx, "Some Name", metadata.Ref{TVDBID: 424242}); !errors.Is(err, metadata.ErrNoSuchTitle) {
		t.Errorf("unknown TVDB id = %v", err)
	}
	mv := e.addMovie("Movies/Foo (2000).mkv", "Foo", 2000)
	if err := e.w.MatchMovie(e.ctx, mv, metadata.Ref{TVDBID: 81189}); !errors.Is(err, metadata.ErrNoSuchTitle) {
		t.Errorf("a movie matched to a TVDB title = %v", err)
	}
	e.src.SetTVDB(nil)
	if err := e.w.MatchShow(e.ctx, "Some Name", metadata.Ref{TVDBID: 81189}); !errors.Is(err, metadata.ErrNoTVDB) {
		t.Errorf("no TVDB key = %v", err)
	}
}

func TestWorkerSearchListsBothAndNamesATVDBShow(t *testing.T) {
	e := newTEnv(t)
	e.withBBBoth()
	e.tvdb.Slugs = map[string]int64{"breaking-bad": 81189}
	q := func(text string) metadata.Query { return metadata.Query{Kind: store.KindSeries, Title: text} }

	res, err := e.w.Search(e.ctx, q("Breaking Bad"))
	if err != nil || len(res) != 2 || res[0].Source != "tvdb" || res[1].Source != "tmdb" {
		t.Fatalf("search = %+v, %v", res, err)
	}
	res, err = e.w.Search(e.ctx, q("https://thetvdb.com/series/breaking-bad"))
	if err != nil || len(res) != 1 || res[0].ID != 81189 || res[0].Source != "tvdb" || res[0].Score != 1 {
		t.Errorf("by address = %+v, %v", res, err)
	}
	if _, err = e.w.Search(e.ctx, q("tvdb:424242")); !errors.Is(err, metadata.ErrNoSuchTitle) {
		t.Errorf("by an id that is not there = %v", err)
	}
	// Movies are TMDB's alone.
	e.withMatrix()
	res, err = e.w.Search(e.ctx, metadata.Query{Kind: store.KindMovie, Title: "The Matrix"})
	if err != nil || len(res) != 1 || res[0].Source != "tmdb" {
		t.Errorf("movie search = %+v, %v", res, err)
	}
	if e.tvdb.Count("SearchMovies") != 0 {
		t.Error("TVDB was searched for a movie")
	}
}

// A TVDB person whose record gives a TMDB id that another person has is that person.
func TestWorkerPersonOfTVDBMergesIntoTheTMDBPerson(t *testing.T) {
	e := newTEnv(t)
	e.withBBBoth()
	show := bbTVDB()
	show.Credits = []store.Credit{{Person: store.Person{TVDBID: 296140, Name: "Bryan Cranston", Profile: "tvdb:/banners/b.jpg"}, Kind: store.CreditCast, Character: "Walter"}}
	e.tvdb.Shows[81189] = show
	e.tvdb.People = map[int64]*store.Person{296140: {TVDBID: 296140, TMDBID: 17419, Name: "Bryan Cranston", Biography: "From TVDB", IMDbID: "nm0186505"}}
	e.addEpisode("Breaking Bad", 1, 1)
	// A movie with the same man, from TMDB.
	e.withMatrix()
	m := matrix()
	m.Credits = append(m.Credits, store.Credit{Person: store.Person{TMDBID: 17419, Name: "Bryan Cranston"}, Kind: store.CreditCast, Character: "Cameo"})
	e.fake.Movies[603] = m
	e.addMovie("Movies/The Matrix (1999).mkv", "The Matrix", 1999)
	e.once()

	d, err := e.st.GetTitleDetail(e.ctx, e.series("Breaking Bad").TitleID)
	if err != nil || len(d.Cast) != 1 {
		t.Fatalf("detail = %+v, %v", d, err)
	}
	tvdbPerson := d.Cast[0].PersonID
	got, err := e.w.EnsurePersonID(e.ctx, tvdbPerson)
	if err != nil {
		t.Fatal(err)
	}
	if got == tvdbPerson {
		t.Fatal("the TVDB person was not merged into the TMDB one")
	}
	p, err := e.st.GetPerson(e.ctx, got)
	if err != nil || p.TMDBID != 17419 || p.TVDBID != 296140 || p.Biography != "From TVDB" || len(p.Credits) != 2 {
		t.Errorf("merged person = %+v, %v", p, err)
	}
	if e.fake.Count("Person(") != 0 {
		t.Errorf("TMDB was asked for a person TVDB described: %q", e.fake.Calls())
	}
	// Asking about the id that is gone is a 404-style answer, and the survivor needs no fetching.
	if _, err := e.w.EnsurePersonID(e.ctx, tvdbPerson); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the old id = %v", err)
	}
	if again, err := e.w.EnsurePersonID(e.ctx, got); err != nil || again != got {
		t.Errorf("the survivor again = %d, %v", again, err)
	}

	// A person only TVDB knows, with no TVDB key now, cannot be fetched.
	other := bbTVDB()
	other.Credits = []store.Credit{{Person: store.Person{TVDBID: 555, Name: "Only Tvdb"}, Kind: store.CreditCast}}
	other.TVDBID, other.TMDBID, other.Title.Title = 900009, 0, "Other"
	e.tvdb.Shows[900009] = other
	e.tvdb.SeriesSearch["other"] = []metadata.Candidate{{ID: 900009, Title: "Other", OriginalTitle: "Other", Popularity: 9}}
	e.addEpisode("Other", 1, 1)
	e.once()
	d, _ = e.st.GetTitleDetail(e.ctx, e.series("Other").TitleID)
	e.src.SetTVDB(nil)
	if _, err := e.w.EnsurePersonID(e.ctx, d.Cast[0].PersonID); !errors.Is(err, metadata.ErrLookupsOff) {
		t.Errorf("a TVDB person with no TVDB key = %v", err)
	}
}

// An NFO's text is laid over TVDB's data as over TMDB's, and the title stays TVDB's: its source
// is what tells a refresh where to ask.
func TestWorkerNFOOverTVDBKeepsTheSource(t *testing.T) {
	e := newTEnv(t)
	e.withBBBoth()
	e.write("TV/Breaking Bad/tvshow.nfo", `<tvshow><title>Breaking Bad</title><plot>My plot.</plot></tvshow>`)
	e.addEpisode("Breaking Bad", 1, 1)
	e.once()
	title := e.title(e.series("Breaking Bad").TitleID)
	if title.Source != store.SourceTVDB || title.TVDBID != 81189 || title.Overview != "My plot." {
		t.Errorf("title = %+v", title)
	}
	// A refresh keeps the overlay and still asks TVDB.
	e.w.RefreshShow("Breaking Bad")
	before := e.tvdb.Count("Series(")
	e.once()
	if got := e.title(title.ID); got.Source != store.SourceTVDB || got.Overview != "My plot." || e.tvdb.Count("Series(") != before+1 {
		t.Errorf("after a refresh: %+v, TVDB calls %q", got, e.tvdb.Calls())
	}
	// Control: the same NFO over a TMDB title makes it "mixed".
	m := newWEnv(t)
	m.withBreakingBad()
	m.write("TV/Breaking Bad/tvshow.nfo", `<tvshow><title>Breaking Bad</title><plot>My plot.</plot></tvshow>`)
	m.addEpisode("Breaking Bad", 1, 1)
	m.once()
	if got := m.title(m.series("Breaking Bad").TitleID); got.Source != store.SourceMixed {
		t.Errorf("TMDB title with an NFO: %+v", got)
	}
}

// A TVDB id in an NFO or a folder name is matched exactly, through the show's own record: no search.
func TestWorkerTVDBIdHintIsMatchedExactlyAndAWrongOneFallsBackToTheTitle(t *testing.T) {
	e := newTEnv(t)
	e.withBBBoth()
	e.write("TV/Weird Name/tvshow.nfo", `<tvshow><title>Weird Name</title><uniqueid type="tvdb">81189</uniqueid></tvshow>`)
	e.addEpisode("Weird Name", 1, 1)
	e.once()
	sm := e.series("Weird Name")
	if sm.State != store.MatchMatched || sm.Score != 1 || e.title(sm.TitleID).TVDBID != 81189 || e.title(sm.TitleID).Source != store.SourceTVDB {
		t.Fatalf("show = %+v", sm)
	}
	if e.tvdb.Count("SearchSeries") != 0 || len(e.fake.Calls()) != 0 {
		t.Errorf("TVDB calls %q, TMDB calls %q: an id needs no search", e.tvdb.Calls(), e.fake.Calls())
	}

	// A folder tag naming an id TVDB does not have is dropped, and the title is searched.
	e.write("TV/Breaking Bad [tvdbid-424242]/x", "x")
	e.addEpisode("Breaking Bad [tvdbid-424242]", 1, 1)
	e.once()
	sm = e.series("Breaking Bad [tvdbid-424242]")
	if sm.State != store.MatchMatched || e.title(sm.TitleID).TVDBID != 81189 || e.tvdb.Count("SearchSeries") == 0 {
		t.Errorf("a wrong id: %+v, TVDB calls %q", sm, e.tvdb.Calls())
	}
}
