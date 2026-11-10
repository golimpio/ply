package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"ply/internal/art"
	"ply/internal/metadata"
	"ply/internal/probe"
	"ply/internal/store"
)

// seedBigLibrary fills the store with movies matched to titles, and shows with seasons of
// episodes matched to series titles. The names, genres and artwork refs are the sizes a real
// library has (the shapes TMDB gives), not a fixture.
func seedBigLibrary(t testing.TB, st *store.Store, movies, shows, seasons, perSeason int) {
	t.Helper()
	ctx := context.Background()
	articles := []string{"The", "A", "An", "", "", "", "", ""}
	adjectives := []string{"Last", "Silent", "Broken", "Golden", "Hidden", "Eternal", "Crimson", "Lost", "Final", "Secret",
		"Burning", "Frozen", "Distant", "Savage", "Gentle", "Midnight"}
	nouns := []string{"Horizon", "Empire", "Garden", "Mirror", "Harvest", "Kingdom", "Voyage", "Witness", "Promise", "Letter",
		"Machine", "Orchard", "Station", "Island", "Bridge", "Signal"}
	genres := []store.Genre{{ID: 28, Name: "Action"}, {ID: 12, Name: "Adventure"}, {ID: 16, Name: "Animation"}, {ID: 35, Name: "Comedy"},
		{ID: 80, Name: "Crime"}, {ID: 99, Name: "Documentary"}, {ID: 18, Name: "Drama"}, {ID: 10751, Name: "Family"},
		{ID: 14, Name: "Fantasy"}, {ID: 27, Name: "Horror"}, {ID: 9648, Name: "Mystery"}, {ID: 878, Name: "Science Fiction"},
		{ID: 53, Name: "Thriller"}}
	name := func(i int) string {
		n := adjectives[i%len(adjectives)] + " " + nouns[(i/len(adjectives))%len(nouns)]
		if i >= len(adjectives)*len(nouns) {
			n += " " + strconv.Itoa(i/(len(adjectives)*len(nouns))+1)
		}
		if a := articles[(i/7)%len(articles)]; a != "" {
			n = a + " " + n
		}
		return n
	}
	ref := func(prefix string, i int) string {
		return fmt.Sprintf("tmdb:/%s%08x%08x.jpg", prefix, uint32(i)*2654435761, uint32(i)*40503+7)
	}
	overview := strings.Repeat("A story told in ordinary words about people who change their minds. ", 4)
	pick := func(i int) []store.Genre {
		return []store.Genre{genres[i%len(genres)], genres[(i/3+1)%len(genres)], genres[(i/5+2)%len(genres)]}
	}

	for i := range movies {
		title := name(i)
		year := 1950 + i%75
		tid, err := st.UpsertTitle(ctx, &store.TitleInput{Title: store.Title{Kind: store.KindMovie, TMDBID: int64(1000 + i),
			IMDbID: fmt.Sprintf("tt%07d", i+1), Title: title, OriginalTitle: title, SortTitle: metadata.SortTitle(title), Year: year,
			Overview: overview, Tagline: "Nothing is what it seems.", ReleaseDate: fmt.Sprintf("%d-03-14", year), RuntimeMin: 90 + i%60,
			Rating: 5 + float64(i%50)/10, VoteCount: 100 + i, Certification: []string{"G", "PG", "PG-13", "R"}[i%4],
			Poster: ref("p", i), Backdrop: ref("b", i), Logo: ref("l", i), Status: "Released", Source: store.SourceTMDB},
			Genres: pick(i)})
		if err != nil {
			t.Fatal(err)
		}
		dir := fmt.Sprintf("/media/movies/%s (%d)", title, year)
		id, err := st.UpsertItem(ctx, &store.Item{Kind: store.KindMovie, Title: title, Year: year, Path: dir + "/" + title + ".mkv",
			Root: "/media/movies", Size: 8 << 30, MTime: 1_700_000_000_000_000_000 + int64(i), Container: "matroska,webm", DurationSec: 6000 + float64(i%3000),
			Bitrate: 12_000_000, AddedAt: 1_700_000_000 + int64(i)}, []probe.Stream{
			{Index: 0, Type: "video", Codec: "hevc", Width: 3840, Height: 2160, PixFmt: "yuv420p10le", ColorTransfer: "smpte2084", Default: true},
			{Index: 1, Type: "audio", Codec: "eac3", Channels: 6, Language: "eng", Default: true},
			{Index: 2, Type: "audio", Codec: "aac", Channels: 2, Language: "por"},
			{Index: 3, Type: "subtitle", Codec: "subrip", Language: "eng"}})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SetItemMatch(ctx, id, store.Match{TitleID: tid, State: store.MatchMatched, Score: 0.95}); err != nil {
			t.Fatal(err)
		}
	}

	for i := range shows {
		show := name(i + 100_000)
		tid, err := st.UpsertTitle(ctx, &store.TitleInput{Title: store.Title{Kind: store.KindSeries, TMDBID: int64(500_000 + i), Title: show,
			OriginalTitle: show, SortTitle: metadata.SortTitle(show), Year: 1990 + i%35, Overview: overview, ReleaseDate: fmt.Sprintf("%d-09-21", 1990+i%35),
			Rating: 6 + float64(i%40)/10, VoteCount: 50 + i, Certification: "TV-MA", Poster: ref("sp", i), Backdrop: ref("sb", i), Logo: ref("sl", i),
			Status: "Ended", Source: store.SourceTMDB}, Genres: pick(i)})
		if err != nil {
			t.Fatal(err)
		}
		for s := 1; s <= seasons; s++ {
			for e := 1; e <= perSeason; e++ {
				path := fmt.Sprintf("/media/shows/%s/Season %d/%s S%02dE%02d.mkv", show, s, show, s, e)
				if _, err := st.UpsertItem(ctx, &store.Item{Kind: store.KindEpisode, Show: show, Season: s, Episode: e, Path: path,
					Root: "/media/shows", Size: 2 << 30, MTime: 1_700_000_000_000_000_000 + int64(i*100+s*10+e), Container: "matroska,webm", DurationSec: 2700,
					Bitrate: 6_000_000, AddedAt: 1_700_000_000}, []probe.Stream{
					{Index: 0, Type: "video", Codec: "h264", Width: 1920, Height: 1080, Default: true},
					{Index: 1, Type: "audio", Codec: "aac", Channels: 2, Language: "eng", Default: true}}); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := st.SetSeriesMatch(ctx, show, store.Match{TitleID: tid, State: store.MatchMatched, Score: 0.9}); err != nil {
			t.Fatal(err)
		}
	}
}

// fieldWeights adds up, over the rows of a JSON list, what each top-level field weighs
// (its name, the colon and its value), biggest first.
func fieldWeights(t testing.TB, body []byte) (rows int, weights []fieldWeight) {
	t.Helper()
	var list []map[string]json.RawMessage
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	sum := map[string]int{}
	for _, row := range list {
		for k, v := range row {
			sum[k] += len(k) + 4 + len(v)
		}
	}
	for k, n := range sum {
		weights = append(weights, fieldWeight{k, n})
	}
	slices.SortFunc(weights, func(a, b fieldWeight) int { return b.bytes - a.bytes })
	return len(list), weights
}

type fieldWeight struct {
	name  string
	bytes int
}

// cardFields are what a tile of the grid shows or sorts by: the fields of a lean projection.
var cardFields = map[string][]string{
	"/api/items?kind=movie": {"id", "kind", "title", "sortTitle", "year", "poster", "rating", "watched", "progress", "titleId", "matchState", "needsReview"},
	"/api/shows":            {"show", "sortTitle", "year", "poster", "rating", "episodeCount", "watchedEpisodes", "inProgress", "titleId", "matchState", "needsReview"},
	"/api/titles":           {"id", "kind", "title", "sortTitle", "year", "poster", "rating", "itemId", "show", "watched", "episodeCount", "watchedEpisodes", "inProgress"},
}

// project keeps the fields of each row of a JSON list, and of a rating its source, value, scale and
// score100: what the projection would weigh.
func project(t testing.TB, body []byte, keep []string) []byte {
	t.Helper()
	var list []map[string]json.RawMessage
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	out := make([]map[string]json.RawMessage, len(list))
	for i, row := range list {
		out[i] = map[string]json.RawMessage{}
		for _, k := range keep {
			v, ok := row[k]
			if !ok {
				continue
			}
			if k == "rating" && string(v) != "null" {
				var r map[string]json.RawMessage
				json.Unmarshal(v, &r)
				v, _ = json.Marshal(map[string]json.RawMessage{"source": r["source"], "value": r["value"], "scale": r["scale"], "score100": r["score100"]})
			}
			out[i][k] = v
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func gzipSize(b []byte, level int) int {
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, level)
	zw.Write(b)
	zw.Close()
	return buf.Len()
}

// median runs fn n times and returns the middle time, and the fastest and slowest.
func median(n int, fn func()) (p50, lo, hi time.Duration) {
	d := make([]time.Duration, n)
	for i := range d {
		start := time.Now()
		fn()
		d[i] = time.Since(start)
	}
	slices.Sort(d)
	return d[n/2], d[0], d[n-1]
}

// TestListPayload measures what the library lists weigh and cost for a big library. It is
// not part of the gate (seeding takes a while):
//
//	PLY_LISTPERF=1 go test -run TestListPayload -v ./internal/api/
//
// PLY_LISTPERF_MOVIES and PLY_LISTPERF_SHOWS change the sizes.
func TestListPayload(t *testing.T) {
	if os.Getenv("PLY_LISTPERF") == "" {
		t.Skip("set PLY_LISTPERF=1 to measure the lists of a big library")
	}
	envInt := func(name string, def int) int {
		if n, err := strconv.Atoi(os.Getenv(name)); err == nil {
			return n
		}
		return def
	}
	movies, shows := envInt("PLY_LISTPERF_MOVIES", 5000), envInt("PLY_LISTPERF_SHOWS", 500)

	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ply.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	start := time.Now()
	seedBigLibrary(t, st, movies, shows, 2, 5)
	t.Logf("seeded %d movies and %d shows (10 episodes each) in %v", movies, shows, time.Since(start).Round(time.Millisecond))

	g, err := art.New(context.Background(), art.Options{Dir: filepath.Join(dir, "art"), FFmpeg: "/nonexistent/ffmpeg"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	srv := New(Options{Store: st, Scanner: &fakeScanner{}, Art: g, ID: "perf", Name: "perf", Version: "0"})

	get := func(target string, hdr map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}

	for _, target := range []string{"/api/items?kind=movie", "/api/items?kind=movie&fields=card", "/api/items?kind=episode", "/api/items?kind=episode&fields=card", "/api/shows", "/api/titles"} {
		rec := get(target, nil)
		if rec.Code != 200 {
			t.Fatalf("GET %s = %d %s", target, rec.Code, rec.Body.String())
		}
		raw := rec.Body.Bytes()
		rows, weights := fieldWeights(t, raw)
		t.Logf("%s: %d rows, %d bytes raw (%.0f B/row), %d gzip default, %d gzip level 1",
			target, rows, len(raw), float64(len(raw))/float64(rows), gzipSize(raw, gzip.DefaultCompression), gzipSize(raw, gzip.BestSpeed))
		for i, w := range weights {
			if i == 8 {
				break
			}
			t.Logf("    %-14s %9d B  %5.1f B/row  %4.1f%%", w.name, w.bytes, float64(w.bytes)/float64(rows), 100*float64(w.bytes)/float64(len(raw)))
		}
		if keep, ok := cardFields[target]; ok {
			t.Logf("    a card projection (only %s; the rating without its url and time) would be %d bytes (%.0f B/row), %d gzipped",
				strings.Join(keep, ", "), len(project(t, raw, keep)), float64(len(project(t, raw, keep)))/float64(rows), gzipSize(project(t, raw, keep), gzip.BestSpeed))
		}
		p50, lo, hi := median(7, func() { get(target, nil) })
		t.Logf("    handler time: p50 %v (min %v, max %v) over 7 runs", p50.Round(time.Millisecond), lo.Round(time.Millisecond), hi.Round(time.Millisecond))
		if etag := rec.Header().Get("ETag"); etag != "" {
			p50, lo, hi = median(7, func() {
				if r := get(target, map[string]string{"If-None-Match": etag}); r.Code != http.StatusNotModified {
					t.Fatalf("If-None-Match %s answered %d", etag, r.Code)
				}
			})
			t.Logf("    304 time:     p50 %v (min %v, max %v)", p50.Round(time.Microsecond), lo.Round(time.Microsecond), hi.Round(time.Microsecond))
		}
		zip := map[string]string{"Accept-Encoding": "gzip"}
		zrec := get(target, zip)
		t.Logf("    Accept-Encoding: gzip -> Content-Encoding %q, %d bytes on the wire", zrec.Header().Get("Content-Encoding"), zrec.Body.Len())
		zp50, _, _ := median(7, func() { get(target, zip) })
		t.Logf("    handler time with gzip: p50 %v", zp50.Round(time.Millisecond))
	}

	// With a token every picture link carries it (?token=...): the same list weighs more.
	tok := strings.Repeat("a", 32)
	srvTok := New(Options{Store: st, Scanner: &fakeScanner{}, Art: g, ID: "perf", Name: "perf", Version: "0", Token: tok})
	for _, target := range []string{"/api/items?kind=movie", "/api/items?kind=movie&fields=card", "/api/items?kind=episode", "/api/items?kind=episode&fields=card", "/api/shows", "/api/titles"} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		srvTok.ServeHTTP(rec, req)
		rows, _ := fieldWeights(t, rec.Body.Bytes())
		t.Logf("with a 32-character token, %s: %d bytes raw (%.0f B/row), %d gzipped", target, rec.Body.Len(), float64(rec.Body.Len())/float64(rows),
			gzipSize(rec.Body.Bytes(), gzip.BestSpeed))
	}

	// Where the time of the biggest list goes.
	ctx := context.Background()
	var entries []store.Entry
	listT, _, _ := median(7, func() {
		var err error
		if entries, err = st.ListItems(ctx, store.ListFilter{Kind: store.KindMovie}); err != nil {
			t.Fatal(err)
		}
	})
	var sums []ItemSummary
	sumT, _, _ := median(7, func() { sums = srv.summaries(ctx, entries) })
	encT, _, _ := median(7, func() {
		if _, err := json.Marshal(sums); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("/api/items?kind=movie breakdown: Store.ListItems %v, summaries (metadata, art, ratings) %v, JSON encoding %v",
		listT.Round(time.Millisecond), sumT.Round(time.Millisecond), encT.Round(time.Millisecond))

	// And what the summaries spend on the titles of the rows.
	ids := make([]int64, 0, len(entries))
	for _, e := range entries {
		ids = append(ids, e.TitleID)
	}
	briefT, _, _ := median(7, func() {
		if _, err := st.TitleBriefs(ctx, ids); err != nil {
			t.Fatal(err)
		}
	})
	filmT, _, _ := median(7, func() {
		if _, err := st.FilmWatches(ctx, ids); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("    of the summaries: Store.TitleBriefs %v, Store.FilmWatches %v", briefT.Round(time.Millisecond), filmT.Round(time.Millisecond))
}
