package metadata

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ply/internal/store"
)

// gzipOf compresses text.
func gzipOf(t testing.TB, text string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(text)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func wanting(ids ...string) func([]byte) bool {
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	return func(b []byte) bool { return set[string(b)] }
}

const imdbHead = "tconst\taverageRating\tnumVotes\n"

// fetchIMDbRatings fetches the ratings dataset with no row count to hold it to.
func fetchIMDbRatings(ctx context.Context, client *http.Client, src, dest string, v imdbValidators) (imdbFetch, error) {
	return fetchIMDb(ctx, client, src, dest, imdbRatingsHeader, 0, v)
}

func TestParseIMDbRatings(t *testing.T) {
	all := func([]byte) bool { return true }
	for _, tc := range []struct {
		name        string
		text        string
		want        func([]byte) bool
		kept        map[string]IMDbRating
		rows, skips int
	}{
		{"header only", imdbHead, all, map[string]IMDbRating{}, 0, 0},
		{"rows", imdbHead + "tt0111161\t9.3\t3135040\ntt0068646\t9.2\t2186352\n", all,
			map[string]IMDbRating{"tt0111161": {9.3, 3135040}, "tt0068646": {9.2, 2186352}}, 2, 0},
		{"only the wanted are kept", imdbHead + "tt1\t5.0\t10\ntt2\t6.0\t20\ntt3\t7.0\t30\n", wanting("tt2"),
			map[string]IMDbRating{"tt2": {6, 20}}, 3, 0},
		{"no newline at the end", imdbHead + "tt1\t5.5\t10", all, map[string]IMDbRating{"tt1": {5.5, 10}}, 1, 0},
		{"CRLF", "tconst\taverageRating\tnumVotes\r\ntt1\t5.5\t10\r\ntt2\t6.5\t20\r\n", all,
			map[string]IMDbRating{"tt1": {5.5, 10}, "tt2": {6.5, 20}}, 2, 0},
		{"blank lines are not rows", imdbHead + "\ntt1\t5.5\t10\n\n", all, map[string]IMDbRating{"tt1": {5.5, 10}}, 1, 0},
		{"\\N is no rating", imdbHead + "tt1\t\\N\t10\ntt2\t5.0\t\\N\ntt3\t4.0\t5\n", all, map[string]IMDbRating{"tt3": {4, 5}}, 3, 2},
		{"bad lines are skipped, the rest read",
			imdbHead + "tt1\t5.0\n" + "xx3\t5.0\t1\n" + "tt4\tfast\t1\n" + "tt5\t5.0\tmany\n" +
				"tt6\t0\t1\n" + "tt7\t11.0\t1\n" + "tt8\t5.0\t-1\n" + "\t5.0\t1\n" + "tt\t5.0\t1\n" + "tt9\t8.1\t99\n", all,
			map[string]IMDbRating{"tt9": {8.1, 99}}, 10, 9},
		// IMDb may add columns at the end of a row (and of the header): the first ones are still the ones.
		{"columns after the three", imdbHead + "tt2\t5.0\t1\textra\ttt1\n", all, map[string]IMDbRating{"tt2": {5, 1}}, 1, 0},
		{"a header with a column added", "tconst\taverageRating\tnumVotes\tgenre\ntt1\t5.5\t10\tdrama\n", all,
			map[string]IMDbRating{"tt1": {5.5, 10}}, 1, 0},
		{"a byte order mark before the header", "\xef\xbb\xbf" + imdbHead + "tt1\t5.5\t10\n", all, map[string]IMDbRating{"tt1": {5.5, 10}}, 1, 0},
		{"a bad line that is not wanted is still a bad line", imdbHead + "tt1\t\\N\t1\n", wanting("tt2"), map[string]IMDbRating{}, 1, 0},
		{"a line of garbage bytes", imdbHead + "\x00\xff\xfe\ntt1\t5.0\t1\n", all, map[string]IMDbRating{"tt1": {5, 1}}, 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kept, stats, err := ParseIMDbRatings(t.Context(), bytes.NewReader(gzipOf(t, tc.text)), tc.want, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(kept) != len(tc.kept) {
				t.Errorf("kept = %v, want %v", kept, tc.kept)
			}
			for k, v := range tc.kept {
				if kept[k] != v {
					t.Errorf("kept[%s] = %+v, want %+v", k, kept[k], v)
				}
			}
			if stats.Rows != tc.rows || stats.Skipped != tc.skips || stats.Kept != len(tc.kept) {
				t.Errorf("stats = %+v, want %d rows, %d skipped, %d kept", stats, tc.rows, tc.skips, len(tc.kept))
			}
		})
	}
}

func TestParseIMDbRatingsRefusesWhatIsNotTheDataset(t *testing.T) {
	all := func([]byte) bool { return true }
	long := "tt1\t5.0\t" + strings.Repeat("9", 400<<10) + "\ntt2\t6.0\t2\n" // a line longer than the reader's buffer
	// The control: a line that long is skipped, and the row after it is read.
	kept, stats, err := ParseIMDbRatings(t.Context(), bytes.NewReader(gzipOf(t, imdbHead+long)), all, nil)
	if err != nil || kept["tt2"].Votes != 2 || len(kept) != 1 || stats.Skipped != 1 {
		t.Fatalf("control: a very long line = %v, %+v, %v", kept, stats, err)
	}

	for name, body := range map[string][]byte{
		"not a gzip":           []byte(imdbHead + "tt1\t5.0\t1\n"),
		"empty":                nil,
		"another header":       gzipOf(t, "id\trating\tvotes\ntt1\t5.0\t1\n"),
		"columns reorder":      gzipOf(t, "tconst\tnumVotes\taverageRating\ntt1\t1\t5.0\n"),
		"a column cut":         gzipOf(t, "tconst\taverageRating\ntt1\t5.0\n"),
		"a longer name":        gzipOf(t, "tconst\taverageRating\tnumVotesTotal\ntt1\t5.0\t1\n"),
		"an empty gzip":        gzipOf(t, ""),
		"no header at all":     gzipOf(t, "tt1\t5.0\t1\n"),
		"the episodes' header": gzipOf(t, episodesHead+"tt2\ttt1\t1\t1\n"),
	} {
		kept, _, err := ParseIMDbRatings(t.Context(), bytes.NewReader(body), all, nil)
		if !errors.Is(err, ErrIMDbFormat) || kept != nil {
			t.Errorf("%s: kept %v, err %v; want ErrIMDbFormat and nothing", name, kept, err)
		}
	}

	// A file cut short is an error, not a short list.
	whole := gzipOf(t, imdbHead+strings.Repeat("tt1\t5.0\t1\n", 5000))
	for _, cut := range []int{len(whole) - 1, len(whole) - 8, len(whole) / 2} {
		kept, _, err := ParseIMDbRatings(t.Context(), bytes.NewReader(whole[:cut]), all, nil)
		if err == nil || kept != nil {
			t.Errorf("cut at %d of %d: kept %d rows, err %v; a half-read file must not pass for the whole", cut, len(whole), len(kept), err)
		}
	}
	if _, _, err := ParseIMDbRatings(t.Context(), bytes.NewReader(whole), all, nil); err != nil {
		t.Errorf("control: the whole file = %v", err)
	}
}

// yield is called as the rows go by, and what it says (stop) ends the parse; so does the context.
func TestParseIMDbRatingsYields(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(imdbHead)
	for i := range 3*imdbYieldEvery + 10 {
		fmt.Fprintf(&sb, "tt%d\t5.0\t1\n", i)
	}
	body := gzipOf(t, sb.String())
	all := func([]byte) bool { return false }

	var calls int
	if _, _, err := ParseIMDbRatings(t.Context(), bytes.NewReader(body), all, func(context.Context) error { calls++; return nil }); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Errorf("yield was called %d times for %d rows, want 3", calls, 3*imdbYieldEvery+10)
	}

	stop := errors.New("stop")
	calls = 0
	_, stats, err := ParseIMDbRatings(t.Context(), bytes.NewReader(body), all, func(context.Context) error {
		calls++
		if calls == 2 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || stats.Rows != 2*imdbYieldEvery {
		t.Errorf("a failing yield: %d rows, err %v; want to stop at row %d with its error", stats.Rows, err, 2*imdbYieldEvery)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := ParseIMDbRatings(ctx, bytes.NewReader(body), all, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled context: %v", err)
	}
}

// A file of 1.5 million rows, the size of IMDb's, is read in memory that does not grow with it, in a
// time that is logged.
func TestParseIMDbRatingsBigFileConstantMemory(t *testing.T) {
	const rows = 1_500_000
	path := filepath.Join(t.TempDir(), IMDbRatingsFile)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(f)
	fmt.Fprint(zw, imdbHead)
	wantIDs := map[string]IMDbRating{}
	for i := range rows {
		id := fmt.Sprintf("tt%07d", i)
		rating := float64(10+i%90) / 10 // 1.0 to 9.9
		fmt.Fprintf(zw, "%s\t%.1f\t%d\n", id, rating, 5+i%100000)
		if i%1500 == 0 { // a thousand titles of the library
			wantIDs[id] = IMDbRating{Value: rating, Votes: 5 + i%100000}
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	info, _ := os.Stat(path)

	in, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	var peak atomic.Uint64
	stopSampling := make(chan struct{})
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		var m runtime.MemStats
		for {
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > peak.Load() {
				peak.Store(m.HeapAlloc)
			}
			select {
			case <-stopSampling:
				return
			case <-time.After(2 * time.Millisecond):
			}
		}
	}()

	start := time.Now()
	kept, stats, err := ParseIMDbRatings(t.Context(), in, func(b []byte) bool { _, ok := wantIDs[string(b)]; return ok }, nil)
	took := time.Since(start)
	close(stopSampling)
	<-sampled
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}

	growth := int64(peak.Load()) - int64(before.HeapAlloc)
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("%d rows (%.1f MB gzipped) parsed in %v: %d kept, heap grew at most %.1f MB, %.1f MB allocated in all",
		stats.Rows, float64(info.Size())/1e6, took.Round(time.Millisecond), len(kept), float64(growth)/1e6, float64(allocated)/1e6)
	if stats.Rows != rows || stats.Skipped != 0 || len(kept) != len(wantIDs) {
		t.Fatalf("stats = %+v, kept %d of %d", stats, len(kept), len(wantIDs))
	}
	for id, want := range wantIDs {
		if kept[id] != want {
			t.Fatalf("kept[%s] = %+v, want %+v", id, kept[id], want)
		}
	}
	// 50 MB is the bound for the whole parse; a parse that kept the rows (or made a string of each) would
	// take several times that.
	if growth > 50<<20 {
		t.Errorf("the heap grew by %.1f MB; the parse is not constant in memory", float64(growth)/1e6)
	}
	if allocated > 50<<20 {
		t.Errorf("%.1f MB allocated for %d rows; a row must not cost an allocation", float64(allocated)/1e6, rows)
	}
}

func FuzzParseIMDbRatings(f *testing.F) {
	for _, seed := range []string{
		imdbHead, imdbHead + "tt1\t5.0\t1\n", imdbHead + "tt1\t\\N\t\\N\n", imdbHead + "tt1\t5.0\n\n\t\t\ntt2\t1e2\t1\n",
		imdbHead + "tt1\t5.0\t1\r\ntt2\t-0\t0", "garbage", "", imdbHead + strings.Repeat("t", 5000),
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		zw.Write(data)
		zw.Close()
		kept, stats, err := ParseIMDbRatings(t.Context(), &buf, func(b []byte) bool { return len(b)%2 == 0 }, nil)
		if err != nil {
			if kept != nil {
				t.Fatalf("an error with rows: %v", err)
			}
			return
		}
		if stats.Kept != len(kept) || stats.Rows < stats.Kept+0 {
			t.Fatalf("stats = %+v, kept %d", stats, len(kept))
		}
		for id, r := range kept {
			if !strings.HasPrefix(id, "tt") || len(id)%2 != 0 || !(r.Value > 0 && r.Value <= 10) || r.Votes < 0 {
				t.Fatalf("kept %q = %+v", id, r)
			}
		}
	})
}

// ---- the download ----

type datasetServer struct {
	*httptest.Server
	body    atomic.Pointer[[]byte]
	etag    string
	modTime string
	hits    atomic.Int32
	seen    atomic.Pointer[http.Header]
	status  atomic.Int32 // when set, answered instead of everything
}

func newDatasetServer(t *testing.T, body []byte) *datasetServer {
	t.Helper()
	d := &datasetServer{etag: `"abc123"`, modTime: "Wed, 04 Nov 2026 03:00:00 GMT"}
	d.body.Store(&body)
	d.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.hits.Add(1)
		h := r.Header.Clone()
		d.seen.Store(&h)
		if st := int(d.status.Load()); st != 0 {
			w.WriteHeader(st)
			return
		}
		if r.Header.Get("If-None-Match") == d.etag || r.Header.Get("If-Modified-Since") == d.modTime {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", d.etag)
		w.Header().Set("Last-Modified", d.modTime)
		w.Header().Set("Content-Type", "application/x-gzip")
		w.Write(*d.body.Load())
	}))
	t.Cleanup(d.Close)
	return d
}

func TestFetchIMDbRatingsIsConditional(t *testing.T) {
	body := gzipOf(t, imdbHead+"tt1\t5.0\t1\n")
	srv := newDatasetServer(t, body)
	dest := filepath.Join(t.TempDir(), "cache", "imdb", IMDbRatingsFile)

	got, err := fetchIMDbRatings(t.Context(), srv.Client(), srv.URL+"/title.ratings.tsv.gz", dest, imdbValidators{})
	if err != nil || got.NotModified || got.ETag != srv.etag || got.LastModified != srv.modTime || got.Bytes != int64(len(body)) {
		t.Fatalf("first fetch = %+v, %v", got, err)
	}
	if h := srv.seen.Load(); h.Get("If-None-Match") != "" || h.Get("If-Modified-Since") != "" {
		t.Errorf("the first request carried validators: %v", *h)
	}
	if h := srv.seen.Load(); h.Get("Accept-Encoding") != "identity" {
		t.Errorf("Accept-Encoding = %q; the file is a gzip and must not be decoded on the way", h.Get("Accept-Encoding"))
	}
	onDisk, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(onDisk, body) {
		t.Fatalf("the file on disk: %d bytes, %v", len(onDisk), err)
	}
	if fi, _ := os.Stat(dest); fi.Mode().Perm() != 0o600 {
		t.Errorf("the cached file is %v, want 0600", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(filepath.Dir(dest)); fi.Mode().Perm() != 0o700 {
		t.Errorf("the cache folder is %v, want 0700", fi.Mode().Perm())
	}

	// Asked again with what it said: a 304, and the file is not touched.
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(dest, old, old); err != nil {
		t.Fatal(err)
	}
	again, err := fetchIMDbRatings(t.Context(), srv.Client(), srv.URL+"/title.ratings.tsv.gz", dest, imdbValidators{ETag: got.ETag, LastModified: got.LastModified})
	if err != nil || !again.NotModified || again.Bytes != 0 {
		t.Fatalf("second fetch = %+v, %v; want a 304", again, err)
	}
	if h := srv.seen.Load(); h.Get("If-None-Match") != srv.etag || h.Get("If-Modified-Since") != srv.modTime {
		t.Errorf("the second request carried %v", *h)
	}
	fi, err := os.Stat(dest)
	if err != nil || !fi.ModTime().Equal(old.Truncate(time.Second)) && fi.ModTime().Sub(old).Abs() > time.Second {
		t.Errorf("a 304 rewrote the file: mtime %v, was %v (%v)", fi.ModTime(), old, err)
	}
	if onDisk2, _ := os.ReadFile(dest); !bytes.Equal(onDisk2, body) {
		t.Error("a 304 changed the file")
	}
	entries, _ := os.ReadDir(filepath.Dir(dest))
	if len(entries) != 1 {
		t.Errorf("the cache folder has %d entries after a 304 (a leftover temporary file?)", len(entries))
	}

	// A new file: replaced, whole.
	newer := gzipOf(t, imdbHead+"tt2\t6.0\t2\n")
	srv.body.Store(&newer)
	srv.etag, srv.modTime = `"def456"`, "Thu, 05 Nov 2026 03:00:00 GMT"
	third, err := fetchIMDbRatings(t.Context(), srv.Client(), srv.URL+"/x", dest, imdbValidators{ETag: got.ETag, LastModified: got.LastModified})
	if err != nil || third.NotModified || third.ETag != `"def456"` {
		t.Fatalf("third fetch = %+v, %v", third, err)
	}
	if onDisk3, _ := os.ReadFile(dest); !bytes.Equal(onDisk3, newer) {
		t.Error("the new file was not stored")
	}
}

// A download that is not a whole gzip, or an answer that is not a 200, leaves the file we have as it is.
func TestFetchIMDbRatingsKeepsTheFileItHasOnFailure(t *testing.T) {
	good := gzipOf(t, imdbHead+"tt1\t5.0\t1\n")
	srv := newDatasetServer(t, good)
	dest := filepath.Join(t.TempDir(), IMDbRatingsFile)
	if _, err := fetchIMDbRatings(t.Context(), srv.Client(), srv.URL, dest, imdbValidators{}); err != nil {
		t.Fatal(err)
	}
	srv.etag, srv.modTime = `"new"`, "Thu, 05 Nov 2026 03:00:00 GMT"

	whole := gzipOf(t, imdbHead+strings.Repeat("tt1\t5.0\t1\n", 3000))
	for name, body := range map[string][]byte{
		"cut short":    whole[:len(whole)-20],
		"not a gzip":   []byte("<html>maintenance</html>"),
		"empty answer": {},
	} {
		srv.body.Store(&body)
		_, err := fetchIMDbRatings(t.Context(), srv.Client(), srv.URL, dest, imdbValidators{})
		if err == nil {
			t.Errorf("%s: no error", name)
		}
		if onDisk, _ := os.ReadFile(dest); !bytes.Equal(onDisk, good) {
			t.Errorf("%s: the file we had was replaced", name)
		}
		if entries, _ := os.ReadDir(filepath.Dir(dest)); len(entries) != 1 {
			t.Errorf("%s: %d files in the folder; the partial download is left behind", name, len(entries))
		}
	}
	for _, status := range []int{500, 503, 404, 403} {
		srv.status.Store(int32(status))
		if _, err := fetchIMDbRatings(t.Context(), srv.Client(), srv.URL, dest, imdbValidators{}); err == nil {
			t.Errorf("status %d: no error", status)
		}
	}
	if onDisk, _ := os.ReadFile(dest); !bytes.Equal(onDisk, good) {
		t.Error("the file we had was replaced by an error")
	}
}

// Only https is asked, except of this machine, and a redirect is followed only to https too.
func TestFetchIMDbRatingsAddresses(t *testing.T) {
	dest := filepath.Join(t.TempDir(), IMDbRatingsFile)
	for _, bad := range []string{"http://datasets.imdbws.com/title.ratings.tsv.gz", "ftp://x/y", "file:///etc/passwd", "://", "https://"} {
		if _, err := fetchIMDbRatings(t.Context(), http.DefaultClient, bad, dest, imdbValidators{}); err == nil {
			t.Errorf("%q was asked", bad)
		}
	}

	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { elsewhere.Add(1) }))
	defer other.Close()
	// A redirect to a plain-http address that is not this machine: refused. (The test server is on
	// 127.0.0.1, so its address is let through; the name below is not.)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://datasets.example.test/title.ratings.tsv.gz", http.StatusFound)
	}))
	defer srv.Close()
	if _, err := fetchIMDbRatings(t.Context(), srv.Client(), srv.URL, dest, imdbValidators{}); err == nil {
		t.Error("a redirect to http on another host was followed")
	}
	if _, err := os.Stat(dest); err == nil {
		t.Error("a file was written")
	}

	// Redirects that loop are cut.
	loop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, r.URL.Path+"x", http.StatusFound) }))
	defer loop.Close()
	if _, err := fetchIMDbRatings(t.Context(), loop.Client(), loop.URL+"/a", dest, imdbValidators{}); err == nil {
		t.Error("a redirect loop was followed to the end")
	}

	// A redirect to this machine (a local mirror) is fine, and the file comes.
	good := gzipOf(t, imdbHead+"tt1\t5.0\t1\n")
	target := newDatasetServer(t, good)
	hop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer hop.Close()
	if _, err := fetchIMDbRatings(t.Context(), hop.Client(), hop.URL, dest, imdbValidators{}); err != nil {
		t.Errorf("a redirect to this machine: %v", err)
	}
	// A server that never answers is let go with the context.
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer hang.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := fetchIMDbRatings(ctx, hang.Client(), hang.URL, dest, imdbValidators{}); err == nil {
		t.Error("no error from a server that never answers")
	}
}

// roundTripFunc is a transport that is not the network: a name that resolves to nothing here can
// answer, so that a request that should not have been made shows.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A redirect to plain http on another host is not followed: the transport of this test would answer
// it, with a good file, were it asked.
func TestFetchIMDbRatingsRefusesARedirectToHTTP(t *testing.T) {
	good := gzipOf(t, imdbHead+"tt1\t5.0\t1\n")
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://mirror.example.test/title.ratings.tsv.gz", http.StatusFound)
	}))
	defer srv.Close()
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "mirror.example.test" {
			asked.Add(1)
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(good)), Request: r}, nil
		}
		return http.DefaultTransport.RoundTrip(r)
	})}
	dest := filepath.Join(t.TempDir(), IMDbRatingsFile)
	if _, err := fetchIMDbRatings(t.Context(), client, srv.URL, dest, imdbValidators{}); err == nil {
		t.Error("a redirect to plain http on another host was followed")
	}
	if asked.Load() != 0 {
		t.Errorf("the other host was asked %d times", asked.Load())
	}
	if _, err := os.Stat(dest); err == nil {
		t.Error("a file was written")
	}
	// Control: the same transport does answer for an https name, so the check above looked at something.
	secure := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://mirror.example.test/title.ratings.tsv.gz", http.StatusFound)
	}))
	defer secure.Close()
	if _, err := fetchIMDbRatings(t.Context(), client, secure.URL, dest, imdbValidators{}); err != nil {
		t.Errorf("a redirect to https: %v", err)
	}
	if asked.Load() != 1 {
		t.Errorf("control: the https host was asked %d times", asked.Load())
	}
}

// What a failed request says does not hold the address's query or the like; and a body larger
// than the cap is refused by its declared length without being read.
func TestFetchIMDbRatingsRefusesAHugeFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(maxIMDbDownload+1))
		w.WriteHeader(200) // and then nothing: the declared length is the reason
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), IMDbRatingsFile)
	_, err := fetchIMDbRatings(t.Context(), srv.Client(), srv.URL, dest, imdbValidators{})
	if err == nil || !strings.Contains(err.Error(), "too much") {
		t.Errorf("err = %v, want a refusal of the size", err)
	}
	_ = io.Discard
}

// A body over the cap is cut off at the cap and refused, whatever it said of its length (here it says
// none), and nothing of it is left in the folder.
func TestFetchIMDbRefusesABodyOverTheCapWhateverItSaid(t *testing.T) {
	maxIMDbDownload = 1 << 20
	t.Cleanup(func() { maxIMDbDownload = 256 << 20 })
	var sent atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := make([]byte, 64<<10)
		for range 128 { // 8 MiB, with no Content-Length
			if _, err := w.Write(chunk); err != nil {
				return
			}
			sent.Add(int64(len(chunk)))
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	dest := filepath.Join(dir, IMDbRatingsFile)
	_, err := fetchIMDbRatings(t.Context(), srv.Client(), srv.URL, dest, imdbValidators{})
	if err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("err = %v, want a refusal of the size", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("%d files left in the folder", len(entries))
	}
	if got := sent.Load(); got > 6<<20 {
		t.Errorf("the server sent %d MiB before it was let go; the cap is 1 MiB", got>>20)
	}
}

const episodesHead = "tconst\tparentTconst\tseasonNumber\tepisodeNumber\n"

func TestParseIMDbEpisodes(t *testing.T) {
	ep := func(id string, season, number int) store.DatasetEpisode {
		return store.DatasetEpisode{IMDbID: id, Season: season, Number: number}
	}
	shows := wanting("tt100", "tt200")
	type kept = map[string][]store.DatasetEpisode
	for _, tc := range []struct {
		name        string
		text        string
		want        kept
		rows, skips int
	}{
		{"header only", episodesHead, kept{}, 0, 0},
		{"the episodes of the shows that are wanted",
			episodesHead + "tt1\ttt100\t1\t1\ntt2\ttt100\t1\t2\ntt3\ttt200\t2\t1\ntt4\ttt300\t1\t1\n",
			kept{"tt100": {ep("tt1", 1, 1), ep("tt2", 1, 2)}, "tt200": {ep("tt3", 2, 1)}}, 4, 0},
		{"\\N for the season or the number is no episode",
			episodesHead + "tt1\ttt100\t\\N\t1\ntt2\ttt100\t1\t\\N\ntt3\ttt100\t\\N\t\\N\ntt4\ttt100\t1\t3\n",
			kept{"tt100": {ep("tt4", 1, 3)}}, 4, 3},
		{"specials (season 0) are left out, episode 0 is not",
			episodesHead + "tt1\ttt100\t0\t1\ntt2\ttt100\t1\t0\ntt3\ttt100\t1\t1\n",
			kept{"tt100": {ep("tt2", 1, 0), ep("tt3", 1, 1)}}, 3, 1},
		{"rows of another shape are skipped",
			episodesHead + "tt1\ttt100\t1\n" + "xx2\ttt100\t1\t1\n" + "tt3\tzz100\t1\t1\n" + "tt4\ttt100\tone\t1\n" +
				"tt5\ttt100\t-1\t1\n" + "tt6\ttt100\t1\t-2\n" + "tt\ttt100\t1\t1\n" + "tt7\ttt100\t2\t5\n",
			kept{"tt100": {ep("tt7", 2, 5)}}, 8, 7},
		{"a bad row of a show that is not wanted is not counted", episodesHead + "tt1\ttt300\t\\N\t1\n", kept{}, 1, 0},
		{"columns after the four", "tconst\tparentTconst\tseasonNumber\tepisodeNumber\tgenre\ntt1\ttt100\t1\t2\tdrama\n",
			kept{"tt100": {ep("tt1", 1, 2)}}, 1, 0},
		{"CRLF, and no newline at the end", "tconst\tparentTconst\tseasonNumber\tepisodeNumber\r\ntt1\ttt100\t1\t1\r\ntt2\ttt100\t1\t2",
			kept{"tt100": {ep("tt1", 1, 1), ep("tt2", 1, 2)}}, 2, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, stats, err := ParseIMDbEpisodes(t.Context(), bytes.NewReader(gzipOf(t, tc.text)), shows, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("kept = %v, want %v", got, tc.want)
			}
			n := 0
			for _, eps := range tc.want {
				n += len(eps)
			}
			if stats.Rows != tc.rows || stats.Skipped != tc.skips || stats.Kept != n {
				t.Errorf("stats = %+v, want %d rows, %d skipped, %d kept", stats, tc.rows, tc.skips, n)
			}
		})
	}

	// What is not the episode dataset is refused: the ratings' header (a file given for the wrong
	// address), a change of columns, a file that is cut short.
	for name, body := range map[string][]byte{
		"the ratings' header": gzipOf(t, imdbHead+"tt1\t5.0\t1\n"),
		"a column cut":        gzipOf(t, "tconst\tparentTconst\tseasonNumber\ntt1\ttt100\t1\n"),
		"not a gzip":          []byte(episodesHead),
		"empty":               nil,
	} {
		if got, _, err := ParseIMDbEpisodes(t.Context(), bytes.NewReader(body), shows, nil); !errors.Is(err, ErrIMDbFormat) || got != nil {
			t.Errorf("%s: kept %v, err %v; want ErrIMDbFormat and nothing", name, got, err)
		}
	}
	whole := gzipOf(t, episodesHead+strings.Repeat("tt1\ttt100\t1\t1\n", 5000))
	if _, _, err := ParseIMDbEpisodes(t.Context(), bytes.NewReader(whole[:len(whole)/2]), shows, nil); err == nil {
		t.Error("a file cut short was read as the whole")
	}
	if _, _, err := ParseIMDbEpisodes(t.Context(), bytes.NewReader(whole), shows, nil); err != nil {
		t.Errorf("control: the whole file = %v", err)
	}

	// It yields as the rows go by, like the ratings.
	var sb strings.Builder
	sb.WriteString(episodesHead)
	for i := range 2*imdbYieldEvery + 5 {
		fmt.Fprintf(&sb, "tt%d\ttt1\t1\t%d\n", i, i)
	}
	calls := 0
	if _, _, err := ParseIMDbEpisodes(t.Context(), bytes.NewReader(gzipOf(t, sb.String())), func([]byte) bool { return false },
		func(context.Context) error { calls++; return nil }); err != nil || calls != 2 {
		t.Errorf("yield called %d times, err %v; want 2", calls, err)
	}
}

// A download is read through, whole, before it is kept: the file we have is replaced only by a dataset
// that has the header, and as many rows as it should have (a mirror that is cut off, or an empty file,
// is not the dataset). Nothing of it is left in the folder.
func TestFetchIMDbChecksTheDownloadBeforeKeepingIt(t *testing.T) {
	good := gzipOf(t, imdbHead+"tt1\t5.0\t1\ntt2\t6.0\t2\ntt3\t7.0\t3\ntt4\t8.0\t4\n")
	srv := newDatasetServer(t, good)
	dest := filepath.Join(t.TempDir(), IMDbRatingsFile)
	got, err := fetchIMDb(t.Context(), srv.Client(), srv.URL, dest, imdbRatingsHeader, 0, imdbValidators{})
	if err != nil || got.Rows != 4 {
		t.Fatalf("first fetch = %+v, %v; want 4 rows", got, err)
	}

	for _, tc := range []struct {
		name    string
		body    []byte
		minRows int
		want    error
	}{
		{"a header that is not the dataset's", gzipOf(t, "id\trating\tvotes\ntt1\t5.0\t1\ntt2\t5.0\t1\n"), 2, ErrIMDbFormat},
		{"a header with a column cut", gzipOf(t, "tconst\taverageRating\ntt1\t5.0\ntt2\t5.0\n"), 2, ErrIMDbFormat},
		{"the other dataset", gzipOf(t, episodesHead+"tt1\ttt9\t1\t1\ntt2\ttt9\t1\t2\n"), 2, ErrIMDbFormat},
		{"a header and no rows", gzipOf(t, imdbHead), 2, ErrIMDbShort},
		{"a header and no rows, with nothing to compare to", gzipOf(t, imdbHead), 0, ErrIMDbShort},
		{"a blank line is not a row", gzipOf(t, imdbHead+"\n\n"), 0, ErrIMDbShort},
		{"less rows than it should have", gzipOf(t, imdbHead+"tt1\t5.0\t1\n"), 2, ErrIMDbShort},
	} {
		srv.body.Store(&tc.body)
		_, err := fetchIMDb(t.Context(), srv.Client(), srv.URL, dest, imdbRatingsHeader, tc.minRows, imdbValidators{})
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
		if onDisk, _ := os.ReadFile(dest); !bytes.Equal(onDisk, good) {
			t.Errorf("%s: the copy we had was replaced", tc.name)
		}
		if entries, _ := os.ReadDir(filepath.Dir(dest)); len(entries) != 1 {
			t.Errorf("%s: %d files in the folder; the refused download is left behind", tc.name, len(entries))
		}
	}

	// Control: a file with just the rows it should have, and an extra column in the header, is kept.
	newer := gzipOf(t, "tconst\taverageRating\tnumVotes\tgenre\ntt1\t5.0\t1\td\ntt2\t6.0\t2\td\n")
	srv.body.Store(&newer)
	if got, err := fetchIMDb(t.Context(), srv.Client(), srv.URL, dest, imdbRatingsHeader, 2, imdbValidators{}); err != nil || got.Rows != 2 {
		t.Fatalf("control: %+v, %v", got, err)
	}
	if onDisk, _ := os.ReadFile(dest); !bytes.Equal(onDisk, newer) {
		t.Error("control: the file was not replaced")
	}
}

// The part files that a download cut off left in the folder are removed when the next one starts, the
// young ones (a download in progress) and other files are not.
func TestFetchIMDbSweepsTheLeftoversOfACutOffDownload(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, IMDbRatingsFile)
	old := time.Now().Add(-3 * time.Hour)
	for name, mtime := range map[string]time.Time{
		".ratings-111.part": old, ".ratings-222.part": old, ".ratings-333.part": time.Now(), "notes.part": old, "other.tsv.gz": old,
	} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	srv := newDatasetServer(t, gzipOf(t, imdbHead+"tt1\t5.0\t1\n"))
	if _, err := fetchIMDbRatings(t.Context(), srv.Client(), srv.URL, dest, imdbValidators{}); err != nil {
		t.Fatal(err)
	}
	var left []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		left = append(left, e.Name())
	}
	if want := []string{".ratings-333.part", "notes.part", "other.tsv.gz", IMDbRatingsFile}; !slices.Equal(left, want) {
		t.Errorf("the folder has %v, want %v", left, want)
	}
}

// A gzip that unzips to more than a dataset can is refused, in a parse and in a download, whatever
// it was said to weigh.
func TestIMDbRefusesAGzipThatUnzipsToTooMuch(t *testing.T) {
	maxIMDbExpanded = 1 << 20
	t.Cleanup(func() { maxIMDbExpanded = 3072 << 20 })
	all := func([]byte) bool { return true }
	big := gzipOf(t, imdbHead+strings.Repeat("tt1\t5.0\t1\n", 150_000)) // 1.6 MB unzipped, a few KB gzipped
	small := gzipOf(t, imdbHead+strings.Repeat("tt1\t5.0\t1\n", 10_000))

	if _, _, err := ParseIMDbRatings(t.Context(), bytes.NewReader(small), all, nil); err != nil { // control
		t.Fatalf("control: %v", err)
	}
	if _, _, err := ParseIMDbRatings(t.Context(), bytes.NewReader(big), all, nil); !errors.Is(err, ErrIMDbFormat) {
		t.Errorf("a parse of too much: %v, want ErrIMDbFormat", err)
	}

	srv := newDatasetServer(t, small)
	dest := filepath.Join(t.TempDir(), IMDbRatingsFile)
	if _, err := fetchIMDbRatings(t.Context(), srv.Client(), srv.URL, dest, imdbValidators{}); err != nil {
		t.Fatalf("control: %v", err)
	}
	srv.body.Store(&big)
	if _, err := fetchIMDbRatings(t.Context(), srv.Client(), srv.URL, dest, imdbValidators{}); !errors.Is(err, ErrIMDbFormat) {
		t.Errorf("a download of too much: %v, want ErrIMDbFormat", err)
	}
	if onDisk, _ := os.ReadFile(dest); !bytes.Equal(onDisk, small) {
		t.Error("the copy we had was replaced")
	}
}
