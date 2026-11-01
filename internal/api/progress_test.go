package api

import (
	"context"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"ply/internal/store"
)

func (e *env) putProgress(id int64, body string, hdr map[string]string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do("PUT", fmt.Sprintf("/api/items/%d/progress", id), hdr, body)
}

func (e *env) summary(id int64) ItemSummary {
	e.t.Helper()
	for _, it := range decode[[]ItemSummary](e.t, e.get("/api/items")) {
		if it.ID == id {
			return it
		}
	}
	e.t.Fatalf("item %d not in the list", id)
	return ItemSummary{}
}

func TestProgressRoundTrip(t *testing.T) {
	e := newEnv(t, "")
	id := e.mkv // 2400 s

	// Every item has the key, null until it is started.
	raw := decode[[]map[string]any](t, e.get("/api/items"))
	for _, it := range raw {
		if v, ok := it["progress"]; !ok || v != nil {
			t.Errorf("item %v: progress = %v (present %v), want null", it["id"], v, ok)
		}
	}
	if d := decode[map[string]any](t, e.get(fmt.Sprintf("/api/items/%d", id))); d["progress"] != nil {
		t.Errorf("detail progress = %v, want null", d["progress"])
	}

	rec := e.putProgress(id, `{"positionSec": 754.5, "durationSec": 2400}`, nil)
	wantStatus(t, rec, 200)
	if p := decode[*Progress](t, rec); p == nil || p.PositionSec != 754.5 || p.Watched {
		t.Errorf("PUT result = %+v", p)
	}

	if p := e.summary(id).Progress; p == nil || p.PositionSec != 754.5 || p.Watched {
		t.Errorf("list progress = %+v", p)
	}
	d := decode[ItemDetail](t, e.get(fmt.Sprintf("/api/items/%d", id)))
	if d.Progress == nil || d.Progress.PositionSec != 754.5 {
		t.Errorf("detail progress = %+v", d.Progress)
	}
	if other := e.summary(e.mp4).Progress; other != nil {
		t.Errorf("another item picked up progress: %+v", other)
	}

	// Later report replaces the earlier one.
	e.putProgress(id, `{"positionSec": 900, "durationSec": 2400}`, nil)
	if p := e.summary(id).Progress; p == nil || p.PositionSec != 900 {
		t.Errorf("progress = %+v, want 900", p)
	}
}

func TestProgressWatchedAndNotStarted(t *testing.T) {
	e := newEnv(t, "")
	id := e.mkv // 2400 s long

	// 90 % is watched, and the item leaves Continue Watching.
	e.putProgress(id, `{"positionSec": 600, "durationSec": 2400}`, nil)
	if cont := decode[[]ItemSummary](t, e.get("/api/continue")); len(cont) != 1 {
		t.Fatalf("setup: continue = %d items, want 1", len(cont))
	}
	rec := e.putProgress(id, `{"positionSec": 2160, "durationSec": 2400}`, nil)
	wantStatus(t, rec, 200)
	if p := decode[*Progress](t, rec); p == nil || !p.Watched {
		t.Errorf("at 90%%: %+v, want watched", p)
	}
	if p := e.summary(id).Progress; p == nil || !p.Watched {
		t.Errorf("list progress = %+v, want watched", p)
	}
	if cont := decode[[]ItemSummary](t, e.get("/api/continue")); len(cont) != 0 {
		t.Errorf("continue lists a watched item: %+v", cont)
	}

	// Below 30 s a position is not "started" and clears the row.
	rec = e.putProgress(id, `{"positionSec": 12, "durationSec": 7200}`, nil)
	wantStatus(t, rec, 200)
	if body := rec.Body.String(); body != "null\n" {
		t.Errorf("not-started body = %q, want null", body)
	}
	if p := e.summary(id).Progress; p != nil {
		t.Errorf("progress after restart = %+v, want none", p)
	}
}

func TestProgressUsesTheItemDurationWhenTheClientHasNone(t *testing.T) {
	e := newEnv(t, "")
	// durationSec 0: the item's own 2400 s applies, so 2300 s is watched.
	rec := e.putProgress(e.mkv, `{"positionSec": 2300, "durationSec": 0}`, nil)
	wantStatus(t, rec, 200)
	if p := decode[*Progress](t, rec); p == nil || !p.Watched {
		t.Errorf("progress = %+v, want watched", p)
	}
	// A position past the end is clamped to it.
	rec = e.putProgress(e.mp4, `{"positionSec": 5000, "durationSec": 120}`, nil)
	if p := decode[*Progress](t, rec); p == nil || p.PositionSec != 120 || !p.Watched {
		t.Errorf("progress = %+v, want clamped to 120 and watched", p)
	}
}

func TestProgressValidation(t *testing.T) {
	e := newEnv(t, "")
	for name, body := range map[string]string{
		"not JSON":          `nope`,
		"empty":             ``,
		"negative position": `{"positionSec": -1, "durationSec": 100}`,
		"negative duration": `{"positionSec": 1, "durationSec": -100}`,
		"string":            `{"positionSec": "12", "durationSec": 100}`,
		"overflow":          `{"positionSec": 1e999, "durationSec": 100}`,
	} {
		t.Run(name, func(t *testing.T) {
			wantJSONError(t, e.putProgress(e.mkv, body, nil), 400)
		})
	}
	wantJSONError(t, e.putProgress(9999, `{"positionSec": 100, "durationSec": 200}`, nil), 404)
	wantJSONError(t, e.do("PUT", "/api/items/abc/progress", nil, `{"positionSec": 100, "durationSec": 200}`), 404)
	// Nothing was stored by any of the rejected requests.
	if p := e.summary(e.mkv).Progress; p != nil {
		t.Errorf("rejected requests stored %+v", p)
	}
	// Positive control: a valid one is stored.
	wantStatus(t, e.putProgress(e.mkv, `{"positionSec": 100, "durationSec": 200}`, nil), 200)
	if e.summary(e.mkv).Progress == nil {
		t.Error("a valid report was not stored")
	}
}

func TestProgressNeedsTheToken(t *testing.T) {
	e := newEnv(t, "tok")
	body := `{"positionSec": 100, "durationSec": 2400}`
	wantJSONError(t, e.putProgress(e.mkv, body, nil), 401)
	wantJSONError(t, e.get("/api/continue"), 401)
	wantStatus(t, e.putProgress(e.mkv, body, map[string]string{"Authorization": "Bearer tok"}), 200)
	rec := e.do("GET", "/api/continue", map[string]string{"Authorization": "Bearer tok"}, "")
	if cont := decode[[]ItemSummary](t, rec); len(cont) != 1 || cont[0].ID != e.mkv {
		t.Errorf("continue = %+v", cont)
	}
}

func TestContinueOrderingAndLimit(t *testing.T) {
	e := newEnv(t, "")
	if rec := e.get("/api/continue"); rec.Body.String() != "[]\n" {
		t.Errorf("empty continue = %q, want []", rec.Body.String())
	}

	e.putProgress(e.mp4, `{"positionSec": 100, "durationSec": 120000}`, nil)
	e.putProgress(e.mkv, `{"positionSec": 100, "durationSec": 2400}`, nil)
	e.putProgress(e.multi, `{"positionSec": 100, "durationSec": 600}`, nil)
	e.putProgress(e.mp4, `{"positionSec": 200, "durationSec": 120000}`, nil) // played last
	cont := decode[[]ItemSummary](t, e.get("/api/continue"))
	if len(cont) != 3 || cont[0].ID != e.mp4 || cont[1].ID != e.multi || cont[2].ID != e.mkv {
		var ids []int64
		for _, c := range cont {
			ids = append(ids, c.ID)
		}
		t.Fatalf("continue order = %v, want [%d %d %d] (most recent first)", ids, e.mp4, e.multi, e.mkv)
	}
	if cont[0].Progress == nil || cont[0].Progress.PositionSec != 200 || cont[0].Title != "Movie" || cont[0].VideoCodec != "h264" {
		t.Errorf("continue entry is not a full ItemSummary: %+v", cont[0])
	}

	// More than 20 started items: only the 20 most recent.
	for i := range 22 {
		path := filepath.Join(e.dir, fmt.Sprintf("extra %02d (2000).mp4", i))
		id, err := e.store.UpsertItem(context.Background(), &store.Item{Kind: store.KindMovie, Title: "Extra", Path: path,
			Root: e.dir, Size: 1, MTime: 1, Container: "mp4", DurationSec: 7200}, streamsH264())
		if err != nil {
			t.Fatal(err)
		}
		wantStatus(t, e.putProgress(id, `{"positionSec": 300, "durationSec": 7200}`, nil), 200)
	}
	if cont = decode[[]ItemSummary](t, e.get("/api/continue")); len(cont) != 20 {
		t.Errorf("continue returned %d items, want the limit of 20", len(cont))
	}
}
