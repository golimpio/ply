package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"ply/internal/art"
	"ply/internal/probe"
	"ply/internal/store"
)

// cardEnv is the metadata env with artwork, in which the film "Movie (2020)", matched to The Matrix,
// is HDR, has been started and has a picture with a colour: the one row with every field there is.
func cardEnv(t *testing.T) *menv {
	t.Helper()
	m := newMetaEnvWith(t, "", func(m *menv, o *Options) {
		g, err := art.New(context.Background(), art.Options{Dir: filepath.Join(t.TempDir(), "art"), FFmpeg: fakeFFmpeg(t, fakeArtScript)})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(g.Close)
		o.Art = g
	})
	ctx := context.Background()
	entry, err := m.store.GetEntry(ctx, m.mp4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.store.UpsertItem(ctx, &entry.Item, []probe.Stream{
		{Index: 0, Type: "video", Codec: "hevc", Width: 3840, Height: 2160, PixFmt: "yuv420p10le", ColorTransfer: "smpte2084", Default: true},
		{Index: 1, Type: "audio", Codec: "eac3", Channels: 6, Language: "eng", Default: true}}); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, m.putProgress(m.mp4, `{"positionSec": 100, "durationSec": 120}`, nil), 200)
	wantStatus(t, m.get(fmt.Sprintf("/api/items/%d/art/thumb", m.mp4)), 200) // makes the picture and learns its colour
	return m
}

func fieldNames(v any) []string {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func wantKeys(t *testing.T, what string, v any, want ...string) {
	t.Helper()
	slices.Sort(want)
	if got := fieldNames(v); !slices.Equal(got, want) {
		t.Errorf("%s has the fields %v, want exactly %v", what, got, want)
	}
}

func TestItemCardHasExactlyTheFields(t *testing.T) {
	m := cardEnv(t)
	cardURL, fullURL := "/api/items?kind=movie&fields=card", "/api/items?kind=movie"

	raw := decode[[]map[string]any](t, m.get(cardURL))
	rows := map[int64]map[string]any{}
	for _, r := range raw {
		rows[int64(r["id"].(float64))] = r
	}

	// The film with everything.
	full := rows[m.mp4]
	if full == nil {
		t.Fatalf("no card for the film; have %v", raw)
	}
	wantKeys(t, "the card of a film that has everything", full, "id", "kind", "title", "sortTitle", "year", "titleId", "addedAt", "poster", "backdrop", "art",
		"rating", "watched", "progress", "durationSec", "container", "width", "height", "hdr", "genres", "matchState", "needsReview")
	wantKeys(t, "its art", full["art"], "thumb", "color")
	wantKeys(t, "its rating", full["rating"], "source", "value", "scale", "score100")
	wantKeys(t, "its progress", full["progress"], "positionSec", "watched")

	// The film with nothing: no title, never started, no picture with a colour. Only what is always there
	// (its art is the link to a frame grab that is not made yet: no colour).
	bare := rows[m.multi]
	if bare == nil {
		t.Fatalf("no card for the other film; have %v", raw)
	}
	wantKeys(t, "the card of a film with nothing", bare, "id", "kind", "title", "sortTitle", "year", "addedAt", "art", "rating", "watched", "progress",
		"durationSec", "container", "width", "height", "matchState", "needsReview")
	wantKeys(t, "its art", bare["art"], "thumb")
	if bare["rating"] != nil || bare["progress"] != nil {
		t.Errorf("rating %v and progress %v of a film with neither: want null", bare["rating"], bare["progress"])
	}

	// The values are the full row's.
	fullRows := map[int64]ItemSummary{}
	for _, it := range decode[[]ItemSummary](t, m.get(fullURL)) {
		fullRows[it.ID] = it
	}
	cards := map[int64]ItemCard{}
	for _, c := range decode[[]ItemCard](t, m.get(cardURL)) {
		cards[c.ID] = c
	}
	if len(cards) != len(fullRows) {
		t.Fatalf("%d cards for %d films", len(cards), len(fullRows))
	}
	for id, it := range fullRows {
		c := cards[id]
		if c.Kind != it.Kind || c.Title != it.Title || c.SortTitle != it.SortTitle || c.AddedAt != it.AddedAt || c.Poster != it.Poster ||
			c.Watched != it.WatchState.Watched || c.DurationSec != it.DurationSec || c.Width != it.Width || c.Height != it.Height ||
			c.Backdrop != it.Backdrop || c.Container != it.Container ||
			c.HDR != it.HDR || c.MatchState != it.MatchState || c.NeedsReview != it.NeedsReview ||
			!slices.Equal(c.Genres, it.Genres) || (c.Year == nil) != (it.Year == nil) || (c.TitleID == nil) != (it.TitleID == nil) {
			t.Errorf("card %+v\nis not the row %+v", c, it)
		}
		if (c.Year != nil && *c.Year != *it.Year) || (c.TitleID != nil && *c.TitleID != *it.TitleID) {
			t.Errorf("card %d: year/titleId differ from the row's", id)
		}
		if (c.Progress == nil) != (it.Progress == nil) || (c.Progress != nil && *c.Progress != *it.Progress) {
			t.Errorf("card %d: progress %+v, row %+v", id, c.Progress, it.Progress)
		}
		if (c.Rating == nil) != (it.Rating == nil) || (c.Rating != nil &&
			*c.Rating != CardRating{Source: it.Rating.Source, Value: it.Rating.Value, Scale: it.Rating.Scale, Score100: it.Rating.Score100, State: it.Rating.State}) {
			t.Errorf("card %d: rating %+v, row %+v", id, c.Rating, it.Rating)
		}
		if (c.Art == nil) != (it.Art == nil) || (c.Art != nil && *c.Art != CardArt{Thumb: it.Art.Thumb, Color: it.Art.Color}) {
			t.Errorf("card %d: art %+v, row's art %+v", id, c.Art, it.Art)
		}
	}
	// And the film that has everything has what it should (a control for the loop above comparing nothing).
	c := cards[m.mp4]
	if c.SortTitle != "Matrix, The" || c.HDR != "hdr10" || c.Width != 3840 || c.Art == nil || c.Art.Color == "" || c.Art.Thumb == "" || c.Backdrop == "" || c.Container != "mp4" ||
		c.Rating == nil || c.Rating.Source != "tmdb" ||
		c.Progress == nil || c.Progress.PositionSec != 100 || !slices.Equal(c.Genres, []string{"Action", "Science Fiction"}) || c.TitleID == nil || *c.TitleID != m.matrixTitle {
		t.Errorf("the card of the film with everything: %+v", c)
	}
}

// wantObjectOf checks an object of a card (its art, its rating) against the row's: both null, or each of
// the fields in keep there in both or in neither, with the same value, and no other field on the card.
func wantObjectOf(t *testing.T, what string, card, row any, keep ...string) {
	t.Helper()
	if card == nil || row == nil {
		if card != nil || row != nil {
			t.Errorf("%s: the card has %v, the row %v: one is null and the other is not", what, card, row)
		}
		return
	}
	c, r := card.(map[string]any), row.(map[string]any)
	for _, k := range keep {
		cv, inCard := c[k]
		rv, inRow := r[k]
		if inCard != inRow || !reflect.DeepEqual(cv, rv) {
			t.Errorf("%s.%s: the card has %v (there: %v), the row %v (there: %v)", what, k, cv, inCard, rv, inRow)
		}
	}
	for k := range c {
		if !slices.Contains(keep, k) {
			t.Errorf("%s: the card has the field %q, which it should not", what, k)
		}
	}
}

// The frame grab (art.thumb, with the ?v=<mtime> of its link), the TMDB backdrop, the container and
// the state of the rating are the full row's, field for field and omitted when the row omits them.
// Four films: the one with all of them; one with a frame grab to come, a rating that is verified hot
// and no backdrop; one that has no more than a frame grab (m.multi); and one with none (no video
// stream, so no art; no container; no title).
func TestItemCardFieldsAreTheFullRows(t *testing.T) {
	m := cardEnv(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	now := fetchedAt.Unix()

	// The Matrix is certified on Rotten Tomatoes' critics, which is its card's rating now (the first there is).
	must(m.store.ReplaceMDBListRatings(ctx, m.matrixTitle, []store.Rating{
		{Source: store.RatingRTCritics, Value: 94, Scale: 100, State: store.StateCertified, URL: "https://www.rottentomatoes.com/m/matrix"}}, now))

	add := func(name, container string, streams []probe.Stream) int64 {
		id, err := m.store.UpsertItem(ctx, &store.Item{Kind: store.KindMovie, Title: name, Year: 2001, Path: "/media/" + name + ".mkv", Root: "/media",
			Size: 1, MTime: 1_700_000_000_123_456_789, Container: container}, streams)
		must(err)
		return id
	}
	hot := add("Hot", "matroska,webm", streamsH264())
	hotTitle, err := m.store.UpsertTitle(ctx, &store.TitleInput{Title: store.Title{Kind: store.KindMovie, Title: "Hot", Year: 2001, Source: store.SourceNFO}})
	must(err)
	must(m.store.SetItemMatch(ctx, hot, store.Match{TitleID: hotTitle, State: store.MatchMatched, Score: 1}))
	must(m.store.ReplaceMDBListRatings(ctx, hotTitle, []store.Rating{
		{Source: store.RatingRTAudience, Value: 97, Scale: 100, State: store.StateVerifiedHot, URL: "https://www.rottentomatoes.com/m/hot"}}, now))
	bare := add("Bare", "", nil)

	rows := func(url string) map[int64]map[string]any {
		t.Helper()
		out := map[int64]map[string]any{}
		for _, r := range decode[[]map[string]any](t, m.get(url)) {
			out[int64(r["id"].(float64))] = r
		}
		return out
	}
	cards, fulls := rows("/api/items?kind=movie&fields=card"), rows("/api/items?kind=movie")
	for _, id := range []int64{m.mp4, m.multi, hot, bare} { // a control for the loop below comparing nothing
		if cards[id] == nil || fulls[id] == nil {
			t.Fatalf("film %d: card %v, row %v", id, cards[id], fulls[id])
		}
	}

	for id, row := range fulls {
		card := cards[id]
		for _, k := range []string{"container", "backdrop", "art", "rating"} { // art and rating are null when there is none, not absent
			cv, inCard := card[k]
			rv, inRow := row[k]
			if inCard != inRow || (k == "container" || k == "backdrop") && cv != rv {
				t.Errorf("film %d, %s: the card has %v (there: %v), the row %v (there: %v)", id, k, cv, inCard, rv, inRow)
			}
		}
		wantObjectOf(t, fmt.Sprintf("film %d art", id), card["art"], row["art"], "thumb", "color")
		wantObjectOf(t, fmt.Sprintf("film %d rating", id), card["rating"], row["rating"], "source", "value", "scale", "score100", "state")
	}

	// And the controls: what each of the four has.
	at := func(v any, keys ...string) any {
		for _, k := range keys {
			obj, _ := v.(map[string]any)
			v = obj[k]
		}
		return v
	}
	thumb := func(id int64) string {
		e, err := m.store.GetEntry(ctx, id)
		must(err)
		return fmt.Sprintf("/api/items/%d/art/thumb?v=%d", id, e.MTime)
	}
	for _, tc := range []struct {
		name                       string
		id                         int64
		colour, backdrop, nullArt  bool
		container, state, ratingBy string
	}{
		{"the film with everything", m.mp4, true, true, false, "mp4", "certified", "rt_critics"},
		{"the verified hot one", hot, false, false, false, "matroska,webm", "verified_hot", "rt_audience"},
		{"the one with a frame grab and no more", m.multi, false, false, false, "mp4", "", ""},
		{"the one with none", bare, false, false, true, "", "", ""},
	} {
		c := cards[tc.id]
		if _, there := c["container"]; !there || c["container"] != tc.container {
			t.Errorf("%s: container %v (there: %v), want %q, always there", tc.name, c["container"], there, tc.container)
		}
		if _, there := c["backdrop"]; there != tc.backdrop || (tc.backdrop && c["backdrop"] == "") {
			t.Errorf("%s: backdrop %v (there: %v), want there: %v", tc.name, c["backdrop"], there, tc.backdrop)
		}
		if tc.nullArt {
			if v, there := c["art"]; !there || v != nil {
				t.Errorf("%s: art %v (there: %v), want null", tc.name, v, there)
			}
		} else {
			if got := at(c, "art", "thumb"); got != thumb(tc.id) {
				t.Errorf("%s: art.thumb %v, want %q", tc.name, got, thumb(tc.id))
			}
			if _, there := at(c, "art").(map[string]any)["color"]; there != tc.colour {
				t.Errorf("%s: art.color there: %v, want %v", tc.name, there, tc.colour)
			}
		}
		if tc.ratingBy == "" {
			if v, there := c["rating"]; !there || v != nil {
				t.Errorf("%s: rating %v (there: %v), want null", tc.name, v, there)
			}
			continue
		}
		if at(c, "rating", "source") != tc.ratingBy || at(c, "rating", "state") != tc.state {
			t.Errorf("%s: rating %v, want %s in the state %q", tc.name, c["rating"], tc.ratingBy, tc.state)
		}
	}
}

// The same films in the same order, whatever the filters; and it is a lean list.
func TestItemCardListIsTheFullListInAnotherShape(t *testing.T) {
	m := cardEnv(t)
	for _, q := range []string{"", "&sort=rating", "&minRating=50", "&sort=rating&minRating=90"} {
		var full, card []int64
		for _, it := range decode[[]ItemSummary](t, m.get("/api/items?kind=movie"+q)) {
			full = append(full, it.ID)
		}
		for _, c := range decode[[]ItemCard](t, m.get("/api/items?kind=movie&fields=card"+q)) {
			card = append(card, c.ID)
		}
		if !slices.Equal(full, card) {
			t.Errorf("%q: the cards are %v, the rows %v", q, card, full)
		}
		if q == "&minRating=50" && len(card) == 0 {
			t.Error("control: the filter left nothing")
		}
	}
	if q := "&minRating=99"; len(decode[[]ItemCard](t, m.get("/api/items?kind=movie&fields=card"+q))) != 0 {
		t.Error("control: a rating of 99 leaves films")
	}
	if full, card := m.get("/api/items?kind=movie").Body.Len(), m.get("/api/items?kind=movie&fields=card").Body.Len(); card*100 > full*75 { // two films with little in them; 5,000 real ones are two thirds (see TestListPayload)
		t.Errorf("a card list is %d bytes, the full one %d: not lean", card, full)
	}
}

// fields=card needs to know which kind of row to cut the card from: the two shapes are not the same
// (TestEpisodeCardHasExactlyTheFields). Both kinds are answered; neither kind, or another fields
// value, is a 400.
func TestCardProjectionNeedsAKind(t *testing.T) {
	m := cardEnv(t)
	for _, url := range []string{"/api/items?fields=card", "/api/items?kind=movie&fields=lean", "/api/items?kind=movie&fields=Card",
		"/api/items?fields=bogus"} {
		rec := m.get(url)
		wantJSONError(t, rec, http.StatusBadRequest)
		if rec.Header().Get("ETag") != "" {
			t.Errorf("GET %s: a 400 with ETag %q", url, rec.Header().Get("ETag"))
		}
	}
	// A control for the loop: both kinds are answered, so that a 400 cannot pass for every URL.
	for _, url := range []string{"/api/items?kind=movie&fields=card", "/api/items?kind=episode&fields=card"} {
		wantStatus(t, m.get(url), 200)
	}
	if n := len(decode[[]EpisodeCard](t, m.get("/api/items?kind=episode&fields=card"))); n == 0 {
		t.Error("control: no episode cards")
	}
	wantStatus(t, m.get("/api/items?kind=movie&fields="), 200) // empty is the full list
	if n := len(decode[[]ItemSummary](t, m.get("/api/items?kind=movie&fields="))); n == 0 {
		t.Error("control: no films")
	}
}

// The projection is in the key: a tag of the full list is not one of the card list, nor the other way round.
func TestItemCardETagIsItsOwn(t *testing.T) {
	m := cardEnv(t)
	cardURL, fullURL := "/api/items?kind=movie&fields=card", "/api/items?kind=movie"
	card, full := m.get(cardURL).Header().Get("ETag"), m.get(fullURL).Header().Get("ETag")
	if card == "" || full == "" || card == full {
		t.Fatalf("ETags: card %q, full %q: want two, and different", card, full)
	}
	if rec := m.condGet(cardURL, card); rec.Code != http.StatusNotModified {
		t.Errorf("the card list with its own tag = %d, want 304", rec.Code)
	}
	if rec := m.condGet(fullURL, full); rec.Code != http.StatusNotModified {
		t.Errorf("the full list with its own tag = %d, want 304", rec.Code)
	}
	if rec := m.condGet(cardURL, full); rec.Code != 200 {
		t.Errorf("the card list with the tag of the full one = %d, want 200", rec.Code)
	}
	if rec := m.condGet(fullURL, card); rec.Code != 200 {
		t.Errorf("the full list with the tag of the card one = %d, want 200", rec.Code)
	}
	// And it moves with the library like the other.
	wantStatus(t, m.putProgress(m.mkv, `{"positionSec": 100, "durationSec": 2400}`, nil), 200)
	if rec := m.condGet(cardURL, card); rec.Code != 200 || rec.Header().Get("ETag") == card {
		t.Errorf("the card list after a progress save, with the old tag: %d, ETag %q", rec.Code, rec.Header().Get("ETag"))
	}
}

// An episode card is the full episode row cut down to what a page draws. What it leaves out is the
// weight: the backdrop, the poster, the logo, the codecs, the container, the genres, the certification,
// the match state, the sort title and the show's rating are in GET /api/items/{id}.
func TestEpisodeCardHasExactlyTheFields(t *testing.T) {
	m := cardEnv(t)
	cardURL, fullURL := "/api/items?kind=episode&fields=card", "/api/items?kind=episode"

	raw := decode[[]map[string]any](t, m.get(cardURL))
	if len(raw) == 0 {
		t.Fatalf("no episode in the fixture, so there is no card to check")
	}
	wantKeys(t, "the card of an episode", raw[0], "id", "kind", "title", "addedAt", "durationSec", "art", "watched",
		"watchedAt", "playCount", "progress", "width", "height", "show", "season", "episode", "episodeTitle", "still", "overview", "hdr")

	// The positive control: the same episode, from the full list, does carry the fields the card leaves
	// out, so that the absence assertions below cannot pass on a fixture that has nothing in it.
	cardBody, fullBody := m.get(cardURL).Body.String(), m.get(fullURL).Body.String()
	for _, key := range []string{"container", "videoCodec", "audioCodec", "matchState", "needsReview", "poster", "backdrop", "genres",
		"certification", "rating"} {
		if !strings.Contains(fullBody, `"`+key+`"`) {
			t.Errorf("control: the full episode list has no %q either, so its absence proves nothing", key)
		}
		if strings.Contains(cardBody, `"`+key+`"`) {
			t.Errorf("the episode card carries %q, which no page draws", key)
		}
	}

	// And its values are the full row's.
	cards := map[int64]EpisodeCard{}
	for _, c := range decode[[]EpisodeCard](t, m.get(cardURL)) {
		cards[c.ID] = c
	}
	rows := decode[[]ItemSummary](t, m.get(fullURL))
	if len(cards) != len(rows) {
		t.Fatalf("%d cards for %d episodes", len(cards), len(rows))
	}
	for _, it := range rows {
		c, ok := cards[it.ID]
		if !ok {
			t.Errorf("no card for the episode %d", it.ID)
			continue
		}
		if c.Kind != it.Kind || c.Title != it.Title || c.Show != it.Show || c.AddedAt != it.AddedAt || c.DurationSec != it.DurationSec ||
			c.Still != it.Still || c.Overview != it.Overview || c.Width != it.Width || c.Height != it.Height || c.HDR != it.HDR ||
			c.Watched != it.WatchState.Watched || c.PlayCount != it.WatchState.PlayCount ||
			(c.Progress == nil) != (it.Progress == nil) || (c.Progress != nil && *c.Progress != *it.Progress) ||
			(c.Season == nil) != (it.Season == nil) || (c.Episode == nil) != (it.Episode == nil) {
			t.Errorf("card %+v\nis not the row %+v", c, it)
		}
		if !slices.Equal(c.Ratings, cardRatings(it.Ratings)) {
			t.Errorf("card %d: ratings %v, row %v", it.ID, c.Ratings, it.Ratings)
		}
	}
}

// cardRatings is what a row's ratings become on a card: the same, without the link and the votes.
func cardRatings(rs []RatingEntry) []CardRating {
	if len(rs) == 0 {
		return nil
	}
	out := make([]CardRating, len(rs))
	for i, r := range rs {
		out[i] = CardRating{Source: r.Source, Value: r.Value, Scale: r.Scale, Score100: r.Score100, State: r.State}
	}
	return out
}

// The fields a page draws from, which the filler rows of the fixture do not exercise: the still, the
// episode's own ratings, HDR, and the watch state the client patches after a mark.
func TestEpisodeCardMapsTheRow(t *testing.T) {
	still, watched := "tmdb:/still1.jpg", int64(1700000000)
	season, episode, year := 1, 2, 2008
	c := episodeCard(ItemSummary{
		ID: 7, Kind: store.KindEpisode, Title: "Pilot", Year: &year, Show: "Cool Show", Season: &season, Episode: &episode,
		EpisodeTitle: "Pilot, part one", DurationSec: 2400, Width: 3840, Height: 2160, HDR: "hdr10", AddedAt: 5,
		Progress:   &Progress{PositionSec: 10},
		Art:        &ArtURLs{Thumb: "/api/items/7/art/thumb", Color: "#112233"},
		WatchState: WatchState{Watched: true, WatchedAt: &watched, PlayCount: 2},
		// The show's rating goes in and is not carried: a card has an episode's own ratings only.
		MetaFields: MetaFields{Rating: &RatingEntry{Source: "imdb", Value: 8, Scale: 10, Score100: 80}},
		Still:      still,
		Ratings:    []RatingEntry{{Source: "imdb", Value: 8, Scale: 10, Score100: 80}, {Source: "tomatoes", Value: 88, Scale: 100, Score100: 88}},
	})
	if c.ID != 7 || c.Kind != store.KindEpisode || c.Title != "Pilot" || c.Show != "Cool Show" || c.EpisodeTitle != "Pilot, part one" ||
		c.Still != still || c.Width != 3840 || c.Height != 2160 || c.HDR != "hdr10" ||
		c.Watched != true || c.WatchedAt == nil || *c.WatchedAt != watched || c.PlayCount != 2 ||
		c.Progress == nil || c.Progress.PositionSec != 10 || c.Art == nil || c.Art.Color != "#112233" || c.Art.Thumb != "/api/items/7/art/thumb" ||
		len(c.Ratings) != 2 || c.Ratings[1].Score100 != 88 ||
		c.Season == nil || *c.Season != season || c.Episode == nil || *c.Episode != episode || c.Year == nil || *c.Year != year {
		t.Errorf("episodeCard = %+v", c)
	}
}

// The episode list is leaner on the wire than the full one, not merely different: that is the whole
// point of the projection (TestListPayload measures 5,000 of them).
func TestEpisodeCardListIsLeaner(t *testing.T) {
	m := cardEnv(t)
	card, full := m.get("/api/items?kind=episode&fields=card").Body.Len(), m.get("/api/items?kind=episode").Body.Len()
	if full == 0 {
		t.Fatal("control: the full episode list is empty")
	}
	if card >= full {
		t.Errorf("the card list is %d bytes, the full one %d: not leaner", card, full)
	}
}

// The three shapes of the episode list keep three keys: the movie cards', the episode cards' and the
// full list's are never the same one.
func TestEpisodeCardETagIsItsOwn(t *testing.T) {
	m := cardEnv(t)
	movieCards, episodeCards, episodeFull := "/api/items?kind=movie&fields=card", "/api/items?kind=episode&fields=card", "/api/items?kind=episode"
	tags := []string{
		m.get(movieCards).Header().Get("ETag"),
		m.get(episodeCards).Header().Get("ETag"),
		m.get(episodeFull).Header().Get("ETag"),
	}
	seen := map[string]bool{}
	for i, tag := range tags {
		if tag == "" {
			t.Fatalf("list %d has no ETag", i)
		}
		if seen[tag] {
			t.Errorf("two list shapes share the ETag %q; a validator must name one shape", tag)
		}
		seen[tag] = true
	}
	if !strings.HasSuffix(tags[0], `.card"`) {
		t.Errorf("the movie card ETag %q: want it to end in .card, or every cached card list is evicted", tags[0])
	}
	if rec := m.condGet(episodeCards, tags[1]); rec.Code != http.StatusNotModified {
		t.Errorf("the episode cards with their own tag = %d, want 304", rec.Code)
	}
	if rec := m.condGet(episodeCards, tags[0]); rec.Code != 200 {
		t.Errorf("the episode cards with the movie cards' tag = %d, want 200", rec.Code)
	}
	if rec := m.condGet(episodeCards, tags[2]); rec.Code != 200 {
		t.Errorf("the episode cards with the full episode list's tag = %d, want 200", rec.Code)
	}
}

// limit answers the first N rows of the list, after the sort and the filters, in every shape; the
// un-limited list is the control that there were more to cut.
func TestItemListLimit(t *testing.T) {
	m := cardEnv(t)
	// A film with no rating whose file sorts before the others: minRating removes it, so a cut made
	// before the filters would take this row and leave nothing. The controls below insist on that.
	m.addFile("Aaa Unrated Movie.mkv", store.Item{Kind: store.KindMovie, Title: "Aaa Unrated Movie"})
	all := decode[[]ItemSummary](t, m.get("/api/items?kind=movie"))
	if len(all) < 2 {
		t.Fatalf("control: %d films in the fixture, want at least 2", len(all))
	}
	for _, url := range []string{"/api/items?kind=movie&limit=1", "/api/items?kind=movie&fields=card&limit=1"} {
		var ids []int64
		if strings.Contains(url, "fields=card") {
			for _, c := range decode[[]ItemCard](t, m.get(url)) {
				ids = append(ids, c.ID)
			}
		} else {
			for _, it := range decode[[]ItemSummary](t, m.get(url)) {
				ids = append(ids, it.ID)
			}
		}
		if !slices.Equal(ids, []int64{all[0].ID}) {
			t.Errorf("GET %s = %v, want just the first film %d", url, ids, all[0].ID)
		}
	}
	if n := len(decode[[]ItemSummary](t, m.get("/api/items?kind=movie&limit=99"))); n != len(all) {
		t.Errorf("limit=99 answered %d of %d films: a limit over the list is the whole list", n, len(all))
	}
	if n := len(decode[[]EpisodeCard](t, m.get("/api/items?kind=episode&fields=card&limit=1"))); n != 1 {
		t.Errorf("the episode cards with limit=1 answered %d", n)
	}
	// After the filters: minRating and show narrow the list, and the cut is of what survives. The
	// controls matter- the fixture must have films the minimum removes and a show of its own, or the
	// cut could be made before them and every assertion below would still pass.
	filtered := decode[[]ItemSummary](t, m.get("/api/items?kind=movie&minRating=50"))
	if len(filtered) == 0 || len(filtered) == len(all) {
		t.Fatalf("control: minRating=50 left %d of %d films: it must leave some and remove some", len(filtered), len(all))
	}
	for _, it := range filtered {
		if it.ID == all[0].ID {
			t.Fatalf("control: the first film of the list survives minRating=50, so a cut before the filter cannot be told apart")
		}
	}
	if got := decode[[]ItemSummary](t, m.get("/api/items?kind=movie&minRating=50&limit=1")); len(got) != 1 || got[0].ID != filtered[0].ID {
		t.Errorf("minRating=50&limit=1 answered %v, want just %d: the cut is after the filter", got, filtered[0].ID)
	}
	// A show of its own whose file sorts before Cool Show's: with the cut made before the show filter,
	// it takes that row and the filter then leaves nothing -- which is what tells the two orders apart.
	m.addFile("Aaa Other Show S01E01.mkv", store.Item{Kind: store.KindEpisode, Show: "Other Show", Season: 1, Episode: 1})
	allEps := decode[[]ItemSummary](t, m.get("/api/items?kind=episode"))
	showEps := decode[[]ItemSummary](t, m.get("/api/items?kind=episode&show=Cool+Show"))
	if len(showEps) == 0 || len(showEps) == len(allEps) {
		t.Fatalf("control: the show filter left %d of %d episodes: it must narrow the list", len(showEps), len(allEps))
	}
	if got := decode[[]EpisodeCard](t, m.get("/api/items?kind=episode&show=Cool+Show&fields=card&limit=1")); len(got) != 1 || got[0].ID != showEps[0].ID {
		t.Errorf("show=Cool+Show&fields=card&limit=1 answered %v, want just %d", got, showEps[0].ID)
	}
	// sort=rating is Go's, not the store's: the best-rated film, never the first row of the store (the
	// first film here has no rating, so sort=rating pushes it last). With the push-down wrongly on, the
	// store would return that first row and the sort would have one row to order.
	best := decode[[]ItemSummary](t, m.get("/api/items?kind=movie&sort=rating&limit=1"))
	if len(best) != 1 || best[0].Rating == nil {
		t.Fatalf("sort=rating&limit=1 = %+v, want one film with a rating", best)
	}
	if best[0].ID == all[0].ID {
		t.Errorf("sort=rating&limit=1 answered the store's first film (%d): the rating sort did not come before the cut", best[0].ID)
	}

	// A limit outside 1..500 is a 400: no page can draw a rail from an empty answer it did not ask
	// for, and one request never takes a whole big library.
	for _, url := range []string{"/api/items?kind=movie&limit=0", "/api/items?kind=movie&limit=-1", "/api/items?kind=movie&limit=abc",
		"/api/items?kind=movie&limit=501"} {
		rec := m.get(url)
		wantJSONError(t, rec, http.StatusBadRequest)
		if rec.Header().Get("ETag") != "" {
			t.Errorf("GET %s: a 400 with ETag %q", url, rec.Header().Get("ETag"))
		}
	}
	if n := len(decode[[]ItemSummary](t, m.get("/api/items?kind=movie&limit=500"))); n == 0 {
		t.Error("limit=500 is in range and answered nothing")
	}
	// The limit is in the key: a limited list is never the validator of the whole one.
	limited, whole := m.get("/api/items?kind=movie&limit=1").Header().Get("ETag"), m.get("/api/items?kind=movie").Header().Get("ETag")
	if limited == "" || whole == "" || limited == whole {
		t.Errorf("ETags: limit=1 %q, the whole list %q: want two, and different", limited, whole)
	}
	if rec := m.condGet("/api/items?kind=movie&limit=1", limited); rec.Code != http.StatusNotModified {
		t.Errorf("the limited list with its own tag = %d, want 304", rec.Code)
	}
}

// limit is the cut of what the same query answers without it, with every sort and filter: the
// database applies it when it can and Go when the filters need the whole list, and the two must
// answer the same rows.
func TestItemListLimitIsTheCutOfTheWholeList(t *testing.T) {
	m := cardEnv(t)
	cut := 0
	for _, q := range []string{"kind=movie", "kind=episode", "kind=movie&sort=rating", "kind=movie&minRating=50", "kind=episode&minRating=50"} {
		all := decode[[]map[string]any](t, m.get("/api/items?"+q))
		got := decode[[]map[string]any](t, m.get("/api/items?"+q+"&limit=1"))
		want := all
		if len(want) > 1 {
			want = want[:1]
			cut++
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: limit=1 = %v, want the first of %v", q, got, all)
		}
	}
	if cut == 0 {
		t.Fatal("control: no query had more than one row, so no cut was tested")
	}
}

// dropTable removes a table behind the store's back, so that what reads it fails and the rest does not.
func dropTable(t *testing.T, m *menv, table string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(filepath.Dir(m.dir), "data dir", "ply.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("DROP TABLE " + table); err != nil {
		t.Fatal(err)
	}
}

// A list that could not be filled in (the rows are sent without their titles, or their episodes)
// is not one to keep: no ETag, no-store. The control is the same list before the table went.
func TestDegradedListIsNotKept(t *testing.T) {
	for _, c := range []struct {
		name, table string
		urls        []string
	}{
		{"the genres of the titles cannot be read", "title_genres", []string{"/api/items?kind=movie", "/api/items?kind=movie&fields=card", "/api/items", "/api/shows"}},
		{"the episodes cannot be read", "episodes", []string{"/api/items?kind=episode", "/api/items"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := newMetaEnv(t, "")
			healthy := map[string]string{}
			for _, url := range c.urls {
				rec := m.get(url)
				wantStatus(t, rec, 200)
				if rec.Header().Get("ETag") == "" || rec.Header().Get("Cache-Control") != "private, no-cache" {
					t.Fatalf("control, GET %s while whole: ETag %q, Cache-Control %q", url, rec.Header().Get("ETag"), rec.Header().Get("Cache-Control"))
				}
				healthy[url] = rec.Body.String()
			}
			dropTable(t, m, c.table)
			for _, url := range c.urls {
				rec := m.get(url)
				wantStatus(t, rec, 200)
				if rec.Header().Get("ETag") != "" || rec.Header().Get("Cache-Control") != "no-store" {
					t.Errorf("GET %s with %s: ETag %q, Cache-Control %q; want none and no-store", url, c.table, rec.Header().Get("ETag"), rec.Header().Get("Cache-Control"))
				}
				if rec.Body.Len() < 100 {
					t.Errorf("GET %s: the list is not sent (%q)", url, rec.Body.String())
				}
				if rec.Body.String() == healthy[url] {
					t.Errorf("GET %s: the same list as when whole; the control did not take something away", url)
				}
			}
		})
	}
}
