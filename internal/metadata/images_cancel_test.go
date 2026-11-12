package metadata

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A fast scroll asks for hundreds of images and the browser gives up on most of
// them a frame later. The image cache stops the work of a request whose client is
// gone (queued for a slot or downloading), unless another client, or a background
// job, still waits for the same image.

// holdingServer is an image server whose answers wait for release. started gets
// the path of each request that arrives, gone the path of each one the client
// abandoned: the server saw the connection close, which is how a cancelled
// context looks from there.
type holdingServer struct {
	*imageServer
	started, gone chan string
	release       chan struct{}
	once          sync.Once
}

func newHoldingServer(t *testing.T) *holdingServer {
	t.Helper()
	h := &holdingServer{started: make(chan string, 1024), gone: make(chan string, 1024), release: make(chan struct{})}
	h.imageServer = newImageServer(t, func(w http.ResponseWriter, r *http.Request) {
		h.started <- r.URL.Path
		select {
		case <-h.release:
			w.Write(jpegBytes)
		case <-r.Context().Done():
			h.gone <- r.URL.Path
		}
	})
	t.Cleanup(h.open)
	return h
}

// open lets every answer, now and later, go out.
func (h *holdingServer) open() { h.once.Do(func() { close(h.release) }) }

func recv(t *testing.T, ch <-chan string, what string) string {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("no %s within 5s", what)
	}
	return ""
}

// silent fails if anything arrives on ch within d.
func silent(t *testing.T, ch <-chan string, d time.Duration, what string) {
	t.Helper()
	select {
	case v := <-ch:
		t.Fatalf("unexpected %s: %s", what, v)
	case <-time.After(d):
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// waiters is how many callers wait for the run of key (-1: no run).
func (c *ImageCache) waiters(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if call, ok := c.inflight[key]; ok {
		return call.refs.Waiters()
	}
	return -1
}

func (c *ImageCache) running() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.inflight)
}

// abandoned reports whether the run of key is under way and nobody waits for it.
func (c *ImageCache) abandoned(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	call, ok := c.inflight[key]
	return ok && call.refs.Abandoned()
}

func (c *ImageCache) remembered() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.failed)
}

// The last client to leave cancels the download: the image server sees the
// request given up, nothing is left on disk, and the image is not remembered as a
// failure (it is fetched when asked for again).
func TestImageLastWaiterLeavingCancelsTheDownload(t *testing.T) {
	up := newHoldingServer(t)
	c := newCache(t, up.imageServer)
	ctx, leave := context.WithCancel(t.Context())
	errc := make(chan error, 1)
	go func() { _, err := c.TMDB(ctx, "w342", "gone.jpg"); errc <- err }()
	recv(t, up.started, "request at the image server") // positive control: the download is under way
	silent(t, up.gone, 50*time.Millisecond, "abandoned request")

	leave()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Errorf("the client that left: %v", err)
	}
	recv(t, up.gone, "download cancelled at the image server")
	waitFor(t, "the run to end", func() bool { return c.running() == 0 })
	if files := filesUnder(t, c.dir); len(files) != 0 {
		t.Errorf("files after a cancelled download: %q", files)
	}

	up.open()
	if _, err := c.TMDB(t.Context(), "w342", "gone.jpg"); err != nil {
		t.Fatalf("asked again: %v (a cancelled download is not a failure to remember)", err)
	}
	if up.hits.Load() != 2 {
		t.Errorf("%d requests to the image server, want 2 (the cancelled one and the new one)", up.hits.Load())
	}
}

// With two clients waiting for one image, one leaving changes nothing: the
// download goes on and the other gets the image.
func TestImageOneOfTwoWaitersLeavingKeepsTheDownload(t *testing.T) {
	up := newHoldingServer(t)
	c := newCache(t, up.imageServer)
	const key = "tmdb/w342/shared.jpg"
	ctx, leave := context.WithCancel(t.Context())
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { _, err := c.TMDB(ctx, "w342", "shared.jpg"); first <- err }()
	recv(t, up.started, "request at the image server")
	go func() { _, err := c.TMDB(t.Context(), "w342", "shared.jpg"); second <- err }()
	waitFor(t, "both clients to wait", func() bool { return c.waiters(key) == 2 })

	leave()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Errorf("the client that left: %v", err)
	}
	silent(t, up.gone, 150*time.Millisecond, "cancelled download")
	if n := c.waiters(key); n != 1 {
		t.Errorf("%d waiters after one left, want 1", n)
	}

	up.open()
	if err := <-second; err != nil {
		t.Fatalf("the client that stayed: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(c.dir, "tmdb", "w342", "shared.jpg")); err != nil || !bytes.Equal(b, jpegBytes) {
		t.Errorf("cached image: %d bytes, %v", len(b), err)
	}
	if up.hits.Load() != 1 {
		t.Errorf("%d downloads, want the one both clients shared", up.hits.Load())
	}
}

// An image that a background job (Prefetch: the metadata worker's) waits for is
// fetched whatever the clients do: the job is a waiter that stays.
func TestImageBackgroundFetchSurvivesTheClientLeaving(t *testing.T) {
	for _, jobFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("jobFirst=%v", jobFirst), func(t *testing.T) {
			up := newHoldingServer(t)
			c := newCache(t, up.imageServer)
			const key = "tmdb/w342/bg.jpg"
			ctx, leave := context.WithCancel(t.Context())
			client, job := make(chan error, 1), make(chan error, 1)
			startClient := func() { go func() { _, err := c.TMDB(ctx, "w342", "bg.jpg"); client <- err }() }
			startJob := func() { go func() { job <- c.Prefetch(t.Context(), "w342", "tmdb:/bg.jpg") }() }
			first, then := startClient, startJob
			if jobFirst {
				first, then = startJob, startClient
			}
			first()
			recv(t, up.started, "request at the image server")
			then()
			waitFor(t, "the client and the job to wait", func() bool { return c.waiters(key) == 2 })

			leave()
			if err := <-client; !errors.Is(err, context.Canceled) {
				t.Errorf("the client that left: %v", err)
			}
			silent(t, up.gone, 150*time.Millisecond, "cancelled download")
			up.open()
			if err := <-job; err != nil {
				t.Fatalf("the job: %v", err)
			}
			if b, err := os.ReadFile(filepath.Join(c.dir, "tmdb", "w342", "bg.jpg")); err != nil || !bytes.Equal(b, jpegBytes) {
				t.Errorf("cached image: %d bytes, %v", len(b), err)
			}
		})
	}
}

// Clients queued for a download slot give up when they leave: they never reach
// the image server, they hold no slot, and they take none from those who do.
func TestImageClientsWaitingForASlotGiveUpWhenTheyLeave(t *testing.T) {
	up := newHoldingServer(t)
	c := newCache(t, up.imageServer)

	// Every slot is held by a download the client of which stays.
	held := make(chan error, maxDownloads)
	for i := range maxDownloads {
		go func() { _, err := c.TMDB(t.Context(), "w342", fmt.Sprintf("held%d.jpg", i)); held <- err }()
	}
	for range maxDownloads {
		recv(t, up.started, "held download")
	}
	if got := len(c.downloads); got != maxDownloads {
		t.Fatalf("setup: %d slots taken, want %d", got, maxDownloads)
	}
	base := runtime.NumGoroutine()

	// A fling: forty images nobody has, all queued for a slot, then the clients leave.
	const fling = 40
	ctx, leave := context.WithCancel(t.Context())
	errs := make(chan error, fling)
	for i := range fling {
		go func() { _, err := c.TMDB(ctx, "w342", fmt.Sprintf("fling%d.jpg", i)); errs <- err }()
	}
	waitFor(t, "the fling to queue", func() bool { return c.running() == maxDownloads+fling })
	leave()
	for range fling {
		if err := <-errs; !errors.Is(err, context.Canceled) {
			t.Errorf("a client that left: %v", err)
		}
	}
	waitFor(t, "the queued runs to go", func() bool { return c.running() == maxDownloads })

	// They freed nothing they did not hold, and took nothing.
	if got := len(c.downloads); got != maxDownloads {
		t.Fatalf("%d slots taken after the fling left, want the %d of the downloads under way", got, maxDownloads)
	}
	silent(t, up.started, 100*time.Millisecond, "request at the image server")
	waitFor(t, fmt.Sprintf("the goroutines of the fling to end (back to %d)", base), func() bool { return runtime.NumGoroutine() <= base })

	// Someone who stays still waits their turn (the slots are not over-subscribed)...
	late := make(chan error, 1)
	go func() { _, err := c.TMDB(t.Context(), "w342", "late.jpg"); late <- err }()
	silent(t, up.started, 100*time.Millisecond, "request past the limit of downloads")
	// ...and gets the image when a slot frees.
	up.open()
	if err := <-late; err != nil {
		t.Fatalf("the client that stayed: %v", err)
	}
	for range maxDownloads {
		if err := <-held; err != nil {
			t.Errorf("a held download: %v", err)
		}
	}
	if got := up.hits.Load(); got != maxDownloads+1 {
		t.Errorf("%d requests to the image server, want %d: the %d images of the fling were never asked for", got, maxDownloads+1, fling)
	}
}

// A request whose client is gone already (the browser aborted it before the server
// got to it) costs nothing: no settings lookup, no run, no download.
func TestImageForAClientThatIsGoneIsNotEvenLookedUp(t *testing.T) {
	srv := newImageServer(t, nil)
	var lookups atomic.Int32
	lookup := func(_ context.Context, size, file string) (string, bool) {
		lookups.Add(1)
		return srv.srv.URL + "/" + size + "/" + file, true
	}
	c := newCache(t, srv, func(o *ImageOptions) { o.URL, o.TVDBURL = lookup, lookup })

	ctx, leave := context.WithCancel(t.Context())
	leave()
	if _, err := c.TMDB(ctx, "w342", "gone.jpg"); !errors.Is(err, context.Canceled) {
		t.Errorf("TMDB: %v", err)
	}
	if _, err := c.TVDB(ctx, "w342", "/banners/posters/1-1.jpg"); !errors.Is(err, context.Canceled) {
		t.Errorf("TVDB: %v", err)
	}
	if lookups.Load() != 0 || srv.hits.Load() != 0 || c.running() != 0 {
		t.Errorf("%d lookups, %d requests, %d runs for clients that were gone", lookups.Load(), srv.hits.Load(), c.running())
	}

	// Positive control: a client that is there is looked up and served.
	if _, err := c.TMDB(t.Context(), "w342", "here.jpg"); err != nil || lookups.Load() == 0 || srv.hits.Load() != 1 {
		t.Errorf("live client: %v, %d lookups, %d requests", err, lookups.Load(), srv.hits.Load())
	}
}

// A free slot and a context that has ended are both ready: the slot is not taken.
func TestAcquireWithAnEndedContextTakesNothing(t *testing.T) {
	sem := make(chan struct{}, 4)
	ctx, leave := context.WithCancel(t.Context())
	leave()
	for range 200 {
		if err := acquire(ctx, sem); !errors.Is(err, context.Canceled) {
			t.Fatalf("acquire = %v, want context.Canceled", err)
		}
	}
	if len(sem) != 0 {
		t.Errorf("%d slots taken by callers that were gone", len(sem))
	}
	if err := acquire(t.Context(), sem); err != nil || len(sem) != 1 { // positive control
		t.Errorf("live acquire: %v, %d slots", err, len(sem))
	}
}

// After a fling that left, nothing is left: the downloads under way were abandoned at
// the image server, most of the queue never got there, and no goroutine remains. The
// image server holds its answers until the end, so that a download nobody cancelled
// would keep its run (and the test) waiting.
func TestImageFlingLeavesNoGoroutinesBehind(t *testing.T) {
	up := newHoldingServer(t)
	c := newCache(t, up.imageServer)
	base := runtime.NumGoroutine()

	const fling = 200
	ctx, leave := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	for i := range fling {
		wg.Go(func() {
			c.TMDB(ctx, "w342", fmt.Sprintf("fling%d.jpg", i))
		})
	}
	recv(t, up.started, "request at the image server") // some are downloading, the rest queue
	waitFor(t, "every image of the fling to have a run", func() bool { return c.running() == fling })
	if during := runtime.NumGoroutine(); during < base+fling { // positive control: the fling is something to clean up
		t.Fatalf("%d goroutines during the fling, %d before it", during, base)
	}

	leave()
	wg.Wait()
	waitFor(t, "the runs to end with the image server still holding its answers", func() bool { return c.running() == 0 })
	time.Sleep(100 * time.Millisecond) // a request still on its way reaches the server and is seen to be abandoned
	hits := int(up.hits.Load())
	if gone := len(up.gone); gone != hits {
		t.Errorf("%d requests reached the image server, %d were abandoned there", hits, gone)
	}
	if hits >= fling/2 {
		t.Errorf("%d of %d requests reached the image server: the queue went on after the clients left", hits, fling)
	}

	up.open()
	waitFor(t, fmt.Sprintf("the goroutine count to come back to %d", base), func() bool {
		http.DefaultTransport.(*http.Transport).CloseIdleConnections()
		return runtime.NumGoroutine() <= base
	})
}

// The last client leaves in the window between the run getting a genuine error (the
// image server's 404) and its writing the failure down; here the 404 is handed over
// after the leave, so that the order is certain. The run is abandoned, but the failure
// is the image's own and is remembered, as it would have been a moment earlier.
func TestImageFailureIsRememberedEvenIfTheLastClientLeftJustAfterIt(t *testing.T) {
	c := newCache(t, newImageServer(t, nil))
	const key = "k"
	var runs atomic.Int32
	entered, answer := make(chan struct{}, 1), make(chan struct{})
	fn := func(context.Context) error {
		runs.Add(1)
		entered <- struct{}{}
		<-answer
		return fmt.Errorf("image w342/x.jpg: 404 Not Found: %w", errImageNotFound)
	}

	ctx, leave := context.WithCancel(t.Context())
	first := make(chan error, 1)
	go func() { first <- c.once(ctx, key, fn) }()
	<-entered
	leave()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("the client that left: %v", err)
	}
	if !c.abandoned(key) || c.remembered() != 0 { // positive control: abandoned, and nothing is remembered yet
		t.Fatalf("setup: abandoned %v, %d failures remembered", c.abandoned(key), c.remembered())
	}

	close(answer)
	waitFor(t, "the run to end", func() bool { return c.running() == 0 })
	if c.remembered() != 1 {
		t.Fatalf("%d failures remembered, want the 404", c.remembered())
	}
	if err := c.once(t.Context(), key, fn); !errors.Is(err, ErrImageUnavailable) || runs.Load() != 1 {
		t.Errorf("asked again: %v after %d runs; want the remembered failure, with no new run", err, runs.Load())
	}
}

// A run that nobody wants any more may still get the image while it unwinds. A client
// that came meanwhile and waited for it is given that image: it is not fetched twice.
func TestImageAskedAgainAsTheAbandonedRunGetsTheImage(t *testing.T) {
	c := newCache(t, newImageServer(t, nil))
	var runs atomic.Int32
	entered, finish := make(chan struct{}, 1), make(chan struct{})
	fn := func(ctx context.Context) error {
		if runs.Add(1) > 1 {
			return nil
		}
		entered <- struct{}{}
		<-ctx.Done() // nobody waits for it any more...
		<-finish     // ...but the image arrives
		return nil
	}

	ctx, leave := context.WithCancel(t.Context())
	first := make(chan error, 1)
	go func() { first <- c.once(ctx, "k", fn) }()
	<-entered
	leave()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("the client that left: %v", err)
	}

	second := make(chan error, 1)
	go func() { second <- c.once(t.Context(), "k", fn) }()
	select {
	case err := <-second:
		t.Fatalf("the next client was answered (%v) while the old run was still unwinding", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(finish)
	if err := <-second; err != nil {
		t.Fatalf("the next client: %v", err)
	}
	if runs.Load() != 1 {
		t.Errorf("%d runs, want the one that got the image", runs.Load())
	}
}

// A run that nobody wants any more may take a while to unwind (ffmpeg being killed).
// A client that comes meanwhile does not get its error, and the two runs do not
// overlap (they would write the same temporary file): it waits, then starts afresh.
func TestImageAskedAgainWhileTheAbandonedRunWindsDown(t *testing.T) {
	c := newCache(t, newImageServer(t, nil))
	var runs, active, peak atomic.Int32
	entered := make(chan struct{}, 2)
	unwind := make(chan struct{})
	fn := func(ctx context.Context) error {
		n := runs.Add(1)
		now := active.Add(1)
		defer active.Add(-1)
		for p := peak.Load(); now > p && !peak.CompareAndSwap(p, now); p = peak.Load() {
		}
		entered <- struct{}{}
		if n > 1 {
			return nil
		}
		<-ctx.Done()
		<-unwind
		return ctx.Err()
	}

	ctx, leave := context.WithCancel(t.Context())
	first := make(chan error, 1)
	go func() { first <- c.once(ctx, "k", fn) }()
	<-entered
	leave()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("the client that left: %v", err)
	}

	second := make(chan error, 1)
	go func() { second <- c.once(t.Context(), "k", fn) }()
	select {
	case err := <-second:
		t.Fatalf("the next client was answered (%v) while the old run was still unwinding", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(unwind)
	if err := <-second; err != nil {
		t.Fatalf("the next client: %v", err)
	}
	if runs.Load() != 2 || peak.Load() != 1 {
		t.Errorf("%d runs, %d at once; want 2, one after the other", runs.Load(), peak.Load())
	}
}
