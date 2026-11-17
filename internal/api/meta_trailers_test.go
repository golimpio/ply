package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"ply/internal/store"
	"ply/internal/trailers"
)

// fakeTrailers is the downloads: Ensure answers d, and counts the titles asked.
type fakeTrailers struct {
	mu    sync.Mutex
	asked []int64
	d     *trailers.Download
}

func (f *fakeTrailers) Ensure(_ context.Context, id int64) (*trailers.Download, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, id)
	return f.d, nil
}

// addTrailerOf puts a trailer next to the matrix film's file, named as that file's name says.
func addTrailerOf(t *testing.T, m *menv) int64 {
	t.Helper()
	ctx := context.Background()
	film, err := m.store.GetEntry(ctx, m.mp4)
	if err != nil {
		t.Fatal(err)
	}
	id, err := m.store.UpsertItem(ctx, &store.Item{Kind: store.KindTrailer, Title: film.Item.Title, Year: film.Item.Year,
		OwnerDir: filepath.Dir(film.Item.Path), Path: filepath.Join(filepath.Dir(film.Item.Path), "x-trailer.mp4"),
		Root: film.Item.Root, Size: 1, MTime: 1, DurationSec: 128.5}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func trailersOf(t *testing.T, m *menv, title int64) (TitleTrailers, string) {
	t.Helper()
	rec := m.get(fmt.Sprintf("/api/titles/%d/trailers", title))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET trailers of %d = %d %s", title, rec.Code, rec.Body)
	}
	var out TitleTrailers
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out, rec.Body.String()
}

// A trailer on disk is listed: no download is asked for, and nothing names YouTube.
func TestTitleTrailersLocal(t *testing.T) {
	f := &fakeTrailers{}
	m := newMetaEnv(t, apiToken, func(o *Options) { o.Trailers = f })
	id := addTrailerOf(t, m)
	got, body := trailersOf(t, m, m.matrixTitle)
	if len(got.Local) != 1 || got.Local[0].ItemID != id || got.Local[0].Name != "x-trailer" || got.Local[0].DurationSec != 128.5 ||
		got.Download != nil || len(f.asked) != 0 {
		t.Errorf("trailers = %s; Ensure asked %v", body, f.asked)
	}
	if strings.Contains(body, "youtube") || strings.Contains(body, `"key"`) {
		t.Errorf("the sites' videos are sent: %s", body)
	}
}

// No trailer on disk: the page's request queues the download, and says so.
func TestTitleTrailersTriggers(t *testing.T) {
	f := &fakeTrailers{d: &trailers.Download{State: "queued", JobID: 7}}
	m := newMetaEnv(t, apiToken, func(o *Options) { o.Trailers = f })
	got, body := trailersOf(t, m, m.matrixTitle)
	if got.Local == nil || len(got.Local) != 0 || got.Download == nil || got.Download.State != "queued" || got.Download.JobID != 7 ||
		len(f.asked) != 1 || f.asked[0] != m.matrixTitle {
		t.Errorf("trailers = %s; Ensure asked %v", body, f.asked)
	}
	if !strings.Contains(body, `"local":[]`) {
		t.Errorf("local is not an empty list: %s", body)
	}
}

// Two pages at once (the web and a phone) see the same job: Ensure, which dedups, is asked each time.
func TestTrailersTriggerDedupWhileRunning(t *testing.T) {
	f := &fakeTrailers{d: &trailers.Download{State: "running", JobID: 9}}
	m := newMetaEnv(t, apiToken, func(o *Options) { o.Trailers = f })
	a, _ := trailersOf(t, m, m.matrixTitle)
	b, _ := trailersOf(t, m, m.matrixTitle)
	if a.Download == nil || b.Download == nil || a.Download.JobID != 9 || b.Download.JobID != 9 || b.Download.State != "running" {
		t.Errorf("downloads %+v, %+v", a.Download, b.Download)
	}
}

// Lookups off: Ensure queues nothing (nil); the trailer on disk still plays.
func TestTitleTrailersLookupsOff(t *testing.T) {
	m := newMetaEnv(t, apiToken, func(o *Options) { o.Trailers = &fakeTrailers{} })
	m.src.Set(m.fake, false)
	id := addTrailerOf(t, m)
	if got, body := trailersOf(t, m, m.matrixTitle); len(got.Local) != 1 || got.Local[0].ItemID != id || got.Download != nil {
		t.Errorf("lookups off: %s", body)
	}
}

func TestTitleTrailersErrors(t *testing.T) {
	m := newMetaEnv(t, apiToken, func(o *Options) { o.Trailers = &fakeTrailers{} })
	for _, bad := range []string{"/api/titles/999999/trailers", "/api/titles/abc/trailers", "/api/titles/0/trailers"} {
		if rec := m.get(bad); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d %s, want 404", bad, rec.Code, rec.Body)
		}
	}
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/titles/%d/trailers", m.matrixTitle), nil)
	anon := httptest.NewRecorder()
	m.srv.ServeHTTP(anon, req)
	if anon.Code != http.StatusUnauthorized {
		t.Errorf("without the token: %d", anon.Code)
	}
}

// Without the metadata subsystem (or the downloads) the trailers on disk are still listed.
func TestTitleTrailersWithoutMetadata(t *testing.T) {
	m := newMetaEnv(t, "", func(o *Options) { o.Metadata = nil })
	id := addTrailerOf(t, m)
	if got, body := trailersOf(t, m, m.matrixTitle); len(got.Local) != 1 || got.Local[0].ItemID != id || got.Download != nil {
		t.Errorf("no metadata subsystem: %s", body)
	}
}
