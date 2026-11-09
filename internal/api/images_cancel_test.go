package api

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// holdingImages is a TMDB image server whose answers wait for release. started
// gets the path of each request that arrives; gone the path of each one the
// client abandoned (the server saw the connection close).
type holdingImages struct {
	*httptest.Server
	started, gone chan string
	hits          atomic.Int32
	release       chan struct{}
	once          sync.Once
}

func newHoldingImages(t *testing.T) *holdingImages {
	t.Helper()
	h := &holdingImages{started: make(chan string, 64), gone: make(chan string, 64), release: make(chan struct{})}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.hits.Add(1)
		h.started <- r.URL.Path
		select {
		case <-h.release:
			w.Write(jpegBytes)
		case <-r.Context().Done():
			h.gone <- r.URL.Path
		}
	}))
	t.Cleanup(h.Server.Close)
	t.Cleanup(h.open)
	return h
}

func (h *holdingImages) open() { h.once.Do(func() { close(h.release) }) }

func recvPath(t *testing.T, ch <-chan string, what string) string {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("no %s within 5s", what)
	}
	return ""
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// A fast scroll asks for dozens of images nobody has and the browser aborts them a
// frame later: the downloads under way are cancelled at the image server, most of the
// queue never reaches it, and a client that stays is served.
func TestTMDBImagesTheBrowserAbortedAreAbandonedUpstream(t *testing.T) {
	up := newHoldingImages(t)
	m := newMetaEnvWith(t, apiToken, func(m *menv, _ *Options) { m.fake.ImageBase = up.URL })
	ts := httptest.NewServer(m.srv)
	t.Cleanup(ts.Close)

	const images = 40
	ctx, abort := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	for i := range images {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/api/images/tmdb/w342/fling%d.jpg", ts.URL, i), nil)
			req.Header.Set("Authorization", "Bearer "+apiToken)
			if resp, err := http.DefaultClient.Do(req); err == nil {
				resp.Body.Close()
				t.Errorf("image %d: status %d, want the request aborted", i, resp.StatusCode)
			}
		}()
	}
	recvPath(t, up.started, "request at the image server") // positive control: the downloads are under way
	time.Sleep(200 * time.Millisecond)                     // the rest wait for a slot
	under := int(up.hits.Load())
	if under < 1 || under > images/4 {
		t.Fatalf("%d of %d requests reached the image server, want a few (the cache limits its downloads)", under, images)
	}

	abort()
	wg.Wait()
	eventually(t, "the downloads under way to be abandoned at the image server", func() bool { return len(up.gone) >= under })
	time.Sleep(200 * time.Millisecond)
	// A download queued behind them may take a slot as it frees, before its own client
	// is seen to have left: it is then abandoned as well. What must not happen is that
	// the queue goes on, or that anything is left running.
	hits := int(up.hits.Load())
	if len(up.gone) != hits {
		t.Errorf("%d requests reached the image server, %d were abandoned there", hits, len(up.gone))
	}
	if hits > images/2 {
		t.Errorf("%d of %d requests reached the image server: the queue went on after the browser left", hits, images)
	}

	// A client that stays is served.
	up.open()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/api/images/tmdb/w342/stay.jpg", nil)
	req.Header.Set("Authorization", "Bearer "+apiToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if b, _ := io.ReadAll(resp.Body); resp.StatusCode != 200 || !bytes.Equal(b, jpegBytes) {
		t.Errorf("the client that stayed: %d, %d bytes", resp.StatusCode, len(b))
	}
}

// The same for a frame grab: a browser that aborts the request of an item's
// artwork kills the ffmpeg that was grabbing it.
func TestArtRequestAbortedByTheBrowserKillsTheFrameGrab(t *testing.T) {
	e := newEnv(t, "")
	pidFile := filepath.Join(t.TempDir(), "pid")
	// (art.New asks ffmpeg for its filters first; that one must end.)
	g := e.withArt(fmt.Sprintf(`[ "$1" = -hide_banner ] && exit 0; echo $$ > %q; exec sleep 60`, pidFile))
	t.Cleanup(g.Close)
	ts := httptest.NewServer(e.srv)
	t.Cleanup(ts.Close)

	ctx, abort := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/api/items/%d/art/thumb", ts.URL, e.mp4), nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
		}
		done <- err
	}()
	var pid int
	eventually(t, "ffmpeg to start", func() bool {
		b, _ := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		return pid > 0
	})
	if !processAlive(pid) {
		t.Fatalf("setup: ffmpeg %d is not running", pid)
	}
	abort()
	if err := <-done; err == nil {
		t.Error("the aborted request was answered")
	}
	eventually(t, "ffmpeg to be killed", func() bool { return !processAlive(pid) })
}
