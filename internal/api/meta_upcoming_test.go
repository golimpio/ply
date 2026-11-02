package api

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"ply/internal/store"
)

// A Monday afternoon, local time: "today" in these tests is 2026-11-02.
var upcomingNow = time.Date(2026, 11, 2, 15, 4, 5, 0, time.Local)

func newUpcomingEnv(t *testing.T, token string) *menv {
	t.Helper()
	return newMetaEnv(t, token, func(o *Options) { o.Now = func() time.Time { return upcomingNow } })
}

// addAiringTitle stores a series title that says its next episode airs on nextAir
// (empty: it has none). No file of it is in the library.
func (m *menv) addAiringTitle(name string, tmdb int64, nextAir string, ep *NextEpisode) int64 {
	m.t.Helper()
	in := &store.TitleInput{Title: store.Title{Kind: store.KindSeries, TMDBID: tmdb, Title: name + " Official", SortTitle: name,
		Year: 2020, Status: "Returning Series", Poster: fmt.Sprintf("tmdb:/p%d.jpg", tmdb), Backdrop: fmt.Sprintf("tmdb:/b%d.jpg", tmdb),
		Source: store.SourceTMDB, NextAirDate: nextAir}}
	if ep != nil {
		in.NextEpisodeSeason, in.NextEpisodeNumber, in.NextEpisodeName = ep.Season, ep.Episode, ep.Name
	}
	id, err := m.store.UpsertTitle(context.Background(), in)
	if err != nil {
		m.t.Fatal(err)
	}
	return id
}

// addAiring is addAiringTitle for a show with an episode file in the library,
// matched to the title.
func (m *menv) addAiring(show string, tmdb int64, nextAir string, ep *NextEpisode) int64 {
	m.t.Helper()
	ctx := context.Background()
	id := m.addAiringTitle(show, tmdb, nextAir, ep)
	if _, err := m.store.UpsertItem(ctx, &store.Item{Kind: store.KindEpisode, Title: "Ep", Show: show, Season: 1, Episode: 1,
		Path: "/media/" + show + "/S01E01.mkv", Root: "/media", Size: 10, MTime: 5, Container: "mkv"}, nil); err != nil {
		m.t.Fatal(err)
	}
	if err := m.store.SetSeriesMatch(ctx, show, store.Match{TitleID: id, State: store.MatchMatched, Score: 0.9}); err != nil {
		m.t.Fatal(err)
	}
	return id
}

func TestUpcomingListsAiringShowsSoonestFirst(t *testing.T) {
	m := newUpcomingEnv(t, apiToken)
	zeta := m.addAiring("Zeta", 101, "2026-11-09", &NextEpisode{Season: 2, Episode: 5, Name: "The Big One"})
	beta := m.addAiring("Beta", 102, "2026-11-03", &NextEpisode{Season: 1, Episode: 1, Name: "Beta Opens"})
	alpha := m.addAiring("Alpha", 103, "2026-11-03", nil)
	tonight := m.addAiring("Tonight", 104, "2026-11-02", &NextEpisode{Season: 3, Episode: 4, Name: ""})

	// Not upcoming, though each is in the database: a show that aired yesterday; shows in
	// the library with nothing to air (Cool Show, from the fixture, and Idle); a series no
	// file of is in the library; and a movie.
	m.addAiring("Yesterday", 105, "2026-11-01", &NextEpisode{Season: 1, Episode: 9, Name: "Gone"})
	m.addAiring("Idle", 106, "", nil)
	unfiled := m.addAiringTitle("Unfiled", 107, "2026-11-04", &NextEpisode{Season: 1, Episode: 1, Name: "Nobody Has It"})
	if row, err := m.store.GetTitle(context.Background(), unfiled); err != nil || row.NextAirDate != "2026-11-04" {
		t.Fatalf("unfiled title = %+v, %v", row, err)
	}
	if _, err := m.store.UpsertTitle(context.Background(), &store.TitleInput{Title: store.Title{Kind: store.KindMovie, TMDBID: 108,
		Title: "Airing Movie", NextAirDate: "2026-11-05", Source: store.SourceTMDB}}); err != nil {
		t.Fatal(err)
	}
	shows := decode[[]ShowSummary](t, m.get("/api/shows"))
	var inLibrary []string
	for _, sh := range shows {
		inLibrary = append(inLibrary, sh.Show)
	}
	for _, name := range []string{"Yesterday", "Idle", "Cool Show", "Zeta"} {
		if !slices.Contains(inLibrary, name) {
			t.Fatalf("%q is not in the library: %q", name, inLibrary)
		}
	}

	rec := m.get("/api/upcoming")
	wantStatus(t, rec, 200)
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
	got := decode[[]UpcomingShow](t, rec)
	want := []UpcomingShow{
		{TitleID: tonight, Show: "Tonight", NextAirDate: "2026-11-02", NextEpisode: &NextEpisode{Season: 3, Episode: 4, Name: ""}},
		{TitleID: alpha, Show: "Alpha", NextAirDate: "2026-11-03"},
		{TitleID: beta, Show: "Beta", NextAirDate: "2026-11-03", NextEpisode: &NextEpisode{Season: 1, Episode: 1, Name: "Beta Opens"}},
		{TitleID: zeta, Show: "Zeta", NextAirDate: "2026-11-09", NextEpisode: &NextEpisode{Season: 2, Episode: 5, Name: "The Big One"}},
	}
	for i := range want {
		tmdb := map[string]int{"Tonight": 104, "Alpha": 103, "Beta": 102, "Zeta": 101}[want[i].Show]
		want[i].Poster = withTok(fmt.Sprintf("/api/images/tmdb/w342/p%d.jpg", tmdb))
		want[i].Backdrop = withTok(fmt.Sprintf("/api/images/tmdb/w1280/b%d.jpg", tmdb))
	}
	if len(got) != len(want) {
		t.Fatalf("upcoming = %+v\nwant %d entries (soonest first, today included, name within a day)", got, len(want))
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.TitleID != w.TitleID || g.Show != w.Show || g.Poster != w.Poster || g.Backdrop != w.Backdrop || g.NextAirDate != w.NextAirDate ||
			(g.NextEpisode == nil) != (w.NextEpisode == nil) || (w.NextEpisode != nil && *g.NextEpisode != *w.NextEpisode) {
			t.Errorf("entry %d = %+v (episode %+v)\nwant   %+v (episode %+v)", i, g, g.NextEpisode, w, w.NextEpisode)
		}
	}
}

func TestUpcomingJSONShape(t *testing.T) {
	m := newUpcomingEnv(t, "")
	m.addAiring("With", 101, "2026-11-03", &NextEpisode{Season: 2, Episode: 5, Name: "Name"})
	m.addAiring("Without", 102, "2026-11-04", nil)
	raw := decode[[]map[string]any](t, m.get("/api/upcoming"))
	if len(raw) != 2 {
		t.Fatalf("upcoming = %+v", raw)
	}
	keys := func(v map[string]any) []string {
		var ks []string
		for k := range v {
			ks = append(ks, k)
		}
		slices.Sort(ks)
		return ks
	}
	if got, want := keys(raw[0]), []string{"backdrop", "nextAirDate", "nextEpisode", "poster", "show", "titleId"}; !slices.Equal(got, want) {
		t.Errorf("keys = %v, want %v", got, want)
	}
	ep, _ := raw[0]["nextEpisode"].(map[string]any)
	if got, want := keys(ep), []string{"episode", "name", "season"}; !slices.Equal(got, want) {
		t.Errorf("nextEpisode keys = %v, want %v", got, want)
	}
	if ep["season"] != 2.0 || ep["episode"] != 5.0 || ep["name"] != "Name" {
		t.Errorf("nextEpisode = %v", ep)
	}
	// A show whose episode is not known has the date only.
	if _, there := raw[1]["nextEpisode"]; there {
		t.Errorf("a show with no episode named has nextEpisode: %v", raw[1])
	}
	// Without a token configured the image URLs have none.
	if raw[0]["poster"] != "/api/images/tmdb/w342/p101.jpg" || raw[0]["backdrop"] != "/api/images/tmdb/w1280/b101.jpg" {
		t.Errorf("images = %v / %v", raw[0]["poster"], raw[0]["backdrop"])
	}
}

func TestUpcomingWithNothingToAirIsAnEmptyList(t *testing.T) {
	m := newUpcomingEnv(t, apiToken)
	rec := m.get("/api/upcoming")
	wantStatus(t, rec, 200)
	if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
		t.Errorf("body = %q, want []", body)
	}
}

func TestUpcomingFollowsTheClock(t *testing.T) {
	var now = upcomingNow
	m := newMetaEnv(t, "", func(o *Options) { o.Now = func() time.Time { return now } })
	m.addAiring("Show", 101, "2026-11-02", nil)
	names := func() []string {
		var out []string
		for _, u := range decode[[]UpcomingShow](t, m.get("/api/upcoming")) {
			out = append(out, u.Show)
		}
		return out
	}
	if got := names(); !slices.Equal(got, []string{"Show"}) {
		t.Fatalf("on the day: %q", got)
	}
	// Just before midnight it is still today's; after, it is yesterday's.
	now = time.Date(2026, 11, 2, 23, 59, 59, 0, time.Local)
	if got := names(); !slices.Equal(got, []string{"Show"}) {
		t.Errorf("at 23:59:59 on the day: %q", got)
	}
	now = time.Date(2026, 11, 3, 0, 0, 0, 0, time.Local)
	if got := names(); len(got) != 0 {
		t.Errorf("at midnight after the day: %q", got)
	}
}

func TestUpcomingListsAtMostFifty(t *testing.T) {
	m := newUpcomingEnv(t, "")
	for i := 0; i < 55; i++ {
		// 55 distinct days, the last ones the latest.
		day := upcomingNow.AddDate(0, 0, i).Format("2006-01-02")
		m.addAiring(fmt.Sprintf("Show %02d", i), int64(1000+i), day, nil)
	}
	got := decode[[]UpcomingShow](t, m.get("/api/upcoming"))
	if len(got) != 50 {
		t.Fatalf("upcoming has %d entries, want 50", len(got))
	}
	if got[0].Show != "Show 00" || got[49].Show != "Show 49" {
		t.Errorf("first %q, last %q; want the 50 soonest", got[0].Show, got[49].Show)
	}
}

func TestTitleCardsCarryTheNextAirDate(t *testing.T) {
	m := newUpcomingEnv(t, apiToken)
	id := m.addAiring("Airing", 101, "2026-11-09", &NextEpisode{Season: 1, Episode: 2, Name: "Two"})
	var found bool
	for _, c := range decode[[]TitleCard](t, m.get("/api/titles")) {
		switch c.ID {
		case id:
			found = true
			if c.NextAirDate != "2026-11-09" {
				t.Errorf("card = %+v", c)
			}
		case m.matrixTitle:
			if c.NextAirDate != "" {
				t.Errorf("a movie's card has nextAirDate %q", c.NextAirDate)
			}
		}
	}
	if !found {
		t.Fatal("the airing show is not in /api/titles")
	}
	detail := decode[ShowDetail](t, m.get("/api/shows/Airing"))
	if detail.Meta == nil || detail.Meta.NextAirDate != "2026-11-09" {
		t.Errorf("show meta = %+v", detail.Meta)
	}
	raw := decode[map[string]any](t, m.get("/api/shows/Airing"))
	meta, _ := raw["meta"].(map[string]any)
	if meta["nextAirDate"] != "2026-11-09" {
		t.Errorf("meta.nextAirDate = %v", meta["nextAirDate"])
	}
	// Title detail through an episode's item page too.
	for _, it := range decode[[]ItemSummary](t, m.get("/api/items?show=Airing")) {
		d := decode[ItemDetail](t, m.get(fmt.Sprintf("/api/items/%d", it.ID)))
		if d.Meta == nil || d.Meta.NextAirDate != "2026-11-09" {
			t.Errorf("episode detail meta = %+v", d.Meta)
		}
	}
}
