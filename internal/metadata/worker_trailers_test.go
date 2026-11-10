package metadata_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"ply/internal/metadata"
	"ply/internal/metadata/metadatatest"
	"ply/internal/store"
)

var (
	trailerA = store.Video{Site: store.SiteYouTube, Key: "vKQi3bBA1y8", Name: "Official Trailer", Type: store.VideoTrailer, Language: "en", Official: true, PublishedAt: "1999-03-01"}
	trailerB = store.Video{Site: store.SiteVimeo, Key: "76979871", Name: "Teaser", Type: store.VideoTeaser}
	trailerC = store.Video{Site: store.SiteYouTube, Key: "HhesaQXLuRY", Name: "Re-release", Type: store.VideoTrailer}
)

// trailersTTL is the worker's: how long trailers are kept before they are asked for again.
const trailersTTL = 30 * 24 * time.Hour

func (e *wenv) matrixTitle() int64 {
	e.t.Helper()
	id, err := e.st.UpsertTitle(e.ctx, matrix())
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func TestWorkerTrailersAreFetchedOnceThenKept(t *testing.T) {
	e := newWEnv(t)
	id := e.matrixTitle()
	e.fake.TrailerLists = map[int64][]store.Video{603: {trailerA, trailerB}}

	got, err := e.w.Trailers(e.ctx, id)
	if err != nil || !slices.Equal(got, []store.Video{trailerA, trailerB}) {
		t.Fatalf("first: %+v, %v", got, err)
	}
	if n := e.fake.Count("Trailers(movie,603)"); n != 1 {
		t.Fatalf("TMDB asked %d times, want 1", n)
	}
	stored, err := e.st.GetTitleVideos(e.ctx, id)
	if err != nil || stored.Ref != "tmdb:603@en-US" || stored.CheckedAt != e.clock.Now().Unix() || !slices.Equal(stored.Videos, got) {
		t.Errorf("stored: %+v, %v", stored, err)
	}

	// Kept: the next visits ask nothing, until they are trailersTTL old.
	e.fake.TrailerLists[603] = []store.Video{trailerC}
	e.clock.Advance(trailersTTL - time.Second)
	if got, err := e.w.Trailers(e.ctx, id); err != nil || !slices.Equal(got, []store.Video{trailerA, trailerB}) {
		t.Errorf("a second before they are old: %+v, %v", got, err)
	}
	if n := e.fake.Count("Trailers("); n != 1 {
		t.Errorf("TMDB asked %d times in all, want 1", n)
	}
	e.clock.Advance(time.Second)
	if got, err := e.w.Trailers(e.ctx, id); err != nil || !slices.Equal(got, []store.Video{trailerC}) {
		t.Errorf("once they are old: %+v, %v", got, err)
	}
	if n := e.fake.Count("Trailers("); n != 2 {
		t.Errorf("TMDB asked %d times in all, want 2", n)
	}

	// A title with none is checked as well: it is not asked about at every visit.
	e.fake.TrailerLists[603] = []store.Video{}
	e.clock.Advance(trailersTTL)
	for range 2 {
		if got, err := e.w.Trailers(e.ctx, id); err != nil || len(got) != 0 {
			t.Errorf("none: %+v, %v", got, err)
		}
	}
	if n := e.fake.Count("Trailers("); n != 3 {
		t.Errorf("TMDB asked %d times in all, want 3", n)
	}

	if _, err := e.w.Trailers(e.ctx, id+100); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("no such title: %v", err)
	}
}

func TestWorkerTrailersAreNotShownWithLookupsOff(t *testing.T) {
	e := newWEnv(t)
	id := e.matrixTitle()
	e.fake.TrailerLists = map[int64][]store.Video{603: {trailerA}}
	if got, err := e.w.Trailers(e.ctx, id); err != nil || len(got) != 1 {
		t.Fatalf("control, lookups on: %+v, %v", got, err)
	}

	e.src.Set(e.fake, false)
	got, err := e.w.Trailers(e.ctx, id)
	if !errors.Is(err, metadata.ErrLookupsOff) || got != nil {
		t.Errorf("lookups off: %+v, %v; want none and ErrLookupsOff", got, err)
	}
	if stored, _ := e.st.GetTitleVideos(e.ctx, id); len(stored.Videos) != 1 {
		t.Errorf("control: the stored trailers are %+v", stored.Videos)
	}
	if n := e.fake.Count("Trailers("); n != 1 {
		t.Errorf("TMDB asked %d times, want 1", n)
	}
}

func TestWorkerTrailersFailureIsRememberedAndTheOldOnesKept(t *testing.T) {
	e := newWEnv(t)
	id := e.matrixTitle()
	e.fake.TrailerLists = map[int64][]store.Video{603: {trailerA}}
	if _, err := e.w.Trailers(e.ctx, id); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(trailersTTL)
	e.fake.Offline()

	for i := range 2 {
		got, err := e.w.Trailers(e.ctx, id)
		if !metadata.IsTransient(err) || !slices.Equal(got, []store.Video{trailerA}) {
			t.Errorf("offline, visit %d: %+v, %v; want the old ones and the error", i+1, got, err)
		}
	}
	if n := e.fake.Count("Trailers("); n != 2 {
		t.Errorf("TMDB asked %d times, want 2: a failure is not asked again at once", n)
	}
	e.clock.Advance(5 * time.Minute)
	e.fake.Online()
	e.fake.TrailerLists[603] = []store.Video{trailerC}
	if got, err := e.w.Trailers(e.ctx, id); err != nil || !slices.Equal(got, []store.Video{trailerC}) {
		t.Errorf("back online 5 minutes later: %+v, %v", got, err)
	}

	// A title TMDB does not have (a 404) is left alone for a day, unless the settings change.
	delete(e.fake.TrailerLists, 603)
	e.clock.Advance(trailersTTL)
	if _, err := e.w.Trailers(e.ctx, id); !metadata.IsNotFound(err) {
		t.Fatalf("a 404: %v", err)
	}
	e.clock.Advance(23 * time.Hour)
	before := e.fake.Count("Trailers(")
	if _, err := e.w.Trailers(e.ctx, id); !metadata.IsNotFound(err) || e.fake.Count("Trailers(") != before {
		t.Errorf("23 hours after a 404: %v, asked %d more times", err, e.fake.Count("Trailers(")-before)
	}
	e.w.Reload()
	if _, err := e.w.Trailers(e.ctx, id); e.fake.Count("Trailers(") != before+1 {
		t.Errorf("after a change of settings: %v, asked %d more times, want 1", err, e.fake.Count("Trailers(")-before)
	}
}

func TestWorkerTrailersAreFetchedAgainForAnotherIDOrLanguage(t *testing.T) {
	for _, other := range []string{"tmdb:999@en-US", "tmdb:603@de-DE", "tmdb:603"} {
		e := newWEnv(t)
		id := e.matrixTitle()
		// Stored for another id at TMDB, or in another language, a moment ago: they are not the ones to show.
		if err := e.st.SetTitleVideos(e.ctx, id, other, []store.Video{trailerB}, e.clock.Now().Unix()); err != nil {
			t.Fatal(err)
		}
		e.fake.Offline()
		if got, err := e.w.Trailers(e.ctx, id); err == nil || got != nil {
			t.Errorf("%s, offline: %+v, %v; want none of them", other, got, err)
		}
		e.w.Reload()
		e.fake.Online()
		e.fake.TrailerLists = map[int64][]store.Video{603: {trailerA}}
		if got, err := e.w.Trailers(e.ctx, id); err != nil || !slices.Equal(got, []store.Video{trailerA}) {
			t.Errorf("%s, online: %+v, %v", other, got, err)
		}
		if n := e.fake.Count("Trailers(movie,603)"); n != 2 {
			t.Errorf("%s: TMDB asked %d times, want 2", other, n)
		}
	}
}

// A failure is the ref's: once the title is asked of another provider (TMDB comes on for a show
// TVDB failed for), it does not hold that one back.
func TestWorkerTrailersFailureIsTheRefs(t *testing.T) {
	e := newWEnv(t)
	tvdb := &metadatatest.Fake{Source: metadata.SourceTVDB} // has no list: a 404, remembered for a day
	e.src.SetTVDB(tvdb)
	e.src.Set(nil, true)
	id, err := e.st.UpsertTitle(e.ctx, &store.TitleInput{Title: store.Title{Kind: store.KindSeries, TVDBID: 81189, TMDBID: 1396,
		Title: "Breaking Bad", Source: store.SourceTVDB}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.w.Trailers(e.ctx, id); !metadata.IsNotFound(err) {
		t.Fatalf("TVDB's 404: %v", err)
	}
	if _, err := e.w.Trailers(e.ctx, id); !metadata.IsNotFound(err) || tvdb.Count("Trailers(") != 1 {
		t.Fatalf("control: the 404 is remembered: %v, TVDB asked %d times", err, tvdb.Count("Trailers("))
	}

	e.fake.TrailerLists = map[int64][]store.Video{1396: {trailerA}}
	e.src.Set(e.fake, true) // no Reload: the failure is still there
	if got, err := e.w.Trailers(e.ctx, id); err != nil || !slices.Equal(got, []store.Video{trailerA}) {
		t.Errorf("TMDB on: %+v, %v; want TMDB asked at once", got, err)
	}
}

// A caller that leaves after TMDB answered still gets the answer stored; one that leaves during the
// fetch says nothing about the title (asked again next time); TMDB not answering in time does (it is
// remembered).
func TestWorkerTrailersAndTheCallerGoingAway(t *testing.T) {
	e := newWEnv(t)
	id := e.matrixTitle()
	e.fake.TrailerLists = map[int64][]store.Video{603: {trailerA}}

	ctx, cancel := context.WithCancel(e.ctx)
	e.fake.Hook = func(context.Context, string) error { cancel(); return nil } // gone as TMDB answers
	if got, _ := e.w.Trailers(ctx, id); !slices.Equal(got, []store.Video{trailerA}) {
		t.Errorf("answered as the caller left: %+v", got)
	}
	if s, err := e.st.GetTitleVideos(e.ctx, id); err != nil || !slices.Equal(s.Videos, []store.Video{trailerA}) {
		t.Errorf("stored after the caller left: %+v, %v", s, err)
	}

	e.clock.Advance(trailersTTL)
	ctx, cancel = context.WithCancel(e.ctx)
	e.fake.Hook = func(context.Context, string) error { cancel(); return context.Canceled } // gone during the fetch
	if _, err := e.w.Trailers(ctx, id); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	e.fake.Hook = func(context.Context, string) error { return context.DeadlineExceeded } // TMDB too slow
	if _, err := e.w.Trailers(e.ctx, id); !errors.Is(err, context.DeadlineExceeded) || e.fake.Count("Trailers(") != 3 {
		t.Fatalf("after a cancelled fetch: %v, TMDB asked %d times; want it asked again", err, e.fake.Count("Trailers("))
	}
	if _, err := e.w.Trailers(e.ctx, id); !errors.Is(err, context.DeadlineExceeded) || e.fake.Count("Trailers(") != 3 {
		t.Errorf("after a timeout: %v, TMDB asked %d times; want the timeout remembered", err, e.fake.Count("Trailers("))
	}
}

func TestWorkerTrailersOfAShowComeFromTMDBElseTVDB(t *testing.T) {
	e := newWEnv(t)
	tvdb := &metadatatest.Fake{Source: metadata.SourceTVDB, TrailerLists: map[int64][]store.Video{81189: {trailerC}, 81190: {trailerB}}}
	e.src.SetTVDB(tvdb)
	e.fake.TrailerLists = map[int64][]store.Video{1396: {trailerA}}
	upsert := func(in store.Title) int64 {
		t.Helper()
		id, err := e.st.UpsertTitle(e.ctx, &store.TitleInput{Title: in})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	tvdbOnly := upsert(store.Title{Kind: store.KindSeries, TVDBID: 81189, Title: "Breaking Bad", Source: store.SourceTVDB})
	both := upsert(store.Title{Kind: store.KindSeries, TVDBID: 81190, TMDBID: 1396, Title: "Both", Source: store.SourceTVDB})
	nfoOnly := upsert(store.Title{Kind: store.KindMovie, Title: "Home Movie", Source: store.SourceNFO})

	if got, err := e.w.Trailers(e.ctx, tvdbOnly); err != nil || !slices.Equal(got, []store.Video{trailerC}) {
		t.Errorf("a show only TVDB knows: %+v, %v", got, err)
	}
	if s, _ := e.st.GetTitleVideos(e.ctx, tvdbOnly); s.Ref != "tvdb:81189@en-US" {
		t.Errorf("stored for %q", s.Ref)
	}
	if got, err := e.w.Trailers(e.ctx, both); err != nil || !slices.Equal(got, []store.Video{trailerA}) {
		t.Errorf("a show both know: %+v, %v; want TMDB's", got, err)
	}
	if !slices.Equal(e.fake.Calls(), []string{"Trailers(series,1396)"}) || !slices.Equal(tvdb.Calls(), []string{"Trailers(series,81189)"}) {
		t.Errorf("asked TMDB %v, TVDB %v", e.fake.Calls(), tvdb.Calls())
	}

	// With no TMDB key the show both know comes from TVDB, under its TVDB ref.
	e.src.Set(nil, true)
	if got, err := e.w.Trailers(e.ctx, both); err != nil || !slices.Equal(got, []store.Video{trailerB}) {
		t.Errorf("no TMDB key: %+v, %v; want TVDB's", got, err)
	}

	// A title no provider knows has none, and nobody is asked.
	before := len(e.fake.Calls()) + len(tvdb.Calls())
	if got, err := e.w.Trailers(e.ctx, nfoOnly); err != nil || got != nil || len(e.fake.Calls())+len(tvdb.Calls()) != before {
		t.Errorf("a title of an NFO only: %+v, %v", got, err)
	}
}
