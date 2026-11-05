package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ply/internal/art"
	"ply/internal/stream"
	"ply/internal/tier"
)

// ---- subtitles: no whole-file read on the play path ---------------------------

// readExactly reads len(want) bytes from body, within a few seconds, and compares.
func readExactly(t *testing.T, body io.Reader, want string) {
	t.Helper()
	got := make(chan string, 1)
	go func() {
		buf := make([]byte, len(want))
		n, _ := io.ReadFull(body, buf)
		got <- string(buf[:n])
	}()
	select {
	case g := <-got:
		if g != want {
			t.Fatalf("read %q, want %q", g, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %q", want)
	}
}

// secondCue is what the slow fake writes after it is let go; halfCue is a failing one's.
const (
	secondCue = "00:03.000 --> 00:04.000\ntwo\n\n"
	halfCue   = "00:03.000 --> 00:04.000\ntw"
)

// slowSubs gives the env a subtitle converter whose ffmpeg writes the first cue at
// once and, only when the test creates the .go file (a big container read over a slow
// share), the rest (tail, as printf takes it), then ends with exit status end. It
// returns the argument log and the file that lets it go on; the fake is released when
// the test ends, whatever happens.
func (e *env) slowSubs(tail string, end int) (log, release string) {
	e.t.Helper()
	log = e.withSubs(fmt.Sprintf(`
echo "$@" >> "$0.log"
for a in "$@"; do last=$a; done
printf 'WEBVTT\n\n00:01.000 --> 00:02.000\none\n\n' > "$last"
while [ ! -e "$0.go" ]; do sleep 0.02; done
printf '%s' >> "$last"
exit %d
`, strings.ReplaceAll(tail, "\n", `\n`), end))
	release = strings.TrimSuffix(log, ".log") + ".go"
	e.srv.subsGrace = 50 * time.Millisecond
	return log, release
}

// httpClient gives up, so that a server that does not answer fails a test instead of hanging it.
var httpClient = &http.Client{Timeout: 20 * time.Second}

func (e *env) liveServer() *httptest.Server {
	ts := httptest.NewServer(e.srv)
	e.t.Cleanup(ts.Close)
	return ts
}

// An embedded track that is not extracted yet is not waited for: past a short grace the
// answer begins, and the cues come as ffmpeg writes them, until the file is complete;
// then it is cached, and the next request is a plain file.
func TestEmbeddedSubtitleIsStreamedWhileItIsExtracted(t *testing.T) {
	e := newEnv(t, "")
	log, release := e.slowSubs(secondCue, 0)
	ts := e.liveServer()
	t.Cleanup(func() { os.WriteFile(release, nil, 0o644) }) // runs before ts.Close: a failing test must not hang on the fake
	url := ts.URL + fmt.Sprintf("/api/items/%d/subtitles/3.vtt", e.mkv)

	resp, err := httpClient.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	for h, want := range map[string]string{
		"Content-Type": "text/vtt; charset=utf-8", "Cache-Control": "no-store", "Access-Control-Allow-Origin": "*",
	} {
		if got := resp.Header.Get(h); got != want {
			t.Errorf("%s = %q, want %q", h, got, want)
		}
	}
	if resp.ContentLength != -1 || len(resp.TransferEncoding) != 1 || resp.TransferEncoding[0] != "chunked" {
		t.Errorf("length %d, transfer encoding %v: the answer is not chunked", resp.ContentLength, resp.TransferEncoding)
	}

	// The first cue is here while ffmpeg is still reading: it has not been let go on.
	const first = "WEBVTT\n\n00:01.000 --> 00:02.000\none\n\n"
	readExactly(t, resp.Body, first)
	if _, err := os.Stat(release); err == nil {
		t.Fatal("setup: the fake was released already")
	}

	os.WriteFile(release, nil, 0o644)
	rest, err := io.ReadAll(resp.Body)
	if err != nil || string(rest) != secondCue {
		t.Errorf("rest = %q, %v", rest, err)
	}

	// Cached now: a plain, sized file, and ffmpeg does not run again.
	again, err := httpClient.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Body.Close()
	body, _ := io.ReadAll(again.Body)
	if again.StatusCode != 200 || again.ContentLength != int64(len(first)+len(secondCue)) || string(body) != first+secondCue {
		t.Errorf("second request: %d, length %d, body %q", again.StatusCode, again.ContentLength, body)
	}
	if again.Header.Get("Cache-Control") == "no-store" {
		t.Error("the complete file is served as no-store")
	}
	if n := strings.Count(string(mustRead(t, log)), "\n"); n != 1 {
		t.Errorf("ffmpeg ran %d times, want 1", n)
	}
}

// If the extraction fails after the answer began, the cues so far stay, the answer ends, and
// the next request is told (422); ffmpeg is not run again for it.
func TestEmbeddedSubtitleFailingMidwayEndsTheAnswer(t *testing.T) {
	e := newEnv(t, "")
	log, release := e.slowSubs(halfCue, 1)
	ts := e.liveServer()
	t.Cleanup(func() { os.WriteFile(release, nil, 0o644) })
	url := ts.URL + fmt.Sprintf("/api/items/%d/subtitles/3.vtt", e.mkv)

	resp, err := httpClient.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 { // positive control: it began as a good answer
		t.Fatalf("status = %d", resp.StatusCode)
	}
	const first = "WEBVTT\n\n00:01.000 --> 00:02.000\none\n\n"
	readExactly(t, resp.Body, first)
	os.WriteFile(release, nil, 0o644)
	// ffmpeg wrote half a cue before it failed: what was said stays valid WebVTT, cut at a cue.
	if rest, err := io.ReadAll(resp.Body); len(rest) != 0 {
		t.Errorf("rest = %q, %v; want the answer to end after the last whole cue", rest, err)
	}

	again, err := httpClient.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Body.Close()
	if again.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("after the failure: %d, want 422", again.StatusCode)
	}
	if n := strings.Count(string(mustRead(t, log)), "\n"); n != 1 {
		t.Errorf("ffmpeg ran %d times, want 1 (the failure is remembered)", n)
	}
}

// The text tracks of an item and its sidecars are what pre-extraction asks for; images
// are not among them.
func TestSubtitleSourcesOfAnItem(t *testing.T) {
	e := newEnv(t, "")
	e.withSubs(fakeSubsScript)
	ctx := context.Background()

	mkv, err := e.store.GetEntry(ctx, e.mkv)
	if err != nil {
		t.Fatal(err)
	}
	got := EmbeddedSubtitles(mkv)
	if len(got) != 1 || got[0].Key != "3" || got[0].Stream != 3 || got[0].Input != mkv.Path || got[0].MTime != mkv.MTime || got[0].ItemID != e.mkv {
		t.Errorf("embedded text tracks of the mkv = %+v, want stream 3 only (2 is a PGS)", got)
	}
	if all := SubtitleSources(mkv); len(all) != 1 {
		t.Errorf("SubtitleSources(mkv) = %+v", all)
	}

	mp4, err := e.store.GetEntry(ctx, e.mp4)
	if err != nil {
		t.Fatal(err)
	}
	all := SubtitleSources(mp4)
	if len(all) != 2 || all[0].Key != "ext0" || all[1].Key != "ext1" || all[0].Stream >= 0 {
		t.Errorf("SubtitleSources(mp4) = %+v, want its two sidecars", all)
	}
	if len(EmbeddedSubtitles(mp4)) != 0 {
		t.Errorf("EmbeddedSubtitles(mp4) = %+v", EmbeddedSubtitles(mp4))
	}
}

// Extracted ahead of time (the queue's job), a track is served from the cache with no
// ffmpeg at all, and without the wait.
func TestPreExtractedSubtitlesAreServedWithoutFFmpeg(t *testing.T) {
	e := newEnv(t, "")
	log := e.withSubs(fakeSubsScript)
	entry, err := e.store.GetEntry(context.Background(), e.mkv)
	if err != nil {
		t.Fatal(err)
	}
	srcs := SubtitleSources(entry)

	// Positive control: before the extraction the cache is empty and a request runs ffmpeg.
	if e.srv.Subs.Cached(srcs[0]) {
		t.Fatal("setup: cached before anything ran")
	}
	if err := e.srv.Subs.Fill(context.Background(), srcs, nil, nil); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(mustRead(t, log)), "\n"); n != 1 {
		t.Fatalf("pre-extraction ran ffmpeg %d times, want 1", n)
	}

	rec := e.get(fmt.Sprintf("/api/items/%d/subtitles/3.vtt", e.mkv))
	wantStatus(t, rec, 200)
	if !strings.HasPrefix(rec.Body.String(), "WEBVTT") || rec.Header().Get("Content-Length") == "" {
		t.Errorf("served %q (length %q)", rec.Body.String(), rec.Header().Get("Content-Length"))
	}
	if n := strings.Count(string(mustRead(t, log)), "\n"); n != 1 {
		t.Errorf("the request after pre-extraction ran ffmpeg (%d runs in all)", n)
	}
}

// ---- the frame grab waits while a play is being set up ------------------------

// grabs records what the API leaves for later.
type grabs struct {
	mu    sync.Mutex
	got   []art.Grab
	again []art.Grab // of them, the ones asked for because a grab under way was stopped
}

func (g *grabs) add(src art.Source, k art.Kind, again bool) {
	g.mu.Lock()
	g.got = append(g.got, art.Grab{Src: src, Kind: k})
	if again {
		g.again = append(g.again, art.Grab{Src: src, Kind: k})
	}
	g.mu.Unlock()
}

func (g *grabs) list() []art.Grab {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]art.Grab(nil), g.got...)
}

func (g *grabs) stopped() []art.Grab {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]art.Grab(nil), g.again...)
}

func (e *env) play(id int64) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do("POST", fmt.Sprintf("/api/items/%d/playback", id), map[string]string{"Content-Type": "application/json"}, `{}`)
}

func artPath(id int64, kind string) string { return fmt.Sprintf("/api/items/%d/art/%s", id, kind) }

// While a play of an item is being set up, and for artHoldFor after, no frame is grabbed
// from its file: the answer is the placeholder (503 with a Retry-After), and the grab is
// left for the queue. Other items are not held, and the hold ends.
func TestArtGrabWaitsWhileAPlayStarts(t *testing.T) {
	e := newEnv(t, "")
	gen := e.withArt(fakeArtScript)
	later := &grabs{}
	e.srv.ArtLater = later.add
	now := time.Unix(1_700_000_000, 0)
	e.srv.Now = func() time.Time { return now }

	// Positive control: with no play, the grab runs at once and nothing is left for later.
	wantStatus(t, e.get(artPath(e.mkv, "thumb")), 200)
	if gen.Runs() != 1 || len(later.list()) != 0 {
		t.Fatalf("no play: %d runs, %d left for later; want 1 run and none", gen.Runs(), len(later.list()))
	}

	wantStatus(t, e.play(e.mp4), 200)
	rec := e.get(artPath(e.mp4, "thumb"))
	wantJSONError(t, rec, 503)
	if got, _ := strconv.Atoi(rec.Header().Get("Retry-After")); got < 1 || got > int(artHoldFor/time.Second) {
		t.Errorf("Retry-After = %q, want 1..%d", rec.Header().Get("Retry-After"), int(artHoldFor/time.Second))
	}
	if gen.Runs() != 1 {
		t.Errorf("a grab ran for the item being played (%d runs)", gen.Runs())
	}
	got := later.list()
	if len(got) != 1 || got[0].Src.ID != e.mp4 || got[0].Kind != art.Thumb {
		t.Errorf("left for later: %+v, want the thumb of the item being played", got)
	}

	// Another item is not held.
	wantStatus(t, e.get(artPath(e.mkv, "backdrop")), 200)
	if gen.Runs() != 2 {
		t.Errorf("another item's grab did not run (%d runs)", gen.Runs())
	}

	// The hold lasts artHoldFor from the start, and no longer; a Retry-After is never 0 ("now").
	now = now.Add(artHoldFor - 400*time.Millisecond)
	rec = e.get(artPath(e.mp4, "thumb"))
	wantJSONError(t, rec, 503)
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After with 400 ms of the hold left = %q, want 1", got)
	}
	if len(later.stopped()) != 0 {
		t.Errorf("a request was passed on as a grab that had been stopped: %+v", later.stopped())
	}
	now = now.Add(2 * time.Second)
	wantStatus(t, e.get(artPath(e.mp4, "thumb")), 200)
	if gen.Runs() != 3 {
		t.Errorf("after the hold: %d runs, want 3", gen.Runs())
	}
}

// An image that is made already costs the play nothing: it is served during the hold.
func TestArtThatIsMadeIsServedDuringTheHold(t *testing.T) {
	e := newEnv(t, "")
	gen := e.withArt(fakeArtScript)
	wantStatus(t, e.get(artPath(e.mp4, "thumb")), 200)
	runs := gen.Runs()
	if runs != 1 {
		t.Fatalf("setup: %d runs", runs)
	}
	wantStatus(t, e.play(e.mp4), 200)
	rec := e.get(artPath(e.mp4, "thumb"))
	wantStatus(t, rec, 200)
	if !strings.HasPrefix(rec.Body.String(), "\xff\xd8") || gen.Runs() != runs {
		t.Errorf("body %q, runs %d -> %d", rec.Body.String(), runs, gen.Runs())
	}
	// ...but the other image, not made yet, waits.
	wantJSONError(t, e.get(artPath(e.mp4, "backdrop")), 503)
}

// While the session is being created the grab waits, however long that takes; if no
// session comes of it, nothing is held afterwards.
func TestArtGrabWaitsWhileTheSessionIsBeingCreated(t *testing.T) {
	e := newEnv(t, "")
	gen := e.withArt(fakeArtScript)
	later := &grabs{}
	e.srv.ArtLater = later.add
	now := time.Unix(1_700_000_000, 0)
	e.srv.Now = func() time.Time { return now }

	end := e.srv.beginPlay(e.mp4)
	now = now.Add(5 * time.Minute) // a keyframe scan over a slow share
	wantJSONError(t, e.get(artPath(e.mp4, "thumb")), 503)
	end(false) // the play did not start (a limit, a missing file)
	wantStatus(t, e.get(artPath(e.mp4, "thumb")), 200)
	if gen.Runs() != 1 || len(later.list()) != 1 {
		t.Errorf("%d runs, %d left for later; want 1 and 1", gen.Runs(), len(later.list()))
	}
}

// A grab that is already reading the file when the play begins is stopped, so that the
// play has the file to itself, and is left for later.
func TestArtGrabUnderWayIsStoppedWhenAPlayBegins(t *testing.T) {
	e := newEnv(t, "")
	release := filepath.Join(t.TempDir(), "go")
	t.Cleanup(func() { os.WriteFile(release, nil, 0o644) })
	gen := e.withArt(`
case "$*" in *-filters*) exit 0;; esac
while [ ! -e "` + release + `" ]; do sleep 0.02; done
` + fakeArtScript)
	t.Cleanup(gen.Close) // kills a grab a failing test leaves running (before the release above is written)
	later := &grabs{}
	e.srv.ArtLater = later.add

	answered := make(chan *httptest.ResponseRecorder, 1)
	go func() { answered <- e.get(artPath(e.mp4, "thumb")) }()
	waitFor(t, "the grab to start", func() bool { return gen.Runs() == 1 })
	select {
	case rec := <-answered: // positive control: a grab that nobody stops goes on holding its request
		t.Fatalf("the request was answered before the play began: %d", rec.Code)
	case <-time.After(200 * time.Millisecond):
	}

	wantStatus(t, e.play(e.mp4), 200)
	select {
	case rec := <-answered:
		wantJSONError(t, rec, 503)
	case <-time.After(5 * time.Second):
		t.Fatal("the grab was not stopped by the play")
	}
	got := later.list()
	if len(got) != 1 || got[0].Src.ID != e.mp4 || got[0].Kind != art.Thumb {
		t.Errorf("left for later: %+v", got)
	}
	// ...as a grab that was stopped: the queue must not take it for one it has been asked for already.
	if s := later.stopped(); len(s) != 1 || s[0].Src.ID != e.mp4 {
		t.Errorf("left for later as stopped: %+v, want the same grab", s)
	}
}

// fakePlays is the plays the governor sees, set by a test.
type fakePlays struct {
	mu    sync.Mutex
	paths []string
}

func (f *fakePlays) PlaybackLoad() tier.PlaybackLoad {
	f.mu.Lock()
	defer f.mu.Unlock()
	return tier.PlaybackLoad{Sessions: len(f.paths), Active: len(f.paths), Paths: append([]string(nil), f.paths...)}
}

// A film playing from a disk keeps frame grabs of every item on that disk from it (the grid of
// a library page asks for a dozen at a time), not only the one being played: they wait for the
// queue, which asks the same question. Another disk is free, a person browsing does not hold
// them, and an image that is made is served. The question is about the item's library folder:
// the governor is not made to look up the disk of every film a page shows.
func TestArtGrabsWaitWhileAFilmPlaysFromTheSameDisk(t *testing.T) {
	e := newEnv(t, "")
	gen := e.withArt(fakeArtScript)
	later := &grabs{}
	e.srv.ArtLater = later.add
	plays := &fakePlays{}
	var mu sync.Mutex
	var looked []string // what the governor was made to look up
	e.srv.Governor = tier.New(tier.Options{Playback: plays, Disk: func(p string) (string, bool) {
		mu.Lock()
		looked = append(looked, p)
		mu.Unlock()
		if strings.HasPrefix(p, filepath.Join(e.dir, "Shows")) {
			return "other disk", true
		}
		return "the nas", true
	}})
	mp4, err := e.store.GetEntry(context.Background(), e.mp4)
	if err != nil {
		t.Fatal(err)
	}
	mkv, err := e.store.GetEntry(context.Background(), e.mkv)
	if err != nil {
		t.Fatal(err)
	}
	multi, err := e.store.GetEntry(context.Background(), e.multi)
	if err != nil {
		t.Fatal(err)
	}
	// The shows are in another library folder, on another disk.
	shows := filepath.Join(e.dir, "Shows")
	if err := e.store.SetItemRoot(context.Background(), e.mkv, shows); err != nil {
		t.Fatal(err)
	}

	wantStatus(t, e.get(artPath(e.mp4, "thumb")), 200) // nothing plays: grabbed at once
	if gen.Runs() != 1 || len(later.list()) != 0 {
		t.Fatalf("nothing playing: %d runs, %d left for later; want 1 and none", gen.Runs(), len(later.list()))
	}

	plays.mu.Lock()
	plays.paths = []string{mp4.Path}
	plays.mu.Unlock()
	rec := e.get(artPath(e.multi, "thumb")) // another film on the same disk
	wantJSONError(t, rec, 503)
	if rec.Header().Get("Retry-After") != "30" || gen.Runs() != 1 {
		t.Errorf("Retry-After %q, %d runs; want 30 and no new grab", rec.Header().Get("Retry-After"), gen.Runs())
	}
	if got := later.list(); len(got) != 1 || got[0].Src.ID != e.multi || got[0].Kind != art.Thumb {
		t.Errorf("left for later: %+v, want the thumb of the other film", got)
	}
	if len(later.stopped()) != 0 {
		t.Errorf("a request was passed on as a grab that had been stopped: %+v", later.stopped())
	}
	wantStatus(t, e.get(artPath(e.mp4, "thumb")), 200)    // made already: served
	wantStatus(t, e.get(artPath(e.mkv, "backdrop")), 200) // the other disk
	if gen.Runs() != 2 {
		t.Errorf("%d runs, want the other disk's grab only", gen.Runs())
	}
	mu.Lock()
	asked := slices.Clone(looked)
	mu.Unlock()
	if !slices.Contains(asked, e.dir) || !slices.Contains(asked, shows) { // positive control: the folders were asked
		t.Errorf("the governor was asked about %v, want the library folders %q and %q", asked, e.dir, shows)
	}
	if slices.Contains(asked, multi.Path) || slices.Contains(asked, mkv.Path) {
		t.Errorf("the governor was made to look up the disk of a film's file: %v", asked)
	}

	// Someone browsing, with nothing playing: not a reason to wait (browsing is what asks).
	plays.mu.Lock()
	plays.paths = nil
	plays.mu.Unlock()
	browse := e.srv.Governor.Track(func(*http.Request) bool { return true })(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	browse.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/items", nil))
	if h := e.srv.Governor.Check(tier.Need{Disks: []string{mp4.Path}}); !h.Held || !h.Browsing {
		t.Fatalf("setup: browsing does not hold the governor's check: %+v", h)
	}
	wantStatus(t, e.get(artPath(e.multi, "backdrop")), 200)
}

// A play that was refused (the limit on plays) never started: the frame grab of its item is not
// held for it.
func TestArtGrabIsNotHeldForARefusedPlay(t *testing.T) {
	e := newEnv(t, "")
	gen := e.withArt(fakeArtScript)
	e.withStreams(fakeFFmpeg(t, "exec sleep 60"), func(o *stream.Options) { o.MaxSessions = 1 })

	wantStatus(t, e.play(e.mkv), 200)   // takes the only place
	wantStatus(t, e.play(e.multi), 503) // "1 of 1 plays in use"

	wantStatus(t, e.get(artPath(e.multi, "thumb")), 200) // the refused play holds nothing
	if gen.Runs() != 1 {
		t.Fatalf("%d runs, want the refused item's grab", gen.Runs())
	}
	wantJSONError(t, e.get(artPath(e.mkv, "thumb")), 503) // positive control: the play that started does
}

// ---- subtitles: what a request is told when the extraction cannot go on --------

// A HEAD asks about a track, it does not start the read of a whole film for it.
func TestHeadOfAnEmbeddedTrackDoesNotStartARun(t *testing.T) {
	e := newEnv(t, "")
	log := e.withSubs(fakeSubsScript)
	url := fmt.Sprintf("/api/items/%d/subtitles/3.vtt", e.mkv)

	rec := e.do("HEAD", url, nil, "")
	wantStatus(t, rec, 200)
	if ct := rec.Header().Get("Content-Type"); ct != "text/vtt; charset=utf-8" || rec.Body.Len() != 0 {
		t.Errorf("HEAD: Content-Type %q, body %q", ct, rec.Body.String())
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatal("a HEAD of a track that is not extracted started ffmpeg")
	}

	// Positive control: a GET does, and then a HEAD is the file's.
	wantStatus(t, e.get(url), 200)
	head := e.do("HEAD", url, nil, "")
	wantStatus(t, head, 200)
	if head.Header().Get("Content-Length") == "" {
		t.Errorf("HEAD of a cached track has no Content-Length: %v", head.Header())
	}
}

// A file that cannot be read (a share that is not mounted yet) is a 503 to try again, not a 422 that
// says the subtitle is bad.
func TestSubtitleOfAnUnreachableFileIsA503(t *testing.T) {
	e := newEnv(t, "")
	e.withSubs("echo 'No such file or directory' >&2; exit 1")
	entry, err := e.store.GetEntry(context.Background(), e.mkv)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(entry.Path); err != nil {
		t.Fatal(err)
	}
	rec := e.get(fmt.Sprintf("/api/items/%d/subtitles/3.vtt", e.mkv))
	wantJSONError(t, rec, 503)
	if rec.Header().Get("Retry-After") != "30" {
		t.Errorf("Retry-After = %q, want 30", rec.Header().Get("Retry-After"))
	}

	// Positive control: with the file there, the same failure is the subtitle's (422).
	if err := os.WriteFile(entry.Path, []byte(fileContent), 0o644); err != nil {
		t.Fatal(err)
	}
	wantJSONError(t, e.get(fmt.Sprintf("/api/items/%d/subtitles/3.vtt", e.mkv)), 422)
}

// An extraction that is stopped by someone else while a request waits for it (a job cancelled in
// Activity, ply stopping) is a 503 to try again: the client is still there, and is not told that
// all is well with an empty answer.
func TestSubtitleStoppedByOthersIsA503(t *testing.T) {
	e := newEnv(t, "")
	_, release := e.slowSubs(secondCue, 0)
	t.Cleanup(func() { os.WriteFile(release, nil, 0o644) })
	e.srv.subsGrace = 30 * time.Second // the request waits for the extraction to end
	answered := make(chan *httptest.ResponseRecorder, 1)
	go func() { answered <- e.get(fmt.Sprintf("/api/items/%d/subtitles/3.vtt", e.mkv)) }()
	select {
	case rec := <-answered:
		t.Fatalf("setup: answered before the extraction was stopped: %d", rec.Code)
	case <-time.After(300 * time.Millisecond):
	}

	e.srv.Subs.Close()
	select {
	case rec := <-answered:
		wantJSONError(t, rec, 503)
		if rec.Header().Get("Retry-After") != "5" {
			t.Errorf("Retry-After = %q, want 5", rec.Header().Get("Retry-After"))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the request was not answered after the extraction was stopped")
	}
}

func TestPlayStartsHold(t *testing.T) {
	var p playStarts
	t0 := time.Unix(1_700_000_000, 0)
	clock := func() time.Time { return t0 }
	if _, held := p.holding(1, t0); held {
		t.Fatal("held with no play")
	}
	a := p.begin(1, clock)
	b := p.begin(1, clock) // two plays of one item at once
	if _, held := p.holding(1, t0); !held {
		t.Fatal("not held while a play is being set up")
	}
	if _, held := p.holding(2, t0); held {
		t.Fatal("another item is held")
	}
	a(false)
	if _, held := p.holding(1, t0); !held {
		t.Fatal("released while the other play is still being set up")
	}
	b(true)
	left, held := p.holding(1, t0.Add(artHoldFor/2))
	if !held || left != artHoldFor/2 {
		t.Errorf("after a start: held %v, %v left; want %v", held, left, artHoldFor/2)
	}
	if _, held := p.holding(1, t0.Add(artHoldFor)); held {
		t.Error("still held when the time is up")
	}
}
