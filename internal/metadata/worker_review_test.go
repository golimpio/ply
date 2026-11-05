package metadata_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ply/internal/metadata"
	"ply/internal/metadata/metadatatest"
	"ply/internal/store"
)

// The independent review of M8 (B1, S1 to S3, S6): what it found, as tests.

var tvdbRefusedKey = &metadata.TVDBError{Op: "POST /login", Status: http.StatusUnauthorized, Message: "TVDB rejected the key or PIN"}

// wireTVDB is The Wire as TVDB gives it.
func wireTVDB() *store.TitleInput {
	in := bbTVDB()
	in.TVDBID, in.TMDBID, in.Title.Title, in.Overview = 79126, 1438, "The Wire", "Wire from TVDB"
	return in
}

// A TVDB key that TVDB refuses does not end the pass: the work at TMDB (here the refresh of a
// movie) goes on, a show from TVDB is asked about once (the others from TVDB wait for the key),
// and a new show that needs TVDB waits an hour.
func TestWorkerRefusedTVDBKeyDoesNotStallWhatIsAtTMDB(t *testing.T) {
	e := newTEnv(t)
	e.withBBBoth()
	e.withMatrix()
	e.tvdb.SeriesSearch["the wire"] = []metadata.Candidate{{ID: 79126, Title: "The Wire", OriginalTitle: "The Wire", Year: 2002, Popularity: 9}}
	e.tvdb.Shows[79126] = wireTVDB()
	e.fake.Movies[603].FetchedAt = time.Now().Add(24 * time.Hour).Unix() // the movie comes last in a refresh, after the shows
	e.addEpisode("Breaking Bad", 1, 1)
	e.addEpisode("The Wire", 1, 1)
	e.addMovie("Movies/The Matrix (1999).mkv", "The Matrix", 1999)
	e.once()
	for _, show := range []string{"Breaking Bad", "The Wire"} {
		if sm := e.series(show); sm.State != store.MatchMatched || e.title(sm.TitleID).Source != store.SourceTVDB {
			t.Fatalf("setup: %s = %+v", show, sm)
		}
	}

	// Control: with TVDB healthy, forty days on, the movie is fetched again.
	before := e.fake.Count("Movie(603)")
	e.clock.Advance(40 * 24 * time.Hour)
	e.once()
	control := e.fake.Count("Movie(603)") - before
	if control == 0 {
		t.Fatal("control: the movie was not refreshed after forty days")
	}

	// Now TVDB refuses the key, and a new show is waiting to be matched.
	e.tvdb.SetError(tvdbRefusedKey)
	e.addEpisode("Obscure Show", 1, 1)
	movies, tvdbSeries := e.fake.Count("Movie(603)"), e.tvdb.Count("Series(")
	start := e.clock.Now()
	e.clock.Advance(40 * 24 * time.Hour)
	for range 3 {
		e.once()
	}
	if got := e.fake.Count("Movie(603)") - movies; got < control {
		t.Errorf("movie fetched %d times in the passes after a refused TVDB key, %d with TVDB healthy: the refusal stalled TMDB's work", got, control)
	}
	if got := e.tvdb.Count("Series(") - tvdbSeries; got != 1 {
		t.Errorf("TVDB was asked for %d shows in the refreshes, want 1: once it refuses, the others from TVDB wait", got)
	}
	// What needed TVDB waits an hour; it is not unmatched.
	if sm := e.series("Obscure Show"); sm.State != store.MatchPending || sm.Attempts != 1 || sm.NextAttemptAt != start.Add(40*24*time.Hour+time.Hour).Unix() {
		t.Errorf("the new show: %+v; want it pending, put off an hour", sm.Match)
	}
	// Positive control: with the key accepted again it is matched, and the shows from TVDB are refreshed.
	e.tvdb.SetError(nil)
	e.tvdb.SeriesSearch["obscure show"] = []metadata.Candidate{{ID: 81189, Title: "Obscure Show", OriginalTitle: "Obscure Show", Popularity: 9}}
	e.clock.Advance(2 * time.Hour)
	e.once()
	if sm := e.series("Obscure Show"); sm.State != store.MatchMatched {
		t.Errorf("the new show once TVDB accepts the key: %+v", sm.Match)
	}
}

// TVDB out of reach during a refresh is not the end of it either: the shows from TVDB wait (one is
// asked, the others are not), what is at TMDB goes on, and the refresh is tried again soon.
func TestWorkerTVDBOutageInARefreshDoesNotStallWhatIsAtTMDB(t *testing.T) {
	e := newTEnv(t)
	e.withBBBoth()
	e.withMatrix()
	e.tvdb.SeriesSearch["the wire"] = []metadata.Candidate{{ID: 79126, Title: "The Wire", OriginalTitle: "The Wire", Year: 2002, Popularity: 9}}
	e.tvdb.Shows[79126] = wireTVDB()
	e.fake.Movies[603].FetchedAt = time.Now().Add(24 * time.Hour).Unix() // the movie comes last in a refresh, after the shows
	e.addEpisode("Breaking Bad", 1, 1)
	e.addEpisode("The Wire", 1, 1)
	matrixItem := e.addMovie("Movies/The Matrix (1999).mkv", "The Matrix", 1999)
	e.once()
	bb := e.series("Breaking Bad").TitleID
	if e.title(bb).Overview != "From TVDB" {
		t.Fatalf("setup: %+v", e.title(bb))
	}
	// What the show says on TVDB changes, and TVDB goes away; the data is old by then.
	e.tvdb.Shows[81189].Overview = "Changed at TVDB"
	e.tvdb.Shows[79126].Overview = "Changed at TVDB"
	e.tvdb.SetError(&metadata.TVDBError{Op: "GET /series/79126/extended", Transient: true, Message: "connection refused"})
	movies, series := e.fake.Count("Movie(603)"), e.tvdb.Count("Series(")
	e.clock.Advance(40 * 24 * time.Hour)
	e.once()
	if got := e.fake.Count("Movie(603)") - movies; got != 1 {
		t.Errorf("movie fetched %d times in the pass with TVDB out of reach, want 1: the outage stalled TMDB's work", got)
	}
	if got := e.tvdb.Count("Series(") - series; got != 1 {
		t.Errorf("TVDB was asked for %d shows, want 1: once it is out of reach the others from TVDB wait", got)
	}
	if got := e.entry(matrixItem).MatchState; got != store.MatchMatched {
		t.Errorf("the movie = %s", got)
	}
	// It is tried again soon, not after the refresh period (a day): the shows are due and TVDB is back.
	e.tvdb.SetError(nil)
	e.clock.Advance(2 * time.Hour)
	e.once()
	if got := e.title(bb).Overview; got != "Changed at TVDB" {
		t.Errorf("the show from TVDB, after TVDB came back: %q; the refresh was not tried again", got)
	}
}

// feedEnv is a worker with a show from TVDB (500) and a show from TMDB (600), both fresh and matched, and a first
// refresh behind it, which has set the poll times.
func feedEnv(t *testing.T) (e *tenv, titles map[string]int64) {
	t.Helper()
	e = newTEnv(t)
	tvdbShow := &store.TitleInput{Title: store.Title{Kind: store.KindSeries, TVDBID: 500, Title: "Same Number", Source: store.SourceTVDB, Overview: "v1"}}
	tmdbShow := &store.TitleInput{Title: store.Title{Kind: store.KindSeries, TMDBID: 600, Title: "Other Number", Source: store.SourceTMDB, Overview: "v1"},
		Seasons: []store.Season{{Number: 1, Episodes: []store.Episode{{Season: 1, Number: 1}}}}}
	e.tvdb.Shows = map[int64]*store.TitleInput{500: {Title: store.Title{Kind: store.KindSeries, TVDBID: 500, Title: "Same Number", Source: store.SourceTVDB, Overview: "v2"}}}
	e.fake.Shows = map[int64]*store.TitleInput{600: {Title: store.Title{Kind: store.KindSeries, TMDBID: 600, Title: "Other Number", Source: store.SourceTMDB, Overview: "v2"}}}
	titles = map[string]int64{}
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
	return e, titles
}

// setting reads a settings row ("" for none).
func (e *wenv) setting(key string) string {
	e.t.Helper()
	v, _, err := e.st.GetSetting(e.ctx, key)
	if err != nil {
		e.t.Fatal(err)
	}
	return v
}

// A refused TVDB key in the updates feed holds back TVDB's share of the refresh and nobody else's: what TMDB's
// feed lists is refreshed and its poll moves; TVDB's poll stays, TVDB's feed is not asked again for an hour, and
// the status line says that the key is rejected (an outage is another thing). Once the key works, only TVDB's
// share is done, and the refresh is complete.
func TestWorkerRefusedTVDBKeyInTheFeedHoldsBackOnlyTVDBsShare(t *testing.T) {
	e, titles := feedEnv(t)
	e.once() // the first pass sets the poll times
	tmdbPoll, tvdbPoll, last := e.setting(metadata.SettingChangesPoll), e.setting(metadata.SettingTVDBChangesPoll), e.setting(metadata.SettingLastRefresh)
	if tmdbPoll == "" || tvdbPoll == "" || last == "" {
		t.Fatalf("setup: poll times %q %q, last refresh %q", tmdbPoll, tvdbPoll, last)
	}
	if tmdbPoll != tvdbPoll {
		t.Fatalf("setup: the two polls differ: %q %q", tmdbPoll, tvdbPoll)
	}

	e.clock.Advance(25 * time.Hour)
	e.tvdb.SetError(tvdbRefusedKey)
	e.fake.ChangedShows = []int64{600}
	e.once()
	if got := overview(t, e.wenv, titles["Other Number"]); got != "v2" {
		t.Errorf("the show at TMDB after TVDB's feed refused the key: %q; TMDB's changes should have been refreshed", got)
	}
	if got := overview(t, e.wenv, titles["Same Number"]); got != "v1" {
		t.Errorf("the show from TVDB was refreshed (%q) though TVDB refuses the key", got)
	}
	if st := e.w.Status(e.ctx); st.ShowsLine != "TVDB rejected the key or PIN" {
		t.Errorf("status line = %q, want it to say the key is rejected", st.ShowsLine)
	}
	if got := e.setting(metadata.SettingChangesPoll); got == tmdbPoll {
		t.Errorf("TMDB's poll time did not move (%q): its share was done", got)
	}
	if got := e.setting(metadata.SettingTVDBChangesPoll); got != tvdbPoll {
		t.Errorf("TVDB's poll time moved to %q (was %q): its feed was not read", got, tvdbPoll)
	}
	if got := e.setting(metadata.SettingLastRefresh); got != last {
		t.Errorf("the refresh is marked complete (%q): TVDB's share is not", got)
	}
	if e.tvdb.Count("Changes(") != 1 {
		t.Errorf("TVDB's feed was asked %d times, want 1", e.tvdb.Count("Changes("))
	}

	// Within the hour TVDB's feed is not asked again, and TMDB's share, which is done, is not either.
	e.clock.Advance(30 * time.Minute)
	tmdbFeed := e.fake.Count("Changes(")
	e.once()
	if e.tvdb.Count("Changes(") != 1 || e.fake.Count("Changes(") != tmdbFeed {
		t.Errorf("a pass within the hour asked TVDB's feed %d times in all and TMDB's %d more", e.tvdb.Count("Changes("), e.fake.Count("Changes(")-tmdbFeed)
	}

	// The key is accepted: TVDB's share is done, TVDB's show is refreshed, and the refresh is complete.
	e.tvdb.SetError(nil)
	e.tvdb.ChangedShows = []int64{500}
	e.clock.Advance(31 * time.Minute)
	e.once()
	if got := overview(t, e.wenv, titles["Same Number"]); got != "v2" {
		t.Errorf("the show from TVDB once the key is accepted: %q", got)
	}
	if got := e.setting(metadata.SettingTVDBChangesPoll); got == tvdbPoll {
		t.Errorf("TVDB's poll time did not move once its share was done")
	}
	if got := e.setting(metadata.SettingLastRefresh); got == last {
		t.Errorf("the refresh is not marked complete")
	}
	if e.fake.Count("Changes(") != tmdbFeed {
		t.Errorf("TMDB's feed was asked %d more times: only TVDB's share was left", e.fake.Count("Changes(")-tmdbFeed)
	}
	if st := e.w.Status(e.ctx); st.ShowsLine != "Shows: TVDB" {
		t.Errorf("status line = %q once TVDB answered", st.ShowsLine)
	}
}

// TVDB's updates feed out of reach (an outage, not a refused key) does not stall TMDB's refresh: its changed
// movie is refreshed. TVDB's share is tried again soon, and only it.
func TestWorkerTVDBOutageInTheFeedDoesNotStallTMDBsRefresh(t *testing.T) {
	e := newTEnv(t)
	e.withBBBoth()
	e.withMatrix()
	e.addEpisode("Breaking Bad", 1, 1)
	e.addMovie("Movies/The Matrix (1999).mkv", "The Matrix", 1999)
	e.once() // matches them, and sets the poll times
	bb := e.series("Breaking Bad").TitleID
	tmdbPoll, tvdbPoll, last := e.setting(metadata.SettingChangesPoll), e.setting(metadata.SettingTVDBChangesPoll), e.setting(metadata.SettingLastRefresh)
	if tmdbPoll == "" || tvdbPoll == "" || last == "" {
		t.Fatalf("setup: poll times %q %q, last refresh %q", tmdbPoll, tvdbPoll, last)
	}

	e.clock.Advance(48 * time.Hour)
	e.fake.ChangedMovies = []int64{603}
	e.tvdb.Shows[81189].Overview = "Changed at TVDB"
	e.tvdb.Offline()
	before := e.fake.Count("Movie(603)")
	e.once()
	if got := e.fake.Count("Movie(603)") - before; got != 1 {
		t.Errorf("TMDB's changed movie was fetched %d times with TVDB's feed down, want 1", got)
	}
	if st := e.w.Status(e.ctx); st.ShowsLine != "TVDB unreachable" {
		t.Errorf("status line = %q, want an outage (the key is not rejected)", st.ShowsLine)
	}
	if got := e.setting(metadata.SettingChangesPoll); got == tmdbPoll {
		t.Error("TMDB's poll time did not move")
	}
	if got := e.setting(metadata.SettingTVDBChangesPoll); got != tvdbPoll {
		t.Errorf("TVDB's poll time moved to %q (was %q)", got, tvdbPoll)
	}
	if got := e.setting(metadata.SettingLastRefresh); got != last {
		t.Errorf("the refresh is marked complete (%q)", got)
	}

	// TVDB is back and the retry time has come: only TVDB's share is done.
	e.tvdb.SetError(nil)
	e.tvdb.ChangedShows = []int64{81189}
	e.clock.Advance(16 * time.Minute)
	tmdbFeed, movies := e.fake.Count("Changes("), e.fake.Count("Movie(")
	e.once()
	if got := e.title(bb).Overview; got != "Changed at TVDB" {
		t.Errorf("the show from TVDB after the retry: %q", got)
	}
	if e.fake.Count("Changes(") != tmdbFeed || e.fake.Count("Movie(") != movies {
		t.Errorf("TMDB was asked again for its share, which was done: %q", e.fake.Calls()[len(e.fake.Calls())-2:])
	}
	if got := e.setting(metadata.SettingLastRefresh); got == last {
		t.Error("the refresh is not marked complete")
	}
	if st := e.w.Status(e.ctx); st.ShowsLine != "Shows: TVDB" {
		t.Errorf("status line = %q once TVDB answered", st.ShowsLine)
	}
}

// A new key may be what the refresh was waiting for: Reload ends the wait of a part that was put off.
func TestWorkerReloadEndsTheWaitOfARefreshThatWasPutOff(t *testing.T) {
	e, titles := feedEnv(t)
	e.once()
	e.clock.Advance(25 * time.Hour)
	e.tvdb.SetError(tvdbRefusedKey)
	e.tvdb.ChangedShows = []int64{500}
	e.once()
	if got := overview(t, e.wenv, titles["Same Number"]); got != "v1" {
		t.Fatalf("control: the show from TVDB was refreshed: %q", got)
	}
	// The key is replaced a minute later (the wait is an hour): the settings call Reload.
	e.tvdb.SetError(nil)
	e.clock.Advance(time.Minute)
	e.once()
	if got := overview(t, e.wenv, titles["Same Number"]); got != "v1" {
		t.Fatalf("control: the show was refreshed within the wait: %q", got)
	}
	e.w.Reload()
	e.once()
	if got := overview(t, e.wenv, titles["Same Number"]); got != "v2" {
		t.Errorf("the show from TVDB after Reload: %q; the wait was not ended", got)
	}
}

// On the age path (no poll within the feed's reach) a refused key leaves the shows from TVDB as they are, and
// then neither TVDB's poll time nor the time of the last refresh may move: nothing was refreshed for them.
func TestWorkerRefusedTVDBKeyOnTheAgePathMovesNeitherPollNorLastRefresh(t *testing.T) {
	e, titles := feedEnv(t)             // (no pass yet: no poll times)
	e.clock.Advance(8 * 24 * time.Hour) // the data is a week old
	e.tvdb.SetError(tvdbRefusedKey)
	e.once()
	if got := overview(t, e.wenv, titles["Other Number"]); got != "v2" {
		t.Errorf("the show at TMDB: %q; it is old, and TMDB is not the one refusing", got)
	}
	if got := overview(t, e.wenv, titles["Same Number"]); got != "v1" {
		t.Errorf("the show from TVDB was refreshed: %q", got)
	}
	if e.setting(metadata.SettingChangesPoll) == "" {
		t.Error("TMDB's poll time was not set: its share is done")
	}
	if got := e.setting(metadata.SettingTVDBChangesPoll); got != "" {
		t.Errorf("TVDB's poll time was set to %q though its titles were passed over", got)
	}
	if got := e.setting(metadata.SettingLastRefresh); got != "" {
		t.Errorf("the refresh is marked complete (%q) though TVDB's titles were passed over", got)
	}

	// Positive control: once the key is accepted, the same titles are refreshed and the times are set.
	e.tvdb.SetError(nil)
	e.clock.Advance(61 * time.Minute)
	e.once()
	if got := overview(t, e.wenv, titles["Same Number"]); got != "v2" {
		t.Errorf("the show from TVDB once the key is accepted: %q", got)
	}
	if e.setting(metadata.SettingTVDBChangesPoll) == "" || e.setting(metadata.SettingLastRefresh) == "" {
		t.Error("the times were not set once TVDB's share was done")
	}
}

// A TMDB key that is refused still ends the pass (control for the two above).
func TestWorkerRefusedTMDBKeyStillEndsTheRefresh(t *testing.T) {
	e := newTEnv(t)
	e.withBBBoth()
	e.withMatrix()
	e.addEpisode("Breaking Bad", 1, 1)
	e.addMovie("Movies/The Matrix (1999).mkv", "The Matrix", 1999)
	e.once()
	e.fake.SetError(&metadata.Error{Op: "GET /movie/603", Status: http.StatusUnauthorized, Message: "Invalid API key"})
	e.clock.Advance(40 * 24 * time.Hour)
	movies := e.fake.Count("Movie(")
	e.once()
	if got := e.fake.Count("Movie(") - movies; got != 1 {
		t.Errorf("%d movie fetches in the pass after a refused TMDB key, want 1: the pass ends on it", got)
	}
	if st := e.w.Status(e.ctx); !strings.Contains(st.LastError, "Invalid API key") {
		t.Errorf("status last error = %q", st.LastError)
	}
}

// S1: the person fetched for was merged into another by a request that won the race. The loser's
// answer is the person that is left, not 0.
func TestWorkerEnsurePersonIDReturnsTheSurvivorOfALostRace(t *testing.T) {
	e := newTEnv(t)
	e.withBBBoth()
	show := bbTVDB()
	show.Credits = []store.Credit{{Person: store.Person{TVDBID: 296140, Name: "Bryan Cranston"}, Kind: store.CreditCast, Character: "Walter"}}
	e.tvdb.Shows[81189] = show
	e.tvdb.People = map[int64]*store.Person{296140: {TVDBID: 296140, TMDBID: 17419, Name: "Bryan Cranston", Biography: "From TVDB"}}
	e.addEpisode("Breaking Bad", 1, 1)
	e.withMatrix()
	m := matrix()
	m.Credits = append(m.Credits, store.Credit{Person: store.Person{TMDBID: 17419, Name: "Bryan Cranston"}, Kind: store.CreditCast, Character: "Cameo"})
	e.fake.Movies[603] = m
	movieItem := e.addMovie("Movies/The Matrix (1999).mkv", "The Matrix", 1999)
	e.once()
	d, _ := e.st.GetTitleDetail(e.ctx, e.series("Breaking Bad").TitleID)
	tvdbPerson := d.Cast[0].PersonID

	// While this request is at TVDB, another request for the same person merges it.
	e.tvdb.Hook = func(ctx context.Context, call string) error {
		if strings.HasPrefix(call, "Person(") {
			e.tvdb.Hook = nil
			if _, err := e.st.SetPersonDetailsMerged(ctx, tvdbPerson, store.Person{TMDBID: 17419, Biography: "x"}, 1); err != nil {
				t.Errorf("setup merge: %v", err)
			}
		}
		return nil
	}
	got, err := e.w.EnsurePersonID(e.ctx, tvdbPerson)
	if err != nil {
		t.Fatalf("EnsurePersonID after losing the race: %v", err)
	}
	// Positive control: the person it asked about is gone, and the one that is left has the TMDB id.
	if _, err := e.st.GetPerson(e.ctx, tvdbPerson); err == nil {
		t.Fatal("control: the person was not merged away")
	}
	md, err := e.st.GetTitleDetail(e.ctx, e.title(e.entry(movieItem).TitleID).ID)
	if err != nil {
		t.Fatal(err)
	}
	var survivor int64
	for _, c := range md.Cast {
		if c.Name == "Bryan Cranston" {
			survivor = c.PersonID
		}
	}
	if survivor == 0 {
		t.Fatal("control: the movie has no cast member Bryan Cranston")
	}
	if got == 0 || got != survivor {
		t.Errorf("EnsurePersonID = %d, want the survivor %d", got, survivor)
	}
	if p, err := e.st.GetPerson(e.ctx, got); err != nil || p.TVDBID != 296140 {
		t.Errorf("the survivor = %+v, %v; it holds the TVDB id", p.Person, err)
	}
}

// And when the store fails for another reason the id stays what it was, with the error.
func TestWorkerEnsurePersonIDKeepsTheIDOnAFailure(t *testing.T) {
	e := newTEnv(t)
	e.withBBBoth()
	show := bbTVDB()
	show.Credits = []store.Credit{{Person: store.Person{TVDBID: 296140, Name: "Bryan Cranston"}, Kind: store.CreditCast, Character: "Walter"}}
	e.tvdb.Shows[81189] = show
	e.tvdb.People = map[int64]*store.Person{296140: {TVDBID: 296140, Name: "Bryan Cranston", Biography: "From TVDB"}}
	episode := e.addEpisode("Breaking Bad", 1, 1)
	e.once()
	d, _ := e.st.GetTitleDetail(e.ctx, e.series("Breaking Bad").TitleID)
	id := d.Cast[0].PersonID
	// The person is gone before the request stores its answer (the file was removed and the library
	// pruned), and TVDB gave no TMDB id to find another by.
	e.tvdb.Hook = func(ctx context.Context, call string) error {
		if strings.HasPrefix(call, "Person(") {
			e.tvdb.Hook = nil
			if err := e.st.DeleteItems(ctx, []int64{episode}); err != nil {
				t.Errorf("setup: %v", err)
			}
			if _, err := e.st.PruneMetadata(ctx); err != nil {
				t.Errorf("setup: %v", err)
			}
		}
		return nil
	}
	got, err := e.w.EnsurePersonID(e.ctx, id)
	if _, gerr := e.st.GetPerson(e.ctx, id); gerr == nil {
		t.Fatal("control: the person is still there")
	}
	if err == nil || got != id {
		t.Errorf("EnsurePersonID of a person that is gone = %d, %v; want the id it was given and the error", got, err)
	}
}

// P3: a show from TVDB, in absolute order, that a person then picks by hand at TMDB: it is TMDB's
// show, with TMDB's episodes and the aired order (the order of a TMDB show is not a thing).
func TestWorkerManualFlipFromTVDBToTMDBDropsTVDBEpisodesAndOrder(t *testing.T) {
	e := newTEnv(t)
	ep := func(s, n int, name string) store.Episode { return store.Episode{Season: s, Number: n, Name: name} }
	anime := func(order string) *store.TitleInput {
		in := &store.TitleInput{Title: store.Title{Kind: store.KindSeries, TVDBID: 900001, TMDBID: 500, Title: "Test Anime", Source: store.SourceTVDB, EpisodeOrder: order}}
		if order == store.OrderAbsolute {
			var eps []store.Episode
			for i := 1; i <= 12; i++ {
				eps = append(eps, ep(1, i, "abs"))
			}
			// (a season that TMDB does not bring too)
			in.Seasons = []store.Season{{Number: 1, Episodes: eps}, {Number: 2, Episodes: []store.Episode{ep(2, 1, "abs season 2")}}}
		} else {
			in.Seasons = []store.Season{{Number: 1, Episodes: []store.Episode{ep(1, 1, "a1"), ep(1, 2, "a2")}}}
		}
		return in
	}
	e.tvdb.SeriesSearch = map[string][]metadata.Candidate{"test anime": {{ID: 900001, Title: "Test Anime", OriginalTitle: "Test Anime", Popularity: 9}}}
	e.tvdb.Shows = map[int64]*store.TitleInput{900001: anime("aired")}
	e.tvdb.ShowOrders = map[metadatatest.ShowOrder]*store.TitleInput{{ID: 900001, Order: "absolute"}: anime("absolute")}
	tm := &store.TitleInput{Title: store.Title{Kind: store.KindSeries, TMDBID: 500, TVDBID: 900001, Title: "Test Anime", Source: store.SourceTMDB},
		Seasons: []store.Season{{Number: 1, Episodes: []store.Episode{ep(1, 1, "t1"), ep(1, 2, "t2")}}}}
	e.fake.Shows = map[int64]*store.TitleInput{500: tm}
	e.addEpisode("Test Anime", 1, 1)
	e.once()
	id := e.series("Test Anime").TitleID
	if err := e.w.SetEpisodeOrder(e.ctx, id, store.OrderAbsolute); err != nil {
		t.Fatal(err)
	}
	// Positive control: the show is TVDB's, in absolute order, with its thirteen episodes.
	if got, eps := e.title(id), episodesOf(t, e.wenv, id); got.Source != store.SourceTVDB || got.EpisodeOrder != store.OrderAbsolute || len(eps) != 13 {
		t.Fatalf("before the pick: %s %s, %d episodes", got.Source, got.EpisodeOrder, len(eps))
	}
	if err := e.w.MatchShow(e.ctx, "Test Anime", metadata.Ref{TMDBID: 500}); err != nil {
		t.Fatal(err)
	}
	got, eps := e.title(id), episodesOf(t, e.wenv, id)
	if got.Source != store.SourceTMDB || got.EpisodeOrder != store.OrderAired {
		t.Errorf("after a manual TMDB pick: source %s, order %s; want tmdb and aired", got.Source, got.EpisodeOrder)
	}
	if len(eps) != 2 || eps[0].Name != "t1" || eps[1].Name != "t2" {
		t.Errorf("episodes = %+v, want TMDB's two (the TVDB absolute ones are gone)", eps)
	}
}

func episodesOf(t *testing.T, e *wenv, id int64) []store.Episode {
	t.Helper()
	eps, err := e.st.Episodes(e.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return eps
}

// S2: the move to TVDB asks Pace before each show, as every other pass does.
func TestWorkerMoveToTVDBAsksPace(t *testing.T) {
	p := newPacer()
	e := newWEnv(t, func(o *metadata.WorkerOptions) { o.Pace = p.pace })
	e.withBreakingBad()
	e.fake.Shows[1396] = bbTMDB()
	e.addEpisode("Breaking Bad", 1, 1)
	e.once()
	id := e.series("Breaking Bad").TitleID
	paced := p.count()
	if e.title(id).Source != store.SourceTMDB {
		t.Fatal("setup: not a show from TMDB")
	}

	// The queue wants the machine, and TVDB comes on.
	p.refuseAfter(0)
	tv := &metadatatest.Fake{Source: metadata.SourceTVDB, ImageBase: e.imgSrv.URL}
	tv.Shows = map[int64]*store.TitleInput{81189: bbTVDB()}
	e.src.SetTVDB(tv)
	e.once()
	if p.count() == paced {
		t.Error("the move did not ask Pace")
	}
	if n := tv.Count("Series("); n != 0 || e.title(id).Source != store.SourceTMDB {
		t.Errorf("the move fetched %d shows from TVDB (source now %s) though Pace refused", n, e.title(id).Source)
	}
	if _, ok, _ := e.st.GetSetting(e.ctx, metadata.SettingTVDBSwitched); ok {
		t.Error("the move was marked done without having been made")
	}

	// Positive control: when Pace allows, the show is moved.
	p.allowAll()
	e.once()
	if e.title(id).Source != store.SourceTVDB {
		t.Errorf("the show after Pace allowed: %s", e.title(id).Source)
	}
}

// S2: so does the refresh, before each title, whether it goes by age or by the feeds.
func TestWorkerRefreshAsksPaceForEachTitle(t *testing.T) {
	p := newPacer()
	e, _, _ := twoMatchedMovies(t, p)
	// What a fetch stores is stamped with the clock of the test, so that a title just refreshed is not due.
	stamp := func() {
		for _, m := range e.fake.Movies {
			m.FetchedAt = e.clock.Now().Unix()
		}
	}

	e.clock.Advance(40 * 24 * time.Hour)
	stamp()
	base, fetched := p.count(), e.fake.Count("Movie(")
	e.once()
	if got := p.count() - base; got != 2 {
		t.Errorf("%d calls of Pace for a refresh of two titles, want 2", got)
	}
	if got := e.fake.Count("Movie(") - fetched; got != 2 {
		t.Fatalf("control: %d titles refreshed, want 2", got)
	}

	// Refused after the first: one is refreshed, the other waits.
	e.clock.Advance(40 * 24 * time.Hour)
	stamp()
	p.refuseAfter(1)
	fetched = e.fake.Count("Movie(")
	e.once()
	if got := e.fake.Count("Movie(") - fetched; got != 1 {
		t.Errorf("%d titles refreshed while Pace refused after the first, want 1", got)
	}
	// Positive control: the next pass does the one that waited, and only that.
	p.allowAll()
	e.once()
	if got := e.fake.Count("Movie(") - fetched; got != 2 {
		t.Errorf("%d titles refreshed in all, want 2: the one that waited, and no other again", got)
	}
}

// S2: and the fetch of a season that has files and no episodes.
func TestWorkerFetchOfASeasonAsksPace(t *testing.T) {
	p := newPacer()
	e := newWEnv(t, func(o *metadata.WorkerOptions) { o.Pace = p.pace })
	e.withBreakingBad()
	e.addEpisode("Breaking Bad", 1, 1)
	e.once()
	id := e.series("Breaking Bad").TitleID

	e.addEpisode("Breaking Bad", 3, 1)
	calls, base := len(e.fake.Calls()), p.count()
	p.refuseAfter(0)
	e.w.Notify()
	e.once()
	if got := p.count() - base; got != 1 {
		t.Errorf("%d calls of Pace for the fetch of a season, want 1", got)
	}
	if need, _ := e.st.SeasonsNeedingFetch(e.ctx, id); !slices.Equal(need, []int{3}) {
		t.Fatalf("seasons needing a fetch = %v; Pace refused, so it should still be needed", need)
	}
	if rest := e.fake.Calls()[calls:]; len(rest) != 0 {
		t.Errorf("provider calls while Pace refused: %q", rest)
	}
	// Positive control: asked for next pass at once, not after the wait for a season TMDB has none of.
	p.allowAll()
	e.once()
	if need, _ := e.st.SeasonsNeedingFetch(e.ctx, id); len(need) != 0 {
		t.Errorf("the season was not fetched when Pace allowed: %v", need)
	}
}

// S3: a refresh that began before a change of order finishes after it, and does not put the
// old order (or the seasons fetched in it) back.
func TestWorkerRefreshDoesNotRevertAConcurrentOrderChange(t *testing.T) {
	e := newTEnv(t)
	ep := func(s, n int, name string) store.Episode { return store.Episode{Season: s, Number: n, Name: name} }
	anime := func(order string) *store.TitleInput {
		in := &store.TitleInput{Title: store.Title{Kind: store.KindSeries, TVDBID: 900001, Title: "Test Anime", Source: store.SourceTVDB, EpisodeOrder: order}}
		if order == store.OrderAbsolute {
			in.Seasons = []store.Season{{Number: 1, Episodes: []store.Episode{ep(1, 1, "abs1"), ep(1, 2, "abs2"), ep(1, 3, "abs3")}}}
		} else {
			in.Seasons = []store.Season{{Number: 1, Episodes: []store.Episode{ep(1, 1, "a1")}}, {Number: 2, Episodes: []store.Episode{ep(2, 1, "a2")}}}
		}
		return in
	}
	e.tvdb.SeriesSearch = map[string][]metadata.Candidate{"test anime": {{ID: 900001, Title: "Test Anime", OriginalTitle: "Test Anime", Popularity: 9}}}
	e.tvdb.Shows = map[int64]*store.TitleInput{900001: anime("aired")}
	e.tvdb.ShowOrders = map[metadatatest.ShowOrder]*store.TitleInput{{ID: 900001, Order: "absolute"}: anime("absolute")}
	e.addEpisode("Test Anime", 1, 1)
	e.once()
	id := e.series("Test Anime").TitleID
	if got := e.title(id); got.EpisodeOrder != store.OrderAired || len(episodesOf(t, e.wenv, id)) != 2 {
		t.Fatalf("setup: %s, %d episodes", got.EpisodeOrder, len(episodesOf(t, e.wenv, id)))
	}

	nested := false
	e.tvdb.Hook = func(ctx context.Context, call string) error {
		if call == "Series(900001, aired)" && !nested {
			nested = true
			if err := e.w.SetEpisodeOrder(ctx, id, store.OrderAbsolute); err != nil { // the person's PATCH, while the refresh is at TVDB
				t.Errorf("setup: %v", err)
			}
		}
		return nil
	}
	e.w.RefreshShow("Test Anime")
	e.once()
	if !nested {
		t.Fatal("control: the refresh never asked TVDB in the aired order")
	}
	got, eps := e.title(id), episodesOf(t, e.wenv, id)
	if got.EpisodeOrder != store.OrderAbsolute {
		t.Errorf("the person's change to absolute was reverted to %q by the refresh that was under way", got.EpisodeOrder)
	}
	if len(eps) != 3 || eps[0].Name != "abs1" {
		t.Errorf("episodes = %+v, want the three of the absolute order", eps)
	}
	// The refresh after it, in the new order, does what it should.
	e.tvdb.Hook = nil
	e.w.RefreshShow("Test Anime")
	e.once()
	if got := e.title(id); got.EpisodeOrder != store.OrderAbsolute || len(episodesOf(t, e.wenv, id)) != 3 {
		t.Errorf("after the next refresh: %s, %d episodes", got.EpisodeOrder, len(episodesOf(t, e.wenv, id)))
	}
}

// S6: Reload, which the settings call when a key changes, clears what the move to TVDB has
// learned about a show it asked for and TVDB did not know, so that TVDB is asked about it again.
func TestWorkerReloadMakesTheMoveToTVDBAskAgainAboutAShowTVDBDidNotKnow(t *testing.T) {
	e, tv, ids := manyTMDBShows(t, 22)
	// The first show (the lowest id, asked first) is one TVDB does not know yet.
	unknown := tvdbIDOf(0)
	delete(tv.Shows, unknown)
	e.src.SetTVDB(tv)
	e.once() // asks 20 shows: the unknown one among them
	if n := tv.Count("Series("); n != 20 {
		t.Fatalf("control: %d shows asked in the first pass, want 20", n)
	}
	if got := e.title(ids[0]); got.Source != store.SourceTMDB {
		t.Fatalf("control: the unknown show moved: %+v", got)
	}

	// TVDB learns the show, and the settings change.
	tv.Shows[unknown] = tvdbShowOf(0)
	e.w.Reload()
	e.once()
	if got := e.title(ids[0]); got.Source != store.SourceTVDB {
		t.Errorf("after Reload the show TVDB had not known is still %s", got.Source)
	}
	// Positive control: without Reload it would not have been asked again, and the rest were asked once.
	if n := tv.Count("Series("); n != 20+3 {
		t.Errorf("%d asks of TVDB in all, want 23 (20, then the 2 left and the show that was not known)", n)
	}
	if v, ok, _ := e.st.GetSetting(e.ctx, metadata.SettingTVDBSwitched); !ok || v != "1" {
		t.Errorf("the move is not marked done: %q, %v", v, ok)
	}
}

// S6: the move takes twenty shows a pass; the cutoff (the highest title id when TVDB came on) is
// kept across passes, so a show matched at TMDB meanwhile (TVDB did not know it) is not moved.
func TestWorkerMoveToTVDBTakesTwentyShowsAPassAndKeepsItsCutoff(t *testing.T) {
	var woke atomic.Int32
	e, tv, ids := manyTMDBShows(t, 23, func(o *metadata.WorkerOptions) { o.OnWake = func() { woke.Add(1) } })
	e.src.SetTVDB(tv)

	// A show that TVDB does not know is matched at TMDB in the first pass, after the cutoff is taken;
	// its TVDB id is on TMDB's record, and TVDB would have it by id if it were asked.
	late := breakingBad()
	late.TMDBID, late.TVDBID, late.Title.Title, late.Overview = 4000, 8000, "Late Show", "From TMDB"
	e.fake.SeriesSearch["late show"] = []metadata.Candidate{{ID: 4000, Title: "Late Show", OriginalTitle: "Late Show", Popularity: 9}}
	e.fake.Shows[4000] = late
	lateTVDB := bbTVDB()
	lateTVDB.TMDBID, lateTVDB.TVDBID, lateTVDB.Title.Title = 4000, 8000, "Late Show"
	tv.Shows[8000] = lateTVDB
	e.addEpisode("Late Show", 1, 1)

	wokeBefore := woke.Load()
	e.once()
	moved := func() (n int) {
		for _, id := range ids {
			if e.title(id).Source == store.SourceTVDB {
				n++
			}
		}
		return n
	}
	if got := tv.Count("Series("); got != 20 {
		t.Errorf("%d shows asked in the first pass, want 20", got)
	}
	if got := moved(); got != 20 {
		t.Errorf("%d shows moved in the first pass, want 20", got)
	}
	if _, ok, _ := e.st.GetSetting(e.ctx, metadata.SettingTVDBSwitched); ok {
		t.Error("the move was marked done with three shows left")
	}
	if v, ok, _ := e.st.GetSetting(e.ctx, metadata.SettingTVDBSwitchUpTo); !ok || v == "" {
		t.Errorf("the cutoff was not kept: %q, %v", v, ok)
	}
	if woke.Load() == wokeBefore {
		t.Error("the worker was not woken to do the rest")
	}
	sm := e.series("Late Show")
	if sm.State != store.MatchMatched || e.title(sm.TitleID).Source != store.SourceTMDB {
		t.Fatalf("control: the late show = %+v, source %s", sm.Match, e.title(sm.TitleID).Source)
	}

	wokeBefore = woke.Load()
	e.once()
	if got := moved(); got != 23 {
		t.Errorf("%d shows moved after the second pass, want all 23", got)
	}
	if v, ok, _ := e.st.GetSetting(e.ctx, metadata.SettingTVDBSwitched); !ok || v != "1" {
		t.Errorf("the move is not marked done: %q, %v", v, ok)
	}
	if _, ok, _ := e.st.GetSetting(e.ctx, metadata.SettingTVDBSwitchUpTo); ok {
		t.Error("the cutoff stayed after the move was done")
	}
	if got := e.title(sm.TitleID); got.Source != store.SourceTMDB {
		t.Errorf("the show matched at TMDB after TVDB came on was moved: %+v", got)
	}
	if got := tv.Count("Series(8000"); got != 0 {
		t.Errorf("TVDB was asked for the late show %d times", got)
	}
	if got := tv.Count("Series("); got != 23 {
		t.Errorf("%d asks of TVDB in all, want 23, once each", got)
	}
	if woke.Load() != wokeBefore {
		t.Errorf("the last batch woke the worker %d times", woke.Load()-wokeBefore)
	}
}

func tvdbIDOf(i int) int64 { return int64(5000 + i) }

func tvdbShowOf(i int) *store.TitleInput {
	in := bbTVDB()
	in.TVDBID, in.TMDBID, in.Title.Title, in.Overview = tvdbIDOf(i), int64(3000+i), fmt.Sprintf("Show %02d", i), "From TVDB"
	return in
}

// manyTMDBShows matches n shows at TMDB (the TVDB side is off), each with a TVDB id on its record, and
// returns the environment, a TVDB that knows them all (not yet set on the source) and their title ids.
func manyTMDBShows(t *testing.T, n int, mod ...func(*metadata.WorkerOptions)) (*wenv, *metadatatest.Fake, []int64) {
	t.Helper()
	e := newWEnv(t, mod...)
	tv := &metadatatest.Fake{Source: metadata.SourceTVDB, ImageBase: e.imgSrv.URL, Shows: map[int64]*store.TitleInput{}, SeriesSearch: map[string][]metadata.Candidate{}}
	e.fake.SeriesSearch = map[string][]metadata.Candidate{}
	e.fake.Shows = map[int64]*store.TitleInput{}
	for i := range n {
		name := fmt.Sprintf("Show %02d", i)
		in := breakingBad()
		in.TMDBID, in.TVDBID, in.Title.Title, in.Overview = int64(3000+i), tvdbIDOf(i), name, "From TMDB"
		e.fake.SeriesSearch[strings.ToLower(name)] = []metadata.Candidate{{ID: in.TMDBID, Title: name, OriginalTitle: name, Popularity: 9}}
		e.fake.Shows[in.TMDBID] = in
		tv.Shows[tvdbIDOf(i)] = tvdbShowOf(i)
		e.addEpisode(name, 1, 1)
	}
	e.once()
	ids := make([]int64, n)
	for i := range n {
		sm := e.series(fmt.Sprintf("Show %02d", i))
		if sm.State != store.MatchMatched || e.title(sm.TitleID).Source != store.SourceTMDB {
			t.Fatalf("setup: show %d = %+v", i, sm.Match)
		}
		ids[i] = sm.TitleID
	}
	return e, tv, ids
}

// S6: the sidecar notifications that Pace refused are put back, and only they: with no refresh
// asked for, nothing else rereads the NFO of the item that was refused.
func TestWorkerPaceThatRefusesRequeuesTheSidecarNotifications(t *testing.T) {
	p := newPacer()
	e, matrixItem, duneItem := twoMatchedMovies(t, p)
	e.write("Movies/The Matrix (1999).nfo", `<movie><title>x</title><plot>Matrix plot from the NFO.</plot></movie>`)
	e.write("Movies/Dune (2021).nfo", `<movie><title>x</title><plot>Dune plot from the NFO.</plot></movie>`)
	reread := func() (n int) {
		for _, item := range []int64{matrixItem, duneItem} {
			if e.title(e.entry(item).TitleID).Source == store.SourceMixed {
				n++
			}
		}
		return n
	}

	p.refuseAfter(1)
	e.w.NotifySidecars([]int64{matrixItem, duneItem})
	e.once()
	if got := reread(); got != 1 {
		t.Fatalf("%d NFOs read in the pass that Pace stopped, want 1", got)
	}

	p.allowAll()
	calls := len(e.fake.Calls())
	e.once()
	if got := reread(); got != 2 {
		t.Errorf("%d NFOs read after the next pass, want 2: the notification that was refused is lost", got)
	}
	if rest := e.fake.Calls()[calls:]; len(rest) != 1 {
		t.Errorf("provider calls in the next pass = %q; one reread, fetched once", rest)
	}
	// Nothing is left: the next pass reads nothing.
	calls = len(e.fake.Calls())
	e.once()
	if rest := e.fake.Calls()[calls:]; len(rest) != 0 {
		t.Errorf("a notification was handled twice: %q", rest)
	}
}

// Another folder or name of a show that is on TVDB already, which TVDB does not find and TMDB does (the
// fallback), is linked to the show's TVDB row: the match does not turn the show into TMDB's, with its
// episodes dropped.
func TestWorkerFallbackMatchOfAnotherNameKeepsTheShowOnTVDB(t *testing.T) {
	e := newTEnv(t)
	e.withBBBoth()
	e.addEpisode("Breaking Bad", 1, 1)
	e.once()
	first := e.series("Breaking Bad").TitleID
	if got := e.title(first); got.Source != store.SourceTVDB || got.Overview != "From TVDB" {
		t.Fatalf("setup: %+v", got)
	}

	// TVDB has nothing for the second name; TMDB has the same show under it.
	e.fake.SeriesSearch["breaking bad extra"] = []metadata.Candidate{{ID: 1396, Title: "Breaking Bad Extra", OriginalTitle: "Breaking Bad Extra", Year: 2008, Popularity: 9}}
	e.addEpisode("Breaking Bad Extra", 1, 1)
	e.once()
	sm := e.series("Breaking Bad Extra")
	if sm.State != store.MatchMatched || sm.TitleID != first {
		t.Fatalf("the second name = %+v; want it matched to the TVDB row %d", sm.Match, first)
	}
	if e.fake.Count("Series(1396") == 0 {
		t.Error("control: TMDB was not asked for the show, so this was no fallback")
	}
	got := e.title(first)
	if got.Source != store.SourceTVDB || got.Overview != "From TVDB" || got.EpisodeOrder != store.OrderAired {
		t.Errorf("the show after the fallback match of its second name: %+v", got)
	}
	if eps := episodesOf(t, e.wenv, first); len(eps) != 3 || eps[1].Name != "TVDB S1E2" {
		t.Errorf("episodes = %+v; the show lost TVDB's", eps)
	}
	// Nothing was stored, so TMDB's artwork was not fetched for it (the show's own is TVDB's).
	if _, err := os.Stat(filepath.Join(e.imgDir, "tmdb", "w342", "bb.jpg")); err == nil {
		t.Error("TMDB's poster was prefetched for a link that stored nothing")
	}
	if _, err := os.Stat(filepath.Join(e.imgDir, "tvdb", "w342", "banners", "posters", "81189-10.jpg")); err != nil {
		t.Errorf("control: TVDB's own poster was not prefetched: %v", err)
	}
	if sm := e.series("Breaking Bad"); sm.TitleID != first || sm.State != store.MatchMatched {
		t.Errorf("the first name = %+v", sm.Match)
	}

	// A person who picks TMDB's title for the second name does turn it into TMDB's.
	if err := e.w.MatchShow(e.ctx, "Breaking Bad Extra", metadata.Ref{TMDBID: 1396}); err != nil {
		t.Fatal(err)
	}
	if got := e.title(first); got.Source != store.SourceTMDB {
		t.Errorf("after a person's pick of TMDB's: %+v", got)
	}
}

// TVDB refuses the key for days. TMDB's share of the refresh has its own period: it starts a new one every
// time, however long TVDB's share stays undone (it was held back, as a share done while the other was not,
// from the second cycle on).
func TestWorkerLongTVDBRefusalDoesNotStarveTMDBRefreshCycles(t *testing.T) {
	e := newTEnv(t)
	e.withBBBoth()
	e.withMatrix()
	e.addEpisode("Breaking Bad", 1, 1)
	e.addMovie("Movies/The Matrix (1999).mkv", "The Matrix", 1999)
	e.once()
	first := e.setting(metadata.SettingLastRefresh)
	if first == "" {
		t.Fatal("setup: no refresh was recorded")
	}
	e.clock.Advance(25 * time.Hour)
	e.tvdb.SetError(tvdbRefusedKey)
	e.fake.ChangedMovies = []int64{603}
	var got []int
	for range 4 {
		before := e.fake.Count("Movie(603)")
		e.once()
		got = append(got, e.fake.Count("Movie(603)")-before)
		e.clock.Advance(25 * time.Hour) // a new refresh period each time; TVDB still refuses
	}
	for i, n := range got {
		if n != 1 {
			t.Errorf("cycle %d: TMDB's changed movie was refreshed %d times, want 1 (%v)", i, n, got)
		}
	}
	// The time of the last refresh is the older of the two: TVDB's share was never done again.
	if now := e.setting(metadata.SettingLastRefresh); now != first {
		t.Errorf("the refresh is marked complete at %q (was %q): TVDB's share is not done", now, first)
	}
	if tmdb, tvdb := e.setting(metadata.SettingTMDBLastRefresh), e.setting(metadata.SettingTVDBLastRefresh); tmdb == first || tvdb != first {
		t.Errorf("the shares' times: TMDB %q TVDB %q, want TMDB moved on and TVDB at %q", tmdb, tvdb, first)
	}

	// Positive control: once TVDB answers, its share is done and the refresh is complete again.
	e.tvdb.SetError(nil)
	e.clock.Advance(time.Hour)
	e.once()
	if now := e.setting(metadata.SettingLastRefresh); now == first {
		t.Error("the refresh is not marked complete when TVDB's share is done")
	}
}

// An install from before the shares had each a time of their own has the one time of the refresh: a share with
// none takes it, so nothing is refreshed ahead of its period.
func TestWorkerAShareWithNoTimeOfItsOwnTakesTheTimeOfTheRefresh(t *testing.T) {
	e, titles := feedEnv(t)
	e.once() // sets everything
	for _, k := range []string{metadata.SettingTMDBLastRefresh, metadata.SettingTVDBLastRefresh} {
		if err := e.st.DeleteSetting(e.ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	e.fake.ChangedShows = []int64{600}
	e.clock.Advance(time.Hour) // (the refresh of the one time was an hour ago)
	e.once()
	if got := overview(t, e.wenv, titles["Other Number"]); got != "v1" {
		t.Errorf("the show at TMDB was refreshed (%q) an hour after the last refresh", got)
	}
	e.clock.Advance(24 * time.Hour)
	e.once()
	if got := overview(t, e.wenv, titles["Other Number"]); got != "v2" {
		t.Errorf("the show at TMDB a period later: %q", got)
	}
}
