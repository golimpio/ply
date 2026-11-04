package api

import (
	"testing"
	"time"
)

// GET /api/folders reports, for each folder, when the scanner last covered all of
// it (Options.LastScan), and 0 when it has not.
func TestFoldersReportTheirLastScan(t *testing.T) {
	f := newFolderEnv(t, "")
	movies, tv := f.path("movies"), f.path("tv")
	f.add(movies, "movies")
	f.add(tv, "shows")
	scanned := time.Unix(1_700_000_123, 456)

	lastScanAt := func() map[string]float64 {
		t.Helper()
		rec := f.get("/api/folders")
		wantStatus(t, rec, 200)
		out := map[string]float64{}
		for _, g := range decode[[]map[string]any](t, rec) {
			out[g["path"].(string)] = g["lastScanAt"].(float64)
		}
		return out
	}

	if got := lastScanAt(); got[movies] != 0 || got[tv] != 0 { // no scanner to ask
		t.Errorf("lastScanAt with no LastScan = %v, want 0", got)
	}
	f.srv.LastScan = func(root string) time.Time {
		if root == movies {
			return scanned
		}
		return time.Time{}
	}
	got := lastScanAt()
	if got[movies] != float64(scanned.Unix()) {
		t.Errorf("lastScanAt of the scanned folder = %v, want %d", got[movies], scanned.Unix())
	}
	if got[tv] != 0 {
		t.Errorf("lastScanAt of a folder that was not scanned = %v, want 0", got[tv])
	}
}
