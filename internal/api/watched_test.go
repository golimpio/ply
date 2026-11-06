package api

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"testing"

	"ply/internal/store"
)

func (m *menv) putWatched(path string, body string) *httptest.ResponseRecorder {
	m.t.Helper()
	return m.do("PUT", path, body)
}

const (
	markWatched   = `{"watched": true}`
	markUnwatched = `{"watched": false}`
)

// addFile indexes one more file in the test library.
func (m *menv) addFile(name string, it store.Item) int64 {
	m.t.Helper()
	it.Path, it.Root, it.Size, it.MTime, it.Container = filepath.Join(m.dir, name), m.dir, 1, 1, "mkv"
	if it.DurationSec == 0 {
		it.DurationSec = 2400
	}
	id, err := m.store.UpsertItem(context.Background(), &it, streamsH264())
	if err != nil {
		m.t.Fatal(err)
	}
	return id
}

// addVersion adds another file of the film "The Matrix" (the title of the item e.mp4).
func (m *menv) addVersion(name string) int64 {
	m.t.Helper()
	id := m.addFile(name, store.Item{Kind: store.KindMovie, Title: "The Matrix", Year: 1999, DurationSec: 7200})
	if err := m.store.SetItemMatch(context.Background(), id, store.Match{TitleID: m.matrixTitle, State: store.MatchMatched, Score: 0.95}); err != nil {
		m.t.Fatal(err)
	}
	return id
}

func (m *menv) addEpisode(season, ep int) int64 {
	m.t.Helper()
	return m.addFile(fmt.Sprintf("Cool Show S%02dE%02d.mkv", season, ep), store.Item{Kind: store.KindEpisode, Show: "Cool Show", Season: season, Episode: ep})
}

func (m *menv) item(id int64) ItemSummary {
	m.t.Helper()
	for _, it := range decode[[]ItemSummary](m.t, m.get("/api/items")) {
		if it.ID == id {
			return it
		}
	}
	m.t.Fatalf("item %d not in the list", id)
	return ItemSummary{}
}

func (m *menv) card(kind string) TitleCard {
	m.t.Helper()
	for _, c := range decode[[]TitleCard](m.t, m.get("/api/titles")) {
		if c.Kind == kind {
			return c
		}
	}
	m.t.Fatalf("no %s card", kind)
	return TitleCard{}
}

func resultIDs(r WatchResult) []int64 {
	out := make([]int64, len(r.Items))
	for i, it := range r.Items {
		out[i] = it.ID
	}
	return out
}

// An episode is marked watched and unwatched through its own endpoint, and the answer, the list, the
// detail and Continue Watching all say so.
func TestMarkAnEpisodeWatched(t *testing.T) {
	m := newMetaEnv(t, "")
	id := m.mkv // 2400 s
	m.putProgress(id, `{"positionSec": 600, "durationSec": 2400}`, nil)
	if cont := decode[[]ItemSummary](t, m.get("/api/continue")); len(cont) != 1 {
		t.Fatalf("control: continue = %d items, want the one that was started", len(cont))
	}

	rec := m.putWatched(fmt.Sprintf("/api/items/%d/watched", id), markWatched)
	wantStatus(t, rec, 200)
	got := decode[WatchResult](t, rec)
	if !got.Watched || len(got.Items) != 1 {
		t.Fatalf("answer = %+v", got)
	}
	it := got.Items[0]
	if it.ID != id || !it.Watched || it.WatchedAt == nil || *it.WatchedAt == 0 || it.PlayCount != 1 || it.Progress == nil || !it.Progress.Watched || it.Progress.PositionSec != 0 {
		t.Errorf("answer item = %+v / %+v, want watched once, at position 0", it, it.Progress)
	}

	s := m.item(id)
	if !s.Watched || s.PlayCount != 1 || s.WatchedAt == nil || s.Progress == nil || !s.Progress.Watched || s.Progress.PositionSec != 0 {
		t.Errorf("list: %+v / %+v", s.WatchState, s.Progress)
	}
	d := decode[ItemDetail](t, m.get(fmt.Sprintf("/api/items/%d", id)))
	if !d.Watched || d.PlayCount != 1 {
		t.Errorf("detail: %+v", d.WatchState)
	}
	if cont := decode[[]ItemSummary](t, m.get("/api/continue")); len(cont) != 0 {
		t.Errorf("continue lists a watched episode: %+v", cont)
	}

	// Unwatched: the position goes too, and the keys are there with null.
	rec = m.putWatched(fmt.Sprintf("/api/items/%d/watched", id), markUnwatched)
	wantStatus(t, rec, 200)
	got = decode[WatchResult](t, rec)
	if got.Watched || len(got.Items) != 1 || got.Items[0].Watched || got.Items[0].Progress != nil || got.Items[0].WatchedAt != nil || got.Items[0].PlayCount != 0 {
		t.Errorf("answer after unwatching = %+v", got)
	}
	raw := decode[[]map[string]any](t, m.get("/api/items"))
	for _, r := range raw {
		for _, key := range []string{"watched", "watchedAt", "playCount", "progress"} {
			if _, ok := r[key]; !ok {
				t.Errorf("item %v has no %q key", r["id"], key)
			}
		}
		if r["watchedAt"] != nil || r["watched"] != false || r["progress"] != nil {
			t.Errorf("item %v after unwatching: watched %v, watchedAt %v, progress %v", r["id"], r["watched"], r["watchedAt"], r["progress"])
		}
	}
	if s := m.item(id); s.Progress != nil || s.Watched {
		t.Errorf("list after unwatching: %+v / %+v", s.WatchState, s.Progress)
	}
	if cont := decode[[]ItemSummary](t, m.get("/api/continue")); len(cont) != 0 {
		t.Errorf("unwatching kept the position: continue lists %+v", cont)
	}
}

// A film is watched in all its versions, and a version nobody has seen yet is a film that was.
func TestMarkAFilmWatchedInAllItsVersions(t *testing.T) {
	m := newMetaEnv(t, "")
	hd := m.mp4
	uhd := m.addVersion("Matrix 2160p (1999).mkv")
	m.putProgress(uhd, `{"positionSec": 900, "durationSec": 7200}`, nil)
	if m.item(hd).Watched || m.item(uhd).Watched || m.card("movie").Watched {
		t.Fatal("control: a film nobody finished is watched")
	}

	rec := m.putWatched(fmt.Sprintf("/api/titles/%d/watched", m.matrixTitle), markWatched)
	wantStatus(t, rec, 200)
	got := decode[WatchResult](t, rec)
	if want := []int64{hd, uhd}; !slices.Equal(resultIDs(got), want) {
		t.Fatalf("covered %v, want both versions %v", resultIDs(got), want)
	}
	for _, it := range got.Items {
		if !it.Watched || it.PlayCount != 1 || it.Progress == nil || it.Progress.PositionSec != 0 {
			t.Errorf("version %d: %+v / %+v", it.ID, it.WatchState, it.Progress)
		}
	}
	for _, id := range []int64{hd, uhd} {
		if s := m.item(id); !s.Watched || s.PlayCount != 1 {
			t.Errorf("version %d in the list: %+v", id, s.WatchState)
		}
	}
	if s := m.item(m.multi); s.Watched || s.Progress != nil { // another film
		t.Errorf("another film: %+v / %+v", s.WatchState, s.Progress)
	}
	if c := m.card("movie"); !c.Watched || c.EpisodeCounts != nil {
		t.Errorf("the film's card: watched %v, counts %+v", c.Watched, c.EpisodeCounts)
	}

	// A version that arrives later has no progress of its own and is watched all the same.
	late := m.addVersion("Matrix director's cut (1999).mkv")
	s := m.item(late)
	if !s.Watched || s.Progress != nil || s.PlayCount != 1 || s.WatchedAt == nil {
		t.Errorf("a version added after the film was seen: %+v / %+v", s.WatchState, s.Progress)
	}
	d := decode[ItemDetail](t, m.get(fmt.Sprintf("/api/items/%d", late)))
	if !d.Watched || d.Meta == nil || !d.Meta.Watched {
		t.Errorf("its detail: %+v, meta %+v", d.WatchState, d.Meta)
	}
	if col := decode[CollectionDetail](t, m.get(fmt.Sprintf("/api/collections/%d", m.collectionID))); len(col.Titles) != 1 || !col.Titles[0].Watched {
		t.Errorf("collection: %+v", col.Titles)
	}
	if p := decode[Person](t, m.get(fmt.Sprintf("/api/people/%d", m.personID))); len(p.Credits) == 0 || !p.Credits[0].Title.Watched {
		t.Errorf("person credits: %+v", p.Credits)
	}

	// Unwatching through one version is unwatching the film: all of them.
	rec = m.putWatched(fmt.Sprintf("/api/items/%d/watched", late), markUnwatched)
	wantStatus(t, rec, 200)
	if got := decode[WatchResult](t, rec); !slices.Equal(resultIDs(got), []int64{hd, uhd, late}) {
		t.Errorf("unwatching a version covered %v, want all three", resultIDs(got))
	}
	for _, id := range []int64{hd, uhd, late} {
		if s := m.item(id); s.Watched || s.Progress != nil {
			t.Errorf("version %d after the film was unwatched: %+v / %+v", id, s.WatchState, s.Progress)
		}
	}
	if m.card("movie").Watched {
		t.Error("the card is still watched")
	}
}

// A season, and a show, are marked, and the counts say how far each is, in the list, the detail and the cards.
func TestMarkASeasonAndAShowWatched(t *testing.T) {
	m := newMetaEnv(t, "")
	e2 := m.mkv // S01E02
	e1, e3, s2e1 := m.addEpisode(1, 1), m.addEpisode(1, 3), m.addEpisode(2, 1)
	show := func() ShowDetail {
		t.Helper()
		return decode[ShowDetail](t, m.get("/api/shows/Cool%20Show"))
	}
	type counts struct{ total, watched, started int }
	want := func(what string, got EpisodeCounts, w counts) {
		t.Helper()
		if got.EpisodeCount != w.total || got.WatchedEpisodes != w.watched || got.InProgress != w.started {
			t.Errorf("%s: %d episodes, %d watched, %d started; want %+v", what, got.EpisodeCount, got.WatchedEpisodes, got.InProgress, w)
		}
	}
	all := func(what string, w, s1, s2 counts) {
		t.Helper()
		list := decode[[]ShowSummary](t, m.get("/api/shows"))
		if len(list) != 1 {
			t.Fatalf("shows = %+v", list)
		}
		want(what+": the list", list[0].EpisodeCounts, w)
		d := show()
		want(what+": the show", d.EpisodeCounts, w)
		if len(d.Seasons) != 2 {
			t.Fatalf("%s: seasons = %+v", what, d.Seasons)
		}
		want(what+": season 1", d.Seasons[0].EpisodeCounts, s1)
		want(what+": season 2", d.Seasons[1].EpisodeCounts, s2)
		c := m.card("series")
		want(what+": the card", *c.EpisodeCounts, w)
		if c.Watched != (w.total > 0 && w.watched == w.total) {
			t.Errorf("%s: the card is watched = %v with %+v", what, c.Watched, w)
		}
	}
	all("nothing watched", counts{4, 0, 0}, counts{3, 0, 0}, counts{1, 0, 0})

	rec := m.putWatched("/api/shows/Cool%20Show/seasons/1/watched", markWatched)
	wantStatus(t, rec, 200)
	if got := decode[WatchResult](t, rec); !got.Watched || !slices.Equal(resultIDs(got), []int64{e1, e2, e3}) {
		t.Errorf("season 1 covered %v, want its three episodes in order %v", resultIDs(got), []int64{e1, e2, e3})
	}
	all("season 1 watched", counts{4, 3, 0}, counts{3, 3, 0}, counts{1, 0, 0})
	if s := m.item(s2e1); s.Watched || s.Progress != nil {
		t.Errorf("an episode of season 2: %+v / %+v", s.WatchState, s.Progress)
	}

	m.putProgress(s2e1, `{"positionSec": 300, "durationSec": 2400}`, nil)
	all("one started", counts{4, 3, 1}, counts{3, 3, 0}, counts{1, 0, 1})

	// The show, found without regard to case; the started episode is watched now and its position is gone.
	rec = m.putWatched("/api/shows/cool%20show/watched", markWatched)
	wantStatus(t, rec, 200)
	if got := decode[WatchResult](t, rec); !slices.Equal(resultIDs(got), []int64{e1, e2, e3, s2e1}) {
		t.Errorf("the show covered %v", resultIDs(got))
	}
	all("show watched", counts{4, 4, 0}, counts{3, 3, 0}, counts{1, 1, 0})
	if s := m.item(s2e1); !s.Watched || s.Progress.PositionSec != 0 {
		t.Errorf("the started episode: %+v / %+v", s.WatchState, s.Progress)
	}

	// The series through its title, back to unwatched.
	rec = m.putWatched(fmt.Sprintf("/api/titles/%d/watched", m.showTitle), markUnwatched)
	wantStatus(t, rec, 200)
	if got := decode[WatchResult](t, rec); got.Watched || len(got.Items) != 4 {
		t.Errorf("the series covered %+v", got)
	}
	all("series unwatched", counts{4, 0, 0}, counts{3, 0, 0}, counts{1, 0, 0})

	// One season back, from a show that is all watched.
	m.putWatched("/api/shows/Cool%20Show/watched", markWatched)
	wantStatus(t, m.putWatched("/api/shows/Cool%20Show/seasons/2/watched", markUnwatched), 200)
	all("season 2 unwatched", counts{4, 3, 0}, counts{3, 3, 0}, counts{1, 0, 0})
}

// The counts are of episodes: a second file of an episode is not another, and a special that is not watched does
// not keep the show from being watched (it has counts of its own, as a season).
func TestShowCountsAreOfEpisodesAndLeaveTheSpecialsOut(t *testing.T) {
	m := newMetaEnv(t, "")
	m.addFile("Cool Show S01E02 (2160p).mkv", store.Item{Kind: store.KindEpisode, Show: "Cool Show", Season: 1, Episode: 2}) // a copy of m.mkv
	m.addFile("Cool Show S00E01 Behind the scenes.mkv", store.Item{Kind: store.KindEpisode, Show: "Cool Show", Season: 0, Episode: 1})
	wantStatus(t, m.putWatched("/api/shows/Cool%20Show/seasons/1/watched", markWatched), 200)

	list := decode[[]ShowSummary](t, m.get("/api/shows"))
	if len(list) != 1 || list[0].EpisodeCounts != (EpisodeCounts{EpisodeCount: 1, WatchedEpisodes: 1}) {
		t.Fatalf("shows = %+v, want one episode, watched: the copy is not another episode, the special is not counted", list)
	}
	d := decode[ShowDetail](t, m.get("/api/shows/Cool%20Show"))
	if len(d.Seasons) != 2 || d.Seasons[0].Season != 0 || d.Seasons[0].EpisodeCounts != (EpisodeCounts{EpisodeCount: 1}) ||
		d.Seasons[1].EpisodeCounts != (EpisodeCounts{EpisodeCount: 1, WatchedEpisodes: 1}) {
		t.Errorf("seasons = %+v, want the specials with one episode not watched, and season 1 with its one watched", d.Seasons)
	}
	if c := m.card("series"); !c.Watched || *c.EpisodeCounts != (EpisodeCounts{EpisodeCount: 1, WatchedEpisodes: 1}) {
		t.Errorf("the series card = %+v / %+v", c, c.EpisodeCounts)
	}
}

func TestWatchedRoutesRefuseWhatTheyCannotDo(t *testing.T) {
	m := newMetaEnv(t, "")
	item := fmt.Sprintf("/api/items/%d/watched", m.mkv)
	bare, err := m.store.UpsertTitle(context.Background(), &store.TitleInput{Title: store.Title{Kind: store.KindMovie, TMDBID: 9999, Title: "Bare", Source: store.SourceTMDB}})
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		path, body string
		want       int
	}{
		"no body":               {item, ``, 400},
		"not JSON":              {item, `nope`, 400},
		"no field":              {item, `{}`, 400},
		"null":                  {item, `{"watched": null}`, 400},
		"a string":              {item, `{"watched": "yes"}`, 400},
		"an unknown item":       {"/api/items/9999/watched", markWatched, 404},
		"a word for an item":    {"/api/items/abc/watched", markWatched, 404},
		"an unknown title":      {"/api/titles/9999/watched", markWatched, 404},
		"title id zero":         {"/api/titles/0/watched", markWatched, 404},
		"a title with no files": {fmt.Sprintf("/api/titles/%d/watched", bare), markWatched, 404},
		"an unknown show":       {"/api/shows/Nope/watched", markWatched, 404},
		"a season of no show":   {"/api/shows/Nope/seasons/1/watched", markWatched, 404},
		"a season it has not":   {"/api/shows/Cool%20Show/seasons/7/watched", markWatched, 404},
		"a word for a season":   {"/api/shows/Cool%20Show/seasons/one/watched", markWatched, 404},
		"a negative season":     {"/api/shows/Cool%20Show/seasons/-1/watched", markWatched, 404},
		"a show with no body":   {"/api/shows/Cool%20Show/watched", ``, 400},
		"a season with no body": {"/api/shows/Cool%20Show/seasons/1/watched", `{}`, 400},
	} {
		t.Run(name, func(t *testing.T) {
			wantJSONError(t, m.putWatched(tc.path, tc.body), tc.want)
		})
	}
	// Nothing was stored by any of them.
	if n := countProgress(t, m); n != 0 {
		t.Errorf("rejected requests stored %d progress rows", n)
	}
	// Positive control: the same calls with a good request are served.
	for _, p := range []string{item, fmt.Sprintf("/api/titles/%d/watched", m.matrixTitle), "/api/shows/Cool%20Show/watched", "/api/shows/Cool%20Show/seasons/1/watched"} {
		wantStatus(t, m.putWatched(p, markWatched), 200)
	}
	if n := countProgress(t, m); n != 2 { // the movie's, and the one episode
		t.Errorf("good requests stored %d progress rows, want 2", n)
	}
	// Only PUT.
	for _, method := range []string{"GET", "POST", "PATCH", "DELETE"} {
		if rec := m.do(method, item, markWatched); rec.Code != 405 && rec.Code != 404 {
			t.Errorf("%s %s = %d, want 405 or 404", method, item, rec.Code)
		}
	}
}

func countProgress(t *testing.T, m *menv) int {
	t.Helper()
	n := 0
	for _, it := range decode[[]ItemSummary](t, m.get("/api/items")) {
		if it.Progress != nil {
			n++
		}
	}
	return n
}

// Like progress, the calls need the token, and they change something, so a page of another site cannot make them.
func TestWatchedRoutesNeedTheToken(t *testing.T) {
	m := newMetaEnv(t, apiToken)
	paths := []string{
		fmt.Sprintf("/api/items/%d/watched", m.mkv),
		fmt.Sprintf("/api/titles/%d/watched", m.matrixTitle),
		"/api/shows/Cool%20Show/watched",
		"/api/shows/Cool%20Show/seasons/1/watched",
	}
	for _, p := range paths {
		wantJSONError(t, m.env.do("PUT", p, nil, markWatched), 401)
		wantJSONError(t, m.env.do("PUT", p, map[string]string{"Authorization": "Bearer nope"}, markWatched), 401)
		wantJSONError(t, m.env.do("PUT", p, map[string]string{"Origin": "http://evil.example"}, markWatched), 403)
		wantJSONError(t, m.env.do("PUT", p, map[string]string{"Origin": "http://evil.example", "Authorization": "Bearer nope"}, markWatched), 403)
	}
	if n := countProgress(t, m); n != 0 {
		t.Fatalf("refused requests stored %d progress rows", n)
	}
	for _, p := range paths {
		wantStatus(t, m.env.do("PUT", p, map[string]string{"Authorization": "Bearer " + apiToken}, markWatched), 200)
		wantStatus(t, m.env.do("PUT", p+"?token="+url.QueryEscape(apiToken), nil, markUnwatched), 200)
	}
}
